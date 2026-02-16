package backends

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"

	inferencev1alpha1 "github.com/inference-operator/inference-operator/api/v1alpha1"
)

const (
	vllmDefaultImage    = "vllm/vllm-openai:latest"
	vllmAPIPort         = 8000
	vllmMetricsPort     = 9090
	vllmHealthEndpoint  = "/health"
	vllmMetricsEndpoint = "/metrics"
)

// VLLMBackend implements the Backend interface for vLLM
type VLLMBackend struct {
	log        logr.Logger
	httpClient *http.Client
}

// NewVLLMBackend creates a new vLLM backend
func NewVLLMBackend(log logr.Logger) *VLLMBackend {
	return &VLLMBackend{
		log: log.WithName("vllm"),
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Name returns the backend name
func (b *VLLMBackend) Name() string {
	return "vllm"
}

// Type returns the backend type
func (b *VLLMBackend) Type() inferencev1alpha1.BackendType {
	return inferencev1alpha1.BackendTypeVLLM
}

// Version returns the backend version
func (b *VLLMBackend) Version() string {
	return "0.4.0"
}

// Validate validates the service configuration
func (b *VLLMBackend) Validate(ctx context.Context, service *inferencev1alpha1.InferenceService) error {
	if service.Spec.Model.Name == "" {
		return fmt.Errorf("model name is required")
	}

	if service.Spec.GPU.Count < 1 {
		return fmt.Errorf("at least 1 GPU is required")
	}

	// Validate tensor parallelism
	if service.Spec.BackendConfig != nil {
		tp := service.Spec.BackendConfig.TensorParallelism
		if tp > 0 && int32(service.Spec.GPU.Count) < tp {
			return fmt.Errorf("tensor parallelism (%d) cannot exceed GPU count (%d)", tp, service.Spec.GPU.Count)
		}
	}

	return nil
}

// GenerateDeployment generates the deployment spec for vLLM
func (b *VLLMBackend) GenerateDeployment(ctx context.Context, service *inferencev1alpha1.InferenceService) (*DeploymentSpec, error) {
	if err := b.Validate(ctx, service); err != nil {
		return nil, err
	}

	args := b.buildArgs(service)
	env := b.buildEnv(service)
	resources := b.buildResources(service)

	spec := &DeploymentSpec{
		Name:      service.Name,
		Namespace: service.Namespace,
		Image:     vllmDefaultImage,
		Args:      args,
		Env:       env,
		Ports: []corev1.ContainerPort{
			{
				Name:          "http",
				ContainerPort: vllmAPIPort,
				Protocol:      corev1.ProtocolTCP,
			},
			{
				Name:          "metrics",
				ContainerPort: vllmMetricsPort,
				Protocol:      corev1.ProtocolTCP,
			},
		},
		Resources:          resources,
		Replicas:           service.Spec.Replicas,
		ServiceAccountName: service.Spec.ServiceAccountName,
		Labels: map[string]string{
			"app.kubernetes.io/name":       service.Name,
			"app.kubernetes.io/component":  "inference",
			"app.kubernetes.io/managed-by": "inference-operator",
			"inference.ai/backend":         "vllm",
		},
		Annotations: map[string]string{
			"inference.ai/model": service.Spec.Model.Name,
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: vllmHealthEndpoint,
					Port: intstr.FromInt(vllmAPIPort),
				},
			},
			InitialDelaySeconds: 30,
			PeriodSeconds:       10,
			TimeoutSeconds:      5,
			FailureThreshold:    3,
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: vllmHealthEndpoint,
					Port: intstr.FromInt(vllmAPIPort),
				},
			},
			InitialDelaySeconds: 60,
			PeriodSeconds:       30,
			TimeoutSeconds:      10,
			FailureThreshold:    3,
		},
		StartupProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: vllmHealthEndpoint,
					Port: intstr.FromInt(vllmAPIPort),
				},
			},
			InitialDelaySeconds: 10,
			PeriodSeconds:       10,
			TimeoutSeconds:      5,
			FailureThreshold:    60, // Allow 10 minutes for model loading
		},
	}

	// Add node selector
	if service.Spec.NodeSelector != nil {
		spec.NodeSelector = service.Spec.NodeSelector
	}

	// Add tolerations
	if service.Spec.Tolerations != nil {
		spec.Tolerations = service.Spec.Tolerations
	}

	// Add volumes for model caching
	spec.Volumes = []corev1.Volume{
		{
			Name: "model-cache",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					Medium: corev1.StorageMediumDefault,
				},
			},
		},
		{
			Name: "shm",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					Medium:    corev1.StorageMediumMemory,
					SizeLimit: resource.NewQuantity(10*1024*1024*1024, resource.BinarySI), // 10Gi
				},
			},
		},
	}

	spec.VolumeMounts = []corev1.VolumeMount{
		{
			Name:      "model-cache",
			MountPath: "/root/.cache/huggingface",
		},
		{
			Name:      "shm",
			MountPath: "/dev/shm",
		},
	}

	return spec, nil
}

// buildArgs builds the command line arguments for vLLM
func (b *VLLMBackend) buildArgs(service *inferencev1alpha1.InferenceService) []string {
	args := []string{
		"--model", service.Spec.Model.Name,
		"--host", "0.0.0.0",
		"--port", strconv.Itoa(vllmAPIPort),
	}

	// Add tensor parallelism
	tp := int32(1)
	if service.Spec.BackendConfig != nil && service.Spec.BackendConfig.TensorParallelism > 0 {
		tp = service.Spec.BackendConfig.TensorParallelism
	}
	args = append(args, "--tensor-parallel-size", strconv.Itoa(int(tp)))

	// Add pipeline parallelism
	if service.Spec.BackendConfig != nil && service.Spec.BackendConfig.PipelineParallelism > 1 {
		args = append(args, "--pipeline-parallel-size", strconv.Itoa(int(service.Spec.BackendConfig.PipelineParallelism)))
	}

	// Add max model length
	if service.Spec.BackendConfig != nil && service.Spec.BackendConfig.MaxSeqLen > 0 {
		args = append(args, "--max-model-len", strconv.Itoa(int(service.Spec.BackendConfig.MaxSeqLen)))
	}

	// Add quantization
	if service.Spec.BackendConfig != nil && service.Spec.BackendConfig.Quantization != "" {
		args = append(args, "--quantization", service.Spec.BackendConfig.Quantization)
	}

	// Add prefix caching if enabled
	if service.Spec.Cache != nil && service.Spec.Cache.Enabled {
		args = append(args, "--enable-prefix-caching")
	}

	// Add extra args
	if service.Spec.BackendConfig != nil && service.Spec.BackendConfig.ExtraArgs != nil {
		for key, value := range service.Spec.BackendConfig.ExtraArgs {
			args = append(args, fmt.Sprintf("--%s", key), value)
		}
	}

	return args
}

// buildEnv builds environment variables for vLLM
func (b *VLLMBackend) buildEnv(service *inferencev1alpha1.InferenceService) []corev1.EnvVar {
	env := []corev1.EnvVar{
		{
			Name:  "VLLM_USAGE_STATS",
			Value: "0",
		},
	}

	// Add HuggingFace token if specified
	if service.Spec.Model.SecretRef != nil {
		env = append(env, corev1.EnvVar{
			Name: "HF_TOKEN",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: *service.Spec.Model.SecretRef,
					Key:                  "token",
				},
			},
		})
	}

	// Set CUDA visible devices based on GPU sharing mode
	if service.Spec.GPU.Sharing == inferencev1alpha1.GPUSharingModeMIG {
		env = append(env, corev1.EnvVar{
			Name:  "CUDA_MPS_PIPE_DIRECTORY",
			Value: "/tmp/nvidia-mps",
		})
	}

	return env
}

// buildResources builds resource requirements for vLLM
func (b *VLLMBackend) buildResources(service *inferencev1alpha1.InferenceService) corev1.ResourceRequirements {
	resources := corev1.ResourceRequirements{
		Limits:   corev1.ResourceList{},
		Requests: corev1.ResourceList{},
	}

	// GPU resources
	gpuQuantity := resource.NewQuantity(int64(service.Spec.GPU.Count), resource.DecimalSI)
	resources.Limits["nvidia.com/gpu"] = *gpuQuantity
	resources.Requests["nvidia.com/gpu"] = *gpuQuantity

	// CPU and memory from service spec
	if service.Spec.Resources != nil {
		if service.Spec.Resources.Limits != nil {
			for key, val := range service.Spec.Resources.Limits {
				resources.Limits[key] = val
			}
		}
		if service.Spec.Resources.Requests != nil {
			for key, val := range service.Spec.Resources.Requests {
				resources.Requests[key] = val
			}
		}
	} else {
		// Default CPU and memory
		resources.Limits[corev1.ResourceCPU] = resource.MustParse("8")
		resources.Limits[corev1.ResourceMemory] = resource.MustParse("32Gi")
		resources.Requests[corev1.ResourceCPU] = resource.MustParse("4")
		resources.Requests[corev1.ResourceMemory] = resource.MustParse("16Gi")
	}

	return resources
}

// GenerateService generates the Kubernetes service spec for vLLM
func (b *VLLMBackend) GenerateService(ctx context.Context, service *inferencev1alpha1.InferenceService) (*ServiceSpec, error) {
	return &ServiceSpec{
		Name:      service.Name,
		Namespace: service.Namespace,
		Type:      corev1.ServiceTypeClusterIP,
		Ports: []corev1.ServicePort{
			{
				Name:       "http",
				Port:       80,
				TargetPort: intstr.FromInt(vllmAPIPort),
				Protocol:   corev1.ProtocolTCP,
			},
			{
				Name:       "metrics",
				Port:       9090,
				TargetPort: intstr.FromInt(vllmMetricsPort),
				Protocol:   corev1.ProtocolTCP,
			},
		},
		Selector: map[string]string{
			"app.kubernetes.io/name": service.Name,
		},
		Labels: map[string]string{
			"app.kubernetes.io/name":       service.Name,
			"app.kubernetes.io/managed-by": "inference-operator",
		},
	}, nil
}

// HealthCheck performs a health check on a vLLM instance
func (b *VLLMBackend) HealthCheck(ctx context.Context, endpoint string) (*HealthStatus, error) {
	url := fmt.Sprintf("http://%s%s", endpoint, vllmHealthEndpoint)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return &HealthStatus{
			Healthy:     false,
			Ready:       false,
			Message:     err.Error(),
			LastChecked: time.Now(),
		}, nil
	}
	defer resp.Body.Close()

	healthy := resp.StatusCode == http.StatusOK

	return &HealthStatus{
		Healthy:      healthy,
		Ready:        healthy,
		ModelLoaded:  healthy,
		GPUAvailable: healthy,
		LastChecked:  time.Now(),
		Message:      fmt.Sprintf("HTTP %d", resp.StatusCode),
	}, nil
}

// GetMetrics retrieves metrics from a vLLM instance
func (b *VLLMBackend) GetMetrics(ctx context.Context, endpoint string) (*BackendMetrics, error) {
	url := fmt.Sprintf("http://%s%s", endpoint, vllmMetricsEndpoint)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch metrics: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	metrics := &BackendMetrics{
		Custom: make(map[string]float64),
	}

	// Parse Prometheus metrics
	// In a real implementation, this would parse the Prometheus format
	// For now, we'll return simulated metrics
	_ = body

	return metrics, nil
}

// Scale handles scaling operations for vLLM
func (b *VLLMBackend) Scale(ctx context.Context, endpoint string, replicas int32) error {
	// vLLM doesn't support dynamic scaling at runtime
	// Scaling is handled by the Kubernetes deployment
	return nil
}

// Configure applies configuration changes to vLLM
func (b *VLLMBackend) Configure(ctx context.Context, endpoint string, config map[string]string) error {
	// vLLM doesn't support runtime configuration changes
	// Configuration changes require a restart
	return nil
}

// vLLMStatsResponse represents the response from vLLM stats endpoint
type vLLMStatsResponse struct {
	NumRequests     int64   `json:"num_requests"`
	NumRunningReqs  int32   `json:"num_running_requests"`
	NumWaitingReqs  int32   `json:"num_waiting_requests"`
	GPUMemoryUsed   int64   `json:"gpu_memory_used"`
	GPUMemoryTotal  int64   `json:"gpu_memory_total"`
	CacheHitRate    float64 `json:"cache_hit_rate"`
	TokensPerSecond float64 `json:"tokens_per_second"`
}

// getVLLMStats retrieves stats from vLLM
func (b *VLLMBackend) getVLLMStats(ctx context.Context, endpoint string) (*vLLMStatsResponse, error) {
	url := fmt.Sprintf("http://%s/stats", endpoint)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var stats vLLMStatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return nil, err
	}

	return &stats, nil
}

func init() {
	// Register vLLM backend
	DefaultFactory.Register(NewVLLMBackend(logr.Discard()))
}

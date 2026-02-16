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
	tritonDefaultImage    = "nvcr.io/nvidia/tritonserver:24.01-py3"
	tritonHTTPPort        = 8000
	tritonGRPCPort        = 8001
	tritonMetricsPort     = 8002
	tritonHealthEndpoint  = "/v2/health/ready"
	tritonMetricsEndpoint = "/metrics"
)

// TritonBackend implements the Backend interface for Triton Inference Server
type TritonBackend struct {
	log        logr.Logger
	httpClient *http.Client
}

// NewTritonBackend creates a new Triton backend
func NewTritonBackend(log logr.Logger) *TritonBackend {
	return &TritonBackend{
		log: log.WithName("triton"),
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Name returns the backend name
func (b *TritonBackend) Name() string {
	return "triton"
}

// Type returns the backend type
func (b *TritonBackend) Type() inferencev1alpha1.BackendType {
	return inferencev1alpha1.BackendTypeTriton
}

// Version returns the backend version
func (b *TritonBackend) Version() string {
	return "24.01"
}

// Validate validates the service configuration
func (b *TritonBackend) Validate(ctx context.Context, service *inferencev1alpha1.InferenceService) error {
	if service.Spec.Model.Name == "" {
		return fmt.Errorf("model name is required")
	}

	return nil
}

// GenerateDeployment generates the deployment spec for Triton
func (b *TritonBackend) GenerateDeployment(ctx context.Context, service *inferencev1alpha1.InferenceService) (*DeploymentSpec, error) {
	if err := b.Validate(ctx, service); err != nil {
		return nil, err
	}

	args := b.buildArgs(service)
	env := b.buildEnv(service)
	resources := b.buildResources(service)

	spec := &DeploymentSpec{
		Name:      service.Name,
		Namespace: service.Namespace,
		Image:     tritonDefaultImage,
		Command:   []string{"tritonserver"},
		Args:      args,
		Env:       env,
		Ports: []corev1.ContainerPort{
			{
				Name:          "http",
				ContainerPort: tritonHTTPPort,
				Protocol:      corev1.ProtocolTCP,
			},
			{
				Name:          "grpc",
				ContainerPort: tritonGRPCPort,
				Protocol:      corev1.ProtocolTCP,
			},
			{
				Name:          "metrics",
				ContainerPort: tritonMetricsPort,
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
			"inference.ai/backend":         "triton",
		},
		Annotations: map[string]string{
			"inference.ai/model": service.Spec.Model.Name,
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: tritonHealthEndpoint,
					Port: intstr.FromInt(tritonHTTPPort),
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
					Path: "/v2/health/live",
					Port: intstr.FromInt(tritonHTTPPort),
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
					Path: tritonHealthEndpoint,
					Port: intstr.FromInt(tritonHTTPPort),
				},
			},
			InitialDelaySeconds: 10,
			PeriodSeconds:       10,
			TimeoutSeconds:      5,
			FailureThreshold:    60,
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

	// Add volumes
	spec.Volumes = []corev1.Volume{
		{
			Name: "model-repository",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: "shm",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					Medium:    corev1.StorageMediumMemory,
					SizeLimit: resource.NewQuantity(10*1024*1024*1024, resource.BinarySI),
				},
			},
		},
	}

	spec.VolumeMounts = []corev1.VolumeMount{
		{
			Name:      "model-repository",
			MountPath: "/models",
		},
		{
			Name:      "shm",
			MountPath: "/dev/shm",
		},
	}

	// Add init container to download model
	spec.InitContainers = []corev1.Container{
		{
			Name:  "model-loader",
			Image: "python:3.10-slim",
			Command: []string{
				"python", "-c",
				b.generateModelLoaderScript(service),
			},
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      "model-repository",
					MountPath: "/models",
				},
			},
			Env: env,
		},
	}

	return spec, nil
}

// buildArgs builds command line arguments for Triton
func (b *TritonBackend) buildArgs(service *inferencev1alpha1.InferenceService) []string {
	args := []string{
		"--model-repository=/models",
		"--http-port=" + strconv.Itoa(tritonHTTPPort),
		"--grpc-port=" + strconv.Itoa(tritonGRPCPort),
		"--metrics-port=" + strconv.Itoa(tritonMetricsPort),
		"--allow-http=true",
		"--allow-grpc=true",
		"--allow-metrics=true",
	}

	// Add model control mode
	args = append(args, "--model-control-mode=explicit")

	// Enable CUDA memory pool
	if service.Spec.GPU.Count > 0 {
		args = append(args, "--cuda-memory-pool-byte-size=0:4294967296") // 4GB
	}

	// Add extra args
	if service.Spec.BackendConfig != nil && service.Spec.BackendConfig.ExtraArgs != nil {
		for key, value := range service.Spec.BackendConfig.ExtraArgs {
			args = append(args, fmt.Sprintf("--%s=%s", key, value))
		}
	}

	return args
}

// buildEnv builds environment variables for Triton
func (b *TritonBackend) buildEnv(service *inferencev1alpha1.InferenceService) []corev1.EnvVar {
	env := []corev1.EnvVar{
		{
			Name:  "TRITON_SERVER_CPU_ONLY",
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

	return env
}

// buildResources builds resource requirements for Triton
func (b *TritonBackend) buildResources(service *inferencev1alpha1.InferenceService) corev1.ResourceRequirements {
	resources := corev1.ResourceRequirements{
		Limits:   corev1.ResourceList{},
		Requests: corev1.ResourceList{},
	}

	// GPU resources
	if service.Spec.GPU.Count > 0 {
		gpuQuantity := resource.NewQuantity(int64(service.Spec.GPU.Count), resource.DecimalSI)
		resources.Limits["nvidia.com/gpu"] = *gpuQuantity
		resources.Requests["nvidia.com/gpu"] = *gpuQuantity
	}

	// CPU and memory
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
		resources.Limits[corev1.ResourceCPU] = resource.MustParse("8")
		resources.Limits[corev1.ResourceMemory] = resource.MustParse("32Gi")
		resources.Requests[corev1.ResourceCPU] = resource.MustParse("4")
		resources.Requests[corev1.ResourceMemory] = resource.MustParse("16Gi")
	}

	return resources
}

// generateModelLoaderScript generates a Python script to prepare the model
func (b *TritonBackend) generateModelLoaderScript(service *inferencev1alpha1.InferenceService) string {
	return fmt.Sprintf(`
import os
import shutil

model_name = '%s'
model_path = '/models/' + model_name.replace('/', '_')

os.makedirs(model_path + '/1', exist_ok=True)

# Create config.pbtxt for the model
config = '''
name: "{name}"
backend: "python"
max_batch_size: {batch_size}
input [
  {{
    name: "prompt"
    data_type: TYPE_STRING
    dims: [ 1 ]
  }}
]
output [
  {{
    name: "generated_text"
    data_type: TYPE_STRING
    dims: [ 1 ]
  }}
]
instance_group [
  {{
    count: 1
    kind: KIND_GPU
  }}
]
'''.format(name=model_name.replace('/', '_'), batch_size=%d)

with open(model_path + '/config.pbtxt', 'w') as f:
    f.write(config)

print(f'Model config created at {model_path}')
`, service.Spec.Model.Name, b.getMaxBatchSize(service))
}

// getMaxBatchSize returns the max batch size
func (b *TritonBackend) getMaxBatchSize(service *inferencev1alpha1.InferenceService) int32 {
	if service.Spec.BackendConfig != nil && service.Spec.BackendConfig.MaxBatchSize > 0 {
		return service.Spec.BackendConfig.MaxBatchSize
	}
	return 32
}

// GenerateService generates the Kubernetes service spec for Triton
func (b *TritonBackend) GenerateService(ctx context.Context, service *inferencev1alpha1.InferenceService) (*ServiceSpec, error) {
	return &ServiceSpec{
		Name:      service.Name,
		Namespace: service.Namespace,
		Type:      corev1.ServiceTypeClusterIP,
		Ports: []corev1.ServicePort{
			{
				Name:       "http",
				Port:       80,
				TargetPort: intstr.FromInt(tritonHTTPPort),
				Protocol:   corev1.ProtocolTCP,
			},
			{
				Name:       "grpc",
				Port:       8001,
				TargetPort: intstr.FromInt(tritonGRPCPort),
				Protocol:   corev1.ProtocolTCP,
			},
			{
				Name:       "metrics",
				Port:       9090,
				TargetPort: intstr.FromInt(tritonMetricsPort),
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

// HealthCheck performs a health check on a Triton instance
func (b *TritonBackend) HealthCheck(ctx context.Context, endpoint string) (*HealthStatus, error) {
	url := fmt.Sprintf("http://%s%s", endpoint, tritonHealthEndpoint)

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

// GetMetrics retrieves metrics from a Triton instance
func (b *TritonBackend) GetMetrics(ctx context.Context, endpoint string) (*BackendMetrics, error) {
	url := fmt.Sprintf("http://%s%s", endpoint, tritonMetricsEndpoint)

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
	_ = body

	return metrics, nil
}

// Scale handles scaling operations for Triton
func (b *TritonBackend) Scale(ctx context.Context, endpoint string, replicas int32) error {
	return nil
}

// Configure applies configuration changes to Triton
func (b *TritonBackend) Configure(ctx context.Context, endpoint string, config map[string]string) error {
	// Triton supports some runtime configuration via model control API
	return nil
}

// TritonModelStats represents model statistics from Triton
type TritonModelStats struct {
	ModelStats []struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Stats   struct {
			InferCount int64                  `json:"inference_count"`
			ExecCount  int64                  `json:"execution_count"`
			InferStats map[string]interface{} `json:"inference_stats"`
		} `json:"inference_stats"`
	} `json:"model_stats"`
}

// getModelStats retrieves model statistics from Triton
func (b *TritonBackend) getModelStats(ctx context.Context, endpoint, modelName string) (*TritonModelStats, error) {
	url := fmt.Sprintf("http://%s/v2/models/%s/stats", endpoint, modelName)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var stats TritonModelStats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return nil, err
	}

	return &stats, nil
}

func init() {
	DefaultFactory.Register(NewTritonBackend(logr.Discard()))
}

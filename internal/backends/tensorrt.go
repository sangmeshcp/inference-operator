package backends

import (
	"context"
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
	tensorrtDefaultImage    = "nvcr.io/nvidia/tritonserver:24.01-trtllm-python-py3"
	tensorrtHTTPPort        = 8000
	tensorrtGRPCPort        = 8001
	tensorrtMetricsPort     = 8002
	tensorrtHealthEndpoint  = "/v2/health/ready"
	tensorrtMetricsEndpoint = "/metrics"
)

// TensorRTBackend implements the Backend interface for TensorRT-LLM
type TensorRTBackend struct {
	log        logr.Logger
	httpClient *http.Client
}

// NewTensorRTBackend creates a new TensorRT-LLM backend
func NewTensorRTBackend(log logr.Logger) *TensorRTBackend {
	return &TensorRTBackend{
		log: log.WithName("tensorrt"),
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Name returns the backend name
func (b *TensorRTBackend) Name() string {
	return "tensorrt-llm"
}

// Type returns the backend type
func (b *TensorRTBackend) Type() inferencev1alpha1.BackendType {
	return inferencev1alpha1.BackendTypeTensorRT
}

// Version returns the backend version
func (b *TensorRTBackend) Version() string {
	return "0.8.0"
}

// Validate validates the service configuration
func (b *TensorRTBackend) Validate(ctx context.Context, service *inferencev1alpha1.InferenceService) error {
	if service.Spec.Model.Name == "" {
		return fmt.Errorf("model name is required")
	}

	if service.Spec.GPU.Count < 1 {
		return fmt.Errorf("at least 1 GPU is required for TensorRT-LLM")
	}

	// TensorRT-LLM requires tensor parallelism to match GPU count for multi-GPU
	if service.Spec.BackendConfig != nil {
		tp := service.Spec.BackendConfig.TensorParallelism
		if tp > 0 && int32(service.Spec.GPU.Count) < tp {
			return fmt.Errorf("tensor parallelism (%d) cannot exceed GPU count (%d)", tp, service.Spec.GPU.Count)
		}
	}

	return nil
}

// GenerateDeployment generates the deployment spec for TensorRT-LLM
func (b *TensorRTBackend) GenerateDeployment(ctx context.Context, service *inferencev1alpha1.InferenceService) (*DeploymentSpec, error) {
	if err := b.Validate(ctx, service); err != nil {
		return nil, err
	}

	args := b.buildArgs(service)
	env := b.buildEnv(service)
	resources := b.buildResources(service)

	spec := &DeploymentSpec{
		Name:      service.Name,
		Namespace: service.Namespace,
		Image:     tensorrtDefaultImage,
		Command:   []string{"python3"},
		Args:      args,
		Env:       env,
		Ports: []corev1.ContainerPort{
			{
				Name:          "http",
				ContainerPort: tensorrtHTTPPort,
				Protocol:      corev1.ProtocolTCP,
			},
			{
				Name:          "grpc",
				ContainerPort: tensorrtGRPCPort,
				Protocol:      corev1.ProtocolTCP,
			},
			{
				Name:          "metrics",
				ContainerPort: tensorrtMetricsPort,
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
			"inference.ai/backend":         "tensorrt-llm",
		},
		Annotations: map[string]string{
			"inference.ai/model": service.Spec.Model.Name,
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: tensorrtHealthEndpoint,
					Port: intstr.FromInt(tensorrtHTTPPort),
				},
			},
			InitialDelaySeconds: 60,
			PeriodSeconds:       10,
			TimeoutSeconds:      5,
			FailureThreshold:    3,
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/v2/health/live",
					Port: intstr.FromInt(tensorrtHTTPPort),
				},
			},
			InitialDelaySeconds: 120,
			PeriodSeconds:       30,
			TimeoutSeconds:      10,
			FailureThreshold:    3,
		},
		StartupProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: tensorrtHealthEndpoint,
					Port: intstr.FromInt(tensorrtHTTPPort),
				},
			},
			InitialDelaySeconds: 30,
			PeriodSeconds:       15,
			TimeoutSeconds:      5,
			FailureThreshold:    80, // Allow 20 minutes for engine build and model loading
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
			Name: "model-cache",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: "engine-cache",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: "shm",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					Medium:    corev1.StorageMediumMemory,
					SizeLimit: resource.NewQuantity(16*1024*1024*1024, resource.BinarySI), // 16Gi
				},
			},
		},
	}

	spec.VolumeMounts = []corev1.VolumeMount{
		{
			Name:      "model-cache",
			MountPath: "/models",
		},
		{
			Name:      "engine-cache",
			MountPath: "/engines",
		},
		{
			Name:      "shm",
			MountPath: "/dev/shm",
		},
	}

	// Add init container for engine building
	spec.InitContainers = []corev1.Container{
		b.buildEngineBuilderContainer(service, env),
	}

	// Security context for GPU access
	privileged := true
	spec.SecurityContext = &corev1.PodSecurityContext{
		RunAsUser:  new(int64),
		RunAsGroup: new(int64),
	}

	// Add capabilities for CUDA
	spec.Annotations["container.apparmor.security.beta.kubernetes.io/"+service.Name] = "unconfined"
	_ = privileged

	return spec, nil
}

// buildArgs builds command line arguments for TensorRT-LLM server
func (b *TensorRTBackend) buildArgs(service *inferencev1alpha1.InferenceService) []string {
	args := []string{
		"-m", "tensorrt_llm_triton_backend.server",
		"--model-repository=/engines",
		"--http-port=" + strconv.Itoa(tensorrtHTTPPort),
		"--grpc-port=" + strconv.Itoa(tensorrtGRPCPort),
		"--metrics-port=" + strconv.Itoa(tensorrtMetricsPort),
	}

	return args
}

// buildEnv builds environment variables for TensorRT-LLM
func (b *TensorRTBackend) buildEnv(service *inferencev1alpha1.InferenceService) []corev1.EnvVar {
	env := []corev1.EnvVar{
		{
			Name:  "CUDA_VISIBLE_DEVICES",
			Value: "all",
		},
		{
			Name:  "NCCL_DEBUG",
			Value: "WARN",
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

	// Tensor parallelism
	tp := int32(1)
	if service.Spec.BackendConfig != nil && service.Spec.BackendConfig.TensorParallelism > 0 {
		tp = service.Spec.BackendConfig.TensorParallelism
	}
	env = append(env, corev1.EnvVar{
		Name:  "TENSOR_PARALLEL_SIZE",
		Value: strconv.Itoa(int(tp)),
	})

	// Pipeline parallelism
	pp := int32(1)
	if service.Spec.BackendConfig != nil && service.Spec.BackendConfig.PipelineParallelism > 0 {
		pp = service.Spec.BackendConfig.PipelineParallelism
	}
	env = append(env, corev1.EnvVar{
		Name:  "PIPELINE_PARALLEL_SIZE",
		Value: strconv.Itoa(int(pp)),
	})

	return env
}

// buildResources builds resource requirements for TensorRT-LLM
func (b *TensorRTBackend) buildResources(service *inferencev1alpha1.InferenceService) corev1.ResourceRequirements {
	resources := corev1.ResourceRequirements{
		Limits:   corev1.ResourceList{},
		Requests: corev1.ResourceList{},
	}

	// GPU resources
	gpuQuantity := resource.NewQuantity(int64(service.Spec.GPU.Count), resource.DecimalSI)
	resources.Limits["nvidia.com/gpu"] = *gpuQuantity
	resources.Requests["nvidia.com/gpu"] = *gpuQuantity

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
		// TensorRT-LLM needs more resources for engine building
		resources.Limits[corev1.ResourceCPU] = resource.MustParse("16")
		resources.Limits[corev1.ResourceMemory] = resource.MustParse("64Gi")
		resources.Requests[corev1.ResourceCPU] = resource.MustParse("8")
		resources.Requests[corev1.ResourceMemory] = resource.MustParse("32Gi")
	}

	return resources
}

// buildEngineBuilderContainer creates the init container for building TensorRT engines
func (b *TensorRTBackend) buildEngineBuilderContainer(service *inferencev1alpha1.InferenceService, env []corev1.EnvVar) corev1.Container {
	tp := int32(1)
	pp := int32(1)
	maxSeqLen := int32(4096)
	maxBatchSize := int32(32)

	if service.Spec.BackendConfig != nil {
		if service.Spec.BackendConfig.TensorParallelism > 0 {
			tp = service.Spec.BackendConfig.TensorParallelism
		}
		if service.Spec.BackendConfig.PipelineParallelism > 0 {
			pp = service.Spec.BackendConfig.PipelineParallelism
		}
		if service.Spec.BackendConfig.MaxSeqLen > 0 {
			maxSeqLen = service.Spec.BackendConfig.MaxSeqLen
		}
		if service.Spec.BackendConfig.MaxBatchSize > 0 {
			maxBatchSize = service.Spec.BackendConfig.MaxBatchSize
		}
	}

	quantization := "float16"
	if service.Spec.BackendConfig != nil && service.Spec.BackendConfig.Quantization != "" {
		quantization = service.Spec.BackendConfig.Quantization
	}

	buildScript := fmt.Sprintf(`
set -e

MODEL_NAME="%s"
TP_SIZE=%d
PP_SIZE=%d
MAX_SEQ_LEN=%d
MAX_BATCH_SIZE=%d
QUANTIZATION="%s"

echo "Building TensorRT-LLM engine for $MODEL_NAME"
echo "TP=$TP_SIZE, PP=$PP_SIZE, MaxSeqLen=$MAX_SEQ_LEN, MaxBatch=$MAX_BATCH_SIZE"

# Download model if from HuggingFace
if [[ "$MODEL_NAME" == *"/"* ]]; then
    echo "Downloading model from HuggingFace..."
    python3 -c "
from huggingface_hub import snapshot_download
import os
token = os.environ.get('HF_TOKEN', None)
snapshot_download(repo_id='$MODEL_NAME', local_dir='/models/hf_model', token=token)
"
    MODEL_PATH="/models/hf_model"
else
    MODEL_PATH="$MODEL_NAME"
fi

# Convert to TensorRT-LLM format
echo "Converting model to TensorRT-LLM format..."
python3 /opt/tensorrtllm_backend/scripts/convert_checkpoint.py \
    --model_dir "$MODEL_PATH" \
    --output_dir /models/trt_ckpt \
    --dtype $QUANTIZATION \
    --tp_size $TP_SIZE \
    --pp_size $PP_SIZE

# Build TensorRT engine
echo "Building TensorRT engine..."
trtllm-build \
    --checkpoint_dir /models/trt_ckpt \
    --output_dir /engines/model \
    --max_batch_size $MAX_BATCH_SIZE \
    --max_input_len $MAX_SEQ_LEN \
    --max_output_len $MAX_SEQ_LEN \
    --gemm_plugin auto \
    --gpt_attention_plugin auto

echo "Engine build complete!"
`, service.Spec.Model.Name, tp, pp, maxSeqLen, maxBatchSize, quantization)

	resources := corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			"nvidia.com/gpu":      resource.MustParse(strconv.Itoa(int(service.Spec.GPU.Count))),
			corev1.ResourceCPU:    resource.MustParse("16"),
			corev1.ResourceMemory: resource.MustParse("128Gi"),
		},
		Requests: corev1.ResourceList{
			"nvidia.com/gpu":      resource.MustParse(strconv.Itoa(int(service.Spec.GPU.Count))),
			corev1.ResourceCPU:    resource.MustParse("8"),
			corev1.ResourceMemory: resource.MustParse("64Gi"),
		},
	}

	return corev1.Container{
		Name:      "engine-builder",
		Image:     tensorrtDefaultImage,
		Command:   []string{"bash", "-c"},
		Args:      []string{buildScript},
		Env:       env,
		Resources: resources,
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "model-cache",
				MountPath: "/models",
			},
			{
				Name:      "engine-cache",
				MountPath: "/engines",
			},
			{
				Name:      "shm",
				MountPath: "/dev/shm",
			},
		},
	}
}

// GenerateService generates the Kubernetes service spec for TensorRT-LLM
func (b *TensorRTBackend) GenerateService(ctx context.Context, service *inferencev1alpha1.InferenceService) (*ServiceSpec, error) {
	return &ServiceSpec{
		Name:      service.Name,
		Namespace: service.Namespace,
		Type:      corev1.ServiceTypeClusterIP,
		Ports: []corev1.ServicePort{
			{
				Name:       "http",
				Port:       80,
				TargetPort: intstr.FromInt(tensorrtHTTPPort),
				Protocol:   corev1.ProtocolTCP,
			},
			{
				Name:       "grpc",
				Port:       8001,
				TargetPort: intstr.FromInt(tensorrtGRPCPort),
				Protocol:   corev1.ProtocolTCP,
			},
			{
				Name:       "metrics",
				Port:       9090,
				TargetPort: intstr.FromInt(tensorrtMetricsPort),
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

// HealthCheck performs a health check on a TensorRT-LLM instance
func (b *TensorRTBackend) HealthCheck(ctx context.Context, endpoint string) (*HealthStatus, error) {
	url := fmt.Sprintf("http://%s%s", endpoint, tensorrtHealthEndpoint)

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

// GetMetrics retrieves metrics from a TensorRT-LLM instance
func (b *TensorRTBackend) GetMetrics(ctx context.Context, endpoint string) (*BackendMetrics, error) {
	url := fmt.Sprintf("http://%s%s", endpoint, tensorrtMetricsEndpoint)

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

	_ = body

	return metrics, nil
}

// Scale handles scaling operations for TensorRT-LLM
func (b *TensorRTBackend) Scale(ctx context.Context, endpoint string, replicas int32) error {
	return nil
}

// Configure applies configuration changes to TensorRT-LLM
func (b *TensorRTBackend) Configure(ctx context.Context, endpoint string, config map[string]string) error {
	return nil
}

func init() {
	DefaultFactory.Register(NewTensorRTBackend(logr.Discard()))
}

package backends

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	inferencev1alpha1 "github.com/inference-operator/inference-operator/api/v1alpha1"
)

// Backend defines the interface for inference backends
type Backend interface {
	// Name returns the backend name
	Name() string

	// Type returns the backend type
	Type() inferencev1alpha1.BackendType

	// Version returns the backend version
	Version() string

	// GenerateDeployment generates Kubernetes deployment spec for the backend
	GenerateDeployment(ctx context.Context, service *inferencev1alpha1.InferenceService) (*DeploymentSpec, error)

	// GenerateService generates Kubernetes service spec for the backend
	GenerateService(ctx context.Context, service *inferencev1alpha1.InferenceService) (*ServiceSpec, error)

	// HealthCheck performs a health check on a running instance
	HealthCheck(ctx context.Context, endpoint string) (*HealthStatus, error)

	// GetMetrics retrieves metrics from a running instance
	GetMetrics(ctx context.Context, endpoint string) (*BackendMetrics, error)

	// Scale handles scaling operations
	Scale(ctx context.Context, endpoint string, replicas int32) error

	// Configure applies configuration changes to running instances
	Configure(ctx context.Context, endpoint string, config map[string]string) error

	// Validate validates the service configuration for this backend
	Validate(ctx context.Context, service *inferencev1alpha1.InferenceService) error
}

// DeploymentSpec contains the specification for creating a deployment
type DeploymentSpec struct {
	// Name is the deployment name
	Name string

	// Namespace is the target namespace
	Namespace string

	// Image is the container image
	Image string

	// Command is the container command
	Command []string

	// Args are the container arguments
	Args []string

	// Env are environment variables
	Env []corev1.EnvVar

	// Ports are container ports
	Ports []corev1.ContainerPort

	// Resources are resource requirements
	Resources corev1.ResourceRequirements

	// VolumeMounts are volume mounts
	VolumeMounts []corev1.VolumeMount

	// Volumes are volumes
	Volumes []corev1.Volume

	// NodeSelector is the node selector
	NodeSelector map[string]string

	// Tolerations are pod tolerations
	Tolerations []corev1.Toleration

	// Replicas is the number of replicas
	Replicas int32

	// Labels are pod labels
	Labels map[string]string

	// Annotations are pod annotations
	Annotations map[string]string

	// ServiceAccountName is the service account
	ServiceAccountName string

	// InitContainers are init containers
	InitContainers []corev1.Container

	// ReadinessProbe is the readiness probe
	ReadinessProbe *corev1.Probe

	// LivenessProbe is the liveness probe
	LivenessProbe *corev1.Probe

	// StartupProbe is the startup probe
	StartupProbe *corev1.Probe

	// SecurityContext is the security context
	SecurityContext *corev1.PodSecurityContext
}

// ServiceSpec contains the specification for creating a service
type ServiceSpec struct {
	// Name is the service name
	Name string

	// Namespace is the target namespace
	Namespace string

	// Type is the service type
	Type corev1.ServiceType

	// Ports are service ports
	Ports []corev1.ServicePort

	// Selector are pod selectors
	Selector map[string]string

	// Labels are service labels
	Labels map[string]string

	// Annotations are service annotations
	Annotations map[string]string
}

// HealthStatus represents the health status of a backend instance
type HealthStatus struct {
	// Healthy indicates if the instance is healthy
	Healthy bool

	// Ready indicates if the instance is ready to serve traffic
	Ready bool

	// Message contains additional status information
	Message string

	// LastChecked is the timestamp of the last health check
	LastChecked time.Time

	// ModelLoaded indicates if the model is loaded
	ModelLoaded bool

	// GPUAvailable indicates if GPU is available
	GPUAvailable bool

	// Details contains backend-specific details
	Details map[string]string
}

// BackendMetrics contains metrics from a backend instance
type BackendMetrics struct {
	// RequestsTotal is the total number of requests
	RequestsTotal int64

	// RequestsPerSecond is the current request rate
	RequestsPerSecond float64

	// AverageLatency is the average request latency
	AverageLatency time.Duration

	// P50Latency is the 50th percentile latency
	P50Latency time.Duration

	// P90Latency is the 90th percentile latency
	P90Latency time.Duration

	// P99Latency is the 99th percentile latency
	P99Latency time.Duration

	// QueueDepth is the current request queue depth
	QueueDepth int32

	// BatchSize is the current/average batch size
	BatchSize float64

	// TokensPerSecond is the token generation rate
	TokensPerSecond float64

	// GPUUtilization is the GPU utilization percentage
	GPUUtilization float64

	// GPUMemoryUsed is the GPU memory used
	GPUMemoryUsed resource.Quantity

	// GPUMemoryTotal is the total GPU memory
	GPUMemoryTotal resource.Quantity

	// CacheHitRate is the KV cache hit rate
	CacheHitRate float64

	// ActiveRequests is the number of active requests
	ActiveRequests int32

	// PendingRequests is the number of pending requests
	PendingRequests int32

	// Errors is the error count
	Errors int64

	// Custom contains backend-specific metrics
	Custom map[string]float64
}

// InferenceRequest represents an inference request
type InferenceRequest struct {
	// RequestID is a unique request identifier
	RequestID string

	// Prompt is the input prompt
	Prompt string

	// MaxTokens is the maximum tokens to generate
	MaxTokens int32

	// Temperature is the sampling temperature
	Temperature float32

	// TopP is the nucleus sampling parameter
	TopP float32

	// TopK is the top-k sampling parameter
	TopK int32

	// StopSequences are sequences to stop generation
	StopSequences []string

	// Stream indicates if streaming is enabled
	Stream bool

	// Metadata contains additional metadata
	Metadata map[string]string
}

// InferenceResponse represents an inference response
type InferenceResponse struct {
	// RequestID is the request identifier
	RequestID string

	// Text is the generated text
	Text string

	// Tokens is the number of generated tokens
	Tokens int32

	// FinishReason is the reason for finishing
	FinishReason string

	// Latency is the total latency
	Latency time.Duration

	// TimeToFirstToken is the time to first token
	TimeToFirstToken time.Duration

	// Usage contains token usage information
	Usage *TokenUsage

	// Error contains any error
	Error error
}

// TokenUsage contains token usage information
type TokenUsage struct {
	// PromptTokens is the number of prompt tokens
	PromptTokens int32

	// CompletionTokens is the number of completion tokens
	CompletionTokens int32

	// TotalTokens is the total tokens
	TotalTokens int32

	// CacheHits is the number of cache hits
	CacheHits int32
}

// BackendFactory creates backend instances
type BackendFactory struct {
	backends map[inferencev1alpha1.BackendType]Backend
}

// NewBackendFactory creates a new backend factory
func NewBackendFactory() *BackendFactory {
	return &BackendFactory{
		backends: make(map[inferencev1alpha1.BackendType]Backend),
	}
}

// Register registers a backend
func (f *BackendFactory) Register(backend Backend) {
	f.backends[backend.Type()] = backend
}

// Get retrieves a backend by type
func (f *BackendFactory) Get(backendType inferencev1alpha1.BackendType) (Backend, bool) {
	backend, exists := f.backends[backendType]
	return backend, exists
}

// List returns all registered backends
func (f *BackendFactory) List() []Backend {
	backends := make([]Backend, 0, len(f.backends))
	for _, b := range f.backends {
		backends = append(backends, b)
	}
	return backends
}

// DefaultFactory is the default backend factory with all backends registered
var DefaultFactory *BackendFactory

func init() {
	DefaultFactory = NewBackendFactory()
	// Backends are registered in their respective files
}

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// GPUSharingMode defines the GPU sharing strategy
// +kubebuilder:validation:Enum=mig;mps;timeslice;exclusive
type GPUSharingMode string

const (
	GPUSharingModeMIG       GPUSharingMode = "mig"
	GPUSharingModeMPS       GPUSharingMode = "mps"
	GPUSharingModeTimeslice GPUSharingMode = "timeslice"
	GPUSharingModeExclusive GPUSharingMode = "exclusive"
)

// BackendType defines the inference backend to use
// +kubebuilder:validation:Enum=vllm;triton;tensorrt
type BackendType string

const (
	BackendTypeVLLM     BackendType = "vllm"
	BackendTypeTriton   BackendType = "triton"
	BackendTypeTensorRT BackendType = "tensorrt"
)

// CacheStrategy defines the caching strategy
// +kubebuilder:validation:Enum=hybrid;prefix;distributed;local;none
type CacheStrategy string

const (
	CacheStrategyHybrid      CacheStrategy = "hybrid"
	CacheStrategyPrefix      CacheStrategy = "prefix"
	CacheStrategyDistributed CacheStrategy = "distributed"
	CacheStrategyLocal       CacheStrategy = "local"
	CacheStrategyNone        CacheStrategy = "none"
)

// ModelSource defines where the model is loaded from
// +kubebuilder:validation:Enum=huggingface;s3;gcs;pvc;local
type ModelSource string

const (
	ModelSourceHuggingFace ModelSource = "huggingface"
	ModelSourceS3          ModelSource = "s3"
	ModelSourceGCS         ModelSource = "gcs"
	ModelSourcePVC         ModelSource = "pvc"
	ModelSourceLocal       ModelSource = "local"
)

// ModelSpec defines the model configuration
type ModelSpec struct {
	// Name is the model identifier (e.g., "meta-llama/Llama-2-70b")
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Source specifies where to load the model from
	// +kubebuilder:default=huggingface
	Source ModelSource `json:"source,omitempty"`

	// Revision is the model version or commit hash
	// +optional
	Revision string `json:"revision,omitempty"`

	// SecretRef references credentials for model download
	// +optional
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`

	// StoragePath is the path where the model is stored (for pvc/local sources)
	// +optional
	StoragePath string `json:"storagePath,omitempty"`
}

// GPUSpec defines GPU requirements and sharing configuration
type GPUSpec struct {
	// Type specifies the GPU type (e.g., "nvidia-a100", "nvidia-h100")
	// +kubebuilder:validation:Required
	Type string `json:"type"`

	// Count is the number of GPUs per replica
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	Count int32 `json:"count,omitempty"`

	// Sharing defines the GPU sharing mode
	// +kubebuilder:default=exclusive
	Sharing GPUSharingMode `json:"sharing,omitempty"`

	// MIGProfile specifies the MIG profile (e.g., "3g.40gb", "1g.10gb")
	// Only used when Sharing is "mig"
	// +optional
	MIGProfile string `json:"migProfile,omitempty"`

	// Memory is the GPU memory requirement per GPU
	// +optional
	Memory *resource.Quantity `json:"memory,omitempty"`

	// GPUPoolRef references a GPUPool for allocation
	// +optional
	GPUPoolRef *corev1.LocalObjectReference `json:"gpuPoolRef,omitempty"`
}

// CacheSpec defines caching configuration
type CacheSpec struct {
	// Enabled determines if caching is enabled
	// +kubebuilder:default=true
	Enabled bool `json:"enabled,omitempty"`

	// Strategy specifies the caching strategy
	// +kubebuilder:default=hybrid
	Strategy CacheStrategy `json:"strategy,omitempty"`

	// PrefixCacheSize is the size of the GPU prefix cache
	// +optional
	PrefixCacheSize *resource.Quantity `json:"prefixCacheSize,omitempty"`

	// TokenCacheRef references a TokenCache resource
	// +optional
	TokenCacheRef *corev1.LocalObjectReference `json:"tokenCacheRef,omitempty"`

	// TTL is the default cache entry time-to-live
	// +optional
	TTL *metav1.Duration `json:"ttl,omitempty"`
}

// AutoscalingMetric defines a metric for autoscaling
type AutoscalingMetric struct {
	// Type is the metric type
	// +kubebuilder:validation:Enum=gpu-utilization;queue-depth;requests-per-second;latency-p99
	Type string `json:"type"`

	// Target is the target value for the metric
	// +kubebuilder:validation:Minimum=1
	Target int32 `json:"target"`
}

// AutoscalingSpec defines autoscaling configuration
type AutoscalingSpec struct {
	// MinReplicas is the minimum number of replicas
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=1
	MinReplicas int32 `json:"minReplicas,omitempty"`

	// MaxReplicas is the maximum number of replicas
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=10
	MaxReplicas int32 `json:"maxReplicas,omitempty"`

	// Metrics defines the metrics used for autoscaling decisions
	// +optional
	Metrics []AutoscalingMetric `json:"metrics,omitempty"`

	// ScaleDownStabilizationSeconds is the stabilization window for scale down
	// +kubebuilder:default=300
	ScaleDownStabilizationSeconds int32 `json:"scaleDownStabilizationSeconds,omitempty"`

	// ScaleUpStabilizationSeconds is the stabilization window for scale up
	// +kubebuilder:default=60
	ScaleUpStabilizationSeconds int32 `json:"scaleUpStabilizationSeconds,omitempty"`
}

// BackendConfig holds backend-specific configuration
type BackendConfig struct {
	// MaxBatchSize is the maximum batch size for inference
	// +kubebuilder:default=32
	MaxBatchSize int32 `json:"maxBatchSize,omitempty"`

	// MaxSeqLen is the maximum sequence length
	// +kubebuilder:default=4096
	MaxSeqLen int32 `json:"maxSeqLen,omitempty"`

	// TensorParallelism is the tensor parallelism degree
	// +kubebuilder:default=1
	TensorParallelism int32 `json:"tensorParallelism,omitempty"`

	// PipelineParallelism is the pipeline parallelism degree
	// +kubebuilder:default=1
	PipelineParallelism int32 `json:"pipelineParallelism,omitempty"`

	// Quantization specifies the quantization method (e.g., "awq", "gptq", "none")
	// +optional
	Quantization string `json:"quantization,omitempty"`

	// ExtraArgs are additional arguments passed to the backend
	// +optional
	ExtraArgs map[string]string `json:"extraArgs,omitempty"`
}

// InferenceServiceSpec defines the desired state of InferenceService
type InferenceServiceSpec struct {
	// Model specifies the model to serve
	// +kubebuilder:validation:Required
	Model ModelSpec `json:"model"`

	// Backend specifies the inference backend to use
	// +kubebuilder:default=vllm
	Backend BackendType `json:"backend,omitempty"`

	// BackendConfig holds backend-specific configuration
	// +optional
	BackendConfig *BackendConfig `json:"backendConfig,omitempty"`

	// Replicas is the desired number of replicas
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=1
	Replicas int32 `json:"replicas,omitempty"`

	// GPU specifies GPU requirements
	// +kubebuilder:validation:Required
	GPU GPUSpec `json:"gpu"`

	// Cache specifies caching configuration
	// +optional
	Cache *CacheSpec `json:"cache,omitempty"`

	// Autoscaling specifies autoscaling configuration
	// +optional
	Autoscaling *AutoscalingSpec `json:"autoscaling,omitempty"`

	// Resources specifies CPU and memory resources
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// NodeSelector for scheduling pods
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Tolerations for scheduling pods
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// ServiceAccountName is the service account for the pods
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// ImagePullSecrets for pulling container images
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
}

// InferenceServicePhase represents the phase of the InferenceService
// +kubebuilder:validation:Enum=Pending;Initializing;Running;Scaling;Failed;Terminating
type InferenceServicePhase string

const (
	InferenceServicePhasePending      InferenceServicePhase = "Pending"
	InferenceServicePhaseInitializing InferenceServicePhase = "Initializing"
	InferenceServicePhaseRunning      InferenceServicePhase = "Running"
	InferenceServicePhaseScaling      InferenceServicePhase = "Scaling"
	InferenceServicePhaseFailed       InferenceServicePhase = "Failed"
	InferenceServicePhaseTerminating  InferenceServicePhase = "Terminating"
)

// InferenceServiceStatus defines the observed state of InferenceService
type InferenceServiceStatus struct {
	// Phase is the current phase of the InferenceService
	Phase InferenceServicePhase `json:"phase,omitempty"`

	// ReadyReplicas is the number of ready replicas
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// AvailableReplicas is the number of available replicas
	AvailableReplicas int32 `json:"availableReplicas,omitempty"`

	// URL is the endpoint URL for the inference service
	URL string `json:"url,omitempty"`

	// GPUAllocations lists the allocated GPU resources
	GPUAllocations []GPUAllocation `json:"gpuAllocations,omitempty"`

	// CacheStatus contains cache-related status information
	CacheStatus *CacheStatus `json:"cacheStatus,omitempty"`

	// Conditions represent the latest available observations
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// LastScaleTime is the last time the service was scaled
	LastScaleTime *metav1.Time `json:"lastScaleTime,omitempty"`

	// ObservedGeneration is the most recent generation observed
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// GPUAllocation represents a GPU resource allocation
type GPUAllocation struct {
	// NodeName is the node where the GPU is allocated
	NodeName string `json:"nodeName"`

	// DeviceID is the GPU device identifier
	DeviceID string `json:"deviceId"`

	// MIGInstance is the MIG instance identifier (if applicable)
	MIGInstance string `json:"migInstance,omitempty"`

	// MemoryAllocated is the amount of GPU memory allocated
	MemoryAllocated resource.Quantity `json:"memoryAllocated,omitempty"`

	// Utilization is the current GPU utilization percentage
	Utilization int32 `json:"utilization,omitempty"`
}

// CacheStatus contains cache-related status information
type CacheStatus struct {
	// HitRate is the cache hit rate
	HitRate float64 `json:"hitRate,omitempty"`

	// Size is the current cache size
	Size resource.Quantity `json:"size,omitempty"`

	// Entries is the number of cache entries
	Entries int64 `json:"entries,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:scale:specpath=.spec.replicas,statuspath=.status.readyReplicas
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.spec.model.name`
// +kubebuilder:printcolumn:name="Backend",type=string,JSONPath=`.spec.backend`
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=`.spec.replicas`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// InferenceService is the Schema for the inferenceservices API
type InferenceService struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InferenceServiceSpec   `json:"spec,omitempty"`
	Status InferenceServiceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// InferenceServiceList contains a list of InferenceService
type InferenceServiceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InferenceService `json:"items"`
}

func init() {
	SchemeBuilder.Register(&InferenceService{}, &InferenceServiceList{})
}

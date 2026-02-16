package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MIGProfileSpec defines a MIG profile configuration
type MIGProfileSpec struct {
	// Name is the MIG profile name (e.g., "3g.40gb", "1g.10gb")
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^\d+g\.\d+gb$`
	Name string `json:"name"`

	// Count is the number of instances to create
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	Count int32 `json:"count,omitempty"`
}

// GPUSharingSpec defines GPU sharing configuration
type GPUSharingSpec struct {
	// Mode specifies the GPU sharing mode
	// +kubebuilder:validation:Enum=mig;mps;timeslice;exclusive
	// +kubebuilder:default=exclusive
	Mode GPUSharingMode `json:"mode,omitempty"`

	// Profiles defines MIG profiles (only used when Mode is "mig")
	// +optional
	Profiles []MIGProfileSpec `json:"profiles,omitempty"`

	// TimeSliceInterval is the time slice duration in milliseconds (only used when Mode is "timeslice")
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=100
	TimeSliceInterval int32 `json:"timeSliceInterval,omitempty"`

	// MPSActiveThreadPercentage limits GPU thread usage per client (only used when Mode is "mps")
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +kubebuilder:default=100
	MPSActiveThreadPercentage int32 `json:"mpsActiveThreadPercentage,omitempty"`

	// MPSMemoryLimit limits GPU memory per client (only used when Mode is "mps")
	// +optional
	MPSMemoryLimit *resource.Quantity `json:"mpsMemoryLimit,omitempty"`
}

// OversubscriptionSpec defines GPU oversubscription configuration
type OversubscriptionSpec struct {
	// Enabled determines if oversubscription is enabled
	// +kubebuilder:default=false
	Enabled bool `json:"enabled,omitempty"`

	// Ratio is the oversubscription ratio (e.g., 1.5 means 150% allocation)
	// +kubebuilder:default="1.0"
	Ratio string `json:"ratio,omitempty"`

	// MemoryOvercommitRatio allows memory overcommit separate from compute
	// +optional
	MemoryOvercommitRatio string `json:"memoryOvercommitRatio,omitempty"`
}

// GPUNodeStatus represents the GPU status on a specific node
type GPUNodeStatus struct {
	// NodeName is the name of the node
	NodeName string `json:"nodeName"`

	// TotalGPUs is the total number of GPUs on the node
	TotalGPUs int32 `json:"totalGPUs"`

	// AvailableGPUs is the number of available GPUs
	AvailableGPUs int32 `json:"availableGPUs"`

	// GPUs contains status of individual GPUs
	GPUs []GPUDeviceStatus `json:"gpus,omitempty"`
}

// GPUDeviceStatus represents the status of a single GPU device
type GPUDeviceStatus struct {
	// DeviceID is the GPU device identifier
	DeviceID string `json:"deviceId"`

	// UUID is the GPU UUID
	UUID string `json:"uuid,omitempty"`

	// Model is the GPU model name
	Model string `json:"model,omitempty"`

	// TotalMemory is the total GPU memory
	TotalMemory resource.Quantity `json:"totalMemory,omitempty"`

	// AvailableMemory is the available GPU memory
	AvailableMemory resource.Quantity `json:"availableMemory,omitempty"`

	// Utilization is the current GPU utilization percentage
	Utilization int32 `json:"utilization,omitempty"`

	// Temperature is the current GPU temperature in Celsius
	Temperature int32 `json:"temperature,omitempty"`

	// PowerUsage is the current power usage in Watts
	PowerUsage int32 `json:"powerUsage,omitempty"`

	// MIGEnabled indicates if MIG is enabled on this GPU
	MIGEnabled bool `json:"migEnabled,omitempty"`

	// MIGInstances contains MIG instance information
	MIGInstances []MIGInstanceStatus `json:"migInstances,omitempty"`

	// Allocations lists current allocations on this GPU
	Allocations []AllocationInfo `json:"allocations,omitempty"`
}

// MIGInstanceStatus represents the status of a MIG instance
type MIGInstanceStatus struct {
	// InstanceID is the MIG instance identifier
	InstanceID string `json:"instanceId"`

	// Profile is the MIG profile name
	Profile string `json:"profile"`

	// Memory is the memory allocated to this instance
	Memory resource.Quantity `json:"memory,omitempty"`

	// ComputeUnits is the number of compute units
	ComputeUnits int32 `json:"computeUnits,omitempty"`

	// InUse indicates if the instance is currently allocated
	InUse bool `json:"inUse,omitempty"`

	// AllocatedTo references the service using this instance
	AllocatedTo string `json:"allocatedTo,omitempty"`
}

// AllocationInfo describes a GPU allocation
type AllocationInfo struct {
	// ServiceRef is a reference to the InferenceService
	ServiceRef string `json:"serviceRef"`

	// Namespace is the namespace of the service
	Namespace string `json:"namespace"`

	// AllocatedMemory is the amount of memory allocated
	AllocatedMemory resource.Quantity `json:"allocatedMemory,omitempty"`

	// AllocatedAt is the time when the allocation was made
	AllocatedAt metav1.Time `json:"allocatedAt,omitempty"`
}

// GPUPoolSpec defines the desired state of GPUPool
type GPUPoolSpec struct {
	// NodeSelector selects nodes to include in the pool
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// GPUType filters GPUs by type (e.g., "nvidia-a100")
	// +optional
	GPUType string `json:"gpuType,omitempty"`

	// Sharing defines GPU sharing configuration
	// +optional
	Sharing *GPUSharingSpec `json:"sharing,omitempty"`

	// Oversubscription defines oversubscription settings
	// +optional
	Oversubscription *OversubscriptionSpec `json:"oversubscription,omitempty"`

	// ReservationPolicy defines how GPUs are reserved
	// +kubebuilder:validation:Enum=best-fit;worst-fit;random
	// +kubebuilder:default=best-fit
	ReservationPolicy string `json:"reservationPolicy,omitempty"`

	// PreemptionPolicy defines preemption behavior
	// +kubebuilder:validation:Enum=none;low-priority;graceful
	// +kubebuilder:default=none
	PreemptionPolicy string `json:"preemptionPolicy,omitempty"`

	// HealthCheckInterval is the interval for GPU health checks
	// +kubebuilder:default="30s"
	HealthCheckInterval *metav1.Duration `json:"healthCheckInterval,omitempty"`
}

// GPUPoolPhase represents the phase of the GPUPool
// +kubebuilder:validation:Enum=Pending;Ready;Degraded;Failed
type GPUPoolPhase string

const (
	GPUPoolPhasePending  GPUPoolPhase = "Pending"
	GPUPoolPhaseReady    GPUPoolPhase = "Ready"
	GPUPoolPhaseDegraded GPUPoolPhase = "Degraded"
	GPUPoolPhaseFailed   GPUPoolPhase = "Failed"
)

// GPUPoolStatus defines the observed state of GPUPool
type GPUPoolStatus struct {
	// Phase is the current phase of the GPUPool
	Phase GPUPoolPhase `json:"phase,omitempty"`

	// TotalGPUs is the total number of GPUs in the pool
	TotalGPUs int32 `json:"totalGPUs,omitempty"`

	// AvailableGPUs is the number of available GPUs
	AvailableGPUs int32 `json:"availableGPUs,omitempty"`

	// AllocatedGPUs is the number of allocated GPUs
	AllocatedGPUs int32 `json:"allocatedGPUs,omitempty"`

	// TotalMemory is the total GPU memory in the pool
	TotalMemory resource.Quantity `json:"totalMemory,omitempty"`

	// AvailableMemory is the available GPU memory
	AvailableMemory resource.Quantity `json:"availableMemory,omitempty"`

	// Nodes contains per-node GPU status
	Nodes []GPUNodeStatus `json:"nodes,omitempty"`

	// Conditions represent the latest available observations
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// LastSyncTime is the last time the pool status was synchronized
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`

	// ObservedGeneration is the most recent generation observed
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.sharing.mode`
// +kubebuilder:printcolumn:name="Total",type=integer,JSONPath=`.status.totalGPUs`
// +kubebuilder:printcolumn:name="Available",type=integer,JSONPath=`.status.availableGPUs`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// GPUPool is the Schema for the gpupools API
type GPUPool struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GPUPoolSpec   `json:"spec,omitempty"`
	Status GPUPoolStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// GPUPoolList contains a list of GPUPool
type GPUPoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GPUPool `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GPUPool{}, &GPUPoolList{})
}

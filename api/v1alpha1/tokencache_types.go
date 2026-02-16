package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CacheTierType defines the type of cache tier
// +kubebuilder:validation:Enum=prefix;local;redis;memcached
type CacheTierType string

const (
	CacheTierTypePrefix    CacheTierType = "prefix"
	CacheTierTypeLocal     CacheTierType = "local"
	CacheTierTypeRedis     CacheTierType = "redis"
	CacheTierTypeMemcached CacheTierType = "memcached"
)

// EvictionPolicy defines the cache eviction policy
// +kubebuilder:validation:Enum=lru;lfu;fifo;ttl
type EvictionPolicy string

const (
	EvictionPolicyLRU  EvictionPolicy = "lru"
	EvictionPolicyLFU  EvictionPolicy = "lfu"
	EvictionPolicyFIFO EvictionPolicy = "fifo"
	EvictionPolicyTTL  EvictionPolicy = "ttl"
)

// CacheTierSpec defines a cache tier configuration
type CacheTierSpec struct {
	// Name is the unique name for this tier
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Type specifies the cache tier type
	// +kubebuilder:validation:Required
	Type CacheTierType `json:"type"`

	// Size is the maximum size for this tier
	// +kubebuilder:validation:Required
	Size resource.Quantity `json:"size"`

	// TTL is the time-to-live for entries in this tier
	// +optional
	TTL *metav1.Duration `json:"ttl,omitempty"`

	// Priority determines the tier priority (lower is higher priority)
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=0
	Priority int32 `json:"priority,omitempty"`

	// RedisConfig contains Redis-specific configuration
	// +optional
	RedisConfig *RedisCacheConfig `json:"redisConfig,omitempty"`

	// LocalConfig contains local cache configuration
	// +optional
	LocalConfig *LocalCacheConfig `json:"localConfig,omitempty"`

	// PrefixConfig contains prefix cache configuration
	// +optional
	PrefixConfig *PrefixCacheConfig `json:"prefixConfig,omitempty"`
}

// RedisCacheConfig defines Redis cache configuration
type RedisCacheConfig struct {
	// Replicas is the number of Redis replicas
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	Replicas int32 `json:"replicas,omitempty"`

	// Mode specifies Redis deployment mode
	// +kubebuilder:validation:Enum=standalone;sentinel;cluster
	// +kubebuilder:default=standalone
	Mode string `json:"mode,omitempty"`

	// ExternalEndpoint specifies an external Redis endpoint
	// +optional
	ExternalEndpoint string `json:"externalEndpoint,omitempty"`

	// PasswordSecretRef references a secret containing the Redis password
	// +optional
	PasswordSecretRef *SecretKeyRef `json:"passwordSecretRef,omitempty"`

	// MaxConnections is the maximum number of connections
	// +kubebuilder:default=100
	MaxConnections int32 `json:"maxConnections,omitempty"`

	// ConnectionTimeout is the connection timeout
	// +kubebuilder:default="5s"
	ConnectionTimeout *metav1.Duration `json:"connectionTimeout,omitempty"`

	// PersistenceEnabled enables Redis persistence
	// +kubebuilder:default=false
	PersistenceEnabled bool `json:"persistenceEnabled,omitempty"`
}

// LocalCacheConfig defines local NVMe cache configuration
type LocalCacheConfig struct {
	// StorageClass is the storage class for the PVC
	// +optional
	StorageClass string `json:"storageClass,omitempty"`

	// MountPath is the mount path for the cache
	// +kubebuilder:default="/cache"
	MountPath string `json:"mountPath,omitempty"`

	// UseRamDisk uses RAM disk instead of persistent storage
	// +kubebuilder:default=false
	UseRamDisk bool `json:"useRamDisk,omitempty"`

	// Compression enables compression for cached data
	// +kubebuilder:default=false
	Compression bool `json:"compression,omitempty"`

	// CompressionAlgorithm specifies the compression algorithm
	// +kubebuilder:validation:Enum=lz4;zstd;snappy
	// +kubebuilder:default=lz4
	CompressionAlgorithm string `json:"compressionAlgorithm,omitempty"`
}

// PrefixCacheConfig defines GPU prefix cache configuration
type PrefixCacheConfig struct {
	// MaxPrefixLength is the maximum prefix length to cache
	// +kubebuilder:default=2048
	MaxPrefixLength int32 `json:"maxPrefixLength,omitempty"`

	// MinPrefixLength is the minimum prefix length to cache
	// +kubebuilder:default=32
	MinPrefixLength int32 `json:"minPrefixLength,omitempty"`

	// BlockSize is the cache block size
	// +kubebuilder:default=16
	BlockSize int32 `json:"blockSize,omitempty"`

	// EnableSwap enables swapping prefix cache to CPU memory
	// +kubebuilder:default=true
	EnableSwap bool `json:"enableSwap,omitempty"`

	// SwapSpaceSize is the size of the swap space
	// +optional
	SwapSpaceSize *resource.Quantity `json:"swapSpaceSize,omitempty"`
}

// SecretKeyRef references a key in a secret
type SecretKeyRef struct {
	// Name is the secret name
	Name string `json:"name"`

	// Key is the key in the secret
	Key string `json:"key"`
}

// WarmupSpec defines cache warmup configuration
type WarmupSpec struct {
	// Enabled determines if cache warmup is enabled
	// +kubebuilder:default=false
	Enabled bool `json:"enabled,omitempty"`

	// CommonPrefixes are common prefixes to preload
	// +optional
	CommonPrefixes []string `json:"commonPrefixes,omitempty"`

	// WarmupSchedule is a cron schedule for cache warmup
	// +optional
	WarmupSchedule string `json:"warmupSchedule,omitempty"`

	// SourceConfigMapRef references a ConfigMap with warmup data
	// +optional
	SourceConfigMapRef *SecretKeyRef `json:"sourceConfigMapRef,omitempty"`
}

// TokenCacheSpec defines the desired state of TokenCache
type TokenCacheSpec struct {
	// Tiers defines the cache tiers
	// +kubebuilder:validation:MinItems=1
	Tiers []CacheTierSpec `json:"tiers"`

	// EvictionPolicy specifies the cache eviction policy
	// +kubebuilder:default=lru
	EvictionPolicy EvictionPolicy `json:"evictionPolicy,omitempty"`

	// Warmup defines cache warmup configuration
	// +optional
	Warmup *WarmupSpec `json:"warmup,omitempty"`

	// CoherencePolicy defines how cache coherence is maintained
	// +kubebuilder:validation:Enum=write-through;write-back;write-around
	// +kubebuilder:default=write-through
	CoherencePolicy string `json:"coherencePolicy,omitempty"`

	// MetricsEnabled enables cache metrics collection
	// +kubebuilder:default=true
	MetricsEnabled bool `json:"metricsEnabled,omitempty"`

	// MetricsPort is the port for metrics endpoint
	// +kubebuilder:default=9090
	MetricsPort int32 `json:"metricsPort,omitempty"`
}

// CacheTierStatus represents the status of a cache tier
type CacheTierStatus struct {
	// Name is the tier name
	Name string `json:"name"`

	// Ready indicates if the tier is ready
	Ready bool `json:"ready,omitempty"`

	// Size is the current size of the tier
	Size resource.Quantity `json:"size,omitempty"`

	// UsedSize is the used size of the tier
	UsedSize resource.Quantity `json:"usedSize,omitempty"`

	// Entries is the number of cache entries
	Entries int64 `json:"entries,omitempty"`

	// HitRate is the cache hit rate for this tier
	HitRate float64 `json:"hitRate,omitempty"`

	// MissRate is the cache miss rate for this tier
	MissRate float64 `json:"missRate,omitempty"`

	// AvgLatency is the average access latency
	AvgLatency *metav1.Duration `json:"avgLatency,omitempty"`

	// Endpoint is the endpoint for distributed tiers
	Endpoint string `json:"endpoint,omitempty"`
}

// TokenCachePhase represents the phase of the TokenCache
// +kubebuilder:validation:Enum=Pending;Initializing;Ready;Degraded;Failed
type TokenCachePhase string

const (
	TokenCachePhasePending      TokenCachePhase = "Pending"
	TokenCachePhaseInitializing TokenCachePhase = "Initializing"
	TokenCachePhaseReady        TokenCachePhase = "Ready"
	TokenCachePhaseDegraded     TokenCachePhase = "Degraded"
	TokenCachePhaseFailed       TokenCachePhase = "Failed"
)

// TokenCacheStatus defines the observed state of TokenCache
type TokenCacheStatus struct {
	// Phase is the current phase of the TokenCache
	Phase TokenCachePhase `json:"phase,omitempty"`

	// TotalSize is the total configured cache size
	TotalSize resource.Quantity `json:"totalSize,omitempty"`

	// UsedSize is the total used cache size
	UsedSize resource.Quantity `json:"usedSize,omitempty"`

	// TotalEntries is the total number of cache entries
	TotalEntries int64 `json:"totalEntries,omitempty"`

	// OverallHitRate is the overall cache hit rate
	OverallHitRate float64 `json:"overallHitRate,omitempty"`

	// Tiers contains per-tier status
	Tiers []CacheTierStatus `json:"tiers,omitempty"`

	// WarmupStatus contains warmup progress
	WarmupStatus *WarmupStatus `json:"warmupStatus,omitempty"`

	// Conditions represent the latest available observations
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// LastSyncTime is the last time the status was synchronized
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`

	// ObservedGeneration is the most recent generation observed
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// WarmupStatus contains cache warmup status
type WarmupStatus struct {
	// InProgress indicates if warmup is in progress
	InProgress bool `json:"inProgress,omitempty"`

	// Progress is the warmup progress percentage
	Progress int32 `json:"progress,omitempty"`

	// LastWarmupTime is the last time warmup was completed
	LastWarmupTime *metav1.Time `json:"lastWarmupTime,omitempty"`

	// EntriesWarmed is the number of entries warmed up
	EntriesWarmed int64 `json:"entriesWarmed,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Tiers",type=integer,JSONPath=`.spec.tiers`,priority=1
// +kubebuilder:printcolumn:name="Policy",type=string,JSONPath=`.spec.evictionPolicy`
// +kubebuilder:printcolumn:name="HitRate",type=number,JSONPath=`.status.overallHitRate`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// TokenCache is the Schema for the tokencaches API
type TokenCache struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TokenCacheSpec   `json:"spec,omitempty"`
	Status TokenCacheStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TokenCacheList contains a list of TokenCache
type TokenCacheList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TokenCache `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TokenCache{}, &TokenCacheList{})
}

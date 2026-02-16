package cache

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	inferencev1alpha1 "github.com/inference-operator/inference-operator/api/v1alpha1"
)

var (
	cacheHitsCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "inference_cache_hits_total",
			Help: "Total number of cache hits",
		},
		[]string{"cache", "tier"},
	)

	cacheMissesCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "inference_cache_misses_total",
			Help: "Total number of cache misses",
		},
		[]string{"cache", "tier"},
	)

	cacheSizeGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "inference_cache_size_bytes",
			Help: "Current cache size in bytes",
		},
		[]string{"cache", "tier"},
	)

	cacheEntriesGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "inference_cache_entries",
			Help: "Number of entries in cache",
		},
		[]string{"cache", "tier"},
	)

	cacheLatencyHistogram = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "inference_cache_latency_seconds",
			Help:    "Cache operation latency in seconds",
			Buckets: prometheus.ExponentialBuckets(0.0001, 2, 15),
		},
		[]string{"cache", "tier", "operation"},
	)
)

func init() {
	metrics.Registry.MustRegister(
		cacheHitsCounter,
		cacheMissesCounter,
		cacheSizeGauge,
		cacheEntriesGauge,
		cacheLatencyHistogram,
	)
}

// CacheEntry represents a cached item
type CacheEntry struct {
	Key         string
	Value       []byte
	Size        int64
	CreatedAt   time.Time
	AccessedAt  time.Time
	AccessCount int64
	TTL         time.Duration
	Tier        string
	Metadata    map[string]string
}

// CacheTier represents a cache tier interface
type CacheTier interface {
	Name() string
	Type() inferencev1alpha1.CacheTierType
	Get(ctx context.Context, key string) (*CacheEntry, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
	Clear(ctx context.Context) error
	Size() resource.Quantity
	UsedSize() resource.Quantity
	Entries() int64
	Stats() TierStats
	Healthy() bool
}

// TierStats contains cache tier statistics
type TierStats struct {
	HitRate    float64
	MissRate   float64
	AvgLatency time.Duration
	Entries    int64
	UsedSize   int64
	MaxSize    int64
	Evictions  int64
}

// Manager orchestrates multi-tier caching
type Manager struct {
	log              logr.Logger
	mu               sync.RWMutex
	caches           map[string]*CacheConfig
	prefixCache      *PrefixCache
	localCache       *LocalCache
	redisCache       *RedisCache
	coherencePolicy  string
	evictionInterval time.Duration
	running          bool
	stopCh           chan struct{}
}

// CacheConfig holds configuration for a token cache
type CacheConfig struct {
	Name            string
	Tiers           []CacheTier
	EvictionPolicy  inferencev1alpha1.EvictionPolicy
	CoherencePolicy string
	WarmupEnabled   bool
	CommonPrefixes  []string
}

// NewManager creates a new cache manager
func NewManager(log logr.Logger) *Manager {
	return &Manager{
		log:              log,
		caches:           make(map[string]*CacheConfig),
		evictionInterval: 60 * time.Second,
		stopCh:           make(chan struct{}),
	}
}

// Start starts the cache manager
func (m *Manager) Start(ctx context.Context) error {
	m.log.Info("Starting cache manager")
	m.running = true

	// Start eviction loop
	go m.runEvictionLoop(ctx)

	return nil
}

// Stop stops the cache manager
func (m *Manager) Stop() {
	m.log.Info("Stopping cache manager")
	m.running = false
	close(m.stopCh)
}

// runEvictionLoop runs the cache eviction loop
func (m *Manager) runEvictionLoop(ctx context.Context) {
	ticker := time.NewTicker(m.evictionInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.runEviction()
		}
	}
}

// runEviction runs cache eviction across all tiers
func (m *Manager) runEviction() {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, cache := range m.caches {
		for _, tier := range cache.Tiers {
			// Check if tier needs eviction
			usedSize := tier.UsedSize()
			maxSize := tier.Size()
			usedRatio := float64(usedSize.Value()) / float64(maxSize.Value())
			if usedRatio > 0.9 { // 90% threshold
				m.log.V(1).Info("Running eviction",
					"cache", cache.Name,
					"tier", tier.Name(),
					"usedRatio", usedRatio,
				)
				// Eviction is handled by individual tiers
			}
		}
	}
}

// ConfigureCache configures a token cache
func (m *Manager) ConfigureCache(ctx context.Context, cache *inferencev1alpha1.TokenCache) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Info("Configuring token cache", "name", cache.Name)

	config := &CacheConfig{
		Name:            cache.Name,
		Tiers:           make([]CacheTier, 0),
		EvictionPolicy:  cache.Spec.EvictionPolicy,
		CoherencePolicy: cache.Spec.CoherencePolicy,
	}

	if cache.Spec.Warmup != nil {
		config.WarmupEnabled = cache.Spec.Warmup.Enabled
		config.CommonPrefixes = cache.Spec.Warmup.CommonPrefixes
	}

	// Create tiers
	for _, tierSpec := range cache.Spec.Tiers {
		tier, err := m.createTier(ctx, cache.Name, tierSpec)
		if err != nil {
			return fmt.Errorf("failed to create tier %s: %w", tierSpec.Name, err)
		}
		config.Tiers = append(config.Tiers, tier)
	}

	m.caches[cache.Name] = config

	// Run warmup if enabled
	if config.WarmupEnabled && len(config.CommonPrefixes) > 0 {
		go m.warmupCache(ctx, cache.Name, config.CommonPrefixes)
	}

	return nil
}

// createTier creates a cache tier based on the spec
func (m *Manager) createTier(ctx context.Context, cacheName string, spec inferencev1alpha1.CacheTierSpec) (CacheTier, error) {
	var ttl time.Duration
	if spec.TTL != nil {
		ttl = spec.TTL.Duration
	}

	switch spec.Type {
	case inferencev1alpha1.CacheTierTypePrefix:
		return NewPrefixCache(m.log.WithName("prefix"), cacheName, spec.Name, spec.Size, ttl, spec.PrefixConfig), nil
	case inferencev1alpha1.CacheTierTypeLocal:
		return NewLocalCache(m.log.WithName("local"), cacheName, spec.Name, spec.Size, ttl, spec.LocalConfig), nil
	case inferencev1alpha1.CacheTierTypeRedis:
		return NewRedisCache(m.log.WithName("redis"), cacheName, spec.Name, spec.Size, ttl, spec.RedisConfig)
	default:
		return nil, fmt.Errorf("unsupported tier type: %s", spec.Type)
	}
}

// CleanupCache removes a token cache configuration
func (m *Manager) CleanupCache(ctx context.Context, cacheName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Info("Cleaning up token cache", "name", cacheName)

	config, exists := m.caches[cacheName]
	if !exists {
		return nil
	}

	// Clear all tiers
	for _, tier := range config.Tiers {
		if err := tier.Clear(ctx); err != nil {
			m.log.Error(err, "Failed to clear tier", "tier", tier.Name())
		}
	}

	delete(m.caches, cacheName)
	return nil
}

// Get retrieves a value from the cache hierarchy
func (m *Manager) Get(ctx context.Context, cacheName, key string) (*CacheEntry, error) {
	m.mu.RLock()
	config, exists := m.caches[cacheName]
	m.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("cache not found: %s", cacheName)
	}

	// Try each tier in order
	for _, tier := range config.Tiers {
		start := time.Now()
		entry, err := tier.Get(ctx, key)
		latency := time.Since(start)

		cacheLatencyHistogram.WithLabelValues(cacheName, tier.Name(), "get").Observe(latency.Seconds())

		if err == nil && entry != nil {
			cacheHitsCounter.WithLabelValues(cacheName, tier.Name()).Inc()
			m.log.V(2).Info("Cache hit", "cache", cacheName, "tier", tier.Name(), "key", key)

			// Promote to higher tiers (write-through)
			if config.CoherencePolicy == "write-through" {
				go m.promoteToPreviousTiers(ctx, config, tier, key, entry)
			}

			return entry, nil
		}
		cacheMissesCounter.WithLabelValues(cacheName, tier.Name()).Inc()
	}

	return nil, fmt.Errorf("key not found: %s", key)
}

// Set stores a value in the cache
func (m *Manager) Set(ctx context.Context, cacheName, key string, value []byte, ttl time.Duration) error {
	m.mu.RLock()
	config, exists := m.caches[cacheName]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("cache not found: %s", cacheName)
	}

	// Write based on coherence policy
	switch config.CoherencePolicy {
	case "write-through":
		// Write to all tiers
		for _, tier := range config.Tiers {
			start := time.Now()
			if err := tier.Set(ctx, key, value, ttl); err != nil {
				m.log.Error(err, "Failed to write to tier", "tier", tier.Name())
			}
			latency := time.Since(start)
			cacheLatencyHistogram.WithLabelValues(cacheName, tier.Name(), "set").Observe(latency.Seconds())
		}
	case "write-back":
		// Write to first tier only, propagate lazily
		if len(config.Tiers) > 0 {
			start := time.Now()
			if err := config.Tiers[0].Set(ctx, key, value, ttl); err != nil {
				return err
			}
			latency := time.Since(start)
			cacheLatencyHistogram.WithLabelValues(cacheName, config.Tiers[0].Name(), "set").Observe(latency.Seconds())
		}
	case "write-around":
		// Write to last tier only (typically distributed)
		if len(config.Tiers) > 0 {
			lastTier := config.Tiers[len(config.Tiers)-1]
			start := time.Now()
			if err := lastTier.Set(ctx, key, value, ttl); err != nil {
				return err
			}
			latency := time.Since(start)
			cacheLatencyHistogram.WithLabelValues(cacheName, lastTier.Name(), "set").Observe(latency.Seconds())
		}
	default:
		// Default to write-through
		for _, tier := range config.Tiers {
			if err := tier.Set(ctx, key, value, ttl); err != nil {
				m.log.Error(err, "Failed to write to tier", "tier", tier.Name())
			}
		}
	}

	return nil
}

// Delete removes a value from all cache tiers
func (m *Manager) Delete(ctx context.Context, cacheName, key string) error {
	m.mu.RLock()
	config, exists := m.caches[cacheName]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("cache not found: %s", cacheName)
	}

	// Delete from all tiers
	for _, tier := range config.Tiers {
		if err := tier.Delete(ctx, key); err != nil {
			m.log.Error(err, "Failed to delete from tier", "tier", tier.Name(), "key", key)
		}
	}

	return nil
}

// Invalidate invalidates entries matching a pattern
func (m *Manager) Invalidate(ctx context.Context, cacheName, pattern string) error {
	m.mu.RLock()
	config, exists := m.caches[cacheName]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("cache not found: %s", cacheName)
	}

	m.log.Info("Invalidating cache entries", "cache", cacheName, "pattern", pattern)

	// Clear all tiers for simplicity
	// A more sophisticated implementation would support pattern-based deletion
	for _, tier := range config.Tiers {
		if err := tier.Clear(ctx); err != nil {
			m.log.Error(err, "Failed to clear tier", "tier", tier.Name())
		}
	}

	return nil
}

// promoteToPreviousTiers promotes a cache entry to higher priority tiers
func (m *Manager) promoteToPreviousTiers(ctx context.Context, config *CacheConfig, currentTier CacheTier, key string, entry *CacheEntry) {
	for _, tier := range config.Tiers {
		if tier.Name() == currentTier.Name() {
			break
		}
		if err := tier.Set(ctx, key, entry.Value, entry.TTL); err != nil {
			m.log.V(1).Error(err, "Failed to promote to tier", "tier", tier.Name())
		}
	}
}

// warmupCache warms up the cache with common prefixes
func (m *Manager) warmupCache(ctx context.Context, cacheName string, prefixes []string) {
	m.log.Info("Starting cache warmup", "cache", cacheName, "prefixes", len(prefixes))

	config, exists := m.caches[cacheName]
	if !exists {
		return
	}

	for _, prefix := range prefixes {
		// Generate cache key for prefix
		key := fmt.Sprintf("prefix:%s", prefix)
		value := []byte(prefix) // In reality, this would be tokenized prefix

		// Store in all tiers
		for _, tier := range config.Tiers {
			if err := tier.Set(ctx, key, value, 24*time.Hour); err != nil {
				m.log.V(1).Error(err, "Failed to warm up tier", "tier", tier.Name())
			}
		}
	}

	m.log.Info("Cache warmup completed", "cache", cacheName)
}

// GetStatus returns the status of a token cache
func (m *Manager) GetStatus(cacheName string) (*inferencev1alpha1.TokenCacheStatus, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	config, exists := m.caches[cacheName]
	if !exists {
		return nil, fmt.Errorf("cache not found: %s", cacheName)
	}

	status := &inferencev1alpha1.TokenCacheStatus{
		Phase: inferencev1alpha1.TokenCachePhaseReady,
		Tiers: make([]inferencev1alpha1.CacheTierStatus, 0, len(config.Tiers)),
	}

	var totalSize, usedSize int64
	var totalEntries int64
	var totalHitRate float64

	for _, tier := range config.Tiers {
		stats := tier.Stats()

		tierStatus := inferencev1alpha1.CacheTierStatus{
			Name:     tier.Name(),
			Ready:    tier.Healthy(),
			Size:     tier.Size(),
			UsedSize: tier.UsedSize(),
			Entries:  stats.Entries,
			HitRate:  stats.HitRate,
			MissRate: stats.MissRate,
		}
		status.Tiers = append(status.Tiers, tierStatus)

		tierSize := tier.Size()
		tierUsedSize := tier.UsedSize()
		totalSize += tierSize.Value()
		usedSize += tierUsedSize.Value()
		totalEntries += stats.Entries
		totalHitRate += stats.HitRate
	}

	status.TotalSize = *resource.NewQuantity(totalSize, resource.BinarySI)
	status.UsedSize = *resource.NewQuantity(usedSize, resource.BinarySI)
	status.TotalEntries = totalEntries

	if len(config.Tiers) > 0 {
		status.OverallHitRate = totalHitRate / float64(len(config.Tiers))
	}

	// Check for degraded state
	for _, tier := range config.Tiers {
		if !tier.Healthy() {
			status.Phase = inferencev1alpha1.TokenCachePhaseDegraded
			break
		}
	}

	// Update metrics
	for _, tier := range config.Tiers {
		stats := tier.Stats()
		cacheSizeGauge.WithLabelValues(cacheName, tier.Name()).Set(float64(stats.UsedSize))
		cacheEntriesGauge.WithLabelValues(cacheName, tier.Name()).Set(float64(stats.Entries))
	}

	return status, nil
}

// GetCacheForService returns the cache configuration for a service
func (m *Manager) GetCacheForService(ctx context.Context, serviceName, namespace string, cacheRef string) (*CacheConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if cacheRef == "" {
		// Return default cache if it exists
		if defaultCache, exists := m.caches["default"]; exists {
			return defaultCache, nil
		}
		return nil, fmt.Errorf("no default cache configured")
	}

	config, exists := m.caches[cacheRef]
	if !exists {
		return nil, fmt.Errorf("cache not found: %s", cacheRef)
	}

	return config, nil
}

// ListCaches returns all configured caches
func (m *Manager) ListCaches() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.caches))
	for name := range m.caches {
		names = append(names, name)
	}
	return names
}

// HealthCheck performs a health check on all cache tiers
func (m *Manager) HealthCheck(ctx context.Context) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for cacheName, config := range m.caches {
		for _, tier := range config.Tiers {
			if !tier.Healthy() {
				return fmt.Errorf("unhealthy tier in cache %s: %s", cacheName, tier.Name())
			}
		}
	}

	return nil
}

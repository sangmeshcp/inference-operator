package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	"github.com/redis/go-redis/v9"
	"k8s.io/apimachinery/pkg/api/resource"

	inferencev1alpha1 "github.com/inference-operator/inference-operator/api/v1alpha1"
)

// RedisCache implements distributed caching using Redis
type RedisCache struct {
	log        logr.Logger
	cacheName  string
	tierName   string
	maxSize    resource.Quantity
	defaultTTL time.Duration
	client     redis.UniversalClient
	config     *inferencev1alpha1.RedisCacheConfig
	mu         sync.RWMutex
	hits       atomic.Int64
	misses     atomic.Int64
	usedSize   atomic.Int64
	entries    atomic.Int64
	healthy    atomic.Bool
	keyPrefix  string
}

// NewRedisCache creates a new Redis cache tier
func NewRedisCache(log logr.Logger, cacheName, tierName string, size resource.Quantity, ttl time.Duration, config *inferencev1alpha1.RedisCacheConfig) (*RedisCache, error) {
	cache := &RedisCache{
		log:        log,
		cacheName:  cacheName,
		tierName:   tierName,
		maxSize:    size,
		defaultTTL: ttl,
		config:     config,
		keyPrefix:  fmt.Sprintf("inference:%s:%s:", cacheName, tierName),
	}

	if ttl == 0 {
		cache.defaultTTL = 7 * 24 * time.Hour // Default 7 days
	}

	if err := cache.connect(); err != nil {
		return nil, err
	}

	// Start health check loop
	go cache.healthCheckLoop()

	return cache, nil
}

// connect establishes connection to Redis
func (c *RedisCache) connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var opts redis.UniversalOptions

	if c.config != nil && c.config.ExternalEndpoint != "" {
		// Use external endpoint
		opts = redis.UniversalOptions{
			Addrs: []string{c.config.ExternalEndpoint},
		}
	} else {
		// Default local connection for development
		opts = redis.UniversalOptions{
			Addrs: []string{"localhost:6379"},
		}
	}

	if c.config != nil {
		if c.config.MaxConnections > 0 {
			opts.PoolSize = int(c.config.MaxConnections)
		}
		if c.config.ConnectionTimeout != nil {
			opts.DialTimeout = c.config.ConnectionTimeout.Duration
			opts.ReadTimeout = c.config.ConnectionTimeout.Duration
			opts.WriteTimeout = c.config.ConnectionTimeout.Duration
		}
		// Password would be retrieved from secret in production
	}

	// Create client based on mode
	if c.config != nil && c.config.Mode == "cluster" {
		c.client = redis.NewClusterClient(&redis.ClusterOptions{
			Addrs:        opts.Addrs,
			PoolSize:     opts.PoolSize,
			DialTimeout:  opts.DialTimeout,
			ReadTimeout:  opts.ReadTimeout,
			WriteTimeout: opts.WriteTimeout,
		})
	} else {
		c.client = redis.NewUniversalClient(&opts)
	}

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.client.Ping(ctx).Err(); err != nil {
		c.log.Error(err, "Failed to connect to Redis")
		c.healthy.Store(false)
		// Don't return error, allow retry
		return nil
	}

	c.healthy.Store(true)
	c.log.Info("Connected to Redis", "tier", c.tierName)
	return nil
}

// healthCheckLoop periodically checks Redis health
func (c *RedisCache) healthCheckLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := c.client.Ping(ctx).Err()
		cancel()

		if err != nil {
			c.log.V(1).Error(err, "Redis health check failed")
			c.healthy.Store(false)

			// Try to reconnect
			if err := c.connect(); err != nil {
				c.log.Error(err, "Failed to reconnect to Redis")
			}
		} else {
			c.healthy.Store(true)
		}
	}
}

// Name returns the tier name
func (c *RedisCache) Name() string {
	return c.tierName
}

// Type returns the tier type
func (c *RedisCache) Type() inferencev1alpha1.CacheTierType {
	return inferencev1alpha1.CacheTierTypeRedis
}

// makeKey creates a Redis key with prefix
func (c *RedisCache) makeKey(key string) string {
	return c.keyPrefix + key
}

// hashKey hashes a key for consistent length
func (c *RedisCache) hashKey(key string) string {
	hash := sha256.Sum256([]byte(key))
	return hex.EncodeToString(hash[:])
}

// Get retrieves a value from Redis
func (c *RedisCache) Get(ctx context.Context, key string) (*CacheEntry, error) {
	if !c.healthy.Load() {
		return nil, fmt.Errorf("redis is not healthy")
	}

	redisKey := c.makeKey(key)

	// Use HGETALL to get all fields
	result, err := c.client.HGetAll(ctx, redisKey).Result()
	if err != nil {
		if err == redis.Nil {
			c.misses.Add(1)
			return nil, nil
		}
		c.misses.Add(1)
		return nil, fmt.Errorf("failed to get from redis: %w", err)
	}

	if len(result) == 0 {
		c.misses.Add(1)
		return nil, nil
	}

	c.hits.Add(1)

	// Update access time
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		c.client.HSet(ctx, redisKey, "accessed_at", time.Now().Unix())
	}()

	// Parse entry
	entry := &CacheEntry{
		Key:      key,
		Value:    []byte(result["value"]),
		Size:     int64(len(result["value"])),
		Tier:     c.tierName,
		Metadata: make(map[string]string),
	}

	return entry, nil
}

// Set stores a value in Redis
func (c *RedisCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if !c.healthy.Load() {
		return fmt.Errorf("redis is not healthy")
	}

	if ttl == 0 {
		ttl = c.defaultTTL
	}

	redisKey := c.makeKey(key)
	now := time.Now().Unix()

	// Use hash to store metadata alongside value
	fields := map[string]interface{}{
		"value":       string(value),
		"created_at":  now,
		"accessed_at": now,
		"size":        len(value),
	}

	pipe := c.client.Pipeline()
	pipe.HSet(ctx, redisKey, fields)
	pipe.Expire(ctx, redisKey, ttl)

	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to set in redis: %w", err)
	}

	c.entries.Add(1)
	c.usedSize.Add(int64(len(value)))

	return nil
}

// Delete removes a value from Redis
func (c *RedisCache) Delete(ctx context.Context, key string) error {
	if !c.healthy.Load() {
		return fmt.Errorf("redis is not healthy")
	}

	redisKey := c.makeKey(key)

	// Get size before delete for tracking
	size, _ := c.client.HGet(ctx, redisKey, "size").Int64()

	err := c.client.Del(ctx, redisKey).Err()
	if err != nil {
		return fmt.Errorf("failed to delete from redis: %w", err)
	}

	c.entries.Add(-1)
	c.usedSize.Add(-size)

	return nil
}

// Clear removes all entries from this cache tier
func (c *RedisCache) Clear(ctx context.Context) error {
	if !c.healthy.Load() {
		return fmt.Errorf("redis is not healthy")
	}

	pattern := c.keyPrefix + "*"
	iter := c.client.Scan(ctx, 0, pattern, 100).Iterator()

	for iter.Next(ctx) {
		if err := c.client.Del(ctx, iter.Val()).Err(); err != nil {
			c.log.V(1).Error(err, "Failed to delete key", "key", iter.Val())
		}
	}

	if err := iter.Err(); err != nil {
		return fmt.Errorf("failed to scan redis: %w", err)
	}

	c.entries.Store(0)
	c.usedSize.Store(0)

	return nil
}

// Size returns the maximum size
func (c *RedisCache) Size() resource.Quantity {
	return c.maxSize
}

// UsedSize returns the current used size
func (c *RedisCache) UsedSize() resource.Quantity {
	return *resource.NewQuantity(c.usedSize.Load(), resource.BinarySI)
}

// Entries returns the number of entries
func (c *RedisCache) Entries() int64 {
	return c.entries.Load()
}

// Stats returns cache statistics
func (c *RedisCache) Stats() TierStats {
	hits := c.hits.Load()
	misses := c.misses.Load()
	total := hits + misses

	var hitRate, missRate float64
	if total > 0 {
		hitRate = float64(hits) / float64(total)
		missRate = float64(misses) / float64(total)
	}

	return TierStats{
		HitRate:  hitRate,
		MissRate: missRate,
		Entries:  c.entries.Load(),
		UsedSize: c.usedSize.Load(),
		MaxSize:  c.maxSize.Value(),
	}
}

// Healthy returns whether Redis is healthy
func (c *RedisCache) Healthy() bool {
	return c.healthy.Load()
}

// GetMulti retrieves multiple values from Redis
func (c *RedisCache) GetMulti(ctx context.Context, keys []string) (map[string]*CacheEntry, error) {
	if !c.healthy.Load() {
		return nil, fmt.Errorf("redis is not healthy")
	}

	results := make(map[string]*CacheEntry)
	pipe := c.client.Pipeline()

	cmds := make(map[string]*redis.MapStringStringCmd)
	for _, key := range keys {
		redisKey := c.makeKey(key)
		cmds[key] = pipe.HGetAll(ctx, redisKey)
	}

	_, err := pipe.Exec(ctx)
	if err != nil && err != redis.Nil {
		return nil, fmt.Errorf("failed to get multi from redis: %w", err)
	}

	for key, cmd := range cmds {
		result, err := cmd.Result()
		if err != nil || len(result) == 0 {
			c.misses.Add(1)
			continue
		}

		c.hits.Add(1)
		results[key] = &CacheEntry{
			Key:   key,
			Value: []byte(result["value"]),
			Size:  int64(len(result["value"])),
			Tier:  c.tierName,
		}
	}

	return results, nil
}

// SetMulti stores multiple values in Redis
func (c *RedisCache) SetMulti(ctx context.Context, entries map[string][]byte, ttl time.Duration) error {
	if !c.healthy.Load() {
		return fmt.Errorf("redis is not healthy")
	}

	if ttl == 0 {
		ttl = c.defaultTTL
	}

	pipe := c.client.Pipeline()
	now := time.Now().Unix()

	for key, value := range entries {
		redisKey := c.makeKey(key)
		fields := map[string]interface{}{
			"value":       string(value),
			"created_at":  now,
			"accessed_at": now,
			"size":        len(value),
		}
		pipe.HSet(ctx, redisKey, fields)
		pipe.Expire(ctx, redisKey, ttl)
	}

	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to set multi in redis: %w", err)
	}

	for _, value := range entries {
		c.entries.Add(1)
		c.usedSize.Add(int64(len(value)))
	}

	return nil
}

// Info returns Redis info
func (c *RedisCache) Info(ctx context.Context) (map[string]string, error) {
	if !c.healthy.Load() {
		return nil, fmt.Errorf("redis is not healthy")
	}

	info, err := c.client.Info(ctx, "memory", "stats", "keyspace").Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get redis info: %w", err)
	}

	return map[string]string{
		"info": info,
	}, nil
}

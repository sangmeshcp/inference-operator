package cache

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/gob"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	"github.com/klauspost/compress/zstd"
	"k8s.io/apimachinery/pkg/api/resource"

	inferencev1alpha1 "github.com/inference-operator/inference-operator/api/v1alpha1"
)

// LocalCacheEntry represents a cached entry on disk
type LocalCacheEntry struct {
	Key        string
	Value      []byte
	CreatedAt  time.Time
	AccessedAt time.Time
	ExpiresAt  time.Time
	Size       int64
	Compressed bool
}

// LocalCache implements local NVMe/disk caching
type LocalCache struct {
	log          logr.Logger
	cacheName    string
	tierName     string
	maxSize      resource.Quantity
	defaultTTL   time.Duration
	config       *inferencev1alpha1.LocalCacheConfig
	basePath     string
	mu           sync.RWMutex
	index        map[string]*LocalCacheEntry
	hits         atomic.Int64
	misses       atomic.Int64
	usedSize     atomic.Int64
	entries      atomic.Int64
	evictions    atomic.Int64
	compression  bool
	compressAlgo string
	evictCh      chan struct{}
	stopCh       chan struct{}
}

// NewLocalCache creates a new local cache tier
func NewLocalCache(log logr.Logger, cacheName, tierName string, size resource.Quantity, ttl time.Duration, config *inferencev1alpha1.LocalCacheConfig) *LocalCache {
	basePath := "/cache"
	compression := false
	compressAlgo := "lz4"

	if config != nil {
		if config.MountPath != "" {
			basePath = config.MountPath
		}
		compression = config.Compression
		if config.CompressionAlgorithm != "" {
			compressAlgo = config.CompressionAlgorithm
		}
	}

	cache := &LocalCache{
		log:          log,
		cacheName:    cacheName,
		tierName:     tierName,
		maxSize:      size,
		defaultTTL:   ttl,
		config:       config,
		basePath:     filepath.Join(basePath, cacheName, tierName),
		index:        make(map[string]*LocalCacheEntry),
		compression:  compression,
		compressAlgo: compressAlgo,
		evictCh:      make(chan struct{}, 1),
		stopCh:       make(chan struct{}),
	}

	if ttl == 0 {
		cache.defaultTTL = 24 * time.Hour // Default 24 hours
	}

	// Initialize cache directory
	if err := cache.initialize(); err != nil {
		log.Error(err, "Failed to initialize local cache")
	}

	// Start background tasks
	go cache.evictionLoop()
	go cache.cleanupLoop()

	return cache
}

// initialize creates the cache directory and loads existing index
func (c *LocalCache) initialize() error {
	// Create cache directory
	if err := os.MkdirAll(c.basePath, 0755); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}

	// Load existing index
	indexPath := filepath.Join(c.basePath, "index.gob")
	if data, err := os.ReadFile(indexPath); err == nil {
		dec := gob.NewDecoder(bytes.NewReader(data))
		if err := dec.Decode(&c.index); err != nil {
			c.log.V(1).Error(err, "Failed to load cache index, starting fresh")
			c.index = make(map[string]*LocalCacheEntry)
		} else {
			// Validate and count entries
			for key, entry := range c.index {
				if time.Now().After(entry.ExpiresAt) {
					delete(c.index, key)
				} else {
					c.entries.Add(1)
					c.usedSize.Add(entry.Size)
				}
			}
			c.log.Info("Loaded cache index", "entries", c.entries.Load())
		}
	}

	return nil
}

// evictionLoop handles eviction when cache is full
func (c *LocalCache) evictionLoop() {
	for {
		select {
		case <-c.stopCh:
			return
		case <-c.evictCh:
			c.evict()
		}
	}
}

// cleanupLoop periodically cleans expired entries
func (c *LocalCache) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.cleanupExpired()
		}
	}
}

// cleanupExpired removes expired entries
func (c *LocalCache) cleanupExpired() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for key, entry := range c.index {
		if now.After(entry.ExpiresAt) {
			c.deleteEntry(key, entry)
		}
	}
}

// evict removes entries to free space (LRU)
func (c *LocalCache) evict() {
	c.mu.Lock()
	defer c.mu.Unlock()

	targetSize := c.maxSize.Value() * 80 / 100 // Target 80% usage

	for c.usedSize.Load() > targetSize && len(c.index) > 0 {
		// Find LRU entry
		var oldestKey string
		var oldestTime time.Time

		for key, entry := range c.index {
			if oldestKey == "" || entry.AccessedAt.Before(oldestTime) {
				oldestKey = key
				oldestTime = entry.AccessedAt
			}
		}

		if oldestKey != "" {
			entry := c.index[oldestKey]
			c.deleteEntry(oldestKey, entry)
			c.evictions.Add(1)
		}
	}
}

// deleteEntry removes an entry from cache
func (c *LocalCache) deleteEntry(key string, entry *LocalCacheEntry) {
	// Remove file
	filePath := c.getFilePath(key)
	os.Remove(filePath)

	// Update tracking
	c.usedSize.Add(-entry.Size)
	c.entries.Add(-1)
	delete(c.index, key)
}

// getFilePath returns the file path for a cache key
func (c *LocalCache) getFilePath(key string) string {
	// Use first 2 chars of key as subdirectory for distribution
	subdir := "00"
	if len(key) >= 2 {
		subdir = key[:2]
	}
	return filepath.Join(c.basePath, subdir, key)
}

// Name returns the tier name
func (c *LocalCache) Name() string {
	return c.tierName
}

// Type returns the tier type
func (c *LocalCache) Type() inferencev1alpha1.CacheTierType {
	return inferencev1alpha1.CacheTierTypeLocal
}

// Get retrieves a value from local cache
func (c *LocalCache) Get(ctx context.Context, key string) (*CacheEntry, error) {
	c.mu.RLock()
	entry, exists := c.index[key]
	c.mu.RUnlock()

	if !exists {
		c.misses.Add(1)
		return nil, nil
	}

	// Check expiration
	if time.Now().After(entry.ExpiresAt) {
		c.mu.Lock()
		c.deleteEntry(key, entry)
		c.mu.Unlock()
		c.misses.Add(1)
		return nil, nil
	}

	// Read file
	filePath := c.getFilePath(key)
	data, err := os.ReadFile(filePath)
	if err != nil {
		c.misses.Add(1)
		// Remove corrupted entry
		c.mu.Lock()
		c.deleteEntry(key, entry)
		c.mu.Unlock()
		return nil, nil
	}

	// Decompress if needed
	if entry.Compressed {
		data, err = c.decompress(data)
		if err != nil {
			c.misses.Add(1)
			return nil, fmt.Errorf("failed to decompress: %w", err)
		}
	}

	c.hits.Add(1)

	// Update access time
	c.mu.Lock()
	entry.AccessedAt = time.Now()
	c.mu.Unlock()

	return &CacheEntry{
		Key:        key,
		Value:      data,
		Size:       entry.Size,
		CreatedAt:  entry.CreatedAt,
		AccessedAt: entry.AccessedAt,
		Tier:       c.tierName,
	}, nil
}

// Set stores a value in local cache
func (c *LocalCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if ttl == 0 {
		ttl = c.defaultTTL
	}

	// Compress if enabled
	data := value
	compressed := false
	if c.compression && len(value) > 1024 { // Only compress > 1KB
		var err error
		data, err = c.compress(value)
		if err != nil {
			c.log.V(1).Error(err, "Failed to compress, storing uncompressed")
			data = value
		} else {
			compressed = true
		}
	}

	// Write file
	filePath := c.getFilePath(key)
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	now := time.Now()
	entry := &LocalCacheEntry{
		Key:        key,
		CreatedAt:  now,
		AccessedAt: now,
		ExpiresAt:  now.Add(ttl),
		Size:       int64(len(data)),
		Compressed: compressed,
	}

	c.mu.Lock()
	// If replacing, subtract old size
	if old, exists := c.index[key]; exists {
		c.usedSize.Add(-old.Size)
	} else {
		c.entries.Add(1)
	}
	c.index[key] = entry
	c.usedSize.Add(entry.Size)
	c.mu.Unlock()

	// Check if eviction needed
	if c.usedSize.Load() > c.maxSize.Value()*90/100 {
		select {
		case c.evictCh <- struct{}{}:
		default:
		}
	}

	return nil
}

// Delete removes a value from local cache
func (c *LocalCache) Delete(ctx context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, exists := c.index[key]
	if !exists {
		return nil
	}

	c.deleteEntry(key, entry)
	return nil
}

// Clear removes all entries from local cache
func (c *LocalCache) Clear(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Remove all files
	err := os.RemoveAll(c.basePath)
	if err != nil {
		return fmt.Errorf("failed to clear cache: %w", err)
	}

	// Recreate directory
	if err := os.MkdirAll(c.basePath, 0755); err != nil {
		return fmt.Errorf("failed to recreate cache directory: %w", err)
	}

	c.index = make(map[string]*LocalCacheEntry)
	c.usedSize.Store(0)
	c.entries.Store(0)

	return nil
}

// Size returns the maximum size
func (c *LocalCache) Size() resource.Quantity {
	return c.maxSize
}

// UsedSize returns the current used size
func (c *LocalCache) UsedSize() resource.Quantity {
	return *resource.NewQuantity(c.usedSize.Load(), resource.BinarySI)
}

// Entries returns the number of entries
func (c *LocalCache) Entries() int64 {
	return c.entries.Load()
}

// Stats returns cache statistics
func (c *LocalCache) Stats() TierStats {
	hits := c.hits.Load()
	misses := c.misses.Load()
	total := hits + misses

	var hitRate, missRate float64
	if total > 0 {
		hitRate = float64(hits) / float64(total)
		missRate = float64(misses) / float64(total)
	}

	return TierStats{
		HitRate:   hitRate,
		MissRate:  missRate,
		Entries:   c.entries.Load(),
		UsedSize:  c.usedSize.Load(),
		MaxSize:   c.maxSize.Value(),
		Evictions: c.evictions.Load(),
	}
}

// Healthy returns whether the cache is healthy
func (c *LocalCache) Healthy() bool {
	// Check if cache directory is accessible
	_, err := os.Stat(c.basePath)
	return err == nil
}

// compress compresses data using the configured algorithm
func (c *LocalCache) compress(data []byte) ([]byte, error) {
	var buf bytes.Buffer

	switch c.compressAlgo {
	case "zstd":
		enc, err := zstd.NewWriter(&buf)
		if err != nil {
			return nil, err
		}
		if _, err := enc.Write(data); err != nil {
			enc.Close()
			return nil, err
		}
		if err := enc.Close(); err != nil {
			return nil, err
		}
	case "gzip":
		enc := gzip.NewWriter(&buf)
		if _, err := enc.Write(data); err != nil {
			enc.Close()
			return nil, err
		}
		if err := enc.Close(); err != nil {
			return nil, err
		}
	default: // lz4 or others - use gzip as fallback
		enc := gzip.NewWriter(&buf)
		if _, err := enc.Write(data); err != nil {
			enc.Close()
			return nil, err
		}
		if err := enc.Close(); err != nil {
			return nil, err
		}
	}

	return buf.Bytes(), nil
}

// decompress decompresses data using the configured algorithm
func (c *LocalCache) decompress(data []byte) ([]byte, error) {
	var reader io.Reader
	buf := bytes.NewReader(data)

	switch c.compressAlgo {
	case "zstd":
		dec, err := zstd.NewReader(buf)
		if err != nil {
			return nil, err
		}
		defer dec.Close()
		reader = dec
	case "gzip":
		dec, err := gzip.NewReader(buf)
		if err != nil {
			return nil, err
		}
		defer dec.Close()
		reader = dec
	default:
		dec, err := gzip.NewReader(buf)
		if err != nil {
			return nil, err
		}
		defer dec.Close()
		reader = dec
	}

	return io.ReadAll(reader)
}

// SaveIndex saves the cache index to disk
func (c *LocalCache) SaveIndex() error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	indexPath := filepath.Join(c.basePath, "index.gob")
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)

	if err := enc.Encode(c.index); err != nil {
		return fmt.Errorf("failed to encode index: %w", err)
	}

	if err := os.WriteFile(indexPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write index: %w", err)
	}

	return nil
}

// Stop stops background tasks and saves state
func (c *LocalCache) Stop() {
	close(c.stopCh)
	c.SaveIndex()
}

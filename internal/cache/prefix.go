package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/resource"

	inferencev1alpha1 "github.com/inference-operator/inference-operator/api/v1alpha1"
)

// PrefixCacheBlock represents a block of cached prefix tokens
type PrefixCacheBlock struct {
	Hash       string
	Tokens     []int32
	KVCache    []byte // Serialized KV cache tensors
	Size       int64
	RefCount   atomic.Int32
	CreatedAt  time.Time
	AccessedAt time.Time
	ExpiresAt  time.Time
	BlockIndex int32
	InGPU      bool
	InSwap     bool
}

// PrefixTree represents a radix tree for prefix matching
type PrefixTree struct {
	mu       sync.RWMutex
	root     *PrefixNode
	blocks   map[string]*PrefixCacheBlock
	maxDepth int32
}

// PrefixNode represents a node in the prefix tree
type PrefixNode struct {
	TokenID  int32
	Children map[int32]*PrefixNode
	Block    *PrefixCacheBlock
	Depth    int32
}

// PrefixCache implements GPU prefix caching for KV tensors
type PrefixCache struct {
	log          logr.Logger
	cacheName    string
	tierName     string
	maxSize      resource.Quantity
	defaultTTL   time.Duration
	config       *inferencev1alpha1.PrefixCacheConfig
	tree         *PrefixTree
	mu           sync.RWMutex
	gpuBlocks    map[string]*PrefixCacheBlock // Blocks in GPU memory
	swapBlocks   map[string]*PrefixCacheBlock // Blocks in CPU memory (swap)
	hits         atomic.Int64
	misses       atomic.Int64
	usedSize     atomic.Int64
	swapUsed     atomic.Int64
	entries      atomic.Int64
	blockSize    int32
	maxPrefixLen int32
	minPrefixLen int32
	enableSwap   bool
	swapSize     int64
}

// NewPrefixCache creates a new prefix cache tier
func NewPrefixCache(log logr.Logger, cacheName, tierName string, size resource.Quantity, ttl time.Duration, config *inferencev1alpha1.PrefixCacheConfig) *PrefixCache {
	cache := &PrefixCache{
		log:        log,
		cacheName:  cacheName,
		tierName:   tierName,
		maxSize:    size,
		defaultTTL: ttl,
		config:     config,
		tree: &PrefixTree{
			root: &PrefixNode{
				TokenID:  -1,
				Children: make(map[int32]*PrefixNode),
				Depth:    0,
			},
			blocks:   make(map[string]*PrefixCacheBlock),
			maxDepth: 2048,
		},
		gpuBlocks:    make(map[string]*PrefixCacheBlock),
		swapBlocks:   make(map[string]*PrefixCacheBlock),
		blockSize:    16,
		maxPrefixLen: 2048,
		minPrefixLen: 32,
		enableSwap:   true,
	}

	if ttl == 0 {
		cache.defaultTTL = time.Hour // Default 1 hour
	}

	if config != nil {
		if config.BlockSize > 0 {
			cache.blockSize = config.BlockSize
		}
		if config.MaxPrefixLength > 0 {
			cache.maxPrefixLen = config.MaxPrefixLength
		}
		if config.MinPrefixLength > 0 {
			cache.minPrefixLen = config.MinPrefixLength
		}
		cache.enableSwap = config.EnableSwap
		if config.SwapSpaceSize != nil {
			cache.swapSize = config.SwapSpaceSize.Value()
		}
	}

	// Start background eviction
	go cache.evictionLoop()

	return cache
}

// evictionLoop handles block eviction
func (c *PrefixCache) evictionLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		c.evictExpired()
		c.evictLRU()
	}
}

// evictExpired removes expired blocks
func (c *PrefixCache) evictExpired() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for hash, block := range c.gpuBlocks {
		if now.After(block.ExpiresAt) && block.RefCount.Load() == 0 {
			c.removeBlock(hash, block)
		}
	}
}

// evictLRU evicts least recently used blocks when cache is full
func (c *PrefixCache) evictLRU() {
	c.mu.Lock()
	defer c.mu.Unlock()

	targetSize := c.maxSize.Value() * 80 / 100 // Target 80%

	if c.usedSize.Load() <= targetSize {
		return
	}

	// Collect blocks for eviction (LRU order)
	type blockInfo struct {
		hash       string
		block      *PrefixCacheBlock
		accessedAt time.Time
	}

	blocks := make([]blockInfo, 0, len(c.gpuBlocks))
	for hash, block := range c.gpuBlocks {
		if block.RefCount.Load() == 0 {
			blocks = append(blocks, blockInfo{hash, block, block.AccessedAt})
		}
	}

	// Sort by access time (oldest first)
	sort.Slice(blocks, func(i, j int) bool {
		return blocks[i].accessedAt.Before(blocks[j].accessedAt)
	})

	// Evict until under target
	for _, bi := range blocks {
		if c.usedSize.Load() <= targetSize {
			break
		}

		if c.enableSwap && c.swapUsed.Load() < c.swapSize {
			// Move to swap instead of deleting
			c.moveToSwap(bi.hash, bi.block)
		} else {
			c.removeBlock(bi.hash, bi.block)
		}
	}
}

// moveToSwap moves a block from GPU to swap space
func (c *PrefixCache) moveToSwap(hash string, block *PrefixCacheBlock) {
	block.InGPU = false
	block.InSwap = true
	delete(c.gpuBlocks, hash)
	c.swapBlocks[hash] = block
	c.usedSize.Add(-block.Size)
	c.swapUsed.Add(block.Size)
	c.log.V(2).Info("Moved block to swap", "hash", hash[:16])
}

// moveToGPU moves a block from swap to GPU memory
func (c *PrefixCache) moveToGPU(hash string, block *PrefixCacheBlock) error {
	// Check if there's space
	if c.usedSize.Load()+block.Size > c.maxSize.Value() {
		c.evictLRU()
	}

	block.InGPU = true
	block.InSwap = false
	delete(c.swapBlocks, hash)
	c.gpuBlocks[hash] = block
	c.swapUsed.Add(-block.Size)
	c.usedSize.Add(block.Size)
	c.log.V(2).Info("Moved block to GPU", "hash", hash[:16])
	return nil
}

// removeBlock removes a block completely
func (c *PrefixCache) removeBlock(hash string, block *PrefixCacheBlock) {
	if block.InGPU {
		delete(c.gpuBlocks, hash)
		c.usedSize.Add(-block.Size)
	}
	if block.InSwap {
		delete(c.swapBlocks, hash)
		c.swapUsed.Add(-block.Size)
	}
	delete(c.tree.blocks, hash)
	c.entries.Add(-1)
}

// Name returns the tier name
func (c *PrefixCache) Name() string {
	return c.tierName
}

// Type returns the tier type
func (c *PrefixCache) Type() inferencev1alpha1.CacheTierType {
	return inferencev1alpha1.CacheTierTypePrefix
}

// hashTokens creates a hash for a token sequence
func (c *PrefixCache) hashTokens(tokens []int32) string {
	data := make([]byte, len(tokens)*4)
	for i, t := range tokens {
		data[i*4] = byte(t >> 24)
		data[i*4+1] = byte(t >> 16)
		data[i*4+2] = byte(t >> 8)
		data[i*4+3] = byte(t)
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// Get retrieves prefix blocks for a token sequence
func (c *PrefixCache) Get(ctx context.Context, key string) (*CacheEntry, error) {
	// Key format: "tokens:<token1>,<token2>,...,<tokenN>"
	// For simplicity, we'll treat the key as a hash of tokens

	c.mu.RLock()
	block, exists := c.gpuBlocks[key]
	if !exists {
		block, exists = c.swapBlocks[key]
	}
	c.mu.RUnlock()

	if !exists {
		c.misses.Add(1)
		return nil, nil
	}

	// Check expiration
	if time.Now().After(block.ExpiresAt) {
		c.mu.Lock()
		c.removeBlock(key, block)
		c.mu.Unlock()
		c.misses.Add(1)
		return nil, nil
	}

	c.hits.Add(1)
	block.RefCount.Add(1)
	defer block.RefCount.Add(-1)

	// Update access time
	c.mu.Lock()
	block.AccessedAt = time.Now()
	// Move from swap to GPU if needed
	if block.InSwap {
		c.moveToGPU(key, block)
	}
	c.mu.Unlock()

	return &CacheEntry{
		Key:        key,
		Value:      block.KVCache,
		Size:       block.Size,
		CreatedAt:  block.CreatedAt,
		AccessedAt: block.AccessedAt,
		Tier:       c.tierName,
	}, nil
}

// Set stores prefix blocks for a token sequence
func (c *PrefixCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if ttl == 0 {
		ttl = c.defaultTTL
	}

	now := time.Now()
	block := &PrefixCacheBlock{
		Hash:       key,
		KVCache:    value,
		Size:       int64(len(value)),
		CreatedAt:  now,
		AccessedAt: now,
		ExpiresAt:  now.Add(ttl),
		InGPU:      true,
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Check if replacing existing
	if old, exists := c.gpuBlocks[key]; exists {
		c.removeBlock(key, old)
	} else if old, exists := c.swapBlocks[key]; exists {
		c.removeBlock(key, old)
	}

	// Check space
	if c.usedSize.Load()+block.Size > c.maxSize.Value() {
		// Trigger eviction
		go c.evictLRU()
	}

	c.gpuBlocks[key] = block
	c.tree.blocks[key] = block
	c.usedSize.Add(block.Size)
	c.entries.Add(1)

	return nil
}

// Delete removes prefix blocks
func (c *PrefixCache) Delete(ctx context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if block, exists := c.gpuBlocks[key]; exists {
		c.removeBlock(key, block)
	} else if block, exists := c.swapBlocks[key]; exists {
		c.removeBlock(key, block)
	}

	return nil
}

// Clear removes all entries
func (c *PrefixCache) Clear(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.gpuBlocks = make(map[string]*PrefixCacheBlock)
	c.swapBlocks = make(map[string]*PrefixCacheBlock)
	c.tree.blocks = make(map[string]*PrefixCacheBlock)
	c.tree.root.Children = make(map[int32]*PrefixNode)
	c.usedSize.Store(0)
	c.swapUsed.Store(0)
	c.entries.Store(0)

	return nil
}

// Size returns the maximum size
func (c *PrefixCache) Size() resource.Quantity {
	return c.maxSize
}

// UsedSize returns the current used size
func (c *PrefixCache) UsedSize() resource.Quantity {
	return *resource.NewQuantity(c.usedSize.Load(), resource.BinarySI)
}

// Entries returns the number of entries
func (c *PrefixCache) Entries() int64 {
	return c.entries.Load()
}

// Stats returns cache statistics
func (c *PrefixCache) Stats() TierStats {
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

// Healthy returns whether the cache is healthy
func (c *PrefixCache) Healthy() bool {
	return true // GPU memory is always "healthy" in this simulation
}

// MatchPrefix finds the longest matching prefix in the cache
func (c *PrefixCache) MatchPrefix(tokens []int32) (matchedLen int, blocks []*PrefixCacheBlock) {
	c.tree.mu.RLock()
	defer c.tree.mu.RUnlock()

	current := c.tree.root
	matched := 0
	matchedBlocks := make([]*PrefixCacheBlock, 0)

	for i, token := range tokens {
		if i >= int(c.maxPrefixLen) {
			break
		}

		child, exists := current.Children[token]
		if !exists {
			break
		}

		current = child
		matched = i + 1

		if current.Block != nil {
			matchedBlocks = append(matchedBlocks, current.Block)
		}
	}

	return matched, matchedBlocks
}

// InsertPrefix inserts a prefix into the cache tree
func (c *PrefixCache) InsertPrefix(tokens []int32, kvCache []byte, ttl time.Duration) error {
	if len(tokens) < int(c.minPrefixLen) {
		return fmt.Errorf("prefix too short: %d < %d", len(tokens), c.minPrefixLen)
	}
	if len(tokens) > int(c.maxPrefixLen) {
		tokens = tokens[:c.maxPrefixLen]
	}

	// Create blocks for each block boundary
	numBlocks := (len(tokens) + int(c.blockSize) - 1) / int(c.blockSize)
	blockStart := 0

	for i := 0; i < numBlocks; i++ {
		blockEnd := blockStart + int(c.blockSize)
		if blockEnd > len(tokens) {
			blockEnd = len(tokens)
		}

		blockTokens := tokens[blockStart:blockEnd]
		blockHash := c.hashTokens(tokens[:blockEnd])

		// Calculate KV cache slice for this block
		kvStart := blockStart * len(kvCache) / len(tokens)
		kvEnd := blockEnd * len(kvCache) / len(tokens)
		blockKV := kvCache[kvStart:kvEnd]

		if err := c.Set(context.Background(), blockHash, blockKV, ttl); err != nil {
			return err
		}

		// Insert into tree
		c.insertIntoTree(blockTokens, blockHash)

		blockStart = blockEnd
	}

	return nil
}

// insertIntoTree inserts a token sequence into the prefix tree
func (c *PrefixCache) insertIntoTree(tokens []int32, blockHash string) {
	c.tree.mu.Lock()
	defer c.tree.mu.Unlock()

	current := c.tree.root
	for i, token := range tokens {
		if _, exists := current.Children[token]; !exists {
			current.Children[token] = &PrefixNode{
				TokenID:  token,
				Children: make(map[int32]*PrefixNode),
				Depth:    int32(i + 1),
			}
		}
		current = current.Children[token]
	}

	// Link block to leaf
	if block, exists := c.tree.blocks[blockHash]; exists {
		current.Block = block
	}
}

// GetSwapStats returns swap space statistics
func (c *PrefixCache) GetSwapStats() map[string]interface{} {
	return map[string]interface{}{
		"enabled":    c.enableSwap,
		"maxSize":    c.swapSize,
		"usedSize":   c.swapUsed.Load(),
		"blockCount": len(c.swapBlocks),
		"gpuBlocks":  len(c.gpuBlocks),
	}
}

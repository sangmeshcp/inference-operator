package gpu

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/go-logr/logr"

	inferencev1alpha1 "github.com/inference-operator/inference-operator/api/v1alpha1"
)

// TimeSlot represents a time slot allocation
type TimeSlot struct {
	SlotID      string
	ServiceName string
	Namespace   string
	StartOffset time.Duration // Offset from the beginning of the interval
	Duration    time.Duration
	Priority    int32
	Active      bool
}

// TimesliceGPU represents a GPU configured for time-slicing
type TimesliceGPU struct {
	NodeName       string
	DeviceID       string
	SlotInterval   time.Duration
	Slots          []*TimeSlot
	MaxSlots       int32
	CurrentSlotIdx int
	LastScheduled  time.Time
	TotalAllocated time.Duration
}

// TimeslicePoolConfig holds time-slicing configuration for a pool
type TimeslicePoolConfig struct {
	PoolName     string
	SlotInterval time.Duration            // e.g., 100ms
	MaxSlots     int32                    // Maximum number of time slots
	GPUs         map[string]*TimesliceGPU // key: nodeName/deviceID
}

// TimesliceAllocation represents a time-slice allocation for a service
type TimesliceAllocation struct {
	ServiceName  string
	Namespace    string
	NodeName     string
	DeviceID     string
	SlotID       string
	SlotDuration time.Duration
	SlotFraction float64 // Fraction of total time allocated (0.0 - 1.0)
	AllocatedAt  time.Time
}

// TimesliceManager manages GPU time-slicing
type TimesliceManager struct {
	log         logr.Logger
	mu          sync.RWMutex
	pools       map[string]*TimeslicePoolConfig
	allocations map[string][]*TimesliceAllocation // key: namespace/serviceName
	schedulerCh chan struct{}
	running     bool
	stopCh      chan struct{}
}

// NewTimesliceManager creates a new time-slice manager
func NewTimesliceManager(log logr.Logger) *TimesliceManager {
	return &TimesliceManager{
		log:         log,
		pools:       make(map[string]*TimeslicePoolConfig),
		allocations: make(map[string][]*TimesliceAllocation),
		schedulerCh: make(chan struct{}, 1),
		stopCh:      make(chan struct{}),
	}
}

// Start starts the time-slice manager
func (m *TimesliceManager) Start(ctx context.Context) error {
	m.log.Info("Starting time-slice manager")
	m.running = true

	// Start the scheduler loop
	go m.runScheduler(ctx)

	return nil
}

// Stop stops the time-slice manager
func (m *TimesliceManager) Stop() {
	m.log.Info("Stopping time-slice manager")
	m.running = false
	close(m.stopCh)
}

// runScheduler runs the time-slice scheduler loop
func (m *TimesliceManager) runScheduler(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.scheduleSlots()
		case <-m.schedulerCh:
			m.scheduleSlots()
		}
	}
}

// scheduleSlots performs time-slice scheduling
func (m *TimesliceManager) scheduleSlots() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()

	for _, pool := range m.pools {
		for _, gpu := range pool.GPUs {
			if len(gpu.Slots) == 0 {
				continue
			}

			// Check if we need to switch slots
			elapsed := now.Sub(gpu.LastScheduled)
			slotDuration := gpu.SlotInterval / time.Duration(len(gpu.Slots))

			if elapsed >= slotDuration {
				// Move to next slot
				gpu.CurrentSlotIdx = (gpu.CurrentSlotIdx + 1) % len(gpu.Slots)
				gpu.LastScheduled = now

				currentSlot := gpu.Slots[gpu.CurrentSlotIdx]
				m.log.V(2).Info("Switching time slot",
					"gpu", fmt.Sprintf("%s/%s", gpu.NodeName, gpu.DeviceID),
					"slot", currentSlot.SlotID,
					"service", currentSlot.ServiceName,
				)

				// In a real implementation, this would:
				// 1. Signal the current process to yield
				// 2. Allow the next process to execute
				// 3. Update GPU context if necessary
			}
		}
	}
}

// ConfigurePool configures time-slicing for a GPU pool
func (m *TimesliceManager) ConfigurePool(ctx context.Context, pool *inferencev1alpha1.GPUPool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Info("Configuring time-slicing for pool", "pool", pool.Name)

	if pool.Spec.Sharing == nil || pool.Spec.Sharing.Mode != inferencev1alpha1.GPUSharingModeTimeslice {
		return fmt.Errorf("pool %s is not configured for time-slice mode", pool.Name)
	}

	slotInterval := time.Duration(pool.Spec.Sharing.TimeSliceInterval) * time.Millisecond
	if slotInterval == 0 {
		slotInterval = 100 * time.Millisecond // Default 100ms
	}

	config := &TimeslicePoolConfig{
		PoolName:     pool.Name,
		SlotInterval: slotInterval,
		MaxSlots:     10, // Default max 10 slots per GPU
		GPUs:         make(map[string]*TimesliceGPU),
	}

	m.pools[pool.Name] = config

	return nil
}

// CleanupPool removes time-slicing configuration for a pool
func (m *TimesliceManager) CleanupPool(ctx context.Context, poolName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Info("Cleaning up time-slicing for pool", "pool", poolName)

	config, exists := m.pools[poolName]
	if !exists {
		return nil
	}

	// Check for active slots
	for key, gpu := range config.GPUs {
		if len(gpu.Slots) > 0 {
			return fmt.Errorf("cannot cleanup pool %s: GPU %s has active slots", poolName, key)
		}
	}

	delete(m.pools, poolName)
	return nil
}

// Allocate allocates time-slice resources for a service
func (m *TimesliceManager) Allocate(ctx context.Context, req AllocationRequest, pool *PoolInfo) ([]GPUAllocationInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Info("Allocating time-slice resources",
		"service", req.ServiceName,
		"namespace", req.Namespace,
		"count", req.GPUCount,
	)

	config, exists := m.pools[pool.Name]
	if !exists {
		return nil, fmt.Errorf("time-slice pool not found: %s", pool.Name)
	}

	allocations := make([]GPUAllocationInfo, 0, req.GPUCount)
	serviceKey := fmt.Sprintf("%s/%s", req.Namespace, req.ServiceName)

	for i := 0; i < int(req.GPUCount); i++ {
		gpu, err := m.findOrCreateTimesliceGPU(ctx, config, pool)
		if err != nil {
			// Rollback
			m.releaseSlots(ctx, req.ServiceName, req.Namespace)
			return nil, err
		}

		// Create slot
		slotID := fmt.Sprintf("slot-%s-%s-%d", req.ServiceName, req.Namespace, len(gpu.Slots))
		slot := &TimeSlot{
			SlotID:      slotID,
			ServiceName: req.ServiceName,
			Namespace:   req.Namespace,
			Duration:    config.SlotInterval / time.Duration(len(gpu.Slots)+1),
			Priority:    0,
			Active:      true,
		}

		gpu.Slots = append(gpu.Slots, slot)
		m.rebalanceSlots(gpu, config.SlotInterval)

		allocation := &TimesliceAllocation{
			ServiceName:  req.ServiceName,
			Namespace:    req.Namespace,
			NodeName:     gpu.NodeName,
			DeviceID:     gpu.DeviceID,
			SlotID:       slotID,
			SlotDuration: slot.Duration,
			SlotFraction: float64(slot.Duration) / float64(config.SlotInterval),
			AllocatedAt:  time.Now(),
		}

		m.allocations[serviceKey] = append(m.allocations[serviceKey], allocation)

		allocations = append(allocations, GPUAllocationInfo{
			NodeName: gpu.NodeName,
			DeviceID: gpu.DeviceID,
			Memory:   req.Memory,
		})
	}

	// Trigger scheduler
	select {
	case m.schedulerCh <- struct{}{}:
	default:
	}

	return allocations, nil
}

// findOrCreateTimesliceGPU finds a GPU with available slots or configures a new one
func (m *TimesliceManager) findOrCreateTimesliceGPU(ctx context.Context, config *TimeslicePoolConfig, pool *PoolInfo) (*TimesliceGPU, error) {
	// Find existing GPU with available slots
	for _, gpu := range config.GPUs {
		if int32(len(gpu.Slots)) < gpu.MaxSlots {
			return gpu, nil
		}
	}

	// Configure a new GPU for time-slicing
	for _, poolGPU := range pool.GPUs {
		key := fmt.Sprintf("%s/%s", poolGPU.NodeName, poolGPU.DeviceID)
		if _, exists := config.GPUs[key]; !exists {
			tsGPU := &TimesliceGPU{
				NodeName:       poolGPU.NodeName,
				DeviceID:       poolGPU.DeviceID,
				SlotInterval:   config.SlotInterval,
				Slots:          make([]*TimeSlot, 0),
				MaxSlots:       config.MaxSlots,
				CurrentSlotIdx: 0,
				LastScheduled:  time.Now(),
			}
			config.GPUs[key] = tsGPU
			m.log.Info("Configured GPU for time-slicing", "key", key)
			return tsGPU, nil
		}
	}

	return nil, fmt.Errorf("no available GPUs for time-slicing")
}

// rebalanceSlots rebalances time slots to ensure fair distribution
func (m *TimesliceManager) rebalanceSlots(gpu *TimesliceGPU, totalInterval time.Duration) {
	if len(gpu.Slots) == 0 {
		return
	}

	// Sort slots by priority
	sort.Slice(gpu.Slots, func(i, j int) bool {
		return gpu.Slots[i].Priority > gpu.Slots[j].Priority
	})

	// Calculate equal time for each slot
	equalDuration := totalInterval / time.Duration(len(gpu.Slots))

	// Assign durations based on priority
	// Higher priority slots get slightly more time
	totalPriority := int32(0)
	for _, slot := range gpu.Slots {
		totalPriority += slot.Priority + 1 // +1 to ensure non-zero
	}

	var offset time.Duration
	for _, slot := range gpu.Slots {
		// Weight by priority
		weight := float64(slot.Priority+1) / float64(totalPriority)
		slot.Duration = time.Duration(float64(totalInterval) * weight)
		if slot.Duration < 10*time.Millisecond {
			slot.Duration = 10 * time.Millisecond // Minimum 10ms
		}
		slot.StartOffset = offset
		offset += slot.Duration
	}

	// Adjust last slot to fill remaining time
	if len(gpu.Slots) > 0 && offset < totalInterval {
		gpu.Slots[len(gpu.Slots)-1].Duration += totalInterval - offset
	}

	gpu.TotalAllocated = offset
	m.log.V(1).Info("Rebalanced time slots",
		"gpu", fmt.Sprintf("%s/%s", gpu.NodeName, gpu.DeviceID),
		"slots", len(gpu.Slots),
		"equalDuration", equalDuration,
	)
}

// releaseSlots releases time slots for a service
func (m *TimesliceManager) releaseSlots(ctx context.Context, serviceName, namespace string) {
	serviceKey := fmt.Sprintf("%s/%s", namespace, serviceName)

	for _, config := range m.pools {
		for _, gpu := range config.GPUs {
			newSlots := make([]*TimeSlot, 0)
			for _, slot := range gpu.Slots {
				if slot.ServiceName != serviceName || slot.Namespace != namespace {
					newSlots = append(newSlots, slot)
				}
			}
			if len(newSlots) != len(gpu.Slots) {
				gpu.Slots = newSlots
				m.rebalanceSlots(gpu, config.SlotInterval)
				m.log.V(1).Info("Released time slots",
					"gpu", fmt.Sprintf("%s/%s", gpu.NodeName, gpu.DeviceID),
					"service", serviceName,
				)
			}
		}
	}

	delete(m.allocations, serviceKey)
}

// Release releases all time-slice resources allocated to a service
func (m *TimesliceManager) Release(ctx context.Context, serviceName, namespace string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Info("Releasing time-slice resources", "service", serviceName, "namespace", namespace)
	m.releaseSlots(ctx, serviceName, namespace)

	// Trigger scheduler
	select {
	case m.schedulerCh <- struct{}{}:
	default:
	}

	return nil
}

// GetSlotInfo returns information about a specific slot
func (m *TimesliceManager) GetSlotInfo(slotID string) (*TimeSlot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, config := range m.pools {
		for _, gpu := range config.GPUs {
			for _, slot := range gpu.Slots {
				if slot.SlotID == slotID {
					return slot, nil
				}
			}
		}
	}

	return nil, fmt.Errorf("slot not found: %s", slotID)
}

// SetSlotPriority sets the priority for a time slot
func (m *TimesliceManager) SetSlotPriority(slotID string, priority int32) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, config := range m.pools {
		for _, gpu := range config.GPUs {
			for _, slot := range gpu.Slots {
				if slot.SlotID == slotID {
					slot.Priority = priority
					m.rebalanceSlots(gpu, config.SlotInterval)
					m.log.Info("Updated slot priority",
						"slotID", slotID,
						"priority", priority,
					)
					return nil
				}
			}
		}
	}

	return fmt.Errorf("slot not found: %s", slotID)
}

// GetGPUStats returns statistics for a time-sliced GPU
func (m *TimesliceManager) GetGPUStats(nodeName, deviceID string) (map[string]interface{}, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	key := fmt.Sprintf("%s/%s", nodeName, deviceID)

	for _, config := range m.pools {
		if gpu, exists := config.GPUs[key]; exists {
			stats := map[string]interface{}{
				"nodeName":       gpu.NodeName,
				"deviceId":       gpu.DeviceID,
				"slotInterval":   gpu.SlotInterval.String(),
				"slotCount":      len(gpu.Slots),
				"maxSlots":       gpu.MaxSlots,
				"currentSlot":    gpu.CurrentSlotIdx,
				"lastScheduled":  gpu.LastScheduled,
				"totalAllocated": gpu.TotalAllocated.String(),
				"utilization":    float64(gpu.TotalAllocated) / float64(gpu.SlotInterval) * 100,
			}

			slotDetails := make([]map[string]interface{}, 0, len(gpu.Slots))
			for _, slot := range gpu.Slots {
				slotDetails = append(slotDetails, map[string]interface{}{
					"slotId":      slot.SlotID,
					"serviceName": slot.ServiceName,
					"namespace":   slot.Namespace,
					"duration":    slot.Duration.String(),
					"priority":    slot.Priority,
					"active":      slot.Active,
				})
			}
			stats["slots"] = slotDetails

			return stats, nil
		}
	}

	return nil, fmt.Errorf("GPU not found: %s", key)
}

// GetAllocations returns all allocations for a service
func (m *TimesliceManager) GetAllocations(serviceName, namespace string) ([]*TimesliceAllocation, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	serviceKey := fmt.Sprintf("%s/%s", namespace, serviceName)
	allocs, exists := m.allocations[serviceKey]
	if !exists {
		return nil, fmt.Errorf("no allocations found for %s", serviceKey)
	}

	return allocs, nil
}

// PauseSlot temporarily pauses a time slot
func (m *TimesliceManager) PauseSlot(slotID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, config := range m.pools {
		for _, gpu := range config.GPUs {
			for _, slot := range gpu.Slots {
				if slot.SlotID == slotID {
					slot.Active = false
					m.log.Info("Paused time slot", "slotID", slotID)
					return nil
				}
			}
		}
	}

	return fmt.Errorf("slot not found: %s", slotID)
}

// ResumeSlot resumes a paused time slot
func (m *TimesliceManager) ResumeSlot(slotID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, config := range m.pools {
		for _, gpu := range config.GPUs {
			for _, slot := range gpu.Slots {
				if slot.SlotID == slotID {
					slot.Active = true
					m.log.Info("Resumed time slot", "slotID", slotID)
					return nil
				}
			}
		}
	}

	return fmt.Errorf("slot not found: %s", slotID)
}

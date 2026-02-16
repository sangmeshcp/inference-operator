package gpu

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
	gpuUtilizationGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "inference_gpu_utilization",
			Help: "Current GPU utilization percentage",
		},
		[]string{"node", "device_id", "pool"},
	)

	gpuMemoryUsedGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "inference_gpu_memory_used_bytes",
			Help: "GPU memory used in bytes",
		},
		[]string{"node", "device_id", "pool"},
	)

	gpuMemoryTotalGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "inference_gpu_memory_total_bytes",
			Help: "Total GPU memory in bytes",
		},
		[]string{"node", "device_id", "pool"},
	)

	gpuTemperatureGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "inference_gpu_temperature_celsius",
			Help: "GPU temperature in Celsius",
		},
		[]string{"node", "device_id", "pool"},
	)

	gpuPowerUsageGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "inference_gpu_power_usage_watts",
			Help: "GPU power usage in Watts",
		},
		[]string{"node", "device_id", "pool"},
	)

	gpuAllocationCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "inference_gpu_allocations_total",
			Help: "Total number of GPU allocations",
		},
		[]string{"pool", "sharing_mode"},
	)
)

func init() {
	metrics.Registry.MustRegister(
		gpuUtilizationGauge,
		gpuMemoryUsedGauge,
		gpuMemoryTotalGauge,
		gpuTemperatureGauge,
		gpuPowerUsageGauge,
		gpuAllocationCounter,
	)
}

// GPUInfo represents information about a GPU device
type GPUInfo struct {
	DeviceID        string
	UUID            string
	Model           string
	NodeName        string
	TotalMemory     resource.Quantity
	AvailableMemory resource.Quantity
	Utilization     int32
	Temperature     int32
	PowerUsage      int32
	MIGEnabled      bool
	MIGInstances    []MIGInstance
	Allocations     []Allocation
}

// MIGInstance represents a MIG instance
type MIGInstance struct {
	InstanceID   string
	Profile      string
	Memory       resource.Quantity
	ComputeUnits int32
	InUse        bool
	AllocatedTo  string
}

// Allocation represents a GPU allocation
type Allocation struct {
	ServiceRef      string
	Namespace       string
	AllocatedMemory resource.Quantity
	AllocatedAt     time.Time
	SharingMode     inferencev1alpha1.GPUSharingMode
}

// AllocationRequest represents a request to allocate GPU resources
type AllocationRequest struct {
	ServiceName string
	Namespace   string
	GPUType     string
	GPUCount    int32
	Memory      resource.Quantity
	SharingMode inferencev1alpha1.GPUSharingMode
	MIGProfile  string
	PoolName    string
}

// AllocationResult represents the result of an allocation request
type AllocationResult struct {
	Success     bool
	Allocations []GPUAllocationInfo
	Error       error
}

// GPUAllocationInfo contains information about a single GPU allocation
type GPUAllocationInfo struct {
	NodeName    string
	DeviceID    string
	MIGInstance string
	Memory      resource.Quantity
}

// Manager manages GPU resources across the cluster
type Manager struct {
	log               logr.Logger
	mu                sync.RWMutex
	gpus              map[string]*GPUInfo // key: nodeName/deviceID
	pools             map[string]*PoolInfo
	migManager        *MIGManager
	mpsManager        *MPSManager
	timesliceManager  *TimesliceManager
	discoveryInterval time.Duration
	stopCh            chan struct{}
}

// PoolInfo contains information about a GPU pool
type PoolInfo struct {
	Name                  string
	NodeSelector          map[string]string
	GPUType               string
	SharingMode           inferencev1alpha1.GPUSharingMode
	MIGProfiles           []inferencev1alpha1.MIGProfileSpec
	OversubscriptionRatio float64
	TotalGPUs             int32
	AvailableGPUs         int32
	GPUs                  []*GPUInfo
}

// NewManager creates a new GPU manager
func NewManager(log logr.Logger) *Manager {
	m := &Manager{
		log:               log,
		gpus:              make(map[string]*GPUInfo),
		pools:             make(map[string]*PoolInfo),
		discoveryInterval: 30 * time.Second,
		stopCh:            make(chan struct{}),
	}

	m.migManager = NewMIGManager(log.WithName("mig"))
	m.mpsManager = NewMPSManager(log.WithName("mps"))
	m.timesliceManager = NewTimesliceManager(log.WithName("timeslice"))

	return m
}

// Start starts the GPU manager
func (m *Manager) Start(ctx context.Context) error {
	m.log.Info("Starting GPU manager")

	// Initial discovery
	if err := m.discoverGPUs(ctx); err != nil {
		m.log.Error(err, "Initial GPU discovery failed")
	}

	// Start periodic discovery
	go m.runDiscoveryLoop(ctx)

	// Start sub-managers
	if err := m.mpsManager.Start(ctx); err != nil {
		return fmt.Errorf("failed to start MPS manager: %w", err)
	}

	if err := m.timesliceManager.Start(ctx); err != nil {
		return fmt.Errorf("failed to start timeslice manager: %w", err)
	}

	return nil
}

// Stop stops the GPU manager
func (m *Manager) Stop() {
	m.log.Info("Stopping GPU manager")
	close(m.stopCh)
	m.mpsManager.Stop()
	m.timesliceManager.Stop()
}

// runDiscoveryLoop runs the GPU discovery loop
func (m *Manager) runDiscoveryLoop(ctx context.Context) {
	ticker := time.NewTicker(m.discoveryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		case <-ticker.C:
			if err := m.discoverGPUs(ctx); err != nil {
				m.log.Error(err, "GPU discovery failed")
			}
		}
	}
}

// discoverGPUs discovers available GPUs in the cluster
func (m *Manager) discoverGPUs(ctx context.Context) error {
	m.log.V(1).Info("Discovering GPUs")

	// In a real implementation, this would:
	// 1. Query the NVIDIA DCGM or device plugin
	// 2. List GPUs on each node
	// 3. Collect metrics and status

	// For now, we'll simulate discovery
	// This would be replaced with actual DCGM calls

	m.mu.Lock()
	defer m.mu.Unlock()

	// Update metrics for existing GPUs
	for key, gpu := range m.gpus {
		m.updateGPUMetrics(key, gpu)
	}

	return nil
}

// updateGPUMetrics updates Prometheus metrics for a GPU
func (m *Manager) updateGPUMetrics(key string, gpu *GPUInfo) {
	poolName := m.getPoolForGPU(gpu)

	gpuUtilizationGauge.WithLabelValues(gpu.NodeName, gpu.DeviceID, poolName).Set(float64(gpu.Utilization))

	memUsed := gpu.TotalMemory.Value() - gpu.AvailableMemory.Value()
	gpuMemoryUsedGauge.WithLabelValues(gpu.NodeName, gpu.DeviceID, poolName).Set(float64(memUsed))
	gpuMemoryTotalGauge.WithLabelValues(gpu.NodeName, gpu.DeviceID, poolName).Set(float64(gpu.TotalMemory.Value()))

	gpuTemperatureGauge.WithLabelValues(gpu.NodeName, gpu.DeviceID, poolName).Set(float64(gpu.Temperature))
	gpuPowerUsageGauge.WithLabelValues(gpu.NodeName, gpu.DeviceID, poolName).Set(float64(gpu.PowerUsage))
}

// getPoolForGPU returns the pool name for a GPU
func (m *Manager) getPoolForGPU(gpu *GPUInfo) string {
	for name, pool := range m.pools {
		for _, poolGPU := range pool.GPUs {
			if poolGPU.UUID == gpu.UUID {
				return name
			}
		}
	}
	return "default"
}

// RegisterPool registers a GPU pool
func (m *Manager) RegisterPool(ctx context.Context, pool *inferencev1alpha1.GPUPool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Info("Registering GPU pool", "name", pool.Name)

	sharingMode := inferencev1alpha1.GPUSharingModeExclusive
	var migProfiles []inferencev1alpha1.MIGProfileSpec
	var oversubscriptionRatio float64 = 1.0

	if pool.Spec.Sharing != nil {
		sharingMode = pool.Spec.Sharing.Mode
		migProfiles = pool.Spec.Sharing.Profiles
	}

	if pool.Spec.Oversubscription != nil && pool.Spec.Oversubscription.Enabled {
		// Parse ratio string to float
		fmt.Sscanf(pool.Spec.Oversubscription.Ratio, "%f", &oversubscriptionRatio)
	}

	poolInfo := &PoolInfo{
		Name:                  pool.Name,
		NodeSelector:          pool.Spec.NodeSelector,
		GPUType:               pool.Spec.GPUType,
		SharingMode:           sharingMode,
		MIGProfiles:           migProfiles,
		OversubscriptionRatio: oversubscriptionRatio,
		GPUs:                  make([]*GPUInfo, 0),
	}

	m.pools[pool.Name] = poolInfo

	// Configure sharing mode
	switch sharingMode {
	case inferencev1alpha1.GPUSharingModeMIG:
		if err := m.migManager.ConfigurePool(ctx, pool); err != nil {
			return fmt.Errorf("failed to configure MIG for pool: %w", err)
		}
	case inferencev1alpha1.GPUSharingModeMPS:
		if err := m.mpsManager.ConfigurePool(ctx, pool); err != nil {
			return fmt.Errorf("failed to configure MPS for pool: %w", err)
		}
	case inferencev1alpha1.GPUSharingModeTimeslice:
		if err := m.timesliceManager.ConfigurePool(ctx, pool); err != nil {
			return fmt.Errorf("failed to configure time-slicing for pool: %w", err)
		}
	}

	return nil
}

// UnregisterPool unregisters a GPU pool
func (m *Manager) UnregisterPool(ctx context.Context, poolName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Info("Unregistering GPU pool", "name", poolName)

	pool, exists := m.pools[poolName]
	if !exists {
		return nil
	}

	// Cleanup based on sharing mode
	switch pool.SharingMode {
	case inferencev1alpha1.GPUSharingModeMIG:
		if err := m.migManager.CleanupPool(ctx, poolName); err != nil {
			m.log.Error(err, "Failed to cleanup MIG for pool", "name", poolName)
		}
	case inferencev1alpha1.GPUSharingModeMPS:
		if err := m.mpsManager.CleanupPool(ctx, poolName); err != nil {
			m.log.Error(err, "Failed to cleanup MPS for pool", "name", poolName)
		}
	case inferencev1alpha1.GPUSharingModeTimeslice:
		if err := m.timesliceManager.CleanupPool(ctx, poolName); err != nil {
			m.log.Error(err, "Failed to cleanup time-slicing for pool", "name", poolName)
		}
	}

	delete(m.pools, poolName)
	return nil
}

// Allocate allocates GPU resources for a service
func (m *Manager) Allocate(ctx context.Context, req AllocationRequest) (*AllocationResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Info("Allocating GPU resources",
		"service", req.ServiceName,
		"namespace", req.Namespace,
		"gpuCount", req.GPUCount,
		"sharingMode", req.SharingMode,
	)

	result := &AllocationResult{
		Allocations: make([]GPUAllocationInfo, 0),
	}

	// Get the pool
	pool, exists := m.pools[req.PoolName]
	if !exists {
		// Try to find a suitable pool
		pool = m.findSuitablePool(req)
		if pool == nil {
			result.Error = fmt.Errorf("no suitable GPU pool found")
			return result, result.Error
		}
	}

	// Allocate based on sharing mode
	var allocations []GPUAllocationInfo
	var err error

	switch req.SharingMode {
	case inferencev1alpha1.GPUSharingModeMIG:
		allocations, err = m.migManager.Allocate(ctx, req, pool)
	case inferencev1alpha1.GPUSharingModeMPS:
		allocations, err = m.mpsManager.Allocate(ctx, req, pool)
	case inferencev1alpha1.GPUSharingModeTimeslice:
		allocations, err = m.timesliceManager.Allocate(ctx, req, pool)
	default:
		allocations, err = m.allocateExclusive(ctx, req, pool)
	}

	if err != nil {
		result.Error = err
		return result, err
	}

	result.Success = true
	result.Allocations = allocations

	// Update metrics
	gpuAllocationCounter.WithLabelValues(pool.Name, string(req.SharingMode)).Inc()

	return result, nil
}

// allocateExclusive allocates exclusive GPU access
func (m *Manager) allocateExclusive(ctx context.Context, req AllocationRequest, pool *PoolInfo) ([]GPUAllocationInfo, error) {
	allocations := make([]GPUAllocationInfo, 0, req.GPUCount)

	availableGPUs := m.findAvailableGPUs(pool, int(req.GPUCount))
	if len(availableGPUs) < int(req.GPUCount) {
		return nil, fmt.Errorf("insufficient GPUs: requested %d, available %d", req.GPUCount, len(availableGPUs))
	}

	for i := 0; i < int(req.GPUCount); i++ {
		gpu := availableGPUs[i]

		allocation := Allocation{
			ServiceRef:      req.ServiceName,
			Namespace:       req.Namespace,
			AllocatedMemory: gpu.TotalMemory,
			AllocatedAt:     time.Now(),
			SharingMode:     inferencev1alpha1.GPUSharingModeExclusive,
		}
		gpu.Allocations = append(gpu.Allocations, allocation)

		allocations = append(allocations, GPUAllocationInfo{
			NodeName: gpu.NodeName,
			DeviceID: gpu.DeviceID,
			Memory:   gpu.TotalMemory,
		})
	}

	return allocations, nil
}

// findAvailableGPUs finds available GPUs in a pool
func (m *Manager) findAvailableGPUs(pool *PoolInfo, count int) []*GPUInfo {
	available := make([]*GPUInfo, 0)

	for _, gpu := range pool.GPUs {
		if len(gpu.Allocations) == 0 {
			available = append(available, gpu)
			if len(available) >= count {
				break
			}
		}
	}

	return available
}

// findSuitablePool finds a suitable pool for the allocation request
func (m *Manager) findSuitablePool(req AllocationRequest) *PoolInfo {
	for _, pool := range m.pools {
		if pool.GPUType != "" && pool.GPUType != req.GPUType {
			continue
		}
		if pool.SharingMode != req.SharingMode {
			continue
		}
		if pool.AvailableGPUs >= req.GPUCount {
			return pool
		}
	}
	return nil
}

// Release releases GPU resources for a service
func (m *Manager) Release(ctx context.Context, serviceName, namespace string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Info("Releasing GPU resources", "service", serviceName, "namespace", namespace)

	for _, gpu := range m.gpus {
		newAllocations := make([]Allocation, 0)
		for _, alloc := range gpu.Allocations {
			if alloc.ServiceRef != serviceName || alloc.Namespace != namespace {
				newAllocations = append(newAllocations, alloc)
			}
		}
		gpu.Allocations = newAllocations
	}

	return nil
}

// GetPoolStatus returns the status of a GPU pool
func (m *Manager) GetPoolStatus(poolName string) (*inferencev1alpha1.GPUPoolStatus, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	pool, exists := m.pools[poolName]
	if !exists {
		return nil, fmt.Errorf("pool not found: %s", poolName)
	}

	status := &inferencev1alpha1.GPUPoolStatus{
		Phase:         inferencev1alpha1.GPUPoolPhaseReady,
		TotalGPUs:     pool.TotalGPUs,
		AvailableGPUs: pool.AvailableGPUs,
		AllocatedGPUs: pool.TotalGPUs - pool.AvailableGPUs,
		Nodes:         make([]inferencev1alpha1.GPUNodeStatus, 0),
	}

	// Aggregate node status
	nodeGPUs := make(map[string][]*GPUInfo)
	for _, gpu := range pool.GPUs {
		nodeGPUs[gpu.NodeName] = append(nodeGPUs[gpu.NodeName], gpu)
	}

	for nodeName, gpus := range nodeGPUs {
		nodeStatus := inferencev1alpha1.GPUNodeStatus{
			NodeName:      nodeName,
			TotalGPUs:     int32(len(gpus)),
			AvailableGPUs: 0,
			GPUs:          make([]inferencev1alpha1.GPUDeviceStatus, 0, len(gpus)),
		}

		for _, gpu := range gpus {
			if len(gpu.Allocations) == 0 {
				nodeStatus.AvailableGPUs++
			}

			deviceStatus := inferencev1alpha1.GPUDeviceStatus{
				DeviceID:        gpu.DeviceID,
				UUID:            gpu.UUID,
				Model:           gpu.Model,
				TotalMemory:     gpu.TotalMemory,
				AvailableMemory: gpu.AvailableMemory,
				Utilization:     gpu.Utilization,
				Temperature:     gpu.Temperature,
				PowerUsage:      gpu.PowerUsage,
				MIGEnabled:      gpu.MIGEnabled,
			}
			nodeStatus.GPUs = append(nodeStatus.GPUs, deviceStatus)
		}

		status.Nodes = append(status.Nodes, nodeStatus)
	}

	return status, nil
}

// GetGPUInfo returns information about a specific GPU
func (m *Manager) GetGPUInfo(nodeName, deviceID string) (*GPUInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	key := fmt.Sprintf("%s/%s", nodeName, deviceID)
	gpu, exists := m.gpus[key]
	if !exists {
		return nil, fmt.Errorf("GPU not found: %s", key)
	}

	return gpu, nil
}

// ListGPUs returns all known GPUs
func (m *Manager) ListGPUs() []*GPUInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	gpus := make([]*GPUInfo, 0, len(m.gpus))
	for _, gpu := range m.gpus {
		gpus = append(gpus, gpu)
	}
	return gpus
}

// HealthCheck performs a health check on GPUs
func (m *Manager) HealthCheck(ctx context.Context) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for key, gpu := range m.gpus {
		// Check for thermal issues
		if gpu.Temperature > 85 {
			m.log.V(0).Info("GPU temperature warning", "gpu", key, "temperature", gpu.Temperature)
		}

		// Check for memory issues
		if gpu.AvailableMemory.Value() < 0 {
			return fmt.Errorf("GPU %s has invalid memory state", key)
		}
	}

	return nil
}

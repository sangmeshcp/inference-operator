package gpu

import (
	"context"
	"fmt"
	"sync"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/resource"

	inferencev1alpha1 "github.com/inference-operator/inference-operator/api/v1alpha1"
)

// MIGProfile represents a MIG profile configuration
type MIGProfile struct {
	Name          string
	Memory        resource.Quantity
	ComputeSlices int32
	MemorySlices  int32
	MaxInstances  int32
}

// Well-known MIG profiles for A100 and H100
var migProfiles = map[string]MIGProfile{
	// A100 80GB profiles
	"7g.80gb": {Name: "7g.80gb", Memory: resource.MustParse("80Gi"), ComputeSlices: 7, MemorySlices: 8, MaxInstances: 1},
	"4g.40gb": {Name: "4g.40gb", Memory: resource.MustParse("40Gi"), ComputeSlices: 4, MemorySlices: 4, MaxInstances: 1},
	"3g.40gb": {Name: "3g.40gb", Memory: resource.MustParse("40Gi"), ComputeSlices: 3, MemorySlices: 4, MaxInstances: 2},
	"2g.20gb": {Name: "2g.20gb", Memory: resource.MustParse("20Gi"), ComputeSlices: 2, MemorySlices: 2, MaxInstances: 3},
	"1g.10gb": {Name: "1g.10gb", Memory: resource.MustParse("10Gi"), ComputeSlices: 1, MemorySlices: 1, MaxInstances: 7},
	"1g.5gb":  {Name: "1g.5gb", Memory: resource.MustParse("5Gi"), ComputeSlices: 1, MemorySlices: 1, MaxInstances: 7},

	// A100 40GB profiles
	"3g.20gb": {Name: "3g.20gb", Memory: resource.MustParse("20Gi"), ComputeSlices: 3, MemorySlices: 4, MaxInstances: 2},
	"2g.10gb": {Name: "2g.10gb", Memory: resource.MustParse("10Gi"), ComputeSlices: 2, MemorySlices: 2, MaxInstances: 3},

	// H100 profiles
	"7g.141gb": {Name: "7g.141gb", Memory: resource.MustParse("141Gi"), ComputeSlices: 7, MemorySlices: 8, MaxInstances: 1},
	"4g.94gb":  {Name: "4g.94gb", Memory: resource.MustParse("94Gi"), ComputeSlices: 4, MemorySlices: 4, MaxInstances: 1},
	"3g.47gb":  {Name: "3g.47gb", Memory: resource.MustParse("47Gi"), ComputeSlices: 3, MemorySlices: 4, MaxInstances: 2},
	"2g.24gb":  {Name: "2g.24gb", Memory: resource.MustParse("24Gi"), ComputeSlices: 2, MemorySlices: 2, MaxInstances: 3},
	"1g.12gb":  {Name: "1g.12gb", Memory: resource.MustParse("12Gi"), ComputeSlices: 1, MemorySlices: 1, MaxInstances: 7},
}

// MIGInstanceInfo represents a created MIG instance
type MIGInstanceInfo struct {
	InstanceID  string
	GPUUUID     string
	Profile     MIGProfile
	NodeName    string
	InUse       bool
	AllocatedTo string
	Namespace   string
}

// MIGPoolConfig holds MIG configuration for a pool
type MIGPoolConfig struct {
	PoolName  string
	Profiles  []inferencev1alpha1.MIGProfileSpec
	Instances map[string]*MIGInstanceInfo // key: instanceID
}

// MIGManager manages MIG instances
type MIGManager struct {
	log       logr.Logger
	mu        sync.RWMutex
	pools     map[string]*MIGPoolConfig
	instances map[string]*MIGInstanceInfo // global index by instanceID
}

// NewMIGManager creates a new MIG manager
func NewMIGManager(log logr.Logger) *MIGManager {
	return &MIGManager{
		log:       log,
		pools:     make(map[string]*MIGPoolConfig),
		instances: make(map[string]*MIGInstanceInfo),
	}
}

// ConfigurePool configures MIG for a GPU pool
func (m *MIGManager) ConfigurePool(ctx context.Context, pool *inferencev1alpha1.GPUPool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Info("Configuring MIG for pool", "pool", pool.Name)

	if pool.Spec.Sharing == nil || pool.Spec.Sharing.Mode != inferencev1alpha1.GPUSharingModeMIG {
		return fmt.Errorf("pool %s is not configured for MIG mode", pool.Name)
	}

	config := &MIGPoolConfig{
		PoolName:  pool.Name,
		Profiles:  pool.Spec.Sharing.Profiles,
		Instances: make(map[string]*MIGInstanceInfo),
	}

	// Validate profiles
	for _, profile := range pool.Spec.Sharing.Profiles {
		if _, exists := migProfiles[profile.Name]; !exists {
			return fmt.Errorf("unknown MIG profile: %s", profile.Name)
		}
	}

	m.pools[pool.Name] = config

	// In a real implementation, this would:
	// 1. Discover GPUs matching the pool's nodeSelector
	// 2. Enable MIG mode on those GPUs (nvidia-smi -i <id> -mig 1)
	// 3. Create MIG instances according to the profiles
	// 4. Register the instances

	return nil
}

// CleanupPool removes MIG configuration for a pool
func (m *MIGManager) CleanupPool(ctx context.Context, poolName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Info("Cleaning up MIG for pool", "pool", poolName)

	config, exists := m.pools[poolName]
	if !exists {
		return nil
	}

	// Check for active allocations
	for _, instance := range config.Instances {
		if instance.InUse {
			return fmt.Errorf("cannot cleanup pool %s: instance %s is in use by %s/%s",
				poolName, instance.InstanceID, instance.Namespace, instance.AllocatedTo)
		}
	}

	// Remove instances from global index
	for instanceID := range config.Instances {
		delete(m.instances, instanceID)
	}

	// In a real implementation, this would:
	// 1. Destroy MIG instances (nvidia-smi mig -dci -i <gpu> -gi <gi>)
	// 2. Disable MIG mode (nvidia-smi -i <id> -mig 0)

	delete(m.pools, poolName)
	return nil
}

// Allocate allocates MIG instances for a service
func (m *MIGManager) Allocate(ctx context.Context, req AllocationRequest, pool *PoolInfo) ([]GPUAllocationInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Info("Allocating MIG instances",
		"service", req.ServiceName,
		"namespace", req.Namespace,
		"profile", req.MIGProfile,
		"count", req.GPUCount,
	)

	config, exists := m.pools[pool.Name]
	if !exists {
		return nil, fmt.Errorf("MIG pool not found: %s", pool.Name)
	}

	// Find available instances matching the profile
	profile, profileExists := migProfiles[req.MIGProfile]
	if !profileExists {
		return nil, fmt.Errorf("unknown MIG profile: %s", req.MIGProfile)
	}

	available := m.findAvailableInstances(config, req.MIGProfile, int(req.GPUCount))
	if len(available) < int(req.GPUCount) {
		// Try to create new instances
		needed := int(req.GPUCount) - len(available)
		created, err := m.createInstances(ctx, config, req.MIGProfile, needed, pool)
		if err != nil {
			return nil, fmt.Errorf("failed to create MIG instances: %w", err)
		}
		available = append(available, created...)
	}

	if len(available) < int(req.GPUCount) {
		return nil, fmt.Errorf("insufficient MIG instances: requested %d, available %d",
			req.GPUCount, len(available))
	}

	// Allocate instances
	allocations := make([]GPUAllocationInfo, 0, req.GPUCount)
	for i := 0; i < int(req.GPUCount); i++ {
		instance := available[i]
		instance.InUse = true
		instance.AllocatedTo = req.ServiceName
		instance.Namespace = req.Namespace

		allocations = append(allocations, GPUAllocationInfo{
			NodeName:    instance.NodeName,
			DeviceID:    instance.GPUUUID,
			MIGInstance: instance.InstanceID,
			Memory:      profile.Memory,
		})
	}

	return allocations, nil
}

// findAvailableInstances finds available MIG instances matching the profile
func (m *MIGManager) findAvailableInstances(config *MIGPoolConfig, profileName string, count int) []*MIGInstanceInfo {
	available := make([]*MIGInstanceInfo, 0)

	for _, instance := range config.Instances {
		if instance.Profile.Name == profileName && !instance.InUse {
			available = append(available, instance)
			if len(available) >= count {
				break
			}
		}
	}

	return available
}

// createInstances creates new MIG instances
func (m *MIGManager) createInstances(ctx context.Context, config *MIGPoolConfig, profileName string, count int, pool *PoolInfo) ([]*MIGInstanceInfo, error) {
	profile, exists := migProfiles[profileName]
	if !exists {
		return nil, fmt.Errorf("unknown MIG profile: %s", profileName)
	}

	created := make([]*MIGInstanceInfo, 0, count)

	// In a real implementation, this would:
	// 1. Find GPUs with available slices
	// 2. Create GPU instances (nvidia-smi mig -cgi <profile> -i <gpu>)
	// 3. Create compute instances (nvidia-smi mig -cci -gi <gi> -i <gpu>)

	// For now, simulate instance creation
	for i := 0; i < count; i++ {
		instanceID := fmt.Sprintf("mig-%s-%d", profileName, len(config.Instances)+i)

		instance := &MIGInstanceInfo{
			InstanceID: instanceID,
			GPUUUID:    fmt.Sprintf("GPU-simulated-%d", i),
			Profile:    profile,
			NodeName:   "node-1", // Would be determined by available GPU
			InUse:      false,
		}

		config.Instances[instanceID] = instance
		m.instances[instanceID] = instance
		created = append(created, instance)
	}

	m.log.Info("Created MIG instances", "profile", profileName, "count", len(created))
	return created, nil
}

// Release releases MIG instances allocated to a service
func (m *MIGManager) Release(ctx context.Context, serviceName, namespace string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Info("Releasing MIG instances", "service", serviceName, "namespace", namespace)

	for _, instance := range m.instances {
		if instance.AllocatedTo == serviceName && instance.Namespace == namespace {
			instance.InUse = false
			instance.AllocatedTo = ""
			instance.Namespace = ""
		}
	}

	return nil
}

// DestroyInstance destroys a specific MIG instance
func (m *MIGManager) DestroyInstance(ctx context.Context, instanceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	instance, exists := m.instances[instanceID]
	if !exists {
		return fmt.Errorf("MIG instance not found: %s", instanceID)
	}

	if instance.InUse {
		return fmt.Errorf("cannot destroy MIG instance %s: in use by %s/%s",
			instanceID, instance.Namespace, instance.AllocatedTo)
	}

	// In a real implementation, this would:
	// 1. Destroy compute instance (nvidia-smi mig -dci -gi <gi> -i <gpu>)
	// 2. Destroy GPU instance (nvidia-smi mig -dgi -gi <gi> -i <gpu>)

	// Remove from pool config
	for _, config := range m.pools {
		delete(config.Instances, instanceID)
	}

	delete(m.instances, instanceID)
	m.log.Info("Destroyed MIG instance", "instanceID", instanceID)

	return nil
}

// GetInstance returns information about a MIG instance
func (m *MIGManager) GetInstance(instanceID string) (*MIGInstanceInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	instance, exists := m.instances[instanceID]
	if !exists {
		return nil, fmt.Errorf("MIG instance not found: %s", instanceID)
	}

	return instance, nil
}

// ListInstances returns all MIG instances for a pool
func (m *MIGManager) ListInstances(poolName string) ([]*MIGInstanceInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	config, exists := m.pools[poolName]
	if !exists {
		return nil, fmt.Errorf("MIG pool not found: %s", poolName)
	}

	instances := make([]*MIGInstanceInfo, 0, len(config.Instances))
	for _, instance := range config.Instances {
		instances = append(instances, instance)
	}

	return instances, nil
}

// GetProfile returns information about a MIG profile
func (m *MIGManager) GetProfile(profileName string) (*MIGProfile, error) {
	profile, exists := migProfiles[profileName]
	if !exists {
		return nil, fmt.Errorf("unknown MIG profile: %s", profileName)
	}
	return &profile, nil
}

// ValidateProfile validates a MIG profile name
func (m *MIGManager) ValidateProfile(profileName string) bool {
	_, exists := migProfiles[profileName]
	return exists
}

// GetAvailableProfiles returns available MIG profiles for a GPU type
func (m *MIGManager) GetAvailableProfiles(gpuType string) []MIGProfile {
	profiles := make([]MIGProfile, 0)

	// Return profiles based on GPU type
	switch gpuType {
	case "nvidia-a100-80gb", "nvidia-a100":
		for name, profile := range migProfiles {
			// Filter A100 profiles
			if name == "7g.80gb" || name == "4g.40gb" || name == "3g.40gb" ||
				name == "2g.20gb" || name == "1g.10gb" || name == "1g.5gb" {
				profiles = append(profiles, profile)
			}
		}
	case "nvidia-a100-40gb":
		for name, profile := range migProfiles {
			if name == "3g.20gb" || name == "2g.10gb" || name == "1g.5gb" {
				profiles = append(profiles, profile)
			}
		}
	case "nvidia-h100":
		for name, profile := range migProfiles {
			if name == "7g.141gb" || name == "4g.94gb" || name == "3g.47gb" ||
				name == "2g.24gb" || name == "1g.12gb" {
				profiles = append(profiles, profile)
			}
		}
	default:
		// Return all profiles
		for _, profile := range migProfiles {
			profiles = append(profiles, profile)
		}
	}

	return profiles
}

// ReconcileInstances ensures MIG instances match the desired configuration
func (m *MIGManager) ReconcileInstances(ctx context.Context, poolName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	config, exists := m.pools[poolName]
	if !exists {
		return fmt.Errorf("MIG pool not found: %s", poolName)
	}

	m.log.Info("Reconciling MIG instances", "pool", poolName)

	// Count current instances per profile
	currentCounts := make(map[string]int32)
	for _, instance := range config.Instances {
		currentCounts[instance.Profile.Name]++
	}

	// Compare with desired counts
	for _, profileSpec := range config.Profiles {
		currentCount := currentCounts[profileSpec.Name]
		desiredCount := profileSpec.Count

		if currentCount < desiredCount {
			// Need to create more instances
			needed := int(desiredCount - currentCount)
			m.log.Info("Creating additional MIG instances",
				"profile", profileSpec.Name,
				"needed", needed,
			)
			// Would create instances here
		} else if currentCount > desiredCount {
			// Need to destroy some instances (only if not in use)
			excess := int(currentCount - desiredCount)
			m.log.Info("Need to remove excess MIG instances",
				"profile", profileSpec.Name,
				"excess", excess,
			)
			// Would destroy instances here
		}
	}

	return nil
}

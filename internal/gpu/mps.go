package gpu

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/resource"

	inferencev1alpha1 "github.com/inference-operator/inference-operator/api/v1alpha1"
)

// MPSClient represents a client connected to MPS
type MPSClient struct {
	ClientID         string
	ServiceName      string
	Namespace        string
	NodeName         string
	DeviceID         string
	ActiveThreadPct  int32
	MemoryLimit      resource.Quantity
	ConnectedAt      time.Time
	LastActivityTime time.Time
}

// MPSDaemon represents an MPS daemon instance
type MPSDaemon struct {
	NodeName             string
	DeviceID             string
	ProcessID            int32
	PipePath             string
	LogPath              string
	ActiveThreadPctLimit int32
	DefaultMemoryLimit   resource.Quantity
	Clients              map[string]*MPSClient
	Started              time.Time
	Status               string
}

// MPSPoolConfig holds MPS configuration for a pool
type MPSPoolConfig struct {
	PoolName        string
	ActiveThreadPct int32
	MemoryLimit     *resource.Quantity
	Daemons         map[string]*MPSDaemon // key: nodeName/deviceID
}

// MPSManager manages CUDA MPS configuration
type MPSManager struct {
	log     logr.Logger
	mu      sync.RWMutex
	pools   map[string]*MPSPoolConfig
	daemons map[string]*MPSDaemon // global index by nodeName/deviceID
	running bool
	stopCh  chan struct{}
}

// NewMPSManager creates a new MPS manager
func NewMPSManager(log logr.Logger) *MPSManager {
	return &MPSManager{
		log:     log,
		pools:   make(map[string]*MPSPoolConfig),
		daemons: make(map[string]*MPSDaemon),
		stopCh:  make(chan struct{}),
	}
}

// Start starts the MPS manager
func (m *MPSManager) Start(ctx context.Context) error {
	m.log.Info("Starting MPS manager")
	m.running = true

	// Start monitoring loop
	go m.monitorDaemons(ctx)

	return nil
}

// Stop stops the MPS manager
func (m *MPSManager) Stop() {
	m.log.Info("Stopping MPS manager")
	m.running = false
	close(m.stopCh)
}

// monitorDaemons monitors MPS daemon health
func (m *MPSManager) monitorDaemons(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.checkDaemonHealth()
		}
	}
}

// checkDaemonHealth checks the health of all MPS daemons
func (m *MPSManager) checkDaemonHealth() {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for key, daemon := range m.daemons {
		// In a real implementation, this would:
		// 1. Check if the MPS control daemon process is running
		// 2. Verify the pipe is accessible
		// 3. Check for any error conditions

		if daemon.Status != "Running" {
			m.log.V(1).Info("MPS daemon not running", "key", key, "status", daemon.Status)
		}
	}
}

// ConfigurePool configures MPS for a GPU pool
func (m *MPSManager) ConfigurePool(ctx context.Context, pool *inferencev1alpha1.GPUPool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Info("Configuring MPS for pool", "pool", pool.Name)

	if pool.Spec.Sharing == nil || pool.Spec.Sharing.Mode != inferencev1alpha1.GPUSharingModeMPS {
		return fmt.Errorf("pool %s is not configured for MPS mode", pool.Name)
	}

	config := &MPSPoolConfig{
		PoolName:        pool.Name,
		ActiveThreadPct: pool.Spec.Sharing.MPSActiveThreadPercentage,
		MemoryLimit:     pool.Spec.Sharing.MPSMemoryLimit,
		Daemons:         make(map[string]*MPSDaemon),
	}

	if config.ActiveThreadPct == 0 {
		config.ActiveThreadPct = 100 // Default to 100%
	}

	m.pools[pool.Name] = config

	// In a real implementation, this would:
	// 1. Discover GPUs matching the pool's nodeSelector
	// 2. Start MPS control daemon on each GPU
	// 3. Configure thread percentage limits

	return nil
}

// CleanupPool removes MPS configuration for a pool
func (m *MPSManager) CleanupPool(ctx context.Context, poolName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Info("Cleaning up MPS for pool", "pool", poolName)

	config, exists := m.pools[poolName]
	if !exists {
		return nil
	}

	// Check for active clients
	for key, daemon := range config.Daemons {
		if len(daemon.Clients) > 0 {
			return fmt.Errorf("cannot cleanup pool %s: daemon %s has active clients", poolName, key)
		}
	}

	// Stop all daemons
	for key, daemon := range config.Daemons {
		if err := m.stopDaemon(ctx, daemon); err != nil {
			m.log.Error(err, "Failed to stop MPS daemon", "key", key)
		}
		delete(m.daemons, key)
	}

	delete(m.pools, poolName)
	return nil
}

// startDaemon starts an MPS daemon on a GPU
func (m *MPSManager) startDaemon(ctx context.Context, nodeName, deviceID string, config *MPSPoolConfig) (*MPSDaemon, error) {
	key := fmt.Sprintf("%s/%s", nodeName, deviceID)

	m.log.Info("Starting MPS daemon", "node", nodeName, "device", deviceID)

	daemon := &MPSDaemon{
		NodeName:             nodeName,
		DeviceID:             deviceID,
		PipePath:             fmt.Sprintf("/tmp/nvidia-mps-%s", deviceID),
		LogPath:              fmt.Sprintf("/var/log/nvidia-mps-%s.log", deviceID),
		ActiveThreadPctLimit: config.ActiveThreadPct,
		Clients:              make(map[string]*MPSClient),
		Started:              time.Now(),
		Status:               "Starting",
	}

	if config.MemoryLimit != nil {
		daemon.DefaultMemoryLimit = *config.MemoryLimit
	}

	// In a real implementation, this would:
	// 1. Set CUDA_VISIBLE_DEVICES to the specific GPU
	// 2. Set CUDA_MPS_PIPE_DIRECTORY to the pipe path
	// 3. Set CUDA_MPS_LOG_DIRECTORY to the log path
	// 4. Start nvidia-cuda-mps-control daemon
	// 5. Configure thread percentage: echo "set_default_active_thread_percentage <pct>" | nvidia-cuda-mps-control

	daemon.ProcessID = 12345 // Simulated
	daemon.Status = "Running"

	config.Daemons[key] = daemon
	m.daemons[key] = daemon

	return daemon, nil
}

// stopDaemon stops an MPS daemon
func (m *MPSManager) stopDaemon(ctx context.Context, daemon *MPSDaemon) error {
	m.log.Info("Stopping MPS daemon", "node", daemon.NodeName, "device", daemon.DeviceID)

	// In a real implementation, this would:
	// 1. Send quit command: echo quit | nvidia-cuda-mps-control
	// 2. Wait for the daemon to terminate
	// 3. Clean up pipe and log files

	daemon.Status = "Stopped"
	return nil
}

// Allocate allocates MPS resources for a service
func (m *MPSManager) Allocate(ctx context.Context, req AllocationRequest, pool *PoolInfo) ([]GPUAllocationInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Info("Allocating MPS resources",
		"service", req.ServiceName,
		"namespace", req.Namespace,
		"count", req.GPUCount,
	)

	config, exists := m.pools[pool.Name]
	if !exists {
		return nil, fmt.Errorf("MPS pool not found: %s", pool.Name)
	}

	allocations := make([]GPUAllocationInfo, 0, req.GPUCount)

	// Find or create MPS daemons
	for i := 0; i < int(req.GPUCount); i++ {
		daemon, err := m.findOrCreateDaemon(ctx, config, pool)
		if err != nil {
			// Rollback previous allocations
			for j := 0; j < i; j++ {
				m.releaseClient(ctx, req.ServiceName, req.Namespace)
			}
			return nil, err
		}

		// Create client
		clientID := fmt.Sprintf("%s/%s-%d", req.Namespace, req.ServiceName, i)
		client := &MPSClient{
			ClientID:         clientID,
			ServiceName:      req.ServiceName,
			Namespace:        req.Namespace,
			NodeName:         daemon.NodeName,
			DeviceID:         daemon.DeviceID,
			ActiveThreadPct:  config.ActiveThreadPct,
			ConnectedAt:      time.Now(),
			LastActivityTime: time.Now(),
		}

		if config.MemoryLimit != nil {
			client.MemoryLimit = *config.MemoryLimit
		}

		daemon.Clients[clientID] = client

		allocations = append(allocations, GPUAllocationInfo{
			NodeName: daemon.NodeName,
			DeviceID: daemon.DeviceID,
			Memory:   client.MemoryLimit,
		})
	}

	return allocations, nil
}

// findOrCreateDaemon finds an existing daemon with capacity or creates a new one
func (m *MPSManager) findOrCreateDaemon(ctx context.Context, config *MPSPoolConfig, pool *PoolInfo) (*MPSDaemon, error) {
	// First, try to find an existing daemon with capacity
	for _, daemon := range config.Daemons {
		if daemon.Status == "Running" {
			// MPS can support up to 48 clients per GPU
			if len(daemon.Clients) < 48 {
				return daemon, nil
			}
		}
	}

	// No available daemon, try to start one on an available GPU
	for _, gpu := range pool.GPUs {
		key := fmt.Sprintf("%s/%s", gpu.NodeName, gpu.DeviceID)
		if _, exists := config.Daemons[key]; !exists {
			return m.startDaemon(ctx, gpu.NodeName, gpu.DeviceID, config)
		}
	}

	return nil, fmt.Errorf("no available GPUs for MPS daemon")
}

// releaseClient releases MPS resources for a specific client
func (m *MPSManager) releaseClient(ctx context.Context, serviceName, namespace string) {
	for _, daemon := range m.daemons {
		for clientID, client := range daemon.Clients {
			if client.ServiceName == serviceName && client.Namespace == namespace {
				delete(daemon.Clients, clientID)
				m.log.V(1).Info("Released MPS client",
					"clientID", clientID,
					"node", daemon.NodeName,
					"device", daemon.DeviceID,
				)
			}
		}
	}
}

// Release releases all MPS resources allocated to a service
func (m *MPSManager) Release(ctx context.Context, serviceName, namespace string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Info("Releasing MPS resources", "service", serviceName, "namespace", namespace)
	m.releaseClient(ctx, serviceName, namespace)
	return nil
}

// GetDaemonStatus returns the status of an MPS daemon
func (m *MPSManager) GetDaemonStatus(nodeName, deviceID string) (*MPSDaemon, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	key := fmt.Sprintf("%s/%s", nodeName, deviceID)
	daemon, exists := m.daemons[key]
	if !exists {
		return nil, fmt.Errorf("MPS daemon not found: %s", key)
	}

	return daemon, nil
}

// ListClients returns all MPS clients for a daemon
func (m *MPSManager) ListClients(nodeName, deviceID string) ([]*MPSClient, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	key := fmt.Sprintf("%s/%s", nodeName, deviceID)
	daemon, exists := m.daemons[key]
	if !exists {
		return nil, fmt.Errorf("MPS daemon not found: %s", key)
	}

	clients := make([]*MPSClient, 0, len(daemon.Clients))
	for _, client := range daemon.Clients {
		clients = append(clients, client)
	}

	return clients, nil
}

// SetActiveThreadPercentage sets the active thread percentage for a client
func (m *MPSManager) SetActiveThreadPercentage(clientID string, percentage int32) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if percentage < 1 || percentage > 100 {
		return fmt.Errorf("invalid percentage: %d (must be 1-100)", percentage)
	}

	for _, daemon := range m.daemons {
		if client, exists := daemon.Clients[clientID]; exists {
			// In a real implementation, this would:
			// echo "set_active_thread_percentage <pid> <pct>" | nvidia-cuda-mps-control
			client.ActiveThreadPct = percentage
			m.log.Info("Set active thread percentage",
				"clientID", clientID,
				"percentage", percentage,
			)
			return nil
		}
	}

	return fmt.Errorf("client not found: %s", clientID)
}

// GetStats returns statistics for an MPS daemon
func (m *MPSManager) GetStats(nodeName, deviceID string) (map[string]interface{}, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	key := fmt.Sprintf("%s/%s", nodeName, deviceID)
	daemon, exists := m.daemons[key]
	if !exists {
		return nil, fmt.Errorf("MPS daemon not found: %s", key)
	}

	stats := map[string]interface{}{
		"nodeName":    daemon.NodeName,
		"deviceId":    daemon.DeviceID,
		"processId":   daemon.ProcessID,
		"status":      daemon.Status,
		"clientCount": len(daemon.Clients),
		"started":     daemon.Started,
		"uptime":      time.Since(daemon.Started).String(),
		"threadLimit": daemon.ActiveThreadPctLimit,
	}

	return stats, nil
}

// RestartDaemon restarts an MPS daemon
func (m *MPSManager) RestartDaemon(ctx context.Context, nodeName, deviceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := fmt.Sprintf("%s/%s", nodeName, deviceID)
	daemon, exists := m.daemons[key]
	if !exists {
		return fmt.Errorf("MPS daemon not found: %s", key)
	}

	// Check for active clients
	if len(daemon.Clients) > 0 {
		return fmt.Errorf("cannot restart daemon with active clients")
	}

	// Find the pool config
	var config *MPSPoolConfig
	for _, poolConfig := range m.pools {
		if _, ok := poolConfig.Daemons[key]; ok {
			config = poolConfig
			break
		}
	}

	if config == nil {
		return fmt.Errorf("pool config not found for daemon: %s", key)
	}

	// Stop and restart
	if err := m.stopDaemon(ctx, daemon); err != nil {
		return fmt.Errorf("failed to stop daemon: %w", err)
	}

	delete(m.daemons, key)
	delete(config.Daemons, key)

	_, err := m.startDaemon(ctx, nodeName, deviceID, config)
	return err
}

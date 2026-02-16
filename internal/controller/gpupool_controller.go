package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	inferencev1alpha1 "github.com/inference-operator/inference-operator/api/v1alpha1"
	"github.com/inference-operator/inference-operator/internal/gpu"
)

const (
	gpuPoolFinalizer = "inference.ai/gpupool-finalizer"
)

// GPUPoolReconciler reconciles a GPUPool object
type GPUPoolReconciler struct {
	client.Client
	Log        logr.Logger
	Scheme     *runtime.Scheme
	Recorder   record.EventRecorder
	GPUManager *gpu.Manager
}

// +kubebuilder:rbac:groups=inference.ai,resources=gpupools,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=inference.ai,resources=gpupools/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=inference.ai,resources=gpupools/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch

// Reconcile handles GPUPool reconciliation
func (r *GPUPoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("gpupool", req.NamespacedName)
	log.V(1).Info("Reconciling GPUPool")

	// Fetch the GPUPool
	var gpuPool inferencev1alpha1.GPUPool
	if err := r.Get(ctx, req.NamespacedName, &gpuPool); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("GPUPool not found, likely deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !gpuPool.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &gpuPool)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&gpuPool, gpuPoolFinalizer) {
		controllerutil.AddFinalizer(&gpuPool, gpuPoolFinalizer)
		if err := r.Update(ctx, &gpuPool); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Validate the spec
	if err := r.validateSpec(ctx, &gpuPool); err != nil {
		return r.updateStatusWithError(ctx, &gpuPool, err)
	}

	// Register pool with GPU manager
	if err := r.GPUManager.RegisterPool(ctx, &gpuPool); err != nil {
		return r.updateStatusWithError(ctx, &gpuPool, err)
	}

	// Discover GPUs matching the pool's selectors
	if err := r.discoverGPUs(ctx, &gpuPool); err != nil {
		log.Error(err, "Failed to discover GPUs")
	}

	// Update status
	if err := r.updateStatus(ctx, &gpuPool); err != nil {
		return ctrl.Result{}, err
	}

	// Requeue for periodic status updates
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// handleDeletion handles resource cleanup when GPUPool is deleted
func (r *GPUPoolReconciler) handleDeletion(ctx context.Context, pool *inferencev1alpha1.GPUPool) (ctrl.Result, error) {
	log := r.Log.WithValues("gpupool", pool.Name)
	log.Info("Handling deletion")

	// Check if pool is in use
	if pool.Status.AllocatedGPUs > 0 {
		r.Recorder.Event(pool, corev1.EventTypeWarning, "DeletionBlocked", "Cannot delete pool with active allocations")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Unregister from GPU manager
	if err := r.GPUManager.UnregisterPool(ctx, pool.Name); err != nil {
		log.Error(err, "Failed to unregister pool")
	}

	r.Recorder.Event(pool, corev1.EventTypeNormal, "Deleted", "GPUPool deleted")

	// Remove finalizer
	controllerutil.RemoveFinalizer(pool, gpuPoolFinalizer)
	if err := r.Update(ctx, pool); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// validateSpec validates the GPUPool spec
func (r *GPUPoolReconciler) validateSpec(ctx context.Context, pool *inferencev1alpha1.GPUPool) error {
	if pool.Spec.Sharing != nil {
		switch pool.Spec.Sharing.Mode {
		case inferencev1alpha1.GPUSharingModeMIG:
			if len(pool.Spec.Sharing.Profiles) == 0 {
				return fmt.Errorf("MIG mode requires at least one profile")
			}
		case inferencev1alpha1.GPUSharingModeMPS:
			if pool.Spec.Sharing.MPSActiveThreadPercentage < 1 || pool.Spec.Sharing.MPSActiveThreadPercentage > 100 {
				return fmt.Errorf("MPS active thread percentage must be between 1 and 100")
			}
		case inferencev1alpha1.GPUSharingModeTimeslice:
			if pool.Spec.Sharing.TimeSliceInterval < 1 {
				return fmt.Errorf("time slice interval must be positive")
			}
		}
	}

	return nil
}

// discoverGPUs discovers GPUs matching the pool's selectors
func (r *GPUPoolReconciler) discoverGPUs(ctx context.Context, pool *inferencev1alpha1.GPUPool) error {
	log := r.Log.WithValues("gpupool", pool.Name)

	// List nodes matching the selector
	nodeList := &corev1.NodeList{}
	listOpts := []client.ListOption{}

	if pool.Spec.NodeSelector != nil && len(pool.Spec.NodeSelector) > 0 {
		listOpts = append(listOpts, client.MatchingLabels(pool.Spec.NodeSelector))
	}

	if err := r.List(ctx, nodeList, listOpts...); err != nil {
		return fmt.Errorf("failed to list nodes: %w", err)
	}

	log.V(1).Info("Found matching nodes", "count", len(nodeList.Items))

	// In a real implementation, we would:
	// 1. Query the NVIDIA device plugin or DCGM for GPU info on each node
	// 2. Filter by GPU type if specified
	// 3. Update the pool's node status

	return nil
}

// updateStatus updates the GPUPool status
func (r *GPUPoolReconciler) updateStatus(ctx context.Context, pool *inferencev1alpha1.GPUPool) error {
	// Get status from GPU manager
	status, err := r.GPUManager.GetPoolStatus(pool.Name)
	if err != nil {
		// Pool might not be fully registered yet
		pool.Status.Phase = inferencev1alpha1.GPUPoolPhasePending
	} else {
		pool.Status.Phase = status.Phase
		pool.Status.TotalGPUs = status.TotalGPUs
		pool.Status.AvailableGPUs = status.AvailableGPUs
		pool.Status.AllocatedGPUs = status.AllocatedGPUs
		pool.Status.TotalMemory = status.TotalMemory
		pool.Status.AvailableMemory = status.AvailableMemory
		pool.Status.Nodes = status.Nodes
	}

	pool.Status.ObservedGeneration = pool.Generation
	pool.Status.LastSyncTime = &metav1.Time{Time: time.Now()}

	// Update conditions
	r.updateConditions(pool)

	return r.Status().Update(ctx, pool)
}

// updateConditions updates status conditions
func (r *GPUPoolReconciler) updateConditions(pool *inferencev1alpha1.GPUPool) {
	now := metav1.Now()

	// Ready condition
	readyCondition := metav1.Condition{
		Type:               "Ready",
		LastTransitionTime: now,
	}

	if pool.Status.TotalGPUs > 0 {
		readyCondition.Status = metav1.ConditionTrue
		readyCondition.Reason = "GPUsAvailable"
		readyCondition.Message = fmt.Sprintf("%d GPUs available in pool", pool.Status.AvailableGPUs)
		pool.Status.Phase = inferencev1alpha1.GPUPoolPhaseReady
	} else {
		readyCondition.Status = metav1.ConditionFalse
		readyCondition.Reason = "NoGPUs"
		readyCondition.Message = "No GPUs discovered in pool"
	}

	r.setCondition(pool, readyCondition)

	// SharingConfigured condition
	if pool.Spec.Sharing != nil {
		sharingCondition := metav1.Condition{
			Type:               "SharingConfigured",
			Status:             metav1.ConditionTrue,
			LastTransitionTime: now,
			Reason:             "SharingEnabled",
			Message:            fmt.Sprintf("GPU sharing mode %s configured", pool.Spec.Sharing.Mode),
		}
		r.setCondition(pool, sharingCondition)
	}
}

// setCondition sets or updates a condition
func (r *GPUPoolReconciler) setCondition(pool *inferencev1alpha1.GPUPool, condition metav1.Condition) {
	for i, existing := range pool.Status.Conditions {
		if existing.Type == condition.Type {
			if existing.Status != condition.Status {
				pool.Status.Conditions[i] = condition
			}
			return
		}
	}
	pool.Status.Conditions = append(pool.Status.Conditions, condition)
}

// updateStatusWithError updates status with error information
func (r *GPUPoolReconciler) updateStatusWithError(ctx context.Context, pool *inferencev1alpha1.GPUPool, err error) (ctrl.Result, error) {
	log := r.Log.WithValues("gpupool", pool.Name)
	log.Error(err, "Reconciliation error")

	pool.Status.Phase = inferencev1alpha1.GPUPoolPhaseFailed

	condition := metav1.Condition{
		Type:               "Error",
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
		Reason:             "ReconcileError",
		Message:            err.Error(),
	}
	r.setCondition(pool, condition)

	if updateErr := r.Status().Update(ctx, pool); updateErr != nil {
		log.Error(updateErr, "Failed to update status")
	}

	r.Recorder.Event(pool, corev1.EventTypeWarning, "ReconcileError", err.Error())

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// SetupWithManager sets up the controller with the Manager
func (r *GPUPoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&inferencev1alpha1.GPUPool{}).
		Complete(r)
}

package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	inferencev1alpha1 "github.com/inference-operator/inference-operator/api/v1alpha1"
	"github.com/inference-operator/inference-operator/internal/backends"
	"github.com/inference-operator/inference-operator/internal/cache"
	"github.com/inference-operator/inference-operator/internal/gpu"
)

const (
	inferenceServiceFinalizer = "inference.ai/finalizer"
	requeueAfter              = 30 * time.Second
)

// InferenceServiceReconciler reconciles an InferenceService object
type InferenceServiceReconciler struct {
	client.Client
	Log            logr.Logger
	Scheme         *runtime.Scheme
	Recorder       record.EventRecorder
	GPUManager     *gpu.Manager
	CacheManager   *cache.Manager
	BackendFactory *backends.BackendFactory
}

// +kubebuilder:rbac:groups=inference.ai,resources=inferenceservices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=inference.ai,resources=inferenceservices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=inference.ai,resources=inferenceservices/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

// Reconcile handles InferenceService reconciliation
func (r *InferenceServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("inferenceservice", req.NamespacedName)
	log.V(1).Info("Reconciling InferenceService")

	// Fetch the InferenceService
	var inferenceService inferencev1alpha1.InferenceService
	if err := r.Get(ctx, req.NamespacedName, &inferenceService); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("InferenceService not found, likely deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !inferenceService.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &inferenceService)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&inferenceService, inferenceServiceFinalizer) {
		controllerutil.AddFinalizer(&inferenceService, inferenceServiceFinalizer)
		if err := r.Update(ctx, &inferenceService); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Validate the spec
	if err := r.validateSpec(ctx, &inferenceService); err != nil {
		return r.updateStatusWithError(ctx, &inferenceService, err)
	}

	// Get the appropriate backend
	backend, exists := r.BackendFactory.Get(inferenceService.Spec.Backend)
	if !exists {
		err := fmt.Errorf("unsupported backend: %s", inferenceService.Spec.Backend)
		return r.updateStatusWithError(ctx, &inferenceService, err)
	}

	// Validate backend-specific configuration
	if err := backend.Validate(ctx, &inferenceService); err != nil {
		return r.updateStatusWithError(ctx, &inferenceService, err)
	}

	// Reconcile GPU allocation
	if err := r.reconcileGPUAllocation(ctx, &inferenceService); err != nil {
		return r.updateStatusWithError(ctx, &inferenceService, err)
	}

	// Reconcile cache configuration
	if err := r.reconcileCacheConfiguration(ctx, &inferenceService); err != nil {
		log.Error(err, "Failed to reconcile cache configuration, continuing")
	}

	// Reconcile Deployment
	deployment, err := r.reconcileDeployment(ctx, &inferenceService, backend)
	if err != nil {
		return r.updateStatusWithError(ctx, &inferenceService, err)
	}

	// Reconcile Service
	if err := r.reconcileService(ctx, &inferenceService, backend); err != nil {
		return r.updateStatusWithError(ctx, &inferenceService, err)
	}

	// Update status
	if err := r.updateStatus(ctx, &inferenceService, deployment); err != nil {
		return ctrl.Result{}, err
	}

	// Requeue for periodic status updates
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// handleDeletion handles resource cleanup when InferenceService is deleted
func (r *InferenceServiceReconciler) handleDeletion(ctx context.Context, svc *inferencev1alpha1.InferenceService) (ctrl.Result, error) {
	log := r.Log.WithValues("inferenceservice", types.NamespacedName{Name: svc.Name, Namespace: svc.Namespace})
	log.Info("Handling deletion")

	// Update status to terminating
	svc.Status.Phase = inferencev1alpha1.InferenceServicePhaseTerminating
	if err := r.Status().Update(ctx, svc); err != nil {
		log.Error(err, "Failed to update status to terminating")
	}

	// Release GPU resources
	if err := r.GPUManager.Release(ctx, svc.Name, svc.Namespace); err != nil {
		log.Error(err, "Failed to release GPU resources")
	}

	// Record event
	r.Recorder.Event(svc, corev1.EventTypeNormal, "Deleted", "InferenceService deleted")

	// Remove finalizer
	controllerutil.RemoveFinalizer(svc, inferenceServiceFinalizer)
	if err := r.Update(ctx, svc); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// validateSpec validates the InferenceService spec
func (r *InferenceServiceReconciler) validateSpec(ctx context.Context, svc *inferencev1alpha1.InferenceService) error {
	if svc.Spec.Model.Name == "" {
		return fmt.Errorf("model name is required")
	}

	if svc.Spec.Replicas < 0 {
		return fmt.Errorf("replicas cannot be negative")
	}

	if svc.Spec.GPU.Count < 1 {
		return fmt.Errorf("at least 1 GPU is required")
	}

	return nil
}

// reconcileGPUAllocation handles GPU resource allocation
func (r *InferenceServiceReconciler) reconcileGPUAllocation(ctx context.Context, svc *inferencev1alpha1.InferenceService) error {
	log := r.Log.WithValues("inferenceservice", types.NamespacedName{Name: svc.Name, Namespace: svc.Namespace})

	poolName := ""
	if svc.Spec.GPU.GPUPoolRef != nil {
		poolName = svc.Spec.GPU.GPUPoolRef.Name
	}

	req := gpu.AllocationRequest{
		ServiceName: svc.Name,
		Namespace:   svc.Namespace,
		GPUType:     svc.Spec.GPU.Type,
		GPUCount:    svc.Spec.GPU.Count,
		SharingMode: svc.Spec.GPU.Sharing,
		MIGProfile:  svc.Spec.GPU.MIGProfile,
		PoolName:    poolName,
	}

	if svc.Spec.GPU.Memory != nil {
		req.Memory = *svc.Spec.GPU.Memory
	}

	result, err := r.GPUManager.Allocate(ctx, req)
	if err != nil {
		log.Error(err, "Failed to allocate GPU resources")
		r.Recorder.Event(svc, corev1.EventTypeWarning, "GPUAllocationFailed", err.Error())
		return err
	}

	if result.Success {
		// Update status with GPU allocations
		svc.Status.GPUAllocations = make([]inferencev1alpha1.GPUAllocation, 0, len(result.Allocations))
		for _, alloc := range result.Allocations {
			svc.Status.GPUAllocations = append(svc.Status.GPUAllocations, inferencev1alpha1.GPUAllocation{
				NodeName:        alloc.NodeName,
				DeviceID:        alloc.DeviceID,
				MIGInstance:     alloc.MIGInstance,
				MemoryAllocated: alloc.Memory,
			})
		}
		r.Recorder.Event(svc, corev1.EventTypeNormal, "GPUAllocated", fmt.Sprintf("Allocated %d GPUs", len(result.Allocations)))
	}

	return nil
}

// reconcileCacheConfiguration handles cache configuration
func (r *InferenceServiceReconciler) reconcileCacheConfiguration(ctx context.Context, svc *inferencev1alpha1.InferenceService) error {
	if svc.Spec.Cache == nil || !svc.Spec.Cache.Enabled {
		return nil
	}

	cacheRef := ""
	if svc.Spec.Cache.TokenCacheRef != nil {
		cacheRef = svc.Spec.Cache.TokenCacheRef.Name
	}

	_, err := r.CacheManager.GetCacheForService(ctx, svc.Name, svc.Namespace, cacheRef)
	if err != nil {
		return err
	}

	return nil
}

// reconcileDeployment creates or updates the Deployment
func (r *InferenceServiceReconciler) reconcileDeployment(ctx context.Context, svc *inferencev1alpha1.InferenceService, backend backends.Backend) (*appsv1.Deployment, error) {
	log := r.Log.WithValues("inferenceservice", types.NamespacedName{Name: svc.Name, Namespace: svc.Namespace})

	// Generate deployment spec from backend
	deploySpec, err := backend.GenerateDeployment(ctx, svc)
	if err != nil {
		return nil, fmt.Errorf("failed to generate deployment spec: %w", err)
	}

	// Build the Deployment object
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        deploySpec.Name,
			Namespace:   deploySpec.Namespace,
			Labels:      deploySpec.Labels,
			Annotations: deploySpec.Annotations,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &deploySpec.Replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/name": svc.Name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      deploySpec.Labels,
					Annotations: deploySpec.Annotations,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: deploySpec.ServiceAccountName,
					NodeSelector:       deploySpec.NodeSelector,
					Tolerations:        deploySpec.Tolerations,
					SecurityContext:    deploySpec.SecurityContext,
					InitContainers:     deploySpec.InitContainers,
					Containers: []corev1.Container{
						{
							Name:           svc.Name,
							Image:          deploySpec.Image,
							Command:        deploySpec.Command,
							Args:           deploySpec.Args,
							Env:            deploySpec.Env,
							Ports:          deploySpec.Ports,
							Resources:      deploySpec.Resources,
							VolumeMounts:   deploySpec.VolumeMounts,
							ReadinessProbe: deploySpec.ReadinessProbe,
							LivenessProbe:  deploySpec.LivenessProbe,
							StartupProbe:   deploySpec.StartupProbe,
						},
					},
					Volumes: deploySpec.Volumes,
				},
			},
		},
	}

	// Add image pull secrets if specified
	if len(svc.Spec.ImagePullSecrets) > 0 {
		deployment.Spec.Template.Spec.ImagePullSecrets = svc.Spec.ImagePullSecrets
	}

	// Set owner reference
	if err := controllerutil.SetControllerReference(svc, deployment, r.Scheme); err != nil {
		return nil, err
	}

	// Check if deployment exists
	found := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: deployment.Name, Namespace: deployment.Namespace}, found)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Create new deployment
			log.Info("Creating Deployment", "name", deployment.Name)
			if err := r.Create(ctx, deployment); err != nil {
				return nil, err
			}
			r.Recorder.Event(svc, corev1.EventTypeNormal, "DeploymentCreated", fmt.Sprintf("Created deployment %s", deployment.Name))
			return deployment, nil
		}
		return nil, err
	}

	// Update existing deployment if spec changed
	if !equality.Semantic.DeepEqual(found.Spec, deployment.Spec) {
		log.Info("Updating Deployment", "name", deployment.Name)
		found.Spec = deployment.Spec
		if err := r.Update(ctx, found); err != nil {
			return nil, err
		}
		r.Recorder.Event(svc, corev1.EventTypeNormal, "DeploymentUpdated", fmt.Sprintf("Updated deployment %s", deployment.Name))
	}

	return found, nil
}

// reconcileService creates or updates the Service
func (r *InferenceServiceReconciler) reconcileService(ctx context.Context, svc *inferencev1alpha1.InferenceService, backend backends.Backend) error {
	log := r.Log.WithValues("inferenceservice", types.NamespacedName{Name: svc.Name, Namespace: svc.Namespace})

	// Generate service spec from backend
	svcSpec, err := backend.GenerateService(ctx, svc)
	if err != nil {
		return fmt.Errorf("failed to generate service spec: %w", err)
	}

	// Build the Service object
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        svcSpec.Name,
			Namespace:   svcSpec.Namespace,
			Labels:      svcSpec.Labels,
			Annotations: svcSpec.Annotations,
		},
		Spec: corev1.ServiceSpec{
			Type:     svcSpec.Type,
			Ports:    svcSpec.Ports,
			Selector: svcSpec.Selector,
		},
	}

	// Set owner reference
	if err := controllerutil.SetControllerReference(svc, service, r.Scheme); err != nil {
		return err
	}

	// Check if service exists
	found := &corev1.Service{}
	err = r.Get(ctx, types.NamespacedName{Name: service.Name, Namespace: service.Namespace}, found)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Creating Service", "name", service.Name)
			if err := r.Create(ctx, service); err != nil {
				return err
			}
			r.Recorder.Event(svc, corev1.EventTypeNormal, "ServiceCreated", fmt.Sprintf("Created service %s", service.Name))
			return nil
		}
		return err
	}

	// Update existing service if spec changed (excluding clusterIP)
	service.Spec.ClusterIP = found.Spec.ClusterIP
	if !equality.Semantic.DeepEqual(found.Spec, service.Spec) {
		log.Info("Updating Service", "name", service.Name)
		found.Spec = service.Spec
		if err := r.Update(ctx, found); err != nil {
			return err
		}
	}

	return nil
}

// updateStatus updates the InferenceService status
func (r *InferenceServiceReconciler) updateStatus(ctx context.Context, svc *inferencev1alpha1.InferenceService, deployment *appsv1.Deployment) error {
	// Determine phase based on deployment status
	phase := inferencev1alpha1.InferenceServicePhasePending
	if deployment != nil {
		if deployment.Status.AvailableReplicas > 0 {
			if deployment.Status.AvailableReplicas == *deployment.Spec.Replicas {
				phase = inferencev1alpha1.InferenceServicePhaseRunning
			} else {
				phase = inferencev1alpha1.InferenceServicePhaseScaling
			}
		} else if deployment.Status.UnavailableReplicas > 0 {
			phase = inferencev1alpha1.InferenceServicePhaseInitializing
		}
	}

	// Update status fields
	svc.Status.Phase = phase
	svc.Status.ObservedGeneration = svc.Generation

	if deployment != nil {
		svc.Status.ReadyReplicas = deployment.Status.ReadyReplicas
		svc.Status.AvailableReplicas = deployment.Status.AvailableReplicas
	}

	// Set URL
	svc.Status.URL = fmt.Sprintf("http://%s.%s.svc.cluster.local", svc.Name, svc.Namespace)

	// Update conditions
	r.updateConditions(svc, deployment)

	return r.Status().Update(ctx, svc)
}

// updateConditions updates status conditions
func (r *InferenceServiceReconciler) updateConditions(svc *inferencev1alpha1.InferenceService, deployment *appsv1.Deployment) {
	now := metav1.Now()

	// Ready condition
	readyCondition := metav1.Condition{
		Type:               "Ready",
		LastTransitionTime: now,
	}

	if deployment != nil && deployment.Status.AvailableReplicas == *deployment.Spec.Replicas {
		readyCondition.Status = metav1.ConditionTrue
		readyCondition.Reason = "AllReplicasReady"
		readyCondition.Message = "All replicas are ready"
	} else {
		readyCondition.Status = metav1.ConditionFalse
		readyCondition.Reason = "ReplicasNotReady"
		readyCondition.Message = "Not all replicas are ready"
	}

	// Update or add condition
	r.setCondition(svc, readyCondition)

	// ModelLoaded condition
	modelCondition := metav1.Condition{
		Type:               "ModelLoaded",
		LastTransitionTime: now,
	}

	if svc.Status.Phase == inferencev1alpha1.InferenceServicePhaseRunning {
		modelCondition.Status = metav1.ConditionTrue
		modelCondition.Reason = "ModelReady"
		modelCondition.Message = "Model is loaded and ready"
	} else {
		modelCondition.Status = metav1.ConditionFalse
		modelCondition.Reason = "ModelLoading"
		modelCondition.Message = "Model is being loaded"
	}

	r.setCondition(svc, modelCondition)
}

// setCondition sets or updates a condition
func (r *InferenceServiceReconciler) setCondition(svc *inferencev1alpha1.InferenceService, condition metav1.Condition) {
	for i, existing := range svc.Status.Conditions {
		if existing.Type == condition.Type {
			if existing.Status != condition.Status {
				svc.Status.Conditions[i] = condition
			}
			return
		}
	}
	svc.Status.Conditions = append(svc.Status.Conditions, condition)
}

// updateStatusWithError updates status with error information
func (r *InferenceServiceReconciler) updateStatusWithError(ctx context.Context, svc *inferencev1alpha1.InferenceService, err error) (ctrl.Result, error) {
	log := r.Log.WithValues("inferenceservice", types.NamespacedName{Name: svc.Name, Namespace: svc.Namespace})
	log.Error(err, "Reconciliation error")

	svc.Status.Phase = inferencev1alpha1.InferenceServicePhaseFailed

	// Add error condition
	condition := metav1.Condition{
		Type:               "Error",
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
		Reason:             "ReconcileError",
		Message:            err.Error(),
	}
	r.setCondition(svc, condition)

	if updateErr := r.Status().Update(ctx, svc); updateErr != nil {
		log.Error(updateErr, "Failed to update status")
	}

	r.Recorder.Event(svc, corev1.EventTypeWarning, "ReconcileError", err.Error())

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// SetupWithManager sets up the controller with the Manager
func (r *InferenceServiceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&inferencev1alpha1.InferenceService{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Complete(r)
}

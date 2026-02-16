package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	inferencev1alpha1 "github.com/inference-operator/inference-operator/api/v1alpha1"
	"github.com/inference-operator/inference-operator/internal/cache"
)

const (
	tokenCacheFinalizer = "inference.ai/tokencache-finalizer"
)

// TokenCacheReconciler reconciles a TokenCache object
type TokenCacheReconciler struct {
	client.Client
	Log          logr.Logger
	Scheme       *runtime.Scheme
	Recorder     record.EventRecorder
	CacheManager *cache.Manager
}

// +kubebuilder:rbac:groups=inference.ai,resources=tokencaches,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=inference.ai,resources=tokencaches/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=inference.ai,resources=tokencaches/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete

// Reconcile handles TokenCache reconciliation
func (r *TokenCacheReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("tokencache", req.NamespacedName)
	log.V(1).Info("Reconciling TokenCache")

	// Fetch the TokenCache
	var tokenCache inferencev1alpha1.TokenCache
	if err := r.Get(ctx, req.NamespacedName, &tokenCache); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("TokenCache not found, likely deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !tokenCache.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &tokenCache)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&tokenCache, tokenCacheFinalizer) {
		controllerutil.AddFinalizer(&tokenCache, tokenCacheFinalizer)
		if err := r.Update(ctx, &tokenCache); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Validate the spec
	if err := r.validateSpec(ctx, &tokenCache); err != nil {
		return r.updateStatusWithError(ctx, &tokenCache, err)
	}

	// Configure cache manager
	if err := r.CacheManager.ConfigureCache(ctx, &tokenCache); err != nil {
		return r.updateStatusWithError(ctx, &tokenCache, err)
	}

	// Reconcile Redis StatefulSet for distributed tier
	if err := r.reconcileRedis(ctx, &tokenCache); err != nil {
		log.Error(err, "Failed to reconcile Redis")
	}

	// Reconcile PVCs for local cache
	if err := r.reconcileLocalStorage(ctx, &tokenCache); err != nil {
		log.Error(err, "Failed to reconcile local storage")
	}

	// Update status
	if err := r.updateStatus(ctx, &tokenCache); err != nil {
		return ctrl.Result{}, err
	}

	// Requeue for periodic status updates
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// handleDeletion handles resource cleanup when TokenCache is deleted
func (r *TokenCacheReconciler) handleDeletion(ctx context.Context, tc *inferencev1alpha1.TokenCache) (ctrl.Result, error) {
	log := r.Log.WithValues("tokencache", types.NamespacedName{Name: tc.Name, Namespace: tc.Namespace})
	log.Info("Handling deletion")

	// Cleanup cache manager
	if err := r.CacheManager.CleanupCache(ctx, tc.Name); err != nil {
		log.Error(err, "Failed to cleanup cache")
	}

	r.Recorder.Event(tc, corev1.EventTypeNormal, "Deleted", "TokenCache deleted")

	// Remove finalizer
	controllerutil.RemoveFinalizer(tc, tokenCacheFinalizer)
	if err := r.Update(ctx, tc); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// validateSpec validates the TokenCache spec
func (r *TokenCacheReconciler) validateSpec(ctx context.Context, tc *inferencev1alpha1.TokenCache) error {
	if len(tc.Spec.Tiers) == 0 {
		return fmt.Errorf("at least one cache tier is required")
	}

	tierNames := make(map[string]bool)
	for _, tier := range tc.Spec.Tiers {
		if tier.Name == "" {
			return fmt.Errorf("tier name is required")
		}
		if tierNames[tier.Name] {
			return fmt.Errorf("duplicate tier name: %s", tier.Name)
		}
		tierNames[tier.Name] = true

		if tier.Size.IsZero() {
			return fmt.Errorf("tier size must be specified for tier: %s", tier.Name)
		}
	}

	return nil
}

// reconcileRedis creates or updates Redis StatefulSet for distributed caching
func (r *TokenCacheReconciler) reconcileRedis(ctx context.Context, tc *inferencev1alpha1.TokenCache) error {
	log := r.Log.WithValues("tokencache", types.NamespacedName{Name: tc.Name, Namespace: tc.Namespace})

	// Find Redis tier
	var redisTier *inferencev1alpha1.CacheTierSpec
	for i := range tc.Spec.Tiers {
		if tc.Spec.Tiers[i].Type == inferencev1alpha1.CacheTierTypeRedis {
			redisTier = &tc.Spec.Tiers[i]
			break
		}
	}

	if redisTier == nil {
		return nil // No Redis tier configured
	}

	// Skip if using external Redis
	if redisTier.RedisConfig != nil && redisTier.RedisConfig.ExternalEndpoint != "" {
		log.Info("Using external Redis endpoint", "endpoint", redisTier.RedisConfig.ExternalEndpoint)
		return nil
	}

	replicas := int32(1)
	if redisTier.RedisConfig != nil && redisTier.RedisConfig.Replicas > 0 {
		replicas = redisTier.RedisConfig.Replicas
	}

	// Create Redis StatefulSet
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-redis", tc.Name),
			Namespace: tc.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       tc.Name,
				"app.kubernetes.io/component":  "redis",
				"app.kubernetes.io/managed-by": "inference-operator",
			},
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: fmt.Sprintf("%s-redis", tc.Name),
			Replicas:    &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/name":      tc.Name,
					"app.kubernetes.io/component": "redis",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app.kubernetes.io/name":      tc.Name,
						"app.kubernetes.io/component": "redis",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "redis",
							Image: "redis:7-alpine",
							Ports: []corev1.ContainerPort{
								{
									Name:          "redis",
									ContainerPort: 6379,
									Protocol:      corev1.ProtocolTCP,
								},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("256Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("500m"),
									corev1.ResourceMemory: redisTier.Size,
								},
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									TCPSocket: &corev1.TCPSocketAction{
										Port: intstr.FromInt(6379),
									},
								},
								InitialDelaySeconds: 5,
								PeriodSeconds:       10,
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									TCPSocket: &corev1.TCPSocketAction{
										Port: intstr.FromInt(6379),
									},
								},
								InitialDelaySeconds: 15,
								PeriodSeconds:       20,
							},
						},
					},
				},
			},
		},
	}

	// Add persistence if enabled
	if redisTier.RedisConfig != nil && redisTier.RedisConfig.PersistenceEnabled {
		sts.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "data",
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{
						corev1.ReadWriteOnce,
					},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: redisTier.Size,
						},
					},
				},
			},
		}
		sts.Spec.Template.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{
			{
				Name:      "data",
				MountPath: "/data",
			},
		}
		sts.Spec.Template.Spec.Containers[0].Args = []string{
			"--appendonly", "yes",
			"--save", "60", "1",
		}
	}

	// Set owner reference
	if err := controllerutil.SetControllerReference(tc, sts, r.Scheme); err != nil {
		return err
	}

	// Create or update
	found := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Name: sts.Name, Namespace: sts.Namespace}, found)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Creating Redis StatefulSet", "name", sts.Name)
			if err := r.Create(ctx, sts); err != nil {
				return err
			}

			// Create headless service
			if err := r.createRedisService(ctx, tc); err != nil {
				return err
			}

			r.Recorder.Event(tc, corev1.EventTypeNormal, "RedisCreated", fmt.Sprintf("Created Redis cluster %s", sts.Name))
			return nil
		}
		return err
	}

	return nil
}

// createRedisService creates a headless service for Redis
func (r *TokenCacheReconciler) createRedisService(ctx context.Context, tc *inferencev1alpha1.TokenCache) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-redis", tc.Name),
			Namespace: tc.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       tc.Name,
				"app.kubernetes.io/component":  "redis",
				"app.kubernetes.io/managed-by": "inference-operator",
			},
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None", // Headless
			Selector: map[string]string{
				"app.kubernetes.io/name":      tc.Name,
				"app.kubernetes.io/component": "redis",
			},
			Ports: []corev1.ServicePort{
				{
					Name:       "redis",
					Port:       6379,
					TargetPort: intstr.FromInt(6379),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(tc, svc, r.Scheme); err != nil {
		return err
	}

	found := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: svc.Name, Namespace: svc.Namespace}, found)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.Create(ctx, svc)
		}
		return err
	}

	return nil
}

// reconcileLocalStorage creates PVCs for local cache tier
func (r *TokenCacheReconciler) reconcileLocalStorage(ctx context.Context, tc *inferencev1alpha1.TokenCache) error {
	// Find local tier
	var localTier *inferencev1alpha1.CacheTierSpec
	for i := range tc.Spec.Tiers {
		if tc.Spec.Tiers[i].Type == inferencev1alpha1.CacheTierTypeLocal {
			localTier = &tc.Spec.Tiers[i]
			break
		}
	}

	if localTier == nil {
		return nil // No local tier configured
	}

	// Skip if using RAM disk
	if localTier.LocalConfig != nil && localTier.LocalConfig.UseRamDisk {
		return nil
	}

	// Create PVC for local cache
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-local-cache", tc.Name),
			Namespace: tc.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       tc.Name,
				"app.kubernetes.io/component":  "local-cache",
				"app.kubernetes.io/managed-by": "inference-operator",
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: localTier.Size,
				},
			},
		},
	}

	// Set storage class if specified
	if localTier.LocalConfig != nil && localTier.LocalConfig.StorageClass != "" {
		pvc.Spec.StorageClassName = &localTier.LocalConfig.StorageClass
	}

	if err := controllerutil.SetControllerReference(tc, pvc, r.Scheme); err != nil {
		return err
	}

	found := &corev1.PersistentVolumeClaim{}
	err := r.Get(ctx, types.NamespacedName{Name: pvc.Name, Namespace: pvc.Namespace}, found)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.Create(ctx, pvc)
		}
		return err
	}

	return nil
}

// updateStatus updates the TokenCache status
func (r *TokenCacheReconciler) updateStatus(ctx context.Context, tc *inferencev1alpha1.TokenCache) error {
	// Get status from cache manager
	status, err := r.CacheManager.GetStatus(tc.Name)
	if err != nil {
		tc.Status.Phase = inferencev1alpha1.TokenCachePhaseInitializing
	} else {
		tc.Status.Phase = status.Phase
		tc.Status.TotalSize = status.TotalSize
		tc.Status.UsedSize = status.UsedSize
		tc.Status.TotalEntries = status.TotalEntries
		tc.Status.OverallHitRate = status.OverallHitRate
		tc.Status.Tiers = status.Tiers
		tc.Status.WarmupStatus = status.WarmupStatus
	}

	tc.Status.ObservedGeneration = tc.Generation
	tc.Status.LastSyncTime = &metav1.Time{Time: time.Now()}

	// Update conditions
	r.updateConditions(tc)

	return r.Status().Update(ctx, tc)
}

// updateConditions updates status conditions
func (r *TokenCacheReconciler) updateConditions(tc *inferencev1alpha1.TokenCache) {
	now := metav1.Now()

	// Ready condition
	readyCondition := metav1.Condition{
		Type:               "Ready",
		LastTransitionTime: now,
	}

	allTiersReady := true
	for _, tier := range tc.Status.Tiers {
		if !tier.Ready {
			allTiersReady = false
			break
		}
	}

	if allTiersReady && len(tc.Status.Tiers) > 0 {
		readyCondition.Status = metav1.ConditionTrue
		readyCondition.Reason = "AllTiersReady"
		readyCondition.Message = "All cache tiers are ready"
		tc.Status.Phase = inferencev1alpha1.TokenCachePhaseReady
	} else {
		readyCondition.Status = metav1.ConditionFalse
		readyCondition.Reason = "TiersNotReady"
		readyCondition.Message = "Not all cache tiers are ready"
	}

	r.setCondition(tc, readyCondition)

	// Warmup condition
	if tc.Spec.Warmup != nil && tc.Spec.Warmup.Enabled {
		warmupCondition := metav1.Condition{
			Type:               "WarmupComplete",
			LastTransitionTime: now,
		}

		if tc.Status.WarmupStatus != nil && !tc.Status.WarmupStatus.InProgress {
			warmupCondition.Status = metav1.ConditionTrue
			warmupCondition.Reason = "WarmupComplete"
			warmupCondition.Message = "Cache warmup completed"
		} else {
			warmupCondition.Status = metav1.ConditionFalse
			warmupCondition.Reason = "WarmupInProgress"
			progress := int32(0)
			if tc.Status.WarmupStatus != nil {
				progress = tc.Status.WarmupStatus.Progress
			}
			warmupCondition.Message = fmt.Sprintf("Cache warmup in progress: %d%%", progress)
		}

		r.setCondition(tc, warmupCondition)
	}
}

// setCondition sets or updates a condition
func (r *TokenCacheReconciler) setCondition(tc *inferencev1alpha1.TokenCache, condition metav1.Condition) {
	for i, existing := range tc.Status.Conditions {
		if existing.Type == condition.Type {
			if existing.Status != condition.Status {
				tc.Status.Conditions[i] = condition
			}
			return
		}
	}
	tc.Status.Conditions = append(tc.Status.Conditions, condition)
}

// updateStatusWithError updates status with error information
func (r *TokenCacheReconciler) updateStatusWithError(ctx context.Context, tc *inferencev1alpha1.TokenCache, err error) (ctrl.Result, error) {
	log := r.Log.WithValues("tokencache", types.NamespacedName{Name: tc.Name, Namespace: tc.Namespace})
	log.Error(err, "Reconciliation error")

	tc.Status.Phase = inferencev1alpha1.TokenCachePhaseFailed

	condition := metav1.Condition{
		Type:               "Error",
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
		Reason:             "ReconcileError",
		Message:            err.Error(),
	}
	r.setCondition(tc, condition)

	if updateErr := r.Status().Update(ctx, tc); updateErr != nil {
		log.Error(updateErr, "Failed to update status")
	}

	r.Recorder.Event(tc, corev1.EventTypeWarning, "ReconcileError", err.Error())

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// SetupWithManager sets up the controller with the Manager
func (r *TokenCacheReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&inferencev1alpha1.TokenCache{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Complete(r)
}

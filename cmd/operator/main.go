package main

import (
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	inferencev1alpha1 "github.com/inference-operator/inference-operator/api/v1alpha1"
	"github.com/inference-operator/inference-operator/internal/backends"
	"github.com/inference-operator/inference-operator/internal/cache"
	"github.com/inference-operator/inference-operator/internal/controller"
	"github.com/inference-operator/inference-operator/internal/gpu"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(inferencev1alpha1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var enableWebhooks bool

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&enableWebhooks, "enable-webhooks", false, "Enable admission webhooks.")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "inference-operator.inference.ai",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Initialize GPU Manager
	gpuManager := gpu.NewManager(ctrl.Log.WithName("gpu-manager"))
	if err := gpuManager.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "unable to start GPU manager")
		os.Exit(1)
	}

	// Initialize Cache Manager
	cacheManager := cache.NewManager(ctrl.Log.WithName("cache-manager"))
	if err := cacheManager.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "unable to start cache manager")
		os.Exit(1)
	}

	// Initialize Backend Factory
	backendFactory := backends.NewBackendFactory()
	backendFactory.Register(backends.NewVLLMBackend(ctrl.Log.WithName("vllm-backend")))
	backendFactory.Register(backends.NewTritonBackend(ctrl.Log.WithName("triton-backend")))
	backendFactory.Register(backends.NewTensorRTBackend(ctrl.Log.WithName("tensorrt-backend")))

	// Setup InferenceService controller
	if err = (&controller.InferenceServiceReconciler{
		Client:         mgr.GetClient(),
		Log:            ctrl.Log.WithName("controllers").WithName("InferenceService"),
		Scheme:         mgr.GetScheme(),
		Recorder:       mgr.GetEventRecorderFor("inferenceservice-controller"),
		GPUManager:     gpuManager,
		CacheManager:   cacheManager,
		BackendFactory: backendFactory,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "InferenceService")
		os.Exit(1)
	}

	// Setup GPUPool controller
	if err = (&controller.GPUPoolReconciler{
		Client:     mgr.GetClient(),
		Log:        ctrl.Log.WithName("controllers").WithName("GPUPool"),
		Scheme:     mgr.GetScheme(),
		Recorder:   mgr.GetEventRecorderFor("gpupool-controller"),
		GPUManager: gpuManager,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "GPUPool")
		os.Exit(1)
	}

	// Setup TokenCache controller
	if err = (&controller.TokenCacheReconciler{
		Client:       mgr.GetClient(),
		Log:          ctrl.Log.WithName("controllers").WithName("TokenCache"),
		Scheme:       mgr.GetScheme(),
		Recorder:     mgr.GetEventRecorderFor("tokencache-controller"),
		CacheManager: cacheManager,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "TokenCache")
		os.Exit(1)
	}

	// Setup health checks
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

package cmd

import (
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/kubesphere/pvc-autoresizer/runners"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	_ = clientgoscheme.AddToScheme(scheme)

	_ = corev1.AddToScheme(scheme)
	// +kubebuilder:scaffold:scheme
}

func subMain() error {
	ctrl.SetLogger(zap.New(zap.UseDevMode(config.development)))

	graceTimeout := 10 * time.Second
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                  scheme,
		Port:                    9443,
		MetricsBindAddress:      config.metricsAddr,
		HealthProbeBindAddress:  config.healthAddr,
		LeaderElection:          true,
		LeaderElectionID:        "kubesphere.io",
		GracefulShutdownTimeout: &graceTimeout,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		return err
	}

	if err := mgr.AddHealthzCheck("ping", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("ping", healthz.Ping); err != nil {
		return err
	}

	promClient, err := runners.NewPrometheusClient(config.prometheusURL, &config.HTTPClientConfig)
	if err != nil {
		setupLog.Error(err, "unable to initialize prometheus client")
		return err
	}

	if err := runners.SetupIndexer(mgr, config.skipAnnotation); err != nil {
		setupLog.Error(err, "unable to initialize pvc autoresizer")
		return err
	}

	pvcAutoresizer := runners.NewPVCAutoresizer(promClient, mgr.GetClient(),
		ctrl.Log.WithName("pvc-autoresizer"),
		config.watchInterval, mgr.GetEventRecorderFor("pvc-autoresizer"))
	if err := mgr.Add(pvcAutoresizer); err != nil {
		setupLog.Error(err, "unable to add autoresier to manager")
		return err
	}

	restarter := runners.NewRestarter(mgr.GetClient(),
		ctrl.Log.WithName("workload-restart"),
		config.watchInterval, mgr.GetEventRecorderFor("workload-restart"))
	if err := mgr.Add(restarter); err != nil {
		setupLog.Error(err, "unable to add restarter to manager")
		return err
	}

	// +kubebuilder:scaffold:builder

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		return err
	}
	return nil
}

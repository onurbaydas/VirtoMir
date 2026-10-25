// Command virtomir-operator manages VirtoMir machines and projects from Kubernetes:
// VirtoMirMachine and VirtoMirProject objects (virtomir.io/v1alpha1).
package main

import (
	"flag"
	"os"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	v1 "github.com/onurbaydas/virtomir/integrations/virtomir-operator/api/v1alpha1"
	"github.com/onurbaydas/virtomir/integrations/virtomir-operator/internal/controller"
	"github.com/onurbaydas/virtomir/integrations/virtomir-operator/internal/virtomir"
)

func main() {
	var metrics, probes string
	var leader bool
	var every time.Duration
	flag.StringVar(&metrics, "metrics-bind-address", "0", `metrics address ("0" = off)`)
	flag.StringVar(&probes, "health-probe-bind-address", ":8081", "health probe address")
	flag.BoolVar(&leader, "leader-elect", false, "elect a leader (for more than one replica)")
	flag.DurationVar(&every, "resync", 30*time.Second, "how often each object is compared with VirtoMir")
	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("setup")

	token := os.Getenv("VIRTOMIR_TOKEN")
	if f := os.Getenv("VIRTOMIR_TOKEN_FILE"); f != "" {
		raw, err := os.ReadFile(f)
		if err != nil {
			log.Error(err, "API token")
			os.Exit(1)
		}
		token = strings.TrimSpace(string(raw))
	}
	vm, err := virtomir.New(os.Getenv("VIRTOMIR_URL"), token, os.Getenv("VIRTOMIR_INSECURE") == "true")
	if err != nil {
		log.Error(err, "VirtoMir")
		os.Exit(1)
	}

	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = v1.AddToScheme(scheme)
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme, Metrics: metricsserver.Options{BindAddress: metrics},
		HealthProbeBindAddress: probes, LeaderElection: leader, LeaderElectionID: "virtomir-operator.virtomir.io",
	})
	if err != nil {
		log.Error(err, "manager")
		os.Exit(1)
	}
	if err := (&controller.MachineReconciler{Client: mgr.GetClient(), VirtoMir: vm, Every: every}).SetupWithManager(mgr); err != nil {
		log.Error(err, "machine controller")
		os.Exit(1)
	}
	if err := (&controller.ProjectReconciler{Client: mgr.GetClient(), VirtoMir: vm, Every: every}).SetupWithManager(mgr); err != nil {
		log.Error(err, "project controller")
		os.Exit(1)
	}
	_ = mgr.AddHealthzCheck("healthz", healthz.Ping)
	_ = mgr.AddReadyzCheck("readyz", healthz.Ping)
	log.Info("starting")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "manager stopped")
		os.Exit(1)
	}
}

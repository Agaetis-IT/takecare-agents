// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

// Command operator runs the TakeCare operator: a controller-runtime manager
// that reconciles TakeCare resources. Controllers are registered from PR 2 on.
package main

import (
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	takecarev1alpha1 "github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// leaderElectionID names the Lease shared by the operator's replicas.
const leaderElectionID = "takecare-operator.takecare.agaetis.fr"

// config holds the operator's command-line configuration.
type config struct {
	metricsAddr    string
	probeAddr      string
	secureMetrics  bool
	enableHTTP2    bool
	leaderElection bool
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		ctrl.Log.WithName("setup").Error(err, "Operator stopped")
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("operator", flag.ContinueOnError)
	var cfg config
	fs.StringVar(&cfg.metricsAddr, "metrics-bind-address", ":8443",
		"Address the metrics endpoint binds to; 0 disables it.")
	fs.StringVar(&cfg.probeAddr, "health-probe-bind-address", ":8081",
		"Address the health and readiness probes bind to.")
	fs.BoolVar(&cfg.secureMetrics, "metrics-secure", true,
		"Serve metrics over HTTPS, behind Kubernetes authentication and authorization.")
	fs.BoolVar(&cfg.enableHTTP2, "enable-http2", false,
		"Enable HTTP/2 on the metrics server (off by default, see CVE-2023-44487).")
	fs.BoolVar(&cfg.leaderElection, "leader-elect", false,
		"Enable leader election, so that only one replica is active.")
	zapOpts := zap.Options{}
	zapOpts.BindFlags(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse flags: %w", err)
	}
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))

	scheme, err := newScheme()
	if err != nil {
		return err
	}
	restConfig, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}
	mgr, err := ctrl.NewManager(restConfig, managerOptions(cfg, scheme))
	if err != nil {
		return fmt.Errorf("create manager: %w", err)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("add health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("add ready check: %w", err)
	}

	ctrl.Log.WithName("setup").Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		return fmt.Errorf("run manager: %w", err)
	}
	return nil
}

// newScheme registers every API group the operator reads or writes.
func newScheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("register client-go types: %w", err)
	}
	if err := takecarev1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("register takecare types: %w", err)
	}
	return scheme, nil
}

// managerOptions translates the command-line configuration into manager options.
func managerOptions(cfg config, scheme *runtime.Scheme) ctrl.Options {
	var tlsOpts []func(*tls.Config)
	if !cfg.enableHTTP2 {
		tlsOpts = append(tlsOpts, func(c *tls.Config) { c.NextProtos = []string{"http/1.1"} })
	}
	metrics := metricsserver.Options{
		BindAddress:   cfg.metricsAddr,
		SecureServing: cfg.secureMetrics,
		TLSOpts:       tlsOpts,
	}
	if cfg.secureMetrics {
		metrics.FilterProvider = filters.WithAuthenticationAndAuthorization
	}
	return ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metrics,
		HealthProbeBindAddress: cfg.probeAddr,
		LeaderElection:         cfg.leaderElection,
		LeaderElectionID:       leaderElectionID,
	}
}

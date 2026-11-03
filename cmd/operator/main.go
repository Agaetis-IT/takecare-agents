// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Command operator runs the TakeCare operator: a controller-runtime manager
// that reconciles TakeCare resources. Controllers are registered from PR 2 on.
//
// The subcommand `gateway-api-crds` is what the takecare-crds chart's hook job
// runs: it installs the Gateway API CRDs embedded in this binary.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	takecarev1alpha1 "github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/alerts"
	"github.com/agaetis-it/takecare-agents/internal/controller/connector"
	"github.com/agaetis-it/takecare-agents/internal/controller/demo"
	"github.com/agaetis-it/takecare-agents/internal/controller/investigation"
	"github.com/agaetis-it/takecare-agents/internal/controller/platform"
	"github.com/agaetis-it/takecare-agents/internal/controller/takecaredoc"
	"github.com/agaetis-it/takecare-agents/internal/engine/kagent0103"
	"github.com/agaetis-it/takecare-agents/internal/gatewayapi"
	"github.com/agaetis-it/takecare-agents/internal/httpclient"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/routes"
	"github.com/agaetis-it/takecare-agents/internal/runs"
	"github.com/agaetis-it/takecare-agents/internal/telemetry"
	"github.com/agaetis-it/takecare-agents/internal/usage"
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
	// operatorNamespace holds the platform's Secrets and the Gateway;
	// engineNamespace is the one namespace engine objects are written in.
	// Both come from the chart (Deployment env), so that the operator's
	// Roles and its behavior name the same namespaces.
	operatorNamespace string
	engineNamespace   string
	// operatorDeployment names the operator's own Deployment, read for the
	// single-replica production gap.
	operatorDeployment string
	// alertsAddr serves the Alertmanager webhook (spec 02 §1), behind the
	// token of the Secret alertsTokenSecret.
	alertsAddr        string
	alertsTokenSecret string
	// investigationRetention is how long an ended investigation is kept.
	investigationRetention time.Duration
	// demo runs the TakeCareDemo controller, with demoAppImage as
	// billing-api's image: the chart sets both with demo.enabled.
	demo         bool
	demoAppImage string
	// usageScrapeWait is how long after a run's end its last model calls
	// are in Prometheus (one scrape interval, spec 02 §5).
	usageScrapeWait time.Duration
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		ctrl.Log.WithName("setup").Error(err, "Operator stopped")
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 && args[0] == "gateway-api-crds" {
		return runGatewayAPICRDs(args[1:])
	}
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
	fs.StringVar(&cfg.operatorNamespace, "operator-namespace", os.Getenv("OPERATOR_NAMESPACE"),
		"Namespace of the operator, where the platform's Secrets and the Gateway live (default: OPERATOR_NAMESPACE).")
	fs.StringVar(&cfg.engineNamespace, "engine-namespace", os.Getenv("ENGINE_NAMESPACE"),
		"The one namespace engine objects are written in (default: ENGINE_NAMESPACE).")
	fs.StringVar(&cfg.alertsAddr, "alerts-bind-address", ":8080",
		"Address the Alertmanager webhook (POST /v1/alerts) binds to; 0 disables it.")
	fs.DurationVar(&cfg.investigationRetention, "investigation-retention", investigation.DefaultRetention,
		"How long an ended investigation is kept before it is deleted (spec 02); Queued and Running ones are never deleted.")
	fs.DurationVar(&cfg.usageScrapeWait, "usage-scrape-wait", 30*time.Second,
		"How long after a run ends its usage is read from Prometheus: one scrape interval of the gateway.")
	fs.StringVar(&cfg.alertsTokenSecret, "alerts-token-secret", alerts.TokenSecretName,
		"Secret, in the operator namespace, whose key token is the webhook's bearer token.")
	fs.StringVar(&cfg.operatorDeployment, "operator-deployment", os.Getenv("OPERATOR_DEPLOYMENT"),
		"Name of the operator's Deployment, for the single-replica production gap (default: OPERATOR_DEPLOYMENT).")
	fs.BoolVar(&cfg.demo, "demo", false,
		"Run the TakeCareDemo controller (demo only: its rights come from the chart's demo Roles).")
	fs.StringVar(&cfg.demoAppImage, "demo-app-image", "",
		"Image of the demo application, billing-api (required with --demo).")
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
	if cfg.demo && cfg.demoAppImage == "" {
		return errors.New("demo-app-image is required with --demo")
	}
	if cfg.operatorNamespace == "" || cfg.engineNamespace == "" {
		return errors.New("operator-namespace and engine-namespace are required (the chart sets OPERATOR_NAMESPACE and ENGINE_NAMESPACE)")
	}
	mgr, err := ctrl.NewManager(restConfig, managerOptions(cfg, scheme))
	if err != nil {
		return fmt.Errorf("create manager: %w", err)
	}
	traces, err := setupControllers(mgr, cfg)
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := traces.Shutdown(shutdownCtx); err != nil {
			ctrl.Log.Error(err, "trace exporter shutdown")
		}
	}()
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

// setupControllers wires every controller and runnable on the manager, in
// production and in the startup test alike: a type a controller watches
// that the scheme does not know fails here, not on the cluster.
func setupControllers(mgr ctrl.Manager, cfg config) (*telemetry.Provider, error) {
	// Outbound HTTP follows the customer's network: CA bundle and egress
	// proxy from the chart's global values, through the environment.
	network := httpclient.FromEnv(5 * time.Second)
	promClient, err := network.New()
	if err != nil {
		return nil, fmt.Errorf("outbound HTTP client: %w", err)
	}
	probe := &platform.PromProbe{Client: promClient}
	if err := mgr.Add(probe); err != nil {
		return nil, fmt.Errorf("add the Prometheus probe: %w", err)
	}
	// One stable tracer provider for the whole process, reconfigured by the
	// platform controller as the TakeCarePlatform changes (ADR 0006).
	traces := telemetry.New(network)
	otel.SetTracerProvider(traces)
	platformReconciler := &platform.Reconciler{
		Client:             mgr.GetClient(),
		APIReader:          mgr.GetAPIReader(),
		Recorder:           mgr.GetEventRecorder("takecare-operator"),
		OperatorNamespace:  cfg.operatorNamespace,
		EngineNamespace:    cfg.engineNamespace,
		Engine:             &kagent0103.Models{Client: mgr.GetClient()},
		OperatorDeployment: cfg.operatorDeployment,
		Probe:              probe,
		Telemetry:          traces,
	}
	if err := platformReconciler.SetupWithManager(mgr); err != nil {
		return nil, fmt.Errorf("set up the platform controller: %w", err)
	}
	mcpClient, err := httpclient.FromEnv(30 * time.Second).New()
	if err != nil {
		return nil, fmt.Errorf("MCP HTTP client: %w", err)
	}
	// The Doc's trigger: the Alertmanager webhook, on every replica, and
	// the admission of its records into the leader's run queue.
	alertMetrics, err := alerts.NewMetrics(crmetrics.Registry)
	if err != nil {
		return nil, err
	}
	if err := mgr.Add(&alertServer{addr: cfg.alertsAddr, handler: &alerts.Handler{
		Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), TokenSecret: cfg.alertsTokenSecret,
		Namespace: cfg.operatorNamespace, Metrics: alertMetrics,
	}}); err != nil {
		return nil, fmt.Errorf("add the alert intake: %w", err)
	}
	phases, err := investigation.NewPhaseMetric(crmetrics.Registry)
	if err != nil {
		return nil, err
	}
	queue := runs.NewQueue(runs.DefaultCapacity)
	if err := (&investigation.Reconciler{
		Client: mgr.GetClient(), Recorder: mgr.GetEventRecorder("takecare-operator"),
		Queue: queue, Phases: phases,
	}).SetupWithManager(mgr); err != nil {
		return nil, fmt.Errorf("set up the investigation controller: %w", err)
	}
	// The retention of ended investigations, on the leader (spec 02).
	if err := mgr.Add(&investigation.Sweeper{
		Client: mgr.GetClient(), Namespace: cfg.operatorNamespace, Retention: cfg.investigationRetention,
	}); err != nil {
		return nil, fmt.Errorf("add the investigation retention: %w", err)
	}
	// The Doc's runs, on the leader: A2A through the gateway, one shared
	// client whose timeout bounds one call (a run is followed by polling).
	a2aClient, err := httpclient.FromEnv(30 * time.Second).New()
	if err != nil {
		return nil, fmt.Errorf("A2A HTTP client: %w", err)
	}
	runMetrics, err := runs.NewMetrics(crmetrics.Registry)
	if err != nil {
		return nil, err
	}
	duration, err := investigation.NewDurationMetric(crmetrics.Registry)
	if err != nil {
		return nil, err
	}
	// Usage from the gateway's metrics on the platform's Prometheus, and
	// publication through the report connector (spec 02 §4, §5).
	usageSource := func(p *takecarev1alpha1.TakeCarePlatform) usage.Source {
		if p.Spec.Observability.Metrics == nil || p.Spec.Observability.Metrics.PrometheusURL == "" {
			return nil
		}
		return usage.Prometheus{HTTP: promClient, URL: p.Spec.Observability.Metrics.PrometheusURL}
	}
	if err := mgr.Add(&investigation.Worker{
		Client: mgr.GetClient(), Recorder: mgr.GetEventRecorder("takecare-operator"), Queue: queue,
		Executor:   &runs.Executor{Engine: &kagent0103.Runs{HTTP: a2aClient}, Role: "doc", Metrics: runMetrics},
		Tracer:     traces.Tracer("github.com/agaetis-it/takecare-agents/internal/controller/investigation"),
		EngineKind: kagent0103.Kind, EngineNamespace: cfg.engineNamespace, Phases: phases,
		Accounting: investigation.Accounting{
			Usage: usageSource, ScrapeWait: cfg.usageScrapeWait,
			Poster: &mcp.Client{HTTP: mcpClient}, Duration: duration,
		},
	}); err != nil {
		return nil, fmt.Errorf("add the investigation worker: %w", err)
	}
	// The Doc's rendering: its routes on the gateway, its agent in the
	// engine, and its agent card probed through the gateway.
	gatewayClient, err := httpclient.FromEnv(3 * time.Second).New()
	if err != nil {
		return nil, fmt.Errorf("gateway HTTP client: %w", err)
	}
	if err := (&takecaredoc.Reconciler{
		Client: mgr.GetClient(), Recorder: mgr.GetEventRecorder("takecare-operator"),
		OperatorNamespace: cfg.operatorNamespace, EngineNamespace: cfg.engineNamespace,
		Engine: &kagent0103.Agents{Client: mgr.GetClient()},
		Probe:  takecaredoc.HTTPCardProbe(gatewayClient),
	}).SetupWithManager(mgr); err != nil {
		return nil, fmt.Errorf("set up the Doc controller: %w", err)
	}
	connectorReconciler := &connector.Reconciler{
		Client:            mgr.GetClient(),
		Recorder:          mgr.GetEventRecorder("takecare-operator"),
		OperatorNamespace: cfg.operatorNamespace,
		MCP:               &mcp.Client{HTTP: mcpClient},
	}
	if err := connectorReconciler.SetupWithManager(mgr); err != nil {
		return nil, fmt.Errorf("set up the connector controller: %w", err)
	}
	// The demo's scenery (spec 03), read outside the cache: its namespaces
	// are not the operator's.
	if cfg.demo {
		if err := (&demo.Reconciler{
			Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Recorder: mgr.GetEventRecorder("takecare-operator"),
			AppImage: cfg.demoAppImage, Rules: demo.PrometheusRules(promClient),
		}).SetupWithManager(mgr); err != nil {
			return nil, fmt.Errorf("set up the demo controller: %w", err)
		}
	}
	return traces, nil
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
	// CRD metadata, watched by the connector controller (the kubernetes
	// add-on caches its API discovery: a new CRD must be probed).
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("register apiextensions types: %w", err)
	}
	return scheme, nil
}

// managerOptions translates the command-line configuration into manager options.
//
// The cache is scoped per kind to the namespaces the operator's Roles cover
// (spec 02 §3): nothing namespaced is listed or watched cluster-wide, and
// Secrets are cached in the operator namespace only.
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
	operatorNS := map[string]cache.Config{cfg.operatorNamespace: {}}
	engineNS := map[string]cache.Config{cfg.engineNamespace: {}}
	byObject := map[client.Object]cache.ByObject{
		&corev1.Secret{}:                          {Namespaces: operatorNS},
		&corev1.ConfigMap{}:                       {Namespaces: engineNS},
		&corev1.Service{}:                         {Namespaces: operatorNS},
		&networkingv1.NetworkPolicy{}:             {Namespaces: engineNS},
		&appsv1.Deployment{}:                      {Namespaces: operatorNS},
		&discoveryv1.EndpointSlice{}:              {Namespaces: operatorNS},
		&takecarev1alpha1.TakeCareConnector{}:     {Namespaces: operatorNS},
		&takecarev1alpha1.TakeCareDoc{}:           {Namespaces: operatorNS},
		&takecarev1alpha1.TakeCareInvestigation{}: {Namespaces: operatorNS},
		&takecarev1alpha1.TakeCareDemo{}:          {Namespaces: operatorNS},
	}
	// Third-party kinds, each once: two controllers may name the same.
	seen := map[schema.GroupVersionKind]bool{}
	scope := func(gvks []schema.GroupVersionKind, namespaces map[string]cache.Config) {
		for _, gvk := range gvks {
			if seen[gvk] {
				continue
			}
			seen[gvk] = true
			u := &unstructured.Unstructured{}
			u.SetGroupVersionKind(gvk)
			byObject[u] = cache.ByObject{Namespaces: namespaces}
		}
	}
	scope(platform.OperatorNamespaceKinds(), operatorNS)
	scope(routes.Kinds(), operatorNS)
	scope((&kagent0103.Models{}).OwnedTypes(), engineNS)
	scope((&kagent0103.Agents{}).AgentTypes(), engineNS)
	return ctrl.Options{
		Scheme:                 scheme,
		Cache:                  cache.Options{ByObject: byObject},
		Metrics:                metrics,
		HealthProbeBindAddress: cfg.probeAddr,
		LeaderElection:         cfg.leaderElection,
		LeaderElectionID:       leaderElectionID,
	}
}

// runGatewayAPICRDs installs the embedded Gateway API CRDs and exits. It runs
// in a Helm hook job, before the charts that need them.
func runGatewayAPICRDs(args []string) error {
	fs := flag.NewFlagSet("gateway-api-crds", flag.ContinueOnError)
	mode := fs.String("mode", string(gatewayapi.ModeAuto),
		"What to do when Gateway API CRDs already exist: auto (install missing, keep newer, fail on older), always (take over), never.")
	zapOpts := zap.Options{}
	zapOpts.BindFlags(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse flags: %w", err)
	}
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
	m, err := gatewayapi.ParseMode(*mode)
	if err != nil {
		return err
	}
	scheme := runtime.NewScheme()
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("register apiextensions types: %w", err)
	}
	restConfig, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}
	c, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}
	ctx := ctrl.SetupSignalHandler()
	return gatewayapi.Install(log.IntoContext(ctx, ctrl.Log.WithName("gateway-api-crds")), c, m)
}

// alertServer serves the Alertmanager webhook on every replica (creating a
// record is idempotent; the run happens on the leader).
type alertServer struct {
	addr    string
	handler http.Handler
}

// NeedLeaderElection keeps the webhook on every replica.
func (*alertServer) NeedLeaderElection() bool { return false }

// Start serves until ctx ends.
func (s *alertServer) Start(ctx context.Context) error {
	if s.addr == "" || s.addr == "0" {
		return nil
	}
	mux := http.NewServeMux()
	mux.Handle("/v1/alerts", s.handler)
	srv := &http.Server{Addr: s.addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	errs := make(chan error, 1)
	go func() { errs <- srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errs:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("alert intake: %w", err)
	}
}

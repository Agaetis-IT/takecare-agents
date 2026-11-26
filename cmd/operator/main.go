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
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	takecarev1alpha1 "github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/alerts"
	"github.com/agaetis-it/takecare-agents/internal/cachescope"
	"github.com/agaetis-it/takecare-agents/internal/controller/connector"
	"github.com/agaetis-it/takecare-agents/internal/controller/demo"
	"github.com/agaetis-it/takecare-agents/internal/controller/intendant"
	"github.com/agaetis-it/takecare-agents/internal/controller/investigation"
	"github.com/agaetis-it/takecare-agents/internal/controller/kine"
	"github.com/agaetis-it/takecare-agents/internal/controller/platform"
	"github.com/agaetis-it/takecare-agents/internal/controller/ranger"
	"github.com/agaetis-it/takecare-agents/internal/controller/takecaredoc"
	"github.com/agaetis-it/takecare-agents/internal/coverage"
	"github.com/agaetis-it/takecare-agents/internal/engine/kagent0103"
	"github.com/agaetis-it/takecare-agents/internal/gatewayapi"
	"github.com/agaetis-it/takecare-agents/internal/httpclient"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
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
	// investigationRetention is how long an ended investigation is kept,
	// and investigationMaxKept how many ended ones at most.
	investigationRetention time.Duration
	investigationMaxKept   int
	// reviewRetention is how long an ended review is kept.
	reviewRetention time.Duration
	// demo runs the TakeCareDemo controller, with demoAppImage as
	// billing-api's image: the chart sets both with demo.enabled.
	demo         bool
	demoAppImage string
	// caBundle is the chart's CA bundle (Kind/name/key), which the gateway
	// is told to trust for a url connector's https server.
	caBundle string
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
		"How long an ended investigation is kept before it is deleted (spec 11 §5); Queued and Running ones are never deleted.")
	fs.IntVar(&cfg.investigationMaxKept, "investigation-max-kept", investigation.DefaultMaxKept,
		"How many ended investigations are kept at most; past it, the oldest ended go first, whatever their age.")
	fs.DurationVar(&cfg.reviewRetention, "review-retention", kine.DefaultReviewRetention,
		"How long an ended review is kept before it is deleted; each Kiné keeps its newest ended review.")
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
	fs.StringVar(&cfg.caBundle, "ca-bundle", "",
		"The chart's CA bundle as ConfigMap/<name>/<key> or Secret/<name>/<key> in the operator namespace; empty: the system's roots.")
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

// kineRuns is what the Kiné's reviews share with the other roles' runs:
// the A2A client, the run metrics, the usage source and the tracer.
type kineRuns struct {
	a2a     *http.Client
	metrics *runs.Metrics
	usage   func(p *takecarev1alpha1.TakeCarePlatform) usage.Source
	tracer  trace.Tracer
}

// setupKine sets up the Kiné (spec 11): the retention of its reviews, its
// controller (its conditions, its scope, its routes on the gateway and the
// triggers of its reviews), and its reviews, queued as they come and run
// one per Kiné at a time.
func setupKine(mgr ctrl.Manager, cfg config, mcpClient *http.Client, rr kineRuns) error {
	metrics, err := kine.NewReviewMetrics(crmetrics.Registry)
	if err != nil {
		return err
	}
	if err := mgr.Add(&kine.Sweeper{
		Client: mgr.GetClient(), Namespace: cfg.operatorNamespace, Retention: cfg.reviewRetention,
	}); err != nil {
		return fmt.Errorf("add the review retention: %w", err)
	}
	if err := (&kine.Reconciler{
		Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Recorder: mgr.GetEventRecorder("takecare-operator"),
		OperatorNamespace: cfg.operatorNamespace, EngineNamespace: cfg.engineNamespace,
		Engine: &kagent0103.Agents{Client: mgr.GetClient()}, Models: &kagent0103.Models{Client: mgr.GetClient()},
		Lookup: &coverage.Lookup{MCP: &mcp.Client{HTTP: mcpClient}}, Metrics: metrics,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("set up the Kiné controller: %w", err)
	}
	reviews := runs.NewQueue(16)
	runner := &kine.Runner{
		Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Queue: reviews,
		Runs: &kagent0103.Runs{HTTP: rr.a2a}, EngineKind: kagent0103.Kind, EngineNamespace: cfg.engineNamespace,
		RunMetrics: rr.metrics, Tracer: rr.tracer, Usage: rr.usage, ScrapeWait: cfg.usageScrapeWait,
		Retention: cfg.investigationRetention,
		// The publication (spec 11 §8), posted as the Doc's diagnoses are.
		Poster: &mcp.Client{HTTP: mcpClient}, Recorder: mgr.GetEventRecorder("takecare-operator"), Metrics: metrics,
	}
	// The queuer ends a Running review the runner does not hold at its
	// bound (spec 11 §6).
	if err := (&kine.ReviewQueuer{
		Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Queue: reviews,
		Recorder: mgr.GetEventRecorder("takecare-operator"), Runs: runner, ScrapeWait: cfg.usageScrapeWait, Metrics: metrics,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("set up the review queue: %w", err)
	}
	if err := mgr.Add(runner); err != nil {
		return fmt.Errorf("add the review runner: %w", err)
	}
	return nil
}

// setupAlerts adds the Doc's trigger: the Alertmanager webhook, on every
// replica, and the admission of its records into the leader's run queue.
func setupAlerts(mgr ctrl.Manager, cfg config, mcpClient *http.Client) error {
	alertMetrics, err := alerts.NewMetrics(crmetrics.Registry)
	if err != nil {
		return err
	}
	// The intake finds an incident's or a fingerprint's investigations
	// through a cache index, not by reading the namespace's.
	if err := alerts.IndexInvestigations(context.Background(), mgr.GetFieldIndexer()); err != nil {
		return err
	}
	if err := mgr.Add(&alertServer{addr: cfg.alertsAddr, handler: &alerts.Handler{
		Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Indexed: true, TokenSecret: cfg.alertsTokenSecret,
		Namespace: cfg.operatorNamespace, Metrics: alertMetrics,
		Resolver: &alerts.MapResolver{Client: mgr.GetClient(), HTTP: mcpClient, Namespace: cfg.operatorNamespace},
	}}); err != nil {
		return fmt.Errorf("add the alert intake: %w", err)
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
		OperatorDeployment: cfg.operatorDeployment,
		Probe:              probe,
		Telemetry:          traces,
	}
	if err := platformReconciler.SetupWithManager(mgr); err != nil {
		return nil, fmt.Errorf("set up the platform controller: %w", err)
	}
	// The Ranger's Markers on the platform's Gateway (spec 09).
	rangerAlerts, err := ranger.NewAlertsMetric(crmetrics.Registry)
	if err != nil {
		return nil, err
	}
	if err := (&ranger.Reconciler{
		Client: mgr.GetClient(), Recorder: mgr.GetEventRecorder("takecare-operator"),
		OperatorNamespace: cfg.operatorNamespace,
		Alerts: &ranger.Alerts{Total: rangerAlerts, Source: func(url string) ranger.Querier {
			return usage.Prometheus{HTTP: promClient, URL: url}
		}},
	}).SetupWithManager(mgr); err != nil {
		return nil, fmt.Errorf("set up the Ranger controller: %w", err)
	}
	mcpClient, err := httpclient.FromEnv(30 * time.Second).New()
	if err != nil {
		return nil, fmt.Errorf("MCP HTTP client: %w", err)
	}
	if err := setupAlerts(mgr, cfg, mcpClient); err != nil {
		return nil, err
	}
	phases, err := investigation.NewPhaseMetric(crmetrics.Registry)
	if err != nil {
		return nil, err
	}
	queue := runs.NewQueue(runs.DefaultCapacity)
	// The retention of ended investigations and reviews, on the leader
	// (spec 11 §5).
	if err := mgr.Add(&investigation.Sweeper{
		Client: mgr.GetClient(), Namespace: cfg.operatorNamespace,
		Retention: cfg.investigationRetention, MaxKept: cfg.investigationMaxKept,
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
	worker := &investigation.Worker{
		Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Recorder: mgr.GetEventRecorder("takecare-operator"), Queue: queue,
		Executor:   &runs.Executor{Engine: &kagent0103.Runs{HTTP: a2aClient}, Role: "doc", Metrics: runMetrics},
		Tracer:     traces.Tracer("github.com/agaetis-it/takecare-agents/internal/controller/investigation"),
		EngineKind: kagent0103.Kind, EngineNamespace: cfg.engineNamespace, Phases: phases,
		Accounting: investigation.Accounting{
			Usage: usageSource, ScrapeWait: cfg.usageScrapeWait,
			Poster: &mcp.Client{HTTP: mcpClient}, Duration: duration,
		},
	}
	if err := mgr.Add(worker); err != nil {
		return nil, fmt.Errorf("add the investigation worker: %w", err)
	}
	// The intake's admission, and the backstop that ends a Running record
	// the worker does not hold past its bound.
	if err := (&investigation.Reconciler{
		Client: mgr.GetClient(), Recorder: mgr.GetEventRecorder("takecare-operator"),
		Queue: queue, Phases: phases, Runs: worker, ScrapeWait: cfg.usageScrapeWait,
	}).SetupWithManager(mgr); err != nil {
		return nil, fmt.Errorf("set up the investigation controller: %w", err)
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
		Models: &kagent0103.Models{Client: mgr.GetClient()},
		Probe:  takecaredoc.HTTPCardProbe(gatewayClient),
		MCP:    &mcp.Client{HTTP: mcpClient},
	}).SetupWithManager(mgr); err != nil {
		return nil, fmt.Errorf("set up the Doc controller: %w", err)
	}
	// The Intendant (spec 10): its conditions, schedule and routes.
	surveyMetrics, err := intendant.NewSurveyMetrics(crmetrics.Registry)
	if err != nil {
		return nil, err
	}
	if err := (&intendant.Reconciler{
		Metrics: surveyMetrics,
		Client:  mgr.GetClient(), Reader: mgr.GetAPIReader(), Recorder: mgr.GetEventRecorder("takecare-operator"),
		OperatorNamespace: cfg.operatorNamespace, EngineNamespace: cfg.engineNamespace,
		AgentKinds: (&kagent0103.Agents{}).AgentTypes(),
		Agents:     &kagent0103.Agents{Client: mgr.GetClient()}, Models: &kagent0103.Models{Client: mgr.GetClient()},
		Quiet: intendant.DefaultQuiet, FirstPassWait: intendant.DefaultFirstPassWait,
	}).SetupWithManager(mgr); err != nil {
		return nil, fmt.Errorf("set up the Intendant controller: %w", err)
	}
	// Its surveys (spec 10 §4): queued as they come, run one per Intendant
	// at a time through its routes on the gateway.
	surveys := runs.NewQueue(16)
	surveyRunner := &intendant.Runner{
		Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Queue: surveys,
		Caller: &mcp.Client{HTTP: gatewayClient}, Canary: &mcp.Client{HTTP: gatewayClient}, Metrics: surveyMetrics,
		Tracer: traces.Tracer("github.com/agaetis-it/takecare-agents/internal/controller/intendant"),
		// The agent pass (spec 10 §5), over the same A2A client as the Doc's.
		Runs: &kagent0103.Runs{HTTP: a2aClient}, EngineKind: kagent0103.Kind, EngineNamespace: cfg.engineNamespace, RunMetrics: runMetrics,
		Usage: usageSource, ScrapeWait: cfg.usageScrapeWait,
		// The digest (spec 10 §6), posted as the Doc's diagnoses are.
		Poster: &mcp.Client{HTTP: mcpClient}, Recorder: mgr.GetEventRecorder("takecare-operator"),
		// The inventory (spec 10 §8) lists the engine's agents.
		AgentKinds: (&kagent0103.Agents{}).AgentTypes(),
	}
	// The queuer ends a Running survey the runner does not hold at its
	// bound (spec 10 §5), with the runner's timeouts.
	if err := (&intendant.SurveyQueuer{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Queue: surveys,
		Recorder: mgr.GetEventRecorder("takecare-operator"), Runs: surveyRunner, Metrics: surveyMetrics,
		PassTimeout: surveyRunner.PassTimeout, ScrapeWait: surveyRunner.ScrapeWait}).SetupWithManager(mgr); err != nil {
		return nil, fmt.Errorf("set up the survey queue: %w", err)
	}
	if err := mgr.Add(surveyRunner); err != nil {
		return nil, fmt.Errorf("add the survey runner: %w", err)
	}
	if err := mgr.Add(&intendant.Sweeper{Client: mgr.GetClient(), Namespace: cfg.operatorNamespace}); err != nil {
		return nil, fmt.Errorf("add the survey sweeper: %w", err)
	}
	if err := setupKine(mgr, cfg, mcpClient, kineRuns{
		a2a: a2aClient, metrics: runMetrics, usage: usageSource,
		tracer: traces.Tracer("github.com/agaetis-it/takecare-agents/internal/controller/kine"),
	}); err != nil {
		return nil, err
	}
	caBundle, err := parseCABundle(cfg.caBundle)
	if err != nil {
		return nil, err
	}
	connectorReconciler := &connector.Reconciler{
		Client:            mgr.GetClient(),
		Recorder:          mgr.GetEventRecorder("takecare-operator"),
		OperatorNamespace: cfg.operatorNamespace,
		MCP:               &mcp.Client{HTTP: mcpClient},
		HTTP:              mcpClient,
		CABundle:          caBundle,
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
// (package cachescope, shared with the envtest managers).
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
		Cache:                  cachescope.Options(cfg.operatorNamespace, cfg.engineNamespace, platform.OperatorNamespaceKinds()),
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

// parseCABundle reads --ca-bundle: Kind/name/key, Kind ConfigMap or Secret.
func parseCABundle(v string) (connector.CARef, error) {
	if v == "" {
		return connector.CARef{}, nil
	}
	parts := strings.Split(v, "/")
	if len(parts) != 3 || (parts[0] != "ConfigMap" && parts[0] != "Secret") || parts[1] == "" || parts[2] == "" {
		return connector.CARef{}, fmt.Errorf("--ca-bundle %q: want ConfigMap/<name>/<key> or Secret/<name>/<key>", v)
	}
	return connector.CARef{Kind: parts[0], Name: parts[1], Key: parts[2]}, nil
}

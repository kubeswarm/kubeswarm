/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package cmd contains the kubeswarm operator Run function. The binary entrypoint
// lives in runtime/cmd/operator/main.go, which registers all third-party
// plugin implementations before calling Run().
package cmd

import (
	"context"
	"crypto/tls"
	"flag"
	"net/http"
	"os"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
	"github.com/kubeswarm/kubeswarm/internal/controller"
	"github.com/kubeswarm/kubeswarm/internal/mcpgateway"
	"github.com/kubeswarm/kubeswarm/internal/registry"
	swarmbhook "github.com/kubeswarm/kubeswarm/internal/webhook"
	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
	"github.com/kubeswarm/kubeswarm/pkg/costs"
	"github.com/kubeswarm/kubeswarm/pkg/observability"
	"github.com/kubeswarm/kubeswarm/pkg/validation"
	// +kubebuilder:scaffold:imports
)

// version is set at build time via -ldflags "-X main.version=<tag>".
var version = "dev"

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(kubeswarmv1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

// webhookRunnable wraps an http.Handler as a controller-runtime Runnable so it
// shares the manager lifecycle (started and gracefully shut down together).
type webhookRunnable struct {
	addr    string
	handler http.Handler
}

func triggerWebhookRunnable(addr string, handler http.Handler) *webhookRunnable {
	return &webhookRunnable{addr: addr, handler: handler}
}

func (r *webhookRunnable) Start(ctx context.Context) error {
	srv := &http.Server{
		Addr:              r.addr,
		Handler:           r.handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		// WriteTimeout is intentionally generous: SSE streams and sync mode
		// can legitimately run for up to maxSyncTimeout (5 minutes).
		WriteTimeout: 6 * time.Minute,
		IdleTimeout:  60 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	select {
	case <-ctx.Done():
		return srv.Shutdown(context.Background()) //nolint:contextcheck
	case err := <-errCh:
		return err
	}
}

func buildTLSOptions(enableHTTP2 bool) []func(*tls.Config) {
	if enableHTTP2 {
		return nil
	}
	return []func(*tls.Config){func(c *tls.Config) {
		setupLog.Info("Disabling HTTP/2")
		c.NextProtos = []string{"http/1.1"}
	}}
}

func buildWebhookServerOptions(tlsOpts []func(*tls.Config), certPath, certName, certKey string) webhook.Options {
	opts := webhook.Options{TLSOpts: tlsOpts}
	if certPath != "" {
		setupLog.Info("Initializing webhook certificate watcher using provided certificates",
			"webhook-cert-path", certPath, "webhook-cert-name", certName, "webhook-cert-key", certKey)
		opts.CertDir = certPath
		opts.CertName = certName
		opts.KeyName = certKey
	}
	return opts
}

func buildMetricsServerOptions(
	bindAddr string, tlsOpts []func(*tls.Config), secureMetrics bool, certPath, certName, certKey string,
) metricsserver.Options {
	opts := metricsserver.Options{
		BindAddress:   bindAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}
	if secureMetrics {
		opts.FilterProvider = filters.WithAuthenticationAndAuthorization
	}
	if certPath != "" {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", certPath, "metrics-cert-name", certName, "metrics-cert-key", certKey)
		opts.CertDir = certPath
		opts.CertName = certName
		opts.KeyName = certKey
	}
	return opts
}

// Run starts the kubeswarm operator. It is called from the binary entrypoint in
// runtime/cmd/operator/main.go after all plugin init() functions have run.
func Run() {
	var metricsAddr string
	var metricsCertPath, metricsCertName, metricsCertKey string
	var webhookCertPath, webhookCertName, webhookCertKey string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var agentImage string
	var agentImagePullPolicy string
	var triggerWebhookAddr string
	var triggerWebhookURL string
	var mcpGatewayAddr string
	var mcpGatewayURL string
	var disableAdmissionWebhooks bool
	var costConfigMap string
	var tlsOpts []func(*tls.Config)
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	flag.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	flag.StringVar(&webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	flag.StringVar(&metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flag.StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	flag.StringVar(&agentImage, "agent-image", "ghcr.io/kubeswarm/runtime:latest",
		"Container image used for agent runtime pods.")
	flag.StringVar(&agentImagePullPolicy, "agent-image-pull-policy", "Always",
		"ImagePullPolicy for agent runtime pods. Use Never for local kind development.")
	flag.StringVar(&triggerWebhookAddr, "trigger-webhook-addr", ":8092",
		"Address for the SwarmEvent webhook HTTP server to listen on.")
	flag.StringVar(&triggerWebhookURL, "trigger-webhook-url", "",
		"Base URL of the trigger webhook server, e.g. http://controller.kubeswarm-system.svc.cluster.local:8092. "+
			"Written to webhook-type SwarmEvent status.webhookURL.")
	flag.StringVar(&mcpGatewayAddr, "mcp-gateway-addr", ":8093",
		"Address for the MCP gateway HTTP server to listen on.")
	flag.StringVar(&mcpGatewayURL, "mcp-gateway-url", "",
		"Base URL of the MCP gateway, e.g. http://kubeswarm-mcp-gateway.kubeswarm-system.svc:8093. "+
			"Required to resolve swarmAgentRef entries in SwarmAgent spec.mcpServers.")
	flag.BoolVar(&disableAdmissionWebhooks, "disable-admission-webhooks", false,
		"Disable the prompt-size admission webhooks. Use in local dev environments without TLS certs.")
	flag.StringVar(&costConfigMap, "cost-configmap", "",
		"Optional name of a ConfigMap in the operator namespace containing model pricing overrides. "+
			"Format: each key is a model prefix, value is \"input_per_1m/output_per_1m\" USD (e.g. \"3.00/15.00\"). "+
			"The ConfigMap is watched for live reload without operator restart.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	setupLog.Info("Starting kubeswarm operator", "version", version)

	otelShutdown, err := observability.Init(context.Background(), "kubeswarm-controller")
	if err != nil {
		setupLog.Error(err, "Failed to initialise OpenTelemetry")
		os.Exit(1)
	}
	defer otelShutdown()

	tlsOpts = buildTLSOptions(enableHTTP2)
	metricsServerOptions := buildMetricsServerOptions(
		metricsAddr, tlsOpts, secureMetrics, metricsCertPath, metricsCertName, metricsCertKey)

	mgrOpts := ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "a7754879.kubeswarm",
		// Allow in-flight reconciles (including SwarmRun finalizer removal) to drain
		// before the process exits. Must be less than the pod terminationGracePeriodSeconds.
		GracefulShutdownTimeout: func() *time.Duration { d := 8 * time.Second; return &d }(),
	}
	if !disableAdmissionWebhooks {
		mgrOpts.WebhookServer = webhook.NewServer(
			buildWebhookServerOptions(tlsOpts, webhookCertPath, webhookCertName, webhookCertKey))
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), mgrOpts)
	if err != nil {
		setupLog.Error(err, "Failed to start manager")
		os.Exit(1)
	}

	if err := (&controller.SwarmAgentReconciler{
		Client:               mgr.GetClient(),
		Scheme:               mgr.GetScheme(),
		AgentImage:           agentImage,
		AgentImagePullPolicy: corev1.PullPolicy(agentImagePullPolicy),
		MCPGatewayURL:        mcpGatewayURL,
		OperatorNamespace:    os.Getenv("POD_NAMESPACE"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "SwarmAgent")
		os.Exit(1)
	}
	if err := (&controller.SwarmSettingsReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "SwarmSettings")
		os.Exit(1)
	}
	// Create the task queue and stream channel once and share them across controllers.
	// Both are nil when TASK_QUEUE_URL is not set, which disables flow execution and SSE streaming.
	var taskQueue queue.TaskQueue
	var streamCh queue.StreamChannel
	if queueURL := os.Getenv("TASK_QUEUE_URL"); queueURL != "" {
		var qErr error
		taskQueue, qErr = queue.NewQueue(queueURL, 3)
		if qErr != nil {
			setupLog.Error(qErr, "Failed to create task queue")
			os.Exit(1)
		}
		streamURL := os.Getenv("STREAM_CHANNEL_URL")
		if streamURL == "" {
			streamURL = queueURL
		}
		streamCh, qErr = queue.NewStream(streamURL)
		if qErr != nil {
			setupLog.Error(qErr, "Failed to create stream channel")
			os.Exit(1)
		}
	}

	if err := (&controller.SwarmMemoryReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "SwarmMemory")
		os.Exit(1)
	}

	triggerReconciler := &controller.SwarmEventReconciler{
		Client:            mgr.GetClient(),
		Scheme:            mgr.GetScheme(),
		TriggerWebhookURL: triggerWebhookURL,
		Recorder:          mgr.GetEventRecorderFor("swarmevent-controller"), //nolint:staticcheck
	}
	if err := triggerReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "SwarmEvent")
		os.Exit(1)
	}

	agentQueueURL := os.Getenv("AGENT_TASK_QUEUE_URL")
	if agentQueueURL == "" {
		agentQueueURL = os.Getenv("TASK_QUEUE_URL")
	}

	// Create the spend store. Defaults to the task queue URL so it reuses the
	// existing Redis connection. Set SPEND_STORE_URL to override (e.g. separate Redis).
	spendStoreURL := os.Getenv("SPEND_STORE_URL")
	if spendStoreURL == "" {
		spendStoreURL = os.Getenv("TASK_QUEUE_URL")
	}
	spendStore, sErr := costs.NewSpendStore(spendStoreURL)
	if sErr != nil {
		setupLog.Error(sErr, "Failed to create spend store")
		os.Exit(1)
	}

	// Build the CostProvider. When --cost-configmap is set, load the named
	// ConfigMap and start a background watcher for live pricing reload.
	// Falls back to the static provider for models not listed in the ConfigMap.
	costProvider := costs.Default()
	if costConfigMap != "" {
		operatorNS := os.Getenv("POD_NAMESPACE")
		if operatorNS == "" {
			operatorNS = "kubeswarm-system"
		}
		cm := &corev1.ConfigMap{}
		if cmErr := mgr.GetAPIReader().Get(context.Background(),
			types.NamespacedName{Namespace: operatorNS, Name: costConfigMap}, cm); cmErr != nil {
			setupLog.Error(cmErr, "Failed to load cost ConfigMap", "name", costConfigMap)
			os.Exit(1)
		}
		cmProvider, cmErr := costs.NewConfigMapCostProvider(cm.Data, costs.Default())
		if cmErr != nil {
			setupLog.Error(cmErr, "Failed to parse cost ConfigMap", "name", costConfigMap)
			os.Exit(1)
		}
		costProvider = cmProvider
		setupLog.Info("Cost ConfigMap loaded", "name", costConfigMap, "models", len(cm.Data))
		// Register a ConfigMap watcher as a manager Runnable so pricing reloads
		// automatically when the ConfigMap is updated (no operator restart needed).
		watcher := controller.NewCostConfigMapWatcher(mgr.GetClient(), operatorNS, costConfigMap, cmProvider)
		if err := mgr.Add(watcher); err != nil {
			setupLog.Error(err, "Failed to add cost ConfigMap watcher")
			os.Exit(1)
		}
	}

	notifyDispatcher := controller.NewNotifyDispatcher(mgr.GetClient())

	// Shared capability registry - maintained by SwarmRegistryReconciler, read by SwarmRunReconciler.
	capRegistry := &registry.Registry{}
	if err := (&controller.SwarmRegistryReconciler{
		Client:            mgr.GetClient(),
		Scheme:            mgr.GetScheme(),
		Registry:          capRegistry,
		TaskQueueURL:      os.Getenv("TASK_QUEUE_URL"),
		AgentTaskQueueURL: agentQueueURL,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "SwarmRegistry")
		os.Exit(1)
	}

	if err := (&controller.SwarmRunReconciler{
		Client:             mgr.GetClient(),
		Scheme:             mgr.GetScheme(),
		TaskQueueURL:       os.Getenv("TASK_QUEUE_URL"),
		AgentTaskQueueURL:  agentQueueURL,
		TaskQueue:          taskQueue,
		Recorder:           mgr.GetEventRecorderFor("swarmrun-controller"), //nolint:staticcheck
		NotifyDispatcher:   notifyDispatcher,
		SemanticValidateFn: validation.BuildSemanticValidateFn(),
		RouterFn:           validation.BuildSemanticValidateFn(),
		CompressFn:         validation.BuildSemanticValidateFn(),
		CostProvider:       costProvider,
		SpendStore:         spendStore,
		BudgetPolicy:       costs.DefaultBudgetPolicy(),
		Registry:           capRegistry,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "SwarmRun")
		os.Exit(1)
	}

	if err := (&controller.SwarmNotifyReconciler{
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		Dispatcher: notifyDispatcher,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "SwarmNotify")
		os.Exit(1)
	}
	if err := (&controller.SwarmBudgetReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		SpendStore:       spendStore,
		BudgetPolicy:     costs.DefaultBudgetPolicy(),
		NotifyDispatcher: notifyDispatcher,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "SwarmBudget")
		os.Exit(1)
	}
	if err := (&controller.SwarmTeamReconciler{
		Client:            mgr.GetClient(),
		Scheme:            mgr.GetScheme(),
		TaskQueueURL:      os.Getenv("TASK_QUEUE_URL"),
		AgentTaskQueueURL: agentQueueURL,
		TaskQueue:         taskQueue,
		Recorder:          mgr.GetEventRecorderFor("swarmteam-controller"), //nolint:staticcheck
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "SwarmTeam")
		os.Exit(1)
	}

	// Start the trigger webhook HTTP server as a manager Runnable so it
	// shares the manager's lifecycle (graceful shutdown on signal).
	// Build a dedicated watch-capable client for the webhook server so it can
	// use Watch instead of polling for flow status.
	watchClient, err := client.NewWithWatch(mgr.GetConfig(), client.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "Failed to create watch client")
		os.Exit(1)
	}
	webhookSrv := controller.NewTriggerWebhookServer(triggerReconciler, watchClient, streamCh)
	if err := mgr.Add(triggerWebhookRunnable(triggerWebhookAddr, webhookSrv)); err != nil {
		setupLog.Error(err, "Failed to add trigger webhook server to manager")
		os.Exit(1)
	}
	setupLog.Info("Trigger webhook server registered", "addr", triggerWebhookAddr)

	// Start the MCP gateway as a manager Runnable so it shares the operator lifecycle.
	// The gateway is started even when TASK_QUEUE_URL is not set — in that case it returns
	// errCodeNoReplicas or errCodeInternal for any tool call, which is the correct behaviour
	// (no queue = no task execution).
	mcpGW := mcpgateway.New(mgr.GetClient(), taskQueue, os.Getenv("TASK_QUEUE_URL"))
	if err := mgr.Add(triggerWebhookRunnable(mcpGatewayAddr, mcpGW)); err != nil {
		setupLog.Error(err, "Failed to add MCP gateway to manager")
		os.Exit(1)
	}
	setupLog.Info("MCP gateway registered", "addr", mcpGatewayAddr)

	// Register prompt-size admission webhooks. failurePolicy=ignore in the
	// ValidatingWebhookConfiguration means these never block apply if the webhook
	// is unavailable (e.g. during operator startup or in envtest).
	if !disableAdmissionWebhooks {
		promptDecoder := admission.NewDecoder(scheme)
		mgr.GetWebhookServer().Register(
			"/validate-kubeswarm-v1alpha1-swarmteam",
			&webhook.Admission{Handler: swarmbhook.NewSwarmTeamPromptValidator(promptDecoder)},
		)
		mgr.GetWebhookServer().Register(
			"/validate-kubeswarm-v1alpha1-swarmagent",
			&webhook.Admission{Handler: swarmbhook.NewSwarmAgentPromptValidator(promptDecoder, mgr.GetClient())},
		)
		setupLog.Info("Prompt size admission webhooks registered")
	} else {
		setupLog.Info("Admission webhooks disabled")
	}

	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "Failed to run manager")
		os.Exit(1)
	}
}

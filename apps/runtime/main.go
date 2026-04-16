package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	adapteronebot "github.com/ohmyopencode/bot-platform/adapters/adapter-onebot"
	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
	runtimecore "github.com/ohmyopencode/bot-platform/packages/runtime-core"
	pluginadmin "github.com/ohmyopencode/bot-platform/plugins/plugin-admin"
	pluginaichat "github.com/ohmyopencode/bot-platform/plugins/plugin-ai-chat"
	pluginecho "github.com/ohmyopencode/bot-platform/plugins/plugin-echo"
	"gopkg.in/yaml.v3"
)

type runtimeApp struct {
	config          runtimecore.Config
	settings        appRuntimeSettings
	runtime         *runtimecore.InMemoryRuntime
	runtimeRaw      *runtimecore.InMemoryRuntime
	logger          *runtimecore.Logger
	tracer          *runtimecore.TraceRecorder
	metrics         *runtimecore.MetricsRegistry
	logs            *logBuffer
	replies         *replyBuffer
	onebotIngress   *adapteronebot.IngressConverter
	audits          *runtimecore.InMemoryAuditLog
	state           *runtimecore.SQLiteStateStore
	lifecycle       *runtimecore.PluginLifecycleService
	queue           *runtimecore.JobQueue
	scheduler       *runtimecore.Scheduler
	schedulerCancel context.CancelFunc
	consoleMeta     map[string]any
	mux             *http.ServeMux
}

type appRuntimeSettings struct {
	SQLitePath          string
	SchedulerIntervalMs int
}

const scheduleCancelPermission = "schedule:cancel"

type aiProviderMock struct{}

func (aiProviderMock) Generate(_ context.Context, prompt string) (string, error) {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return "", fmt.Errorf("empty prompt")
	}
	if strings.Contains(strings.ToLower(trimmed), "fail") {
		return "", fmt.Errorf("mock upstream failure")
	}
	return "AI: " + trimmed, nil
}

type sourceScopedEventHandler struct {
	allowed map[string]struct{}
	inner   interface {
		OnEvent(eventmodel.Event, eventmodel.ExecutionContext) error
	}
}

func (h sourceScopedEventHandler) OnEvent(event eventmodel.Event, ctx eventmodel.ExecutionContext) error {
	if h.inner == nil {
		return nil
	}
	if len(h.allowed) > 0 {
		if _, ok := h.allowed[event.Source]; !ok {
			return nil
		}
	}
	return h.inner.OnEvent(event, ctx)
}

type replyRecord struct {
	Capability string `json:"capability"`
	TargetID   string `json:"target_id"`
	MessageID  string `json:"message_id,omitempty"`
	Kind       string `json:"kind"`
	Payload    string `json:"payload"`
}

type replyBuffer struct {
	mu      sync.Mutex
	logger  *runtimecore.Logger
	tracer  *runtimecore.TraceRecorder
	replies []replyRecord
}

func newReplyBuffer(logger *runtimecore.Logger, tracer *runtimecore.TraceRecorder) *replyBuffer {
	return &replyBuffer{logger: logger, tracer: tracer}
}

func (b *replyBuffer) ReplyText(handle eventmodel.ReplyHandle, text string) error {
	return b.record("text", handle, text)
}

func (b *replyBuffer) ReplyImage(handle eventmodel.ReplyHandle, imageURL string) error {
	return b.record("image", handle, imageURL)
}

func (b *replyBuffer) ReplyFile(handle eventmodel.ReplyHandle, fileURL string) error {
	return b.record("file", handle, fileURL)
}

func (b *replyBuffer) record(kind string, handle eventmodel.ReplyHandle, payload string) error {
	b.mu.Lock()
	record := replyRecord{
		Capability: handle.Capability,
		TargetID:   handle.TargetID,
		MessageID:  handle.MessageID,
		Kind:       kind,
		Payload:    payload,
	}
	b.replies = append(b.replies, record)
	b.mu.Unlock()

	if b.logger != nil {
		ctx := replyLogContext(handle)
		if b.tracer != nil {
			finish := b.tracer.StartSpan(ctx.TraceID, "reply.send", ctx.EventID, ctx.PluginID, ctx.RunID, ctx.CorrelationID, map[string]any{
				"target_id":  handle.TargetID,
				"message_id": handle.MessageID,
				"kind":       kind,
			})
			finish()
		}
		_ = b.logger.Log("info", "runtime demo reply recorded", ctx, map[string]any{
			"target_id":  handle.TargetID,
			"message_id": handle.MessageID,
			"kind":       kind,
			"payload":    payload,
		})
	}
	return nil
}

func replyLogContext(handle eventmodel.ReplyHandle) runtimecore.LogContext {
	metadata := handle.Metadata
	return runtimecore.LogContext{
		TraceID:       stringValue(metadata["trace_id"]),
		EventID:       stringValue(metadata["event_id"]),
		PluginID:      stringValue(metadata["plugin_id"]),
		RunID:         stringValue(metadata["run_id"]),
		CorrelationID: stringValue(metadata["correlation_id"]),
	}
}

func (b *replyBuffer) Count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.replies)
}

func (b *replyBuffer) Since(index int) []replyRecord {
	b.mu.Lock()
	defer b.mu.Unlock()
	if index < 0 || index > len(b.replies) {
		index = len(b.replies)
	}
	return append([]replyRecord(nil), b.replies[index:]...)
}

type logBuffer struct {
	mu    sync.Mutex
	lines []string
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, string(p))
	return len(p), nil
}

func (b *logBuffer) Lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.lines...)
}

type registeredAdapter struct {
	id     string
	source string
}

func (a registeredAdapter) ID() string     { return a.id }
func (a registeredAdapter) Source() string { return a.source }

func demoOneBotAdapterInstanceState(settings appRuntimeSettings) (runtimecore.AdapterInstanceState, error) {
	configPayload, err := json.Marshal(map[string]any{
		"mode":        "demo-ingress",
		"sqlite_path": settings.SQLitePath,
		"demo_path":   "/demo/onebot/message",
		"platform":    "onebot/v11",
	})
	if err != nil {
		return runtimecore.AdapterInstanceState{}, fmt.Errorf("marshal onebot demo adapter config: %w", err)
	}
	return runtimecore.AdapterInstanceState{
		InstanceID: "adapter-onebot-demo",
		Adapter:    "onebot",
		Source:     "onebot",
		RawConfig:  configPayload,
		Status:     "registered",
		Health:     "ready",
		Online:     true,
	}, nil
}

func newRuntimeApp(configPath string) (*runtimeApp, error) {
	config, err := runtimecore.LoadConfig(configPath)
	if err != nil {
		return nil, err
	}
	settings, err := loadAppRuntimeSettings(configPath)
	if err != nil {
		return nil, err
	}

	logs := &logBuffer{}
	logger := runtimecore.NewLogger(io.MultiWriter(os.Stdout, logs))
	tracer := runtimecore.NewTraceRecorder()
	metrics := runtimecore.NewMetricsRegistry()
	replies := newReplyBuffer(logger, tracer)
	audits := runtimecore.NewInMemoryAuditLog()
	queue := runtimecore.NewJobQueue()
	queue.SetObservability(logger, tracer, metrics)
	state, err := runtimecore.OpenSQLiteStateStore(settings.SQLitePath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite state store: %w", err)
	}
	queue.SetStore(state)
	queue.SetAlertSink(state)
	if err := queue.Restore(context.Background()); err != nil {
		_ = state.Close()
		return nil, fmt.Errorf("restore job queue: %w", err)
	}
	scheduler := runtimecore.NewScheduler()
	scheduler.SetObservability(logger, tracer, metrics)
	scheduler.SetStore(state)

	runtime := runtimecore.NewInMemoryRuntime(runtimecore.NoopSupervisor{}, runtimecore.DirectPluginHost{})
	runtime.SetObservability(logger, tracer, metrics)
	runtime.SetAuditRecorder(audits)
	runtime.SetDispatchRecorder(state)
	lifecycle := runtimecore.NewPluginLifecycleService(state)
	runtime.SetPluginEnabledStateSource(lifecycle)
	if config.RBAC != nil {
		runtime.SetCommandAuthorizer(runtimecore.NewAdminCommandAuthorizer(config.RBAC))
	}

	echoConfig, echoRawConfig, err := loadPersistedEchoConfig(state)
	if err != nil {
		_ = state.Close()
		return nil, fmt.Errorf("load echo plugin config: %w", err)
	}
	echoPlugin := pluginecho.New(replies, echoConfig)
	echoDefinition := echoPlugin.Definition()
	echoDefinition.InstanceConfig = echoRawConfig
	echoDefinition.Handlers.Event = sourceScopedEventHandler{allowed: allowedSources("onebot", "runtime-demo-scheduler"), inner: echoPlugin}
	if err := runtime.RegisterPlugin(echoDefinition); err != nil {
		_ = state.Close()
		return nil, fmt.Errorf("register echo plugin: %w", err)
	}
	if err := state.SavePluginManifest(context.Background(), echoDefinition.Manifest); err != nil {
		_ = state.Close()
		return nil, fmt.Errorf("save echo plugin manifest: %w", err)
	}
	aiPlugin := pluginaichat.New(queue, aiProviderMock{}, state, replies)
	aiDefinition := aiPlugin.Definition()
	aiDefinition.Handlers.Event = sourceScopedEventHandler{allowed: allowedSources("runtime-ai"), inner: aiPlugin}
	if err := runtime.RegisterPlugin(aiDefinition); err != nil {
		_ = state.Close()
		return nil, fmt.Errorf("register ai plugin: %w", err)
	}
	if err := state.SavePluginManifest(context.Background(), aiDefinition.Manifest); err != nil {
		_ = state.Close()
		return nil, fmt.Errorf("save ai plugin manifest: %w", err)
	}
	adminPlugin := pluginadmin.New(lifecycle, nil, nil, actorRoles(config.RBAC), policies(config.RBAC), audits)
	adminDefinition := adminPlugin.Definition()
	if err := runtime.RegisterPlugin(adminDefinition); err != nil {
		_ = state.Close()
		return nil, fmt.Errorf("register admin plugin: %w", err)
	}
	if err := state.SavePluginManifest(context.Background(), adminDefinition.Manifest); err != nil {
		_ = state.Close()
		return nil, fmt.Errorf("save admin plugin manifest: %w", err)
	}
	if err := runtime.RegisterAdapter(runtimecore.AdapterRegistration{
		ID:      "adapter-onebot-demo",
		Source:  "onebot",
		Adapter: registeredAdapter{id: "adapter-onebot-demo", source: "onebot"},
	}); err != nil {
		_ = state.Close()
		return nil, fmt.Errorf("register onebot demo adapter: %w", err)
	}
	onebotAdapterInstance, err := demoOneBotAdapterInstanceState(settings)
	if err != nil {
		_ = state.Close()
		return nil, err
	}
	if err := state.SaveAdapterInstance(context.Background(), onebotAdapterInstance); err != nil {
		_ = state.Close()
		return nil, fmt.Errorf("save onebot demo adapter instance: %w", err)
	}

	ingressLogs := io.MultiWriter(os.Stdout, logs)
	onebotIngress := adapteronebot.NewIngressConverter(ingressLogs)
	onebotIngress.SetObservability(tracer)

	app := &runtimeApp{
		config:        config,
		settings:      settings,
		runtime:       runtime,
		runtimeRaw:    runtime,
		logger:        logger,
		tracer:        tracer,
		metrics:       metrics,
		logs:          logs,
		replies:       replies,
		onebotIngress: onebotIngress,
		audits:        audits,
		state:         state,
		lifecycle:     lifecycle,
		queue:         queue,
		scheduler:     scheduler,
		consoleMeta: map[string]any{
			"runtime_entry": "apps/runtime",
			"demo_paths": []string{
				"/demo/onebot/message",
				"/demo/ai/message",
				"/demo/jobs/enqueue",
				"/demo/jobs/timeout",
				"/demo/jobs/{job-id}/retry",
				"/demo/schedules/echo-delay",
				"/demo/schedules/{schedule-id}/cancel",
				"/demo/plugins/{plugin-id}/disable",
				"/demo/plugins/{plugin-id}/enable",
				"/demo/replies",
				"/demo/state/counts",
			},
			"sqlite_path":           settings.SQLitePath,
			"scheduler_interval_ms": settings.SchedulerIntervalMs,
			"ai_job_dispatcher":     "runtime-job-queue",
			"console_mode":          "read+operator-plugin-enable-disable",
		},
		mux: http.NewServeMux(),
	}
	schedulerCtx, cancel := context.WithCancel(context.Background())
	app.schedulerCancel = cancel
	if err := scheduler.Start(schedulerCtx, time.Duration(settings.SchedulerIntervalMs)*time.Millisecond, app.dispatchScheduledEvent); err != nil {
		_ = state.Close()
		cancel()
		return nil, fmt.Errorf("start scheduler: %w", err)
	}
	if err := queue.RegisterDispatcher("ai.chat", queuedRuntimeJobDispatcher{runtime: runtime, queue: queue}); err != nil {
		_ = state.Close()
		cancel()
		return nil, fmt.Errorf("register ai job dispatcher: %w", err)
	}
	app.routes()
	return app, nil
}

func (a *runtimeApp) Close() error {
	if a.schedulerCancel != nil {
		a.schedulerCancel()
	}
	if a.state != nil {
		return a.state.Close()
	}
	return nil
}

func (a *runtimeApp) routes() {
	a.mux.HandleFunc("/healthz", a.handleHealth)
	a.mux.HandleFunc("/demo/onebot/message", a.handleOneBotMessage)
	a.mux.HandleFunc("/demo/ai/message", a.handleAIMessage)
	a.mux.HandleFunc("/demo/jobs/enqueue", a.handleJobEnqueue)
	a.mux.HandleFunc("/demo/jobs/timeout", a.handleJobTimeout)
	a.mux.HandleFunc("/demo/jobs/", a.handleJobOperator)
	a.mux.HandleFunc("/demo/schedules/echo-delay", a.handleScheduleEchoDelay)
	a.mux.HandleFunc("/demo/schedules/", a.handleScheduleOperator)
	a.mux.HandleFunc("/demo/plugins/", a.handlePluginOperator)
	a.mux.HandleFunc("/demo/replies", a.handleReplies)
	a.mux.HandleFunc("/demo/state/counts", a.handleStateCounts)
	a.mux.Handle("/api/console", http.HandlerFunc(a.handleConsole))
	a.mux.HandleFunc("/metrics", a.handleMetrics)
}

func (a *runtimeApp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mux.ServeHTTP(w, r)
}

func (a *runtimeApp) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":             "ok",
		"environment":        a.config.Runtime.Environment,
		"sqlite_path":        a.settings.SQLitePath,
		"scheduler_running":  a.scheduler != nil && a.scheduler.Running(),
		"scheduler_interval": a.settings.SchedulerIntervalMs,
	})
}

func (a *runtimeApp) handleConsole(w http.ResponseWriter, r *http.Request) {
	console := runtimecore.NewConsoleAPI(a.runtimeRaw, a.queue, a.config, a.logs.Lines(), a.audits)
	console.SetJobReader(runtimecore.NewSQLiteConsoleJobReader(a.state))
	console.SetAlertReader(runtimecore.NewSQLiteConsoleAlertReader(a.state))
	console.SetScheduleReader(runtimecore.NewSQLiteConsoleScheduleReader(a.state))
	console.SetAdapterInstanceReader(runtimecore.NewSQLiteConsoleAdapterInstanceReader(a.state))
	console.SetPluginSnapshotReader(runtimecore.NewSQLiteConsolePluginSnapshotReader(a.state))
	console.SetPluginEnabledStateReader(runtimecore.NewSQLiteConsolePluginEnabledStateReader(a.state))
	console.SetRecoverySource(newRuntimeRecoverySource(a.queue, a.scheduler))
	recovery := a.queue.LastRecoverySnapshot()
	scheduleRecovery := a.scheduler.LastRecoverySnapshot()
	meta := make(map[string]any, len(a.consoleMeta)+6)
	for key, value := range a.consoleMeta {
		meta[key] = value
	}
	meta["scheduler_running"] = a.scheduler != nil && a.scheduler.Running()
	meta["ai_job_dispatcher_registered"] = true
	meta["adapter_read_model"] = "sqlite-adapter-instances"
	meta["adapter_state_persisted"] = true
	meta["adapter_operator_scope"] = "already-registered adapters only"
	meta["adapter_status_model"] = "persisted-registered-instance-status"
	meta["plugin_read_model"] = "runtime-registry+sqlite-plugin-status-snapshot"
	meta["plugin_enabled_state_read_model"] = "runtime-registry+sqlite-plugin-enabled-overlay"
	meta["plugin_enabled_state_persisted"] = true
	meta["plugin_operator_actions"] = []string{"/demo/plugins/{plugin-id}/enable", "/demo/plugins/{plugin-id}/disable"}
	meta["plugin_operator_scope"] = "already-registered plugins only"
	meta["plugin_dispatch_source"] = "sqlite-plugin-status-snapshot+runtime-dispatch-results"
	meta["plugin_status_persisted"] = true
	meta["plugin_status_source"] = "runtime-registry+sqlite-plugin-status-snapshot+runtime-dispatch-results"
	meta["plugin_status_evidence_model"] = "manifest-static-or-last-persisted-plugin-snapshot-with-live-overlay"
	meta["plugin_dispatch_kind_visibility"] = "last-persisted-or-live-dispatch-kind"
	meta["plugin_recovery_visibility"] = "last-dispatch-failed|last-dispatch-succeeded|recovered-after-failure|no-runtime-evidence"
	meta["plugin_status_staleness"] = "static-registration|persisted-snapshot|persisted-snapshot+live-overlay|process-local-volatile"
	meta["plugin_status_staleness_reason"] = "persisted plugin snapshots survive restart while current-process live overlay remains explicitly distinguished from the stored snapshot"
	meta["plugin_runtime_state_live"] = true
	meta["rbac_capability_surface"] = "read-only declaration of current authorization and adjacent dispatch-boundary facts"
	meta["rbac_read_model_scope"] = "current runtime authorizer entrypoints, adjacent dispatch contract/filter boundaries, deny audit taxonomy, and known system gaps"
	meta["rbac_current_state"] = "partial-runtime-local-read-model"
	meta["rbac_system_model_state"] = "not-complete-global-rbac-authn-or-audit-system"
	meta["rbac_current_authorization_paths"] = []string{"admin-command-runtime-authorizer", "event-metadata-runtime-authorizer", "job-metadata-runtime-authorizer", "schedule-metadata-runtime-authorizer", "console-read-authorizer", "schedule-operator-runtime-authorizer"}
	meta["rbac_current_authorization_paths_count"] = 6
	meta["rbac_authorization_boundaries"] = []string{"admin-command-runtime-authorizer", "event-metadata-runtime-authorizer", "job-metadata-runtime-authorizer", "schedule-metadata-runtime-authorizer", "console-read-authorizer", "schedule-operator-runtime-authorizer"}
	meta["rbac_authorizer_entrypoints"] = []string{"admin-command-runtime-authorizer", "event-metadata-runtime-authorizer", "job-metadata-runtime-authorizer", "schedule-metadata-runtime-authorizer", "console-read-authorizer", "schedule-operator-runtime-authorizer"}
	meta["rbac_non_authorizer_runtime_boundaries"] = []string{"dispatch-manifest-permission-gate", "job-target-plugin-filter"}
	meta["rbac_deny_audit_covered_paths"] = []string{"admin-command-runtime-authorizer", "event-metadata-runtime-authorizer", "job-metadata-runtime-authorizer", "schedule-metadata-runtime-authorizer", "console-read-authorizer", "schedule-operator-runtime-authorizer"}
	meta["rbac_deny_audit_taxonomy"] = []string{"permission_denied", "plugin_scope_denied"}
	meta["rbac_deny_audit_scope"] = "authorizer deny paths only"
	meta["rbac_contract_checks"] = []string{"dispatch-manifest-permission-gate"}
	meta["rbac_dispatch_filters"] = []string{"job-target-plugin-filter"}
	meta["rbac_manifest_permission_gate_audited"] = false
	meta["rbac_manifest_permission_gate_boundary"] = "independent dispatch contract check; not part of deny audit taxonomy"
	meta["rbac_job_target_plugin_filter_boundary"] = "dispatch filter only; not an authorizer entrypoint or deny audit taxonomy item"
	meta["rbac_known_system_gaps"] = []string{"persistent-policy-store", "policy-hot-reload", "unified-authentication", "unified-resource-model", "independent-authorization-read-model"}
	meta["rbac_non_goals"] = []string{"console-login-auth", "console-write-authorization", "persistent-policy-store", "new-target-kinds"}
	meta["rbac_job_dispatch_fields"] = []string{"actor", "permission", "target_plugin_id"}
	meta["rbac_console_read_permission"] = a.config.RBAC != nil && a.config.RBAC.ConsoleReadPermission != ""
	meta["rbac_console_read_actor_header"] = runtimecore.ConsoleReadActorHeader
	meta["rbac_console_limitations"] = []string{"console read authorization is optional and only enforced when rbac.console_read_permission is configured", "console read authorization currently reads actor only from the X-Bot-Platform-Actor header", "deny audit taxonomy currently distinguishes only permission_denied and plugin_scope_denied", "manifest permission gate remains a separate dispatch contract check and does not emit deny audit entries", "target_plugin_id remains a dispatch filter, not a global RBAC resource kind", "Q8 currently remains a partial runtime-local closure rather than a complete global RBAC, authn, or audit system"}
	meta["replay_policy"] = runtimecore.ReplayPolicy()
	meta["replay_namespace"] = runtimecore.ReplayPolicy().Namespace
	meta["replay_console_limitations"] = []string{"replay policy declaration is read-only and mirrors existing runtime behavior only", "replay remains limited to single-event explicit replay via admin command; no batch replay or dry-run"}
	meta["secrets_policy"] = runtimecore.SecretPolicy()
	meta["secrets_provider"] = runtimecore.SecretPolicy().Provider
	meta["secrets_runtime_owned_ref_prefix"] = runtimecore.SecretPolicy().RefPrefix
	meta["secrets_console_limitations"] = []string{"secrets policy declaration is read-only and mirrors existing runtime behavior only", "secrets remain limited to env provider and webhook token single-read path; no secret write API, rotation, or console management"}
	meta["rollout_policy"] = runtimecore.RolloutPolicy()
	meta["rollout_record_store"] = runtimecore.RolloutPolicy().RecordStore
	meta["rollout_console_limitations"] = []string{"rollout policy declaration is read-only and mirrors existing runtime behavior only", "rollout remains limited to manual /admin prepare|activate with minimal manifest preflight and activate-time drift re-check; no rollback, staged rollout, or persisted rollout history"}
	meta["log_source"] = "runtime-log-buffer"
	meta["trace_source"] = "runtime-trace-recorder"
	meta["metrics_source"] = "runtime-metrics-registry"
	meta["job_read_model"] = "sqlite"
	meta["job_status_source"] = "sqlite-jobs"
	meta["job_status_persisted"] = true
	meta["job_recovery_source"] = "runtime-startup-restore"
	meta["job_recovery_reason"] = "running jobs are retried or dead-lettered after restart"
	meta["job_recovery_recovered_jobs"] = recovery.RecoveredJobs
	meta["job_recovery_total_jobs"] = recovery.TotalJobs
	meta["schedule_read_model"] = "sqlite"
	meta["schedule_status_source"] = "sqlite-schedule-plans"
	meta["schedule_status_persisted"] = true
	meta["schedule_operator_actions"] = []string{"/demo/schedules/{schedule-id}/cancel"}
	meta["schedule_operator_scope"] = "currently-registered schedules only"
	meta["schedule_recovery_source"] = "runtime-startup-restore"
	meta["schedule_recovery_reason"] = "missing dueAt is recomputed and persisted during startup restore; invalid persisted plans are skipped"
	meta["schedule_recovery_total_schedules"] = scheduleRecovery.TotalSchedules
	meta["schedule_recovery_recovered_schedules"] = scheduleRecovery.RecoveredSchedules
	meta["schedule_recovery_invalid_schedules"] = scheduleRecovery.InvalidSchedules
	meta["mixed_read_model"] = true
	meta["snapshot_atomic"] = false
	meta["verification_endpoints"] = []string{"GET /api/console", "GET /metrics", "GET /demo/state/counts", "GET /demo/replies", "go test ./packages/runtime-core ./apps/runtime -run Replay"}
	jobs, _ := console.Jobs()
	schedules, _ := console.Schedules()
	jobReady := 0
	for _, job := range jobs {
		if job.DispatchReady {
			jobReady++
		}
	}
	scheduleReady := 0
	for _, schedule := range schedules {
		if schedule.DueReady {
			scheduleReady++
		}
	}
	a.metrics.SetJobDispatchReadyCount(jobReady)
	a.metrics.SetScheduleDueReadyCount(scheduleReady)
	meta["generated_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	console.SetMeta(meta)
	console.ServeHTTP(w, r)
}

type runtimeRecoverySource struct {
	queue     *runtimecore.JobQueue
	scheduler *runtimecore.Scheduler
}

func newRuntimeRecoverySource(queue *runtimecore.JobQueue, scheduler *runtimecore.Scheduler) runtimeRecoverySource {
	return runtimeRecoverySource{queue: queue, scheduler: scheduler}
}

func (s runtimeRecoverySource) LastRecoverySnapshot() runtimecore.RecoverySnapshot {
	var snapshot runtimecore.RecoverySnapshot
	if s.queue != nil {
		snapshot = s.queue.LastRecoverySnapshot()
	}
	if s.scheduler != nil {
		scheduleRecovery := s.scheduler.LastRecoverySnapshot()
		if snapshot.RecoveredAt.IsZero() || (!scheduleRecovery.RecoveredAt.IsZero() && scheduleRecovery.RecoveredAt.After(snapshot.RecoveredAt)) {
			snapshot.RecoveredAt = scheduleRecovery.RecoveredAt
		}
		snapshot.TotalSchedules = scheduleRecovery.TotalSchedules
		snapshot.RecoveredSchedules = scheduleRecovery.RecoveredSchedules
		snapshot.InvalidSchedules = scheduleRecovery.InvalidSchedules
		if len(scheduleRecovery.ScheduleKinds) > 0 {
			snapshot.ScheduleKinds = make(map[runtimecore.ScheduleKind]int, len(scheduleRecovery.ScheduleKinds))
			for kind, count := range scheduleRecovery.ScheduleKinds {
				snapshot.ScheduleKinds[kind] = count
			}
		}
	}
	return snapshot
}

func (a *runtimeApp) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = w.Write([]byte(a.metrics.RenderPrometheus()))
}

func (a *runtimeApp) handleOneBotMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload adapteronebot.MessageEventPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	before := a.replies.Count()
	event, err := a.onebotIngress.ConvertMessageEvent(payload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	duplicate, err := a.persistAndDispatchEvent(r.Context(), event)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":           "ok",
		"duplicate":        duplicate,
		"event_id":         event.EventID,
		"trace_id":         event.TraceID,
		"dispatch_results": a.runtime.DispatchResults(),
		"replies":          a.replies.Since(before),
	})
}

func (a *runtimeApp) handleJobEnqueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		ID            string `json:"id"`
		Type          string `json:"type"`
		CorrelationID string `json:"correlation_id"`
		MaxRetries    *int   `json:"max_retries"`
		Prompt        string `json:"prompt"`
		UserID        string `json:"user_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&payload)
	if strings.TrimSpace(payload.ID) == "" {
		payload.ID = fmt.Sprintf("job-demo-%d", time.Now().UTC().UnixNano())
	}
	if strings.TrimSpace(payload.Type) == "" {
		payload.Type = "demo.echo"
	}
	maxRetries := 2
	if payload.MaxRetries != nil {
		maxRetries = *payload.MaxRetries
	}
	job := runtimecore.NewJob(payload.ID, payload.Type, maxRetries, 30*time.Second)
	job.Correlation = payload.CorrelationID
	if strings.TrimSpace(job.Type) == "ai.chat" {
		if err := applyDemoAIJobContract(&job, payload.Prompt, payload.UserID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if err := a.queue.Enqueue(r.Context(), job); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(job)
}

func (a *runtimeApp) handleJobOperator(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	jobID, action, ok := parseJobOperatorPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if action != "retry" {
		http.NotFound(w, r)
		return
	}
	actor := strings.TrimSpace(r.Header.Get(runtimecore.ConsoleReadActorHeader))
	if actor == "" {
		actor = "admin-user"
	}
	retried, err := a.queue.RetryDeadLetter(r.Context(), jobID)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "job not found") {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	if a.audits != nil {
		_ = a.audits.RecordAudit(pluginsdk.AuditEntry{
			Actor:      actor,
			Action:     "retry",
			Target:     jobID,
			Allowed:    true,
			Reason:     "job_dead_letter_retried",
			OccurredAt: time.Now().UTC().Format(time.RFC3339),
		})
	}
	a.queue.DispatchReady(r.Context(), time.Now().UTC())
	current, err := a.queue.Inspect(r.Context(), jobID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":      "ok",
		"job_id":      jobID,
		"action":      action,
		"accepted":    true,
		"retried_job": retried,
		"current_job": current,
	})
}

func (a *runtimeApp) handleAIMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		Prompt string `json:"prompt"`
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	prompt := strings.TrimSpace(payload.Prompt)
	if prompt == "" {
		http.Error(w, "prompt is required", http.StatusBadRequest)
		return
	}
	userID := strings.TrimSpace(payload.UserID)
	if userID == "" {
		userID = "user-1"
	}
	before := a.replies.Count()
	event := aiEvent(prompt, userID)
	duplicate, err := a.persistAndDispatchEvent(r.Context(), event)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jobID := "job-ai-chat-" + event.EventID
	job, jobErr := a.queue.Inspect(r.Context(), jobID)
	response := map[string]any{
		"status":    "ok",
		"duplicate": duplicate,
		"event_id":  event.EventID,
		"trace_id":  event.TraceID,
		"job_id":    jobID,
		"replies":   a.replies.Since(before),
	}
	if jobErr == nil {
		response["job"] = job
	}
	a.queue.DispatchReady(r.Context(), time.Now().UTC())
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func (a *runtimeApp) handleJobTimeout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	job, err := a.queue.Inspect(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if job.Status == runtimecore.JobStatusPending {
		if _, err := a.queue.MarkRunning(r.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	job, err = a.queue.Timeout(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(job)
}

func (a *runtimeApp) handleScheduleEchoDelay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		ID      string `json:"id"`
		DelayMs int    `json:"delay_ms"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(payload.ID) == "" {
		payload.ID = fmt.Sprintf("schedule-demo-%d", time.Now().UTC().UnixNano())
	}
	if payload.DelayMs <= 0 {
		payload.DelayMs = 100
	}
	if strings.TrimSpace(payload.Message) == "" {
		payload.Message = "scheduled hello"
	}
	plan := runtimecore.SchedulePlan{
		ID:        payload.ID,
		Kind:      runtimecore.ScheduleKindDelay,
		Delay:     time.Duration(payload.DelayMs) * time.Millisecond,
		Source:    "runtime-demo-scheduler",
		EventType: "message.received",
		Metadata: map[string]any{
			"message_text": payload.Message,
			"target_id":    "group-42",
			"message_type": "group",
			"group_id":     42,
			"user_id":      10001,
		},
	}
	if err := a.scheduler.Register(plan); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(plan)
}

func (a *runtimeApp) handleScheduleOperator(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	scheduleID, action, ok := parseScheduleOperatorPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	actor := strings.TrimSpace(r.Header.Get(runtimecore.ConsoleReadActorHeader))
	if actor == "" {
		actor = "admin-user"
	}
	permission := scheduleOperatorPermission(action)
	if err := runtimecore.AuthorizeRBACAction(a.config.RBAC, actor, permission, scheduleID); err != nil {
		runtimecore.RecordAuthorizationDeniedAudit(a.audits, actor, permission, scheduleID, err)
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if err := a.scheduler.Cancel(scheduleID); err != nil {
		status := http.StatusInternalServerError
		logMessage := "runtime schedule cancel failed"
		logLevel := "error"
		logFields := map[string]any{
			"actor":       actor,
			"action":      action,
			"schedule_id": scheduleID,
			"reason":      "schedule_cancel_failed",
		}
		if strings.Contains(err.Error(), "schedule not found") {
			status = http.StatusNotFound
			logMessage = "runtime schedule cancel not found"
			logLevel = "warn"
			logFields["reason"] = "schedule_not_found"
		}
		if a.logger != nil {
			_ = a.logger.Log(logLevel, logMessage, runtimecore.LogContext{}, logFields)
		}
		http.Error(w, err.Error(), status)
		return
	}
	if a.audits != nil {
		_ = a.audits.RecordAudit(pluginsdk.AuditEntry{
			Actor:      actor,
			Permission: permission,
			Action:     action,
			Target:     scheduleID,
			Allowed:    true,
			Reason:     "schedule_cancelled",
			OccurredAt: time.Now().UTC().Format(time.RFC3339),
		})
	}
	if a.logger != nil {
		_ = a.logger.Log("info", "runtime schedule cancelled", runtimecore.LogContext{}, map[string]any{
			"actor":       actor,
			"action":      action,
			"schedule_id": scheduleID,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":      "ok",
		"schedule_id": scheduleID,
		"action":      action,
	})
}

func (a *runtimeApp) handleReplies(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(a.replies.Since(0))
}

func (a *runtimeApp) handleStateCounts(w http.ResponseWriter, r *http.Request) {
	counts, err := a.state.Counts(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(counts)
}

func (a *runtimeApp) handlePluginOperator(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if strings.HasSuffix(strings.TrimSpace(r.URL.Path), "/config") {
		a.handlePluginConfigOperator(w, r)
		return
	}
	pluginID, action, ok := parsePluginOperatorPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	actor := strings.TrimSpace(r.Header.Get(runtimecore.ConsoleReadActorHeader))
	if actor == "" {
		actor = "admin-user"
	}
	command := eventmodel.CommandInvocation{
		Name:      "admin",
		Arguments: []string{action, pluginID},
		Metadata:  map[string]any{"actor": actor},
	}
	executionContext := eventmodel.ExecutionContext{
		TraceID: fmt.Sprintf("trace-admin-%d", time.Now().UTC().UnixNano()),
		EventID: fmt.Sprintf("evt-admin-%d", time.Now().UTC().UnixNano()),
	}
	if err := a.runtime.DispatchCommand(r.Context(), command, executionContext); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, context.Canceled) {
			status = http.StatusRequestTimeout
		} else if strings.Contains(err.Error(), "permission denied") || strings.Contains(err.Error(), "plugin scope denied") {
			status = http.StatusForbidden
		}
		http.Error(w, err.Error(), status)
		return
	}
	state, err := a.state.LoadPluginEnabledState(r.Context(), pluginID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":     "ok",
		"plugin_id":  pluginID,
		"action":     action,
		"enabled":    state.Enabled,
		"updated_at": state.UpdatedAt.UTC().Format(time.RFC3339),
	})
}

func (a *runtimeApp) handlePluginConfigOperator(w http.ResponseWriter, r *http.Request) {
	pluginID, ok := parsePluginConfigPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if pluginID != "plugin-echo" {
		http.Error(w, "plugin config operator only supports plugin-echo", http.StatusNotFound)
		return
	}
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("read plugin config: %v", err), http.StatusBadRequest)
		return
	}
	rawConfigJSON, _, typedConfig, err := validateAndDecodeEchoConfig(rawBody)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.state.SavePluginConfig(r.Context(), pluginID, rawConfigJSON); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stored, err := a.state.LoadPluginConfig(r.Context(), pluginID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":      "ok",
		"plugin_id":   pluginID,
		"config":      map[string]any{"prefix": typedConfig.Prefix},
		"updated_at":  stored.UpdatedAt.UTC().Format(time.RFC3339),
		"persisted":   true,
		"config_path": "/demo/plugins/plugin-echo/config",
	})
}

func parsePluginOperatorPath(path string) (pluginID string, action string, ok bool) {
	trimmed := strings.Trim(strings.TrimSpace(path), "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) != 4 || parts[0] != "demo" || parts[1] != "plugins" {
		return "", "", false
	}
	pluginID = strings.TrimSpace(parts[2])
	action = strings.TrimSpace(parts[3])
	if pluginID == "" {
		return "", "", false
	}
	if action != "enable" && action != "disable" {
		return "", "", false
	}
	return pluginID, action, true
}

func parseJobOperatorPath(path string) (jobID string, action string, ok bool) {
	trimmed := strings.Trim(strings.TrimSpace(path), "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) != 4 || parts[0] != "demo" || parts[1] != "jobs" {
		return "", "", false
	}
	jobID = strings.TrimSpace(parts[2])
	action = strings.TrimSpace(parts[3])
	if jobID == "" {
		return "", "", false
	}
	if action != "retry" {
		return "", "", false
	}
	return jobID, action, true
}

func parseScheduleOperatorPath(path string) (scheduleID string, action string, ok bool) {
	trimmed := strings.Trim(strings.TrimSpace(path), "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) != 4 || parts[0] != "demo" || parts[1] != "schedules" {
		return "", "", false
	}
	scheduleID = strings.TrimSpace(parts[2])
	action = strings.TrimSpace(parts[3])
	if scheduleID == "" {
		return "", "", false
	}
	if action != "cancel" {
		return "", "", false
	}
	return scheduleID, action, true
}

func applyDemoAIJobContract(job *runtimecore.Job, prompt string, userID string) error {
	if job == nil {
		return errors.New("job is required")
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return errors.New("prompt is required for ai.chat demo jobs")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		userID = "user-1"
	}
	if strings.TrimSpace(job.TraceID) == "" {
		job.TraceID = fmt.Sprintf("trace-demo-job-%d", time.Now().UTC().UnixNano())
	}
	if strings.TrimSpace(job.EventID) == "" {
		job.EventID = fmt.Sprintf("evt-demo-job-%d", time.Now().UTC().UnixNano())
	}
	if strings.TrimSpace(job.Correlation) == "" {
		job.Correlation = fmt.Sprintf("runtime-ai:%s:%s", userID, prompt)
	}
	job.Payload = map[string]any{
		"prompt": prompt,
		"dispatch": map[string]any{
			"actor":            "runtime-job-runner",
			"permission":       "job:run",
			"target_plugin_id": "plugin-ai-chat",
		},
		"reply_target": "group-42",
		"reply_handle": map[string]any{
			"capability": "onebot.reply",
			"target_id":  "group-42",
			"message_id": "msg-" + job.EventID,
			"metadata": map[string]any{
				"message_type": "group",
				"group_id":     42,
				"user_id":      10001,
			},
		},
		"session_id": "session-" + userID,
	}
	return nil
}

func scheduleOperatorPermission(action string) string {
	switch action {
	case "cancel":
		return scheduleCancelPermission
	default:
		return ""
	}
}

func parsePluginConfigPath(path string) (pluginID string, ok bool) {
	trimmed := strings.Trim(strings.TrimSpace(path), "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) != 4 || parts[0] != "demo" || parts[1] != "plugins" || parts[3] != "config" {
		return "", false
	}
	pluginID = strings.TrimSpace(parts[2])
	if pluginID == "" {
		return "", false
	}
	return pluginID, true
}

func loadPersistedEchoConfig(state *runtimecore.SQLiteStateStore) (pluginecho.Config, map[string]any, error) {
	defaultConfig := pluginecho.Config{Prefix: "echo: "}
	if state == nil {
		return defaultConfig, nil, nil
	}
	stored, err := state.LoadPluginConfig(context.Background(), "plugin-echo")
	if err == sql.ErrNoRows {
		return defaultConfig, nil, nil
	}
	if err != nil {
		return pluginecho.Config{}, nil, err
	}
	_, rawConfig, typedConfig, err := validateAndDecodeEchoConfig(stored.RawConfig)
	if err != nil {
		return pluginecho.Config{}, nil, err
	}
	return typedConfig, rawConfig, nil
}

func validateAndDecodeEchoConfig(raw []byte) (json.RawMessage, map[string]any, pluginecho.Config, error) {
	var rawConfig map[string]any
	if err := json.Unmarshal(raw, &rawConfig); err != nil {
		return nil, nil, pluginecho.Config{}, fmt.Errorf("plugin-echo config must be a JSON object: %w", err)
	}
	if rawConfig == nil {
		return nil, nil, pluginecho.Config{}, errors.New("plugin-echo config must be a JSON object")
	}
	if prefix, exists := rawConfig["prefix"]; exists {
		if _, ok := prefix.(string); !ok {
			return nil, nil, pluginecho.Config{}, errors.New(`plugin-echo config property "prefix" must be a string`)
		}
	}
	encoded, err := json.Marshal(rawConfig)
	if err != nil {
		return nil, nil, pluginecho.Config{}, fmt.Errorf("marshal plugin-echo config: %w", err)
	}
	var typedConfig pluginecho.Config
	if err := json.Unmarshal(encoded, &typedConfig); err != nil {
		return nil, nil, pluginecho.Config{}, fmt.Errorf("decode plugin-echo config: %w", err)
	}
	return encoded, rawConfig, typedConfig, nil
}

func actorRoles(cfg *runtimecore.RBACConfig) map[string][]string {
	if cfg == nil {
		return map[string][]string{"admin-user": {"admin"}}
	}
	return cfg.ActorRoles
}

func policies(cfg *runtimecore.RBACConfig) map[string]pluginadmin.RolePolicy {
	if cfg == nil {
		return map[string]pluginadmin.RolePolicy{
			"admin": {
				Permissions: []string{"plugin:enable", "plugin:disable"},
				PluginScope: []string{"*"},
			},
		}
	}
	result := make(map[string]pluginadmin.RolePolicy, len(cfg.Policies))
	for role, policy := range cfg.Policies {
		result[role] = pluginadmin.RolePolicy(policy)
	}
	return result
}

func (a *runtimeApp) persistAndDispatchEvent(ctx context.Context, event eventmodel.Event) (bool, error) {
	if a.state != nil && event.IdempotencyKey != "" {
		exists, err := a.state.HasIdempotencyKey(ctx, event.IdempotencyKey)
		if err != nil {
			return false, err
		}
		if exists {
			return true, nil
		}
	}
	if err := a.runtime.DispatchEvent(ctx, event); err != nil {
		return false, err
	}
	if a.state != nil {
		if err := a.state.RecordEvent(ctx, event); err != nil {
			return false, err
		}
		if event.IdempotencyKey != "" {
			if err := a.state.SaveIdempotencyKey(ctx, event.IdempotencyKey, event.EventID); err != nil {
				return false, err
			}
		}
	}
	return false, nil
}

func (a *runtimeApp) dispatchScheduledEvent(event eventmodel.Event) error {
	transformed := event
	message, _ := transformed.Metadata["message_text"].(string)
	targetID, _ := transformed.Metadata["target_id"].(string)
	messageType, _ := transformed.Metadata["message_type"].(string)
	if message != "" {
		transformed.Actor = &eventmodel.Actor{ID: "scheduler", Type: "system", DisplayName: "scheduler"}
		transformed.Channel = &eventmodel.Channel{ID: targetID, Type: messageType, Title: targetID}
		transformed.Message = &eventmodel.Message{ID: "msg-" + transformed.EventID, Text: message}
		transformed.Reply = &eventmodel.ReplyHandle{
			Capability: "onebot.reply",
			TargetID:   targetID,
			MessageID:  "msg-" + transformed.EventID,
			Metadata: map[string]any{
				"message_type": messageType,
				"group_id":     transformed.Metadata["group_id"],
				"user_id":      transformed.Metadata["user_id"],
			},
		}
	}
	_, err := a.persistAndDispatchEvent(context.Background(), transformed)
	return err
}

type queuedRuntimeJobDispatcher struct {
	runtime *runtimecore.InMemoryRuntime
	queue   *runtimecore.JobQueue
}

func (d queuedRuntimeJobDispatcher) DispatchQueuedJob(ctx context.Context, job runtimecore.Job) error {
	return d.runtime.DispatchQueuedJob(ctx, d.queue, job)
}

func aiEvent(prompt, userID string) eventmodel.Event {
	now := time.Now().UTC()
	eventID := fmt.Sprintf("evt-ai-%d", now.UnixNano())
	traceID := fmt.Sprintf("trace-ai-%d", now.UnixNano())
	return eventmodel.Event{
		EventID:        eventID,
		TraceID:        traceID,
		Source:         "runtime-ai",
		Type:           "message.received",
		Timestamp:      now,
		IdempotencyKey: fmt.Sprintf("runtime-ai:%s:%s", userID, prompt),
		Actor:          &eventmodel.Actor{ID: userID, Type: "user", DisplayName: userID},
		Channel:        &eventmodel.Channel{ID: "group-42", Type: "group", Title: "group-42"},
		Message:        &eventmodel.Message{ID: "msg-" + eventID, Text: prompt},
		Reply: &eventmodel.ReplyHandle{
			Capability: "onebot.reply",
			TargetID:   "group-42",
			MessageID:  "msg-" + eventID,
			Metadata: map[string]any{
				"message_type": "group",
				"group_id":     42,
				"user_id":      10001,
			},
		},
	}
}

func allowedSources(items ...string) map[string]struct{} {
	allowed := make(map[string]struct{}, len(items))
	for _, item := range items {
		allowed[item] = struct{}{}
	}
	return allowed
}

func stringValue(value any) string {
	if typed, ok := value.(string); ok {
		return typed
	}
	return ""
}

func main() {
	configPath := flag.String("config", "deploy/config.dev.yaml", "path to runtime config")
	flag.Parse()

	app, err := newRuntimeApp(*configPath)
	if err != nil {
		log.Fatalf("start runtime app: %v", err)
	}
	defer func() {
		if err := app.Close(); err != nil {
			log.Printf("close runtime app: %v", err)
		}
	}()

	addr := fmt.Sprintf(":%d", app.config.Runtime.HTTPPort)
	if err := app.logger.Log("info", "runtime app starting", runtimecore.LogContext{}, map[string]any{
		"http_port":    app.config.Runtime.HTTPPort,
		"environment":  app.config.Runtime.Environment,
		"sqlite_path":  app.settings.SQLitePath,
		"console_path": "/api/console",
		"demo_path":    "/demo/onebot/message",
		"metrics_path": "/metrics",
	}); err != nil {
		log.Fatalf("log startup: %v", err)
	}

	if err := http.ListenAndServe(addr, app); err != nil {
		log.Fatalf("listen and serve: %v", err)
	}
}

func loadAppRuntimeSettings(path string) (appRuntimeSettings, error) {
	type rawConfig struct {
		Runtime struct {
			SQLitePath          string `yaml:"sqlite_path"`
			SchedulerIntervalMs int    `yaml:"scheduler_interval_ms"`
		} `yaml:"runtime"`
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return appRuntimeSettings{}, fmt.Errorf("read app runtime config: %w", err)
	}
	var cfg rawConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return appRuntimeSettings{}, fmt.Errorf("unmarshal app runtime config: %w", err)
	}
	settings := appRuntimeSettings{
		SQLitePath:          strings.TrimSpace(cfg.Runtime.SQLitePath),
		SchedulerIntervalMs: cfg.Runtime.SchedulerIntervalMs,
	}
	if value := strings.TrimSpace(os.Getenv("BOT_PLATFORM_RUNTIME_SQLITE_PATH")); value != "" {
		settings.SQLitePath = value
	}
	if value := strings.TrimSpace(os.Getenv("BOT_PLATFORM_RUNTIME_SCHEDULER_INTERVAL_MS")); value != "" {
		if interval, convErr := strconv.Atoi(value); convErr == nil {
			settings.SchedulerIntervalMs = interval
		}
	}
	if settings.SQLitePath == "" {
		settings.SQLitePath = "data/dev/runtime.sqlite"
	}
	if settings.SchedulerIntervalMs <= 0 {
		settings.SchedulerIntervalMs = 100
	}
	return settings, nil
}

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	adapteronebot "github.com/ohmyopencode/bot-platform/adapters/adapter-onebot"
	adapterwebhook "github.com/ohmyopencode/bot-platform/adapters/adapter-webhook"
	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
	runtimecore "github.com/ohmyopencode/bot-platform/packages/runtime-core"
	pluginadmin "github.com/ohmyopencode/bot-platform/plugins/plugin-admin"
	pluginaichat "github.com/ohmyopencode/bot-platform/plugins/plugin-ai-chat"
	pluginecho "github.com/ohmyopencode/bot-platform/plugins/plugin-echo"
	pluginworkflowdemo "github.com/ohmyopencode/bot-platform/plugins/plugin-workflow-demo"
)

type runtimeApp struct {
	config           runtimecore.Config
	settings         appRuntimeSettings
	runtime          *runtimecore.InMemoryRuntime
	runtimeRaw       *runtimecore.InMemoryRuntime
	logger           *runtimecore.Logger
	tracer           *runtimecore.TraceRecorder
	metrics          *runtimecore.MetricsRegistry
	logs             *logBuffer
	replies          *replyBuffer
	onebotIngress    *adapteronebot.IngressConverter
	onebotRoutes     map[string]runtimecore.RuntimeBotInstance
	webhookRoutes    map[string]*adapterwebhook.Adapter
	audits           *runtimecore.InMemoryAuditLog
	auditRecorder    runtimecore.AuditRecorder
	state            *runtimecore.SQLiteStateStore
	runtimeState     runtimecore.RuntimeStateStore
	controlState     runtimeControlStateStore
	smokeStore       runtimeSmokeStore
	replay           *runtimeAppReplayService
	pluginConfigs    appPluginConfigRegistry
	lifecycle        *runtimecore.PluginLifecycleService
	queue            *runtimecore.JobQueue
	scheduler        *runtimecore.Scheduler
	workflowRuntime  *runtimecore.WorkflowRuntime
	schedulerCancel  context.CancelFunc
	authorizer       *runtimecore.CurrentRBACAuthorizerProvider
	identityResolver *runtimecore.OperatorBearerIdentityResolver
	consoleMeta      map[string]any
	mux              *http.ServeMux
}

type appRuntimeSettings struct {
	SQLitePath          string
	SmokeStoreBackend   string
	PostgresDSN         string
	SchedulerIntervalMs int
	BotInstances        []runtimecore.RuntimeBotInstance
	TraceExporter       appTraceExporterSettings
}

type appTraceExporterSettings struct {
	Enabled  bool
	Kind     string
	Endpoint string
}

type runtimeSmokeStore interface {
	RecordEvent(context.Context, eventmodel.Event) error
	SaveIdempotencyKey(context.Context, string, string) error
	HasIdempotencyKey(context.Context, string) (bool, error)
	Counts(context.Context) (map[string]int, error)
	Close() error
}

type runtimeExecutionStateStore = runtimecore.RuntimeStateStore

type runtimeEventDispatcher interface {
	DispatchEvent(context.Context, eventmodel.Event) error
}

type runtimeAuditLogReader interface {
	AuditEntries() []pluginsdk.AuditEntry
}

type runtimeAuditRecorder interface {
	RecordAudit(entry pluginsdk.AuditEntry) error
}

type runtimeAuditEntriesReader interface {
	AuditEntries() []pluginsdk.AuditEntry
}

type sqliteRuntimeAuditRecorder struct {
	store *runtimecore.SQLiteStateStore
}

func (r sqliteRuntimeAuditRecorder) RecordAudit(entry pluginsdk.AuditEntry) error {
	if r.store == nil {
		return nil
	}
	method := reflect.ValueOf(r.store).MethodByName("SaveAudit")
	if !method.IsValid() {
		method = reflect.ValueOf(r.store).MethodByName("RecordAudit")
		if !method.IsValid() {
			return fmt.Errorf("sqlite audit recorder method unavailable")
		}
		results := method.Call([]reflect.Value{reflect.ValueOf(entry)})
		if len(results) == 1 && !results[0].IsNil() {
			return results[0].Interface().(error)
		}
		return nil
	}
	results := method.Call([]reflect.Value{reflect.ValueOf(context.Background()), reflect.ValueOf(entry)})
	if len(results) == 1 && !results[0].IsNil() {
		return results[0].Interface().(error)
	}
	return nil
}

type sqliteRuntimeAuditEntriesReader struct {
	store *runtimecore.SQLiteStateStore
}

func (r sqliteRuntimeAuditEntriesReader) AuditEntries() []pluginsdk.AuditEntry {
	if r.store == nil {
		return nil
	}
	method := reflect.ValueOf(r.store).MethodByName("ListAudits")
	if method.IsValid() {
		results := method.Call([]reflect.Value{reflect.ValueOf(context.Background())})
		if len(results) == 2 {
			if !results[1].IsNil() {
				return nil
			}
			entries, _ := results[0].Interface().([]pluginsdk.AuditEntry)
			return entries
		}
	}
	method = reflect.ValueOf(r.store).MethodByName("AuditEntries")
	if method.IsValid() {
		results := method.Call(nil)
		if len(results) == 1 {
			entries, _ := results[0].Interface().([]pluginsdk.AuditEntry)
			return entries
		}
	}
	return nil
}

type runtimeAuditLog struct {
	recorder runtimeAuditRecorder
	reader   runtimeAuditEntriesReader
}

func (l runtimeAuditLog) RecordAudit(entry pluginsdk.AuditEntry) error {
	if l.recorder == nil {
		return nil
	}
	return l.recorder.RecordAudit(entry)
}

func (l runtimeAuditLog) AuditEntries() []pluginsdk.AuditEntry {
	if l.reader == nil {
		return nil
	}
	return l.reader.AuditEntries()
}

type runtimeServeFunc func(string, http.Handler) error

type sqlitePluginManifestReader struct {
	store *runtimecore.SQLiteStateStore
}

func (r sqlitePluginManifestReader) LoadPluginManifest(pluginID string) (pluginsdk.PluginManifest, error) {
	if r.store == nil {
		return pluginsdk.PluginManifest{}, fmt.Errorf("plugin manifest store is required")
	}
	return r.store.LoadPluginManifest(context.Background(), pluginID)
}

type sqliteStaticCandidateManifestReader struct {
	manifests map[string]pluginsdk.PluginManifest
}

func (r sqliteStaticCandidateManifestReader) LoadPluginManifest(pluginID string) (pluginsdk.PluginManifest, error) {
	manifest, ok := r.manifests[strings.TrimSpace(pluginID)]
	if !ok {
		return pluginsdk.PluginManifest{}, fmt.Errorf("plugin manifest not found")
	}
	return manifest, nil
}

type runtimeConfigCheckResult struct {
	Status              string `json:"status"`
	Mode                string `json:"mode"`
	ConfigPath          string `json:"config_path"`
	Environment         string `json:"environment,omitempty"`
	HTTPPort            int    `json:"http_port,omitempty"`
	SQLitePath          string `json:"sqlite_path,omitempty"`
	SmokeStoreBackend   string `json:"smoke_store_backend,omitempty"`
	SchedulerIntervalMs int    `json:"scheduler_interval_ms,omitempty"`
	HTTPServerStarted   bool   `json:"http_server_started"`
	Error               string `json:"error,omitempty"`
}

type runtimeHealthResponse struct {
	Status              string                  `json:"status"`
	Environment         string                  `json:"environment"`
	SQLitePath          string                  `json:"sqlite_path"`
	SchedulerIntervalMs int                     `json:"scheduler_interval_ms"`
	Components          runtimeHealthComponents `json:"components"`
}

func nextRuntimeCandidateManifest(manifest pluginsdk.PluginManifest) pluginsdk.PluginManifest {
	candidate := manifest
	version := strings.TrimSpace(candidate.Version)
	if version == "" {
		version = "0.0.1-candidate"
	} else {
		version += "-candidate"
	}
	candidate.Version = version
	return candidate
}

type runtimeHealthComponents struct {
	Storage   runtimeHealthStorageComponent   `json:"storage"`
	Scheduler runtimeHealthSchedulerComponent `json:"scheduler"`
}

type runtimeHealthStorageComponent struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type runtimeHealthSchedulerComponent struct {
	Status  string `json:"status"`
	Running bool   `json:"running"`
}

type sqliteRuntimeSmokeStore struct {
	store *runtimecore.SQLiteStateStore
}

func (s sqliteRuntimeSmokeStore) RecordEvent(ctx context.Context, event eventmodel.Event) error {
	return s.store.RecordEvent(ctx, event)
}

func (s sqliteRuntimeSmokeStore) LoadEvent(ctx context.Context, eventID string) (eventmodel.Event, error) {
	return s.store.LoadEvent(ctx, eventID)
}

func (s sqliteRuntimeSmokeStore) SaveIdempotencyKey(ctx context.Context, key string, eventID string) error {
	return s.store.SaveIdempotencyKey(ctx, key, eventID)
}

func (s sqliteRuntimeSmokeStore) HasIdempotencyKey(ctx context.Context, key string) (bool, error) {
	return s.store.HasIdempotencyKey(ctx, key)
}

func (s sqliteRuntimeSmokeStore) Counts(ctx context.Context) (map[string]int, error) {
	return s.store.Counts(ctx)
}

func (s sqliteRuntimeSmokeStore) Close() error {
	return nil
}

type postgresRuntimeSmokeStore struct {
	store *runtimecore.PostgresStore
}

type runtimeAuditRecorderSlice struct {
	recorders []runtimeAuditRecorder
}

func newRuntimeAuditRecorder(recorders ...runtimeAuditRecorder) runtimecore.AuditRecorder {
	filtered := make([]runtimeAuditRecorder, 0, len(recorders))
	for _, recorder := range recorders {
		if recorder != nil {
			filtered = append(filtered, recorder)
		}
	}
	return runtimeAuditRecorderSlice{recorders: filtered}
}

func (m runtimeAuditRecorderSlice) RecordAudit(entry pluginsdk.AuditEntry) error {
	var errs []error
	for _, recorder := range m.recorders {
		if err := recorder.RecordAudit(entry); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func bindOperatorRequestSession(ctx context.Context, store runtimecore.SessionStateStore) error {
	if store == nil {
		return nil
	}
	return runtimecore.BindRequestIdentitySession(ctx, store)
}

func requestSessionID(ctx context.Context) string {
	return runtimecore.RequestIdentityContextFromContext(ctx).SessionID
}

func (a *runtimeApp) operatorAuthConfigured() bool {
	if a == nil {
		return false
	}
	return runtimecore.OperatorAuthConfigured(a.config.OperatorAuth)
}

func (a *runtimeApp) requestActorID(r *http.Request) (string, error) {
	if r == nil {
		return runtimecore.RequestActorID(nil, a != nil && a.operatorAuthConfigured(), "")
	}
	return runtimecore.RequestActorID(r.Context(), a != nil && a.operatorAuthConfigured(), r.Header.Get(runtimecore.ConsoleReadActorHeader))
}

func operatorDeniedReason(err error) string {
	if err == nil {
		return "permission_denied"
	}
	switch strings.TrimSpace(strings.ToLower(err.Error())) {
	case "plugin scope denied":
		return "plugin_scope_denied"
	default:
		return "permission_denied"
	}
}

func (s postgresRuntimeSmokeStore) RecordEvent(ctx context.Context, event eventmodel.Event) error {
	return s.store.SaveEvent(ctx, event)
}

func (s postgresRuntimeSmokeStore) LoadEvent(ctx context.Context, eventID string) (eventmodel.Event, error) {
	return s.store.LoadEvent(ctx, eventID)
}

func (s postgresRuntimeSmokeStore) SaveIdempotencyKey(ctx context.Context, key string, eventID string) error {
	return s.store.SaveIdempotencyKey(ctx, key, eventID)
}

func (s postgresRuntimeSmokeStore) HasIdempotencyKey(ctx context.Context, key string) (bool, error) {
	_, found, err := s.store.FindIdempotencyKey(ctx, key)
	if err != nil {
		return false, err
	}
	return found, nil
}

func (s postgresRuntimeSmokeStore) Counts(ctx context.Context) (map[string]int, error) {
	return s.store.Counts(ctx)
}

func (s postgresRuntimeSmokeStore) Close() error {
	s.store.Close()
	return nil
}

type runtimeAppReplayService struct {
	journal runtimecore.EventJournalReader
	runtime *runtimecore.InMemoryRuntime
	store   runtimeReplayOperationStore
}

func (s *runtimeAppReplayService) ReplayEvent(eventID string) (eventmodel.Event, error) {
	if s == nil {
		return eventmodel.Event{}, fmt.Errorf("replay service is required")
	}
	return runtimecore.NewEventReplayerWithStore(s.journal, s.runtime, s.store).ReplayEvent(context.Background(), eventID)
}

const (
	scheduleCancelPermission = "schedule:cancel"
	jobRetryPermission       = "job:retry"
	pluginConfigPermission   = "plugin:config"
)

type operatorActionResult struct {
	Status   string `json:"status"`
	Action   string `json:"action"`
	Target   string `json:"target"`
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"`
	Error    string `json:"error,omitempty"`
}

type operatorPluginStateResponse struct {
	operatorActionResult
	PluginID  string `json:"plugin_id,omitempty"`
	Enabled   *bool  `json:"enabled,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type operatorPluginConfigResponse struct {
	operatorActionResult
	PluginID   string `json:"plugin_id,omitempty"`
	Config     any    `json:"config,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
	Persisted  *bool  `json:"persisted,omitempty"`
	ConfigPath string `json:"config_path,omitempty"`
}

type operatorJobResponse struct {
	operatorActionResult
	JobID      string            `json:"job_id,omitempty"`
	RetriedJob *runtimecore.Job  `json:"retried_job,omitempty"`
	CurrentJob *runtimecore.Job  `json:"current_job,omitempty"`
}

type operatorScheduleResponse struct {
	operatorActionResult
	ScheduleID string `json:"schedule_id,omitempty"`
}

func operatorActionName(action, permission string) string {
	permission = strings.TrimSpace(permission)
	switch permission {
	case "plugin:enable":
		return "plugin.enable"
	case "plugin:disable":
		return "plugin.disable"
	case pluginConfigPermission:
		return "plugin.config"
	case jobRetryPermission:
		return "job.retry"
	case scheduleCancelPermission:
		return "schedule.cancel"
	}
	switch strings.TrimSpace(action) {
	case "enable":
		return "plugin.enable"
	case "disable":
		return "plugin.disable"
	case "config":
		return "plugin.config"
	case "retry":
		return "job.retry"
	case "cancel":
		return "schedule.cancel"
	default:
		if permission != "" {
			return strings.ReplaceAll(permission, ":", ".")
		}
		return strings.TrimSpace(action)
	}
}

func operatorSuccessResult(action, target, reason string) operatorActionResult {
	return operatorActionResult{
		Status:   "ok",
		Action:   action,
		Target:   target,
		Accepted: true,
		Reason:   strings.TrimSpace(reason),
	}
}

func operatorDeniedResult(action, target string, err error) operatorActionResult {
	result := operatorActionResult{
		Status:   "forbidden",
		Action:   action,
		Target:   target,
		Accepted: false,
		Reason:   operatorDeniedReason(err),
	}
	if err != nil {
		result.Error = err.Error()
	}
	return result
}

func writeJSONResponse(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}

func boolPtr(value bool) *bool {
	return &value
}

func recordOperatorSuccessAudit(recorder runtimecore.AuditRecorder, requestContext context.Context, executionContext eventmodel.ExecutionContext, actor, permission, action, target, reason string) {
	if recorder == nil || strings.TrimSpace(permission) == "" || strings.TrimSpace(target) == "" {
		return
	}
	reason = strings.TrimSpace(reason)
	entry := pluginsdk.AuditEntry{
		Actor:      strings.TrimSpace(actor),
		Permission: strings.TrimSpace(permission),
		Action:     operatorActionName(action, permission),
		Target:     strings.TrimSpace(target),
		Allowed:    true,
		OccurredAt: time.Now().UTC().Format(time.RFC3339),
	}
	if reason != "" {
		entry.Reason = reason
		entry.ErrorCategory = "operator"
		entry.ErrorCode = reason
	}
	runtimecore.ApplyAuditRequestIdentity(&entry, requestContext)
	runtimecore.ApplyAuditExecutionContext(&entry, executionContext)
	_ = recorder.RecordAudit(entry)
}

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

type runtimeIngressSummary struct {
	OneBotPaths  []string
	WebhookPaths []string
}

type runtimeIngressDispatcher struct {
	runtime    runtimeEventDispatcher
	smokeStore runtimeSmokeStore
}

func (d runtimeIngressDispatcher) DispatchEvent(ctx context.Context, event eventmodel.Event) error {
	_, err := persistRuntimeEvent(ctx, d.runtime, d.smokeStore, event)
	return err
}

func demoOneBotAdapterInstance() runtimecore.RuntimeBotInstance {
	return runtimecore.RuntimeBotInstance{
		ID:       "adapter-onebot-demo",
		Adapter:  "onebot",
		Source:   "onebot",
		Platform: "onebot/v11",
		Path:     "/demo/onebot/message",
		DemoPath: "/demo/onebot/message",
	}
}

func configuredBotInstances(settings appRuntimeSettings) []runtimecore.RuntimeBotInstance {
	if len(settings.BotInstances) > 0 {
		return append([]runtimecore.RuntimeBotInstance(nil), settings.BotInstances...)
	}
	return []runtimecore.RuntimeBotInstance{demoOneBotAdapterInstance()}
}

func adapterInstanceState(settings appRuntimeSettings, instance runtimecore.RuntimeBotInstance) (runtimecore.AdapterInstanceState, error) {
	config := map[string]any{
		"mode":        adapterInstanceMode(instance),
		"sqlite_path": settings.SQLitePath,
		"path":        instance.Path,
		"platform":    instance.Platform,
		"source":      instance.Source,
	}
	if instance.DemoPath != "" {
		config["demo_path"] = instance.DemoPath
	}
	if instance.SelfID != 0 {
		config["self_id"] = instance.SelfID
	}
	configPayload, err := json.Marshal(config)
	if err != nil {
		return runtimecore.AdapterInstanceState{}, fmt.Errorf("marshal %s adapter config for %q: %w", instance.Adapter, instance.ID, err)
	}
	return runtimecore.AdapterInstanceState{
		InstanceID: instance.ID,
		Adapter:    instance.Adapter,
		Source:     instance.Source,
		RawConfig:  configPayload,
		Status:     "registered",
		Health:     "ready",
		Online:     true,
	}, nil
}

func adapterInstanceMode(instance runtimecore.RuntimeBotInstance) string {
	switch instance.Adapter {
	case "webhook":
		return "runtime-ingress"
	default:
		return "demo-ingress"
	}
}

func configuredEventIngressSources(settings appRuntimeSettings) []string {
	instances := configuredOneBotInstances(settings)
	if len(instances) == 0 {
		return nil
	}
	sources := make([]string, 0, len(instances))
	for _, instance := range instances {
		sources = append(sources, instance.Source)
	}
	return sources
}

func configuredOneBotInstances(settings appRuntimeSettings) []runtimecore.RuntimeBotInstance {
	instances := configuredBotInstances(settings)
	filtered := make([]runtimecore.RuntimeBotInstance, 0, len(instances))
	for _, instance := range instances {
		if instance.Adapter == "onebot" {
			filtered = append(filtered, instance)
		}
	}
	return filtered
}

func configuredWebhookInstances(settings appRuntimeSettings) []runtimecore.RuntimeBotInstance {
	instances := configuredBotInstances(settings)
	filtered := make([]runtimecore.RuntimeBotInstance, 0, len(instances))
	for _, instance := range instances {
		if instance.Adapter == "webhook" {
			filtered = append(filtered, instance)
		}
	}
	return filtered
}

func buildIngressSummary(settings appRuntimeSettings) runtimeIngressSummary {
	onebotInstances := configuredOneBotInstances(settings)
	webhookInstances := configuredWebhookInstances(settings)
	summary := runtimeIngressSummary{
		OneBotPaths:  make([]string, 0, len(onebotInstances)),
		WebhookPaths: make([]string, 0, len(webhookInstances)),
	}
	for _, instance := range onebotInstances {
		summary.OneBotPaths = append(summary.OneBotPaths, instance.Path)
	}
	for _, instance := range webhookInstances {
		summary.WebhookPaths = append(summary.WebhookPaths, instance.Path)
	}
	return summary
}

func runtimeDemoPaths(settings appRuntimeSettings) []string {
	summary := buildIngressSummary(settings)
	paths := make([]string, 0, len(summary.OneBotPaths)+len(summary.WebhookPaths)+12)
	paths = append(paths, summary.OneBotPaths...)
	paths = append(paths, summary.WebhookPaths...)
	paths = append(paths,
		"/demo/workflows/message",
		"/demo/ai/message",
		"/demo/jobs/enqueue",
		"/demo/jobs/timeout",
		"/demo/jobs/{job-id}/retry",
		"/demo/schedules/echo-delay",
		"/demo/schedules/{schedule-id}/cancel",
		"/demo/plugins/{plugin-id}/disable",
		"/demo/plugins/{plugin-id}/enable",
		"/demo/plugins/{plugin-id}/config",
		"/demo/replies",
		"/demo/state/counts",
	)
	return paths
}

func newRuntimeApp(configPath string) (*runtimeApp, error) {
	return newRuntimeAppWithOptions(configPath, runtimeAppBuildOptions{})
}

func newRuntimeAppWithOutput(configPath string, output io.Writer) (*runtimeApp, error) {
	return newRuntimeAppWithOutputAndOptions(configPath, output, runtimeAppBuildOptions{})
}

func newRuntimeAppWithOptions(configPath string, options runtimeAppBuildOptions) (*runtimeApp, error) {
	return newRuntimeAppWithOutputAndOptions(configPath, os.Stdout, options)
}

func newRuntimeAppWithOutputAndOptions(configPath string, output io.Writer, options runtimeAppBuildOptions) (*runtimeApp, error) {
	if output == nil {
		output = io.Discard
	}
	config, err := runtimecore.LoadConfig(configPath)
	if err != nil {
		return nil, err
	}
	settings := loadAppRuntimeSettings(config)

	logs := &logBuffer{}
	logger := runtimecore.NewLogger(io.MultiWriter(output, logs))
	tracer := runtimecore.NewTraceRecorder()
	traceExporter := options.traceExporter
	if traceExporter == nil {
		traceExporter = buildRuntimeTraceExporter(settings)
	}
	if traceExporter != nil {
		tracer.SetExporter(traceExporter)
	}
	metrics := runtimecore.NewMetricsRegistry()
	replies := newReplyBuffer(logger, tracer)
	inMemoryAudits := runtimecore.NewInMemoryAuditLog()
	queue := runtimecore.NewJobQueue()
	queue.SetObservability(logger, tracer, metrics)
	queue.SetWorkerIdentity(runtimeWorkerID())
	state, err := runtimecore.OpenSQLiteStateStore(settings.SQLitePath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite state store: %w", err)
	}
	runtimeState := runtimeExecutionStateStore(state)
	pluginConfigs := newAppPluginConfigRegistry()
	smokeStore, err := openRuntimeSmokeStore(settings, state)
	if err != nil {
		_ = state.Close()
		return nil, err
	}
	if selectedRuntimeState, err := runtimeSelectedExecutionStateStore(settings, smokeStore, state); err != nil {
		_ = smokeStore.Close()
		_ = state.Close()
		return nil, fmt.Errorf("select execution state store: %w", err)
	} else {
		runtimeState = selectedRuntimeState
	}
	queue.SetStore(runtimeState)
	queue.SetAlertSink(runtimeState)
	if err := queue.Restore(context.Background()); err != nil {
		_ = smokeStore.Close()
		_ = state.Close()
		return nil, fmt.Errorf("restore job queue: %w", err)
	}
	controlState, err := runtimeSelectedControlStateStore(settings, smokeStore, state)
	if err != nil {
		_ = smokeStore.Close()
		_ = state.Close()
		return nil, fmt.Errorf("select control state store: %w", err)
	}
	var authorizerProvider *runtimecore.CurrentRBACAuthorizerProvider
	if config.RBAC != nil {
		if err := runtimecore.PersistConfiguredRBACState(context.Background(), controlState, config.RBAC); err != nil {
			_ = smokeStore.Close()
			_ = state.Close()
			return nil, fmt.Errorf("persist configured rbac state: %w", err)
		}
		authorizerProvider, err = runtimecore.NewCurrentRBACAuthorizerProviderFromStore(context.Background(), controlState)
		if err != nil {
			_ = smokeStore.Close()
			_ = state.Close()
			return nil, fmt.Errorf("load current rbac snapshot: %w", err)
		}
	}
	scheduler := runtimecore.NewScheduler()
	scheduler.SetObservability(logger, tracer, metrics)
	scheduler.SetStore(runtimeState)
	workflowRuntime := runtimecore.NewWorkflowRuntime(runtimeState)
	if metricsAwareWorkflowRuntime, ok := any(workflowRuntime).(interface {
		SetMetrics(*runtimecore.MetricsRegistry)
	}); ok {
		metricsAwareWorkflowRuntime.SetMetrics(metrics)
	}
	if err := workflowRuntime.Restore(context.Background()); err != nil {
		_ = smokeStore.Close()
		_ = state.Close()
		return nil, fmt.Errorf("restore workflow runtime: %w", err)
	}

	pluginHostFactory := options.pluginHostFactory
	if pluginHostFactory == nil {
		eventScopes := map[string]map[string]struct{}{
			"plugin-echo":          allowedSources(append(configuredEventIngressSources(settings), "runtime-demo-scheduler")...),
			"plugin-workflow-demo": allowedSources("runtime-workflow-demo"),
		}
		pluginHostFactory = func(replies *replyBuffer, workflowRuntime *runtimecore.WorkflowRuntime) runtimecore.PluginHost {
			return newRuntimePluginHostWithEventScopes(replies, workflowRuntime, []string{"plugin-echo", "plugin-workflow-demo"}, eventScopes)
		}
	}
	runtime := runtimecore.NewInMemoryRuntime(runtimecore.NoopSupervisor{}, pluginHostFactory(replies, workflowRuntime))
	if host, ok := runtime.PluginHost().(runtimePluginHostObservabilitySetter); ok {
		host.SetObservability(logger, tracer, metrics)
	}
	runtime.SetObservability(logger, tracer, metrics)
	persistedAuditRecorder := newRuntimeAuditRecorder(inMemoryAudits, controlState)
	runtime.SetAuditRecorder(persistedAuditRecorder)
	runtime.SetDispatchRecorder(controlState)
	lifecycle := runtimecore.NewPluginLifecycleService(controlState)
	runtime.SetPluginEnabledStateSource(lifecycle)
	if authorizerProvider != nil {
		runtime.SetCommandAuthorizer(runtimecore.NewAdminCommandAuthorizerFromProvider(authorizerProvider))
		runtime.SetEventAuthorizer(runtimecore.NewMetadataEventAuthorizerFromProvider(authorizerProvider))
		runtime.SetJobAuthorizer(runtimecore.NewMetadataJobAuthorizerFromProvider(authorizerProvider))
		runtime.SetScheduleAuthorizer(runtimecore.NewMetadataScheduleAuthorizerFromProvider(authorizerProvider))
	}
	replayJournal, ok := smokeStore.(runtimecore.EventJournalReader)
	if !ok {
		_ = smokeStore.Close()
		_ = state.Close()
		return nil, fmt.Errorf("selected smoke store backend %q does not implement event journal reader", settings.SmokeStoreBackend)
	}
	replayService := &runtimeAppReplayService{journal: replayJournal, runtime: runtime, store: controlState}
	secretRegistry := runtimecore.NewSecretRegistry(runtimecore.EnvSecretProvider{}, persistedAuditRecorder)
	identityResolver, err := runtimecore.NewOperatorBearerIdentityResolver(context.Background(), secretRegistry, config.OperatorAuth)
	if err != nil {
		_ = smokeStore.Close()
		_ = state.Close()
		return nil, fmt.Errorf("build operator bearer identity resolver: %w", err)
	}
	aiProvider, err := buildAIProvider(context.Background(), config, secretRegistry, logger)
	if err != nil {
		_ = smokeStore.Close()
		_ = state.Close()
		return nil, fmt.Errorf("build ai chat provider: %w", err)
	}

	echoBinding, ok := pluginConfigs.Lookup("plugin-echo")
	if !ok {
		_ = smokeStore.Close()
		_ = state.Close()
		return nil, fmt.Errorf("plugin config binding for %q is required", "plugin-echo")
	}
	echoConfigState, err := loadPersistedPluginConfig(controlState, echoBinding)
	if err != nil {
		_ = smokeStore.Close()
		_ = state.Close()
		return nil, fmt.Errorf("load plugin config for %q: %w", echoBinding.PluginID, err)
	}
	echoConfig, ok := echoConfigState.TypedConfig.(pluginecho.Config)
	if !ok {
		_ = smokeStore.Close()
		_ = state.Close()
		return nil, fmt.Errorf("plugin config binding %q returned %T, want pluginecho.Config", echoBinding.PluginID, echoConfigState.TypedConfig)
	}
	eventIngressSources := configuredEventIngressSources(settings)
	echoPlugin := pluginecho.New(replies, echoConfig)
	echoDefinition := echoPlugin.Definition()
	echoDefinition.InstanceConfig = echoConfigState.InstanceConfig
	if echoDefinition.InstanceConfig == nil && strings.TrimSpace(echoConfig.Prefix) != "" {
		echoDefinition.InstanceConfig = map[string]any{"prefix": echoConfig.Prefix}
	}
	echoDefinition.Handlers.Event = sourceScopedEventHandler{allowed: allowedSources(append(eventIngressSources, "runtime-demo-scheduler")...), inner: echoPlugin}
	if err := runtime.RegisterPlugin(echoDefinition); err != nil {
		_ = smokeStore.Close()
		_ = state.Close()
		return nil, fmt.Errorf("register echo plugin: %w", err)
	}
	if err := controlState.SavePluginManifest(context.Background(), echoDefinition.Manifest); err != nil {
		_ = smokeStore.Close()
		_ = state.Close()
		return nil, fmt.Errorf("save echo plugin manifest: %w", err)
	}
	aiPlugin := pluginaichat.New(queue, aiProvider, controlState, replies)
	aiDefinition := aiPlugin.Definition()
	aiDefinition.Handlers.Event = sourceScopedEventHandler{allowed: allowedSources("runtime-ai"), inner: aiPlugin}
	if err := runtime.RegisterPlugin(aiDefinition); err != nil {
		_ = smokeStore.Close()
		_ = state.Close()
		return nil, fmt.Errorf("register ai plugin: %w", err)
	}
	if err := controlState.SavePluginManifest(context.Background(), aiDefinition.Manifest); err != nil {
		_ = smokeStore.Close()
		_ = state.Close()
		return nil, fmt.Errorf("save ai plugin manifest: %w", err)
	}
	rolloutManager := runtimecore.NewSQLiteRolloutManager(runtimePersistedPluginManifestReader{store: controlState}, sqliteStaticCandidateManifestReader{manifests: map[string]pluginsdk.PluginManifest{
		echoDefinition.Manifest.ID: nextRuntimeCandidateManifest(echoDefinition.Manifest),
		aiDefinition.Manifest.ID:   nextRuntimeCandidateManifest(aiDefinition.Manifest),
		"plugin-admin":             nextRuntimeCandidateManifest(pluginsdk.PluginManifest{ID: "plugin-admin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess}),
	}}, controlState)
	adminPlugin := pluginadmin.New(lifecycle, rolloutManager, replayService, authorizerProvider, actorRoles(config.RBAC), policies(config.RBAC), persistedAuditRecorder)
	adminDefinition := adminPlugin.Definition()
	if err := runtime.RegisterPlugin(adminDefinition); err != nil {
		_ = smokeStore.Close()
		_ = state.Close()
		return nil, fmt.Errorf("register admin plugin: %w", err)
	}
	if err := controlState.SavePluginManifest(context.Background(), adminDefinition.Manifest); err != nil {
		_ = smokeStore.Close()
		_ = state.Close()
		return nil, fmt.Errorf("save admin plugin manifest: %w", err)
	}
	workflowPlugin := pluginworkflowdemo.New(replies, workflowRuntime)
	workflowDefinition := workflowPlugin.Definition()
	workflowDefinition.Handlers.Event = sourceScopedEventHandler{allowed: allowedSources("runtime-workflow-demo"), inner: workflowPlugin}
	if err := runtime.RegisterPlugin(workflowDefinition); err != nil {
		_ = smokeStore.Close()
		_ = state.Close()
		return nil, fmt.Errorf("register workflow plugin: %w", err)
	}
	if err := controlState.SavePluginManifest(context.Background(), workflowDefinition.Manifest); err != nil {
		_ = smokeStore.Close()
		_ = state.Close()
		return nil, fmt.Errorf("save workflow plugin manifest: %w", err)
	}
	botInstances := configuredBotInstances(settings)
	onebotInstances := configuredOneBotInstances(settings)
	webhookInstances := configuredWebhookInstances(settings)
	onebotRoutes := make(map[string]runtimecore.RuntimeBotInstance, len(onebotInstances))
	for _, instance := range onebotInstances {
		onebotRoutes[instance.Path] = instance
	}
	webhookDispatcher := runtimeIngressDispatcher{runtime: runtime, smokeStore: smokeStore}
	webhookRoutes := make(map[string]*adapterwebhook.Adapter, len(webhookInstances))
	for _, instance := range botInstances {
		if err := runtime.RegisterAdapter(runtimecore.AdapterRegistration{
			ID:      instance.ID,
			Source:  instance.Source,
			Adapter: registeredAdapter{id: instance.ID, source: instance.Source},
		}); err != nil {
			_ = smokeStore.Close()
			_ = state.Close()
			return nil, fmt.Errorf("register %s adapter %q: %w", instance.Adapter, instance.ID, err)
		}
		adapterInstanceState, err := adapterInstanceState(settings, instance)
		if err != nil {
			_ = smokeStore.Close()
			_ = state.Close()
			return nil, err
		}
		if err := controlState.SaveAdapterInstance(context.Background(), adapterInstanceState); err != nil {
			_ = smokeStore.Close()
			_ = state.Close()
			return nil, fmt.Errorf("save %s adapter instance %q: %w", instance.Adapter, instance.ID, err)
		}
		if instance.Adapter == "webhook" {
			webhookAdapter, err := adapterwebhook.NewWithSecretRef(config.Secrets.WebhookTokenRef, secretRegistry, webhookDispatcher, logger, persistedAuditRecorder)
			if err != nil {
				_ = smokeStore.Close()
				_ = state.Close()
				return nil, fmt.Errorf("build webhook adapter %q: %w", instance.ID, err)
			}
			webhookAdapter.Tracer = tracer
			webhookAdapter.Source = instance.Source
			webhookAdapter.Platform = instance.Platform
			webhookAdapter.InstanceID = instance.ID
			webhookRoutes[instance.Path] = webhookAdapter
		}
	}

	ingressLogs := io.MultiWriter(output, logs)
	onebotIngress := adapteronebot.NewIngressConverter(ingressLogs)
	onebotIngress.SetObservability(tracer)
	ingressSummary := buildIngressSummary(settings)

	app := &runtimeApp{
		config:           config,
		settings:         settings,
		runtime:          runtime,
		runtimeRaw:       runtime,
		logger:           logger,
		tracer:           tracer,
		metrics:          metrics,
		logs:             logs,
		replies:          replies,
		onebotIngress:    onebotIngress,
		onebotRoutes:     onebotRoutes,
		webhookRoutes:    webhookRoutes,
		audits:           inMemoryAudits,
		auditRecorder:    persistedAuditRecorder,
		state:            state,
		runtimeState:     runtimeState,
		controlState:     controlState,
		smokeStore:       smokeStore,
		replay:           replayService,
		pluginConfigs:    pluginConfigs,
		lifecycle:        lifecycle,
		queue:            queue,
		scheduler:        scheduler,
		workflowRuntime:  workflowRuntime,
		authorizer:       authorizerProvider,
		identityResolver: identityResolver,
		consoleMeta: map[string]any{
			"runtime_entry":           "apps/runtime",
			"demo_paths":              runtimeDemoPaths(settings),
			"sqlite_path":             settings.SQLitePath,
			"smoke_store_backend":     settings.SmokeStoreBackend,
			"smoke_store_debug_scope": "event_journal+idempotency_keys",
			"onebot_ingress_paths":    ingressSummary.OneBotPaths,
			"webhook_ingress_paths":   ingressSummary.WebhookPaths,
			"webhook_runtime_owned":   len(webhookInstances) > 0,
			"scheduler_interval_ms":   settings.SchedulerIntervalMs,
			"ai_job_dispatcher":       "runtime-job-queue",
			"ai_chat_provider":        config.AIChat.Provider,
			"runtime_worker_id":       runtimeWorkerID(),
			"console_mode":            "read+operator-plugin-enable-disable+plugin-config+job-retry+schedule-cancel",
			"trace_exporter_enabled":  tracer.ExporterEnabled(),
			"trace_exporter_kind":     settings.TraceExporter.Kind,
		},
		mux: http.NewServeMux(),
	}
	schedulerCtx, cancel := context.WithCancel(context.Background())
	app.schedulerCancel = cancel
	if err := scheduler.Start(schedulerCtx, time.Duration(settings.SchedulerIntervalMs)*time.Millisecond, app.dispatchScheduledEvent); err != nil {
		_ = smokeStore.Close()
		_ = state.Close()
		cancel()
		return nil, fmt.Errorf("start scheduler: %w", err)
	}
	if err := queue.RegisterDispatcher("ai.chat", queuedRuntimeJobDispatcher{runtime: runtime, queue: queue}); err != nil {
		_ = smokeStore.Close()
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
	var errs []error
	if a.smokeStore != nil {
		if err := a.smokeStore.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if a.state != nil {
		if err := a.state.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if a.runtime != nil {
		if closer, ok := a.runtime.PluginHost().(runtimePluginHostCloser); ok {
			if err := closer.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (a *runtimeApp) reloadCurrentRBACAuthorizer(ctx context.Context) error {
	if a == nil || a.authorizer == nil {
		return nil
	}
	if a.controlState == nil {
		return fmt.Errorf("reload current rbac snapshot: control state store is required")
	}
	if err := a.authorizer.ReloadFromStore(ctx, a.controlState); err != nil {
		return fmt.Errorf("reload current rbac snapshot: %w", err)
	}
	return nil
}

func (a *runtimeApp) consoleReadPermissionConfigured() bool {
	if a == nil {
		return false
	}
	if a.authorizer != nil {
		snapshot := a.authorizer.CurrentSnapshot()
		if snapshot == nil {
			return false
		}
		return strings.TrimSpace(snapshot.ConsoleReadPermission) != ""
	}
	return a.config.RBAC != nil && strings.TrimSpace(a.config.RBAC.ConsoleReadPermission) != ""
}

func (a *runtimeApp) routes() {
	a.mux.HandleFunc("/healthz", a.handleHealth)
	for path := range a.onebotRoutes {
		a.mux.HandleFunc(path, a.handleOneBotMessage)
	}
	for path, adapter := range a.webhookRoutes {
		if adapter == nil {
			continue
		}
		a.mux.Handle(path, http.HandlerFunc(adapter.HandleWebhook))
	}
	a.mux.HandleFunc("/demo/workflows/message", a.handleWorkflowMessage)
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
	if a.identityResolver != nil {
		if identity, ok := a.identityResolver.ResolveAuthorizationHeader(r.Header.Get("Authorization")); ok {
			r = r.WithContext(runtimecore.WithRequestIdentityContext(r.Context(), identity))
		}
	}
	a.mux.ServeHTTP(w, r)
}

func (a *runtimeApp) handleHealth(w http.ResponseWriter, r *http.Request) {
	statusCode, response := a.runtimeHealth(r.Context())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(response)
}

func (a *runtimeApp) runtimeHealth(ctx context.Context) (int, runtimeHealthResponse) {
	schedulerRunning := a.scheduler != nil && a.scheduler.Running()
	response := runtimeHealthResponse{
		Environment:         a.config.Runtime.Environment,
		SQLitePath:          a.settings.SQLitePath,
		SchedulerIntervalMs: a.settings.SchedulerIntervalMs,
		Components: runtimeHealthComponents{
			Scheduler: runtimeHealthSchedulerComponent{
				Running: schedulerRunning,
			},
		},
	}
	if schedulerRunning {
		response.Components.Scheduler.Status = "ok"
	} else {
		response.Components.Scheduler.Status = "degraded"
	}

	if _, err := a.runtimeStateCounts(ctx); err != nil {
		response.Status = "error"
		response.Components.Storage.Status = "error"
		response.Components.Storage.Error = err.Error()
		return http.StatusServiceUnavailable, response
	}

	response.Components.Storage.Status = "ok"
	if schedulerRunning {
		response.Status = "ok"
		return http.StatusOK, response
	}

	response.Status = "degraded"
	return http.StatusOK, response
}

func (a *runtimeApp) handleConsole(w http.ResponseWriter, r *http.Request) {
	if err := bindOperatorRequestSession(r.Context(), a.controlState); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := a.reloadCurrentRBACAuthorizer(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	consoleAudits := runtimeAuditLog{recorder: a.auditRecorder, reader: a.controlState}
	console := runtimecore.NewConsoleAPI(a.runtimeRaw, a.queue, a.config, a.logs.Lines(), consoleAudits)
	console.SetCurrentAuthorizerProvider(a.authorizer)
	console.SetReadAuthorizer(runtimecore.NewRequestIdentityConsoleReadAuthorizer(a.authorizer, a.operatorAuthConfigured()))
	console.SetJobReader(runtimecore.NewSQLiteConsoleJobReader(a.runtimeState))
	console.SetAlertReader(runtimecore.NewSQLiteConsoleAlertReader(a.runtimeState))
	console.SetReplayOperationReader(runtimecore.NewSQLiteConsoleReplayOperationReader(a.controlState, runtimeControlStateSource(a.settings, "replay-operation-records")))
	console.SetRolloutOperationReader(runtimecore.NewSQLiteConsoleRolloutOperationReader(a.controlState, runtimeControlStateSource(a.settings, "rollout-operation-records")))
	console.SetScheduleReader(runtimecore.NewSQLiteConsoleScheduleReader(a.runtimeState))
	console.SetWorkflowReader(runtimecore.NewSQLiteConsoleWorkflowReader(a.runtimeState, runtimeExecutionStateSource(a.settings, "workflow-instances")))
	console.SetAdapterInstanceReader(runtimecore.NewSQLiteConsoleAdapterInstanceReader(a.controlState, runtimeControlStateSource(a.settings, "adapter-instances")))
	console.SetPluginSnapshotReader(runtimecore.NewSQLiteConsolePluginSnapshotReader(a.controlState, runtimeControlStateSource(a.settings, "plugin-status-snapshot")))
	console.SetPluginEnabledStateReader(runtimecore.NewSQLiteConsolePluginEnabledStateReader(a.controlState, runtimeControlStateSource(a.settings, "plugin-enabled-overlay")))
	console.SetPluginConfigReader(runtimecore.NewSQLiteConsolePluginConfigReader(a.controlState, runtimeControlStateSource(a.settings, "plugin-config")))
	console.SetPluginConfigBindings(a.pluginConfigs.ConsoleBindings())
	console.SetRecoverySource(newRuntimeRecoverySource(a.queue, a.scheduler))
	recovery := a.queue.LastRecoverySnapshot()
	scheduleRecovery := a.scheduler.LastRecoverySnapshot()
	meta := make(map[string]any, len(a.consoleMeta)+6)
	for key, value := range a.consoleMeta {
		meta[key] = value
	}
	meta["scheduler_running"] = a.scheduler != nil && a.scheduler.Running()
	meta["ai_job_dispatcher_registered"] = true
	meta["adapter_read_model"] = runtimeControlStateSource(a.settings, "adapter-instances")
	meta["adapter_state_persisted"] = true
	meta["adapter_operator_scope"] = "already-registered adapters only"
	meta["adapter_status_model"] = "persisted-registered-instance-status"
	meta["plugin_read_model"] = "runtime-registry+" + runtimeControlStateSource(a.settings, "plugin-status-snapshot")
	meta["plugin_config_state_read_model"] = "runtime-registry+" + runtimeControlStateSource(a.settings, "plugin-config")
	meta["plugin_config_state_kind"] = pluginConfigStateKindPersistedInput
	meta["plugin_config_state_persisted"] = true
	meta["plugin_config_operator_actions"] = []string{"/demo/plugins/{plugin-id}/config"}
	meta["plugin_config_operator_scope"] = "plugins with app-local persisted config bindings only"
	meta["plugin_enabled_state_read_model"] = "runtime-registry+" + runtimeControlStateSource(a.settings, "plugin-enabled-overlay")
	meta["plugin_enabled_state_persisted"] = true
	meta["plugin_operator_actions"] = []string{"/demo/plugins/{plugin-id}/enable", "/demo/plugins/{plugin-id}/disable"}
	meta["plugin_operator_scope"] = "already-registered plugins only"
	meta["plugin_dispatch_source"] = runtimeControlStateSource(a.settings, "plugin-status-snapshot") + "+runtime-dispatch-results"
	meta["plugin_status_persisted"] = true
	meta["plugin_status_source"] = "runtime-registry+" + runtimeControlStateSource(a.settings, "plugin-status-snapshot") + "+runtime-dispatch-results"
	meta["plugin_status_evidence_model"] = "manifest-static-or-last-persisted-plugin-snapshot-with-live-overlay"
	meta["plugin_dispatch_kind_visibility"] = "last-persisted-or-live-dispatch-kind"
	meta["plugin_recovery_visibility"] = "last-dispatch-failed|last-dispatch-succeeded|recovered-after-failure|no-runtime-evidence"
	meta["plugin_status_staleness"] = "static-registration|persisted-snapshot|persisted-snapshot+live-overlay|process-local-volatile"
	meta["plugin_status_staleness_reason"] = "persisted plugin snapshots survive restart while current-process live overlay remains explicitly distinguished from the stored snapshot"
	meta["plugin_runtime_state_live"] = true
	meta["rbac_capability_surface"] = "read-only declaration of current authorization and adjacent dispatch-boundary facts"
	meta["rbac_read_model_scope"] = "current runtime authorizer entrypoints, adjacent dispatch contract/filter boundaries, deny audit taxonomy, and known system gaps"
	meta["rbac_current_state"] = "persisted-runtime-current-snapshot"
	meta["rbac_system_model_state"] = "not-complete-global-rbac-authn-or-audit-system"
	meta["rbac_current_authorization_paths"] = []string{"admin-command-runtime-authorizer", "event-metadata-runtime-authorizer", "job-metadata-runtime-authorizer", "schedule-metadata-runtime-authorizer", "console-read-authorizer", "job-operator-runtime-authorizer", "schedule-operator-runtime-authorizer", "plugin-config-runtime-authorizer", "plugin-admin-current-authorizer-provider"}
	meta["rbac_current_authorization_paths_count"] = 9
	meta["rbac_authorization_boundaries"] = []string{"admin-command-runtime-authorizer", "event-metadata-runtime-authorizer", "job-metadata-runtime-authorizer", "schedule-metadata-runtime-authorizer", "console-read-authorizer", "job-operator-runtime-authorizer", "schedule-operator-runtime-authorizer", "plugin-config-runtime-authorizer", "plugin-admin-current-authorizer-provider"}
	meta["rbac_authorizer_entrypoints"] = []string{"admin-command-runtime-authorizer", "event-metadata-runtime-authorizer", "job-metadata-runtime-authorizer", "schedule-metadata-runtime-authorizer", "console-read-authorizer", "job-operator-runtime-authorizer", "schedule-operator-runtime-authorizer", "plugin-config-runtime-authorizer", "plugin-admin-current-authorizer-provider"}
	meta["rbac_non_authorizer_runtime_boundaries"] = []string{"dispatch-manifest-permission-gate", "job-target-plugin-filter"}
	meta["rbac_deny_audit_covered_paths"] = []string{"admin-command-runtime-authorizer", "event-metadata-runtime-authorizer", "job-metadata-runtime-authorizer", "schedule-metadata-runtime-authorizer", "console-read-authorizer", "job-operator-runtime-authorizer", "schedule-operator-runtime-authorizer", "plugin-config-runtime-authorizer", "plugin-admin-current-authorizer-provider"}
	meta["rbac_deny_audit_taxonomy"] = []string{"permission_denied", "plugin_scope_denied"}
	meta["rbac_deny_audit_scope"] = "authorizer deny paths only"
	meta["rbac_contract_checks"] = []string{"dispatch-manifest-permission-gate"}
	meta["rbac_dispatch_filters"] = []string{"job-target-plugin-filter"}
	meta["rbac_manifest_permission_gate_audited"] = false
	meta["rbac_manifest_permission_gate_boundary"] = "independent dispatch contract check; not part of deny audit taxonomy"
	meta["rbac_job_target_plugin_filter_boundary"] = "dispatch filter only; not an authorizer entrypoint or deny audit taxonomy item"
	meta["rbac_snapshot_source"] = runtimeRBACSnapshotSource(a.settings)
	meta["rbac_snapshot_persisted"] = true
	meta["rbac_operator_identities_persisted"] = true
	meta["rbac_snapshot_activation"] = "startup-persist-and-pre-authorize-reload-single-current-snapshot"
	meta["rbac_known_system_gaps"] = []string{"unified-authentication", "unified-resource-model", "independent-authorization-read-model"}
	meta["rbac_non_goals"] = []string{"console-login-auth", "console-write-authorization", "new-target-kinds"}
	meta["rbac_job_dispatch_fields"] = []string{"actor", "permission", "target_plugin_id"}
	meta["rbac_console_read_permission"] = a.consoleReadPermissionConfigured()
	meta["rbac_console_read_actor_header"] = runtimecore.ConsoleReadActorHeader
	meta["rbac_console_limitations"] = []string{"console read authorization is optional and only enforced when rbac.console_read_permission is configured", "console read authorization uses bearer-backed request identity when operator_auth.tokens is configured; otherwise it falls back to the X-Bot-Platform-Actor header", "deny audit taxonomy currently distinguishes only permission_denied and plugin_scope_denied", "manifest permission gate remains a separate dispatch contract check and does not emit deny audit entries", "target_plugin_id remains a dispatch filter, not a global RBAC resource kind", "current slice persists and reloads a single runtime snapshot but does not add login/authn UX or a global resource hierarchy"}
	meta["replay_policy"] = runtimecore.ReplayPolicy()
	meta["replay_namespace"] = runtimecore.ReplayPolicy().Namespace
	meta["replay_record_read_model"] = runtimeControlStateSource(a.settings, "replay-operation-records")
	meta["replay_record_persisted"] = true
	meta["replay_console_limitations"] = []string{"replay policy declaration is read-only and mirrors existing runtime behavior only", "replay remains limited to single-event explicit replay via admin command; no batch replay or dry-run"}
	meta["secrets_policy"] = runtimecore.SecretPolicy()
	meta["secrets_provider"] = runtimecore.SecretPolicy().Provider
	meta["secrets_runtime_owned_ref_prefix"] = runtimecore.SecretPolicy().RefPrefix
	meta["secrets_console_limitations"] = []string{"secrets policy declaration is read-only and mirrors existing runtime behavior only", "secrets remain limited to env provider and webhook token single-read path; no secret write API, rotation, or console management"}
	meta["rollout_policy"] = runtimeRolloutPolicy(a.settings)
	meta["rollout_record_store"] = runtimeRolloutRecordStore(a.settings)
	meta["rollout_record_read_model"] = runtimeControlStateSource(a.settings, "rollout-operation-records")
	meta["rollout_record_persisted"] = true
	meta["rollout_console_limitations"] = []string{"rollout policy declaration is read-only and mirrors existing runtime behavior only", "rollout remains limited to manual /admin prepare|activate with minimal manifest preflight and activate-time drift re-check; no rollback or staged rollout"}
	meta["log_source"] = "runtime-log-buffer"
	meta["trace_source"] = runtimeTraceSource(a.tracer, a.settings)
	meta["metrics_source"] = "runtime-metrics-registry"
	meta["job_read_model"] = runtimeExecutionBackend(a.settings)
	meta["job_status_source"] = runtimeExecutionStateSource(a.settings, "jobs")
	meta["job_status_persisted"] = true
	meta["job_worker_model"] = "runtime-local-worker-lease"
	meta["job_worker_identity"] = runtimeWorkerID()
	meta["job_worker_lease_visibility"] = true
	meta["job_recovery_reason_codes"] = []string{"runtime_restart", "worker_abandoned", "timeout", "dispatch_retry", "dispatch_dead", "execution_retry", "execution_dead"}
	meta["job_recovery_source"] = "runtime-startup-restore"
	meta["job_recovery_reason"] = "running jobs are retried or dead-lettered after restart"
	meta["job_recovery_recovered_jobs"] = recovery.RecoveredJobs
	meta["job_recovery_total_jobs"] = recovery.TotalJobs
	meta["job_operator_actions"] = []string{"/demo/jobs/{job-id}/retry"}
	meta["job_operator_scope"] = "dead-letter jobs only"
	meta["schedule_read_model"] = runtimeExecutionBackend(a.settings)
	meta["schedule_status_source"] = runtimeExecutionStateSource(a.settings, "schedule-plans")
	meta["schedule_status_persisted"] = true
	meta["workflow_read_model"] = runtimeExecutionStateSource(a.settings, "workflow-instances")
	meta["workflow_status_source"] = runtimeExecutionStateSource(a.settings, "workflow-instances")
	meta["workflow_status_persisted"] = true
	meta["workflow_runtime_owner"] = "runtime-core"
	meta["schedule_operator_actions"] = []string{"/demo/schedules/{schedule-id}/cancel"}
	meta["schedule_operator_scope"] = "currently-registered schedules only"
	meta["schedule_recovery_source"] = "runtime-startup-restore"
	meta["schedule_recovery_reason"] = "missing dueAt is recomputed and persisted during startup restore; invalid persisted plans are skipped"
	meta["schedule_recovery_total_schedules"] = scheduleRecovery.TotalSchedules
	meta["schedule_recovery_recovered_schedules"] = scheduleRecovery.RecoveredSchedules
	meta["schedule_recovery_invalid_schedules"] = scheduleRecovery.InvalidSchedules
	meta["mixed_read_model"] = true
	meta["snapshot_atomic"] = true
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
	instance, ok := a.onebotRoutes[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}

	var payload adapteronebot.MessageEventPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	before := a.replies.Count()
	event, err := a.onebotIngress.ConvertMessageEventWithConfig(payload, adapteronebot.IngressConfig{
		InstanceID: instance.ID,
		Source:     instance.Source,
		Platform:   instance.Platform,
	})
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
		http.Error(w, "ai.chat jobs must be created through /demo/ai/message", http.StatusBadRequest)
		return
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
	if err := bindOperatorRequestSession(r.Context(), a.controlState); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
	if err := a.reloadCurrentRBACAuthorizer(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	actor, err := a.requestActorID(r)
	if err != nil {
		if errors.Is(err, runtimecore.ErrRequestUnauthorized) {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	permission := jobOperatorPermission(action)
	operatorAction := operatorActionName(action, permission)
	if err := runtimecore.AuthorizeRBACActionWithProvider(a.authorizer, actor, permission, jobID); err != nil {
		executionContext := eventmodel.ExecutionContext{Metadata: map[string]any{"session_id": requestSessionID(r.Context())}}
		runtimecore.RecordAuthorizationDeniedAuditWithContext(a.auditRecorder, executionContext, actor, permission, jobID, err)
		writeJSONResponse(w, http.StatusForbidden, operatorDeniedResult(operatorAction, jobID, err))
		return
	}
	current, err := a.queue.Inspect(r.Context(), jobID)
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "job not found") {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
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
	recordOperatorSuccessAudit(a.auditRecorder, r.Context(), eventmodel.ExecutionContext{
		TraceID:       current.TraceID,
		EventID:       current.EventID,
		RunID:         current.RunID,
		CorrelationID: current.Correlation,
	}, actor, permission, operatorAction, jobID, "job_dead_letter_retried")
	a.queue.DispatchReady(r.Context(), time.Now().UTC())
	current, err = a.queue.Inspect(r.Context(), jobID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, http.StatusOK, operatorJobResponse{
		operatorActionResult: operatorSuccessResult(operatorAction, jobID, "job_dead_letter_retried"),
		JobID:                jobID,
		RetriedJob:           &retried,
		CurrentJob:           &current,
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
	if err := bindOperatorRequestSession(r.Context(), a.controlState); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	scheduleID, action, ok := parseScheduleOperatorPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := a.reloadCurrentRBACAuthorizer(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	actor, err := a.requestActorID(r)
	if err != nil {
		if errors.Is(err, runtimecore.ErrRequestUnauthorized) {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	permission := scheduleOperatorPermission(action)
	operatorAction := operatorActionName(action, permission)
	if err := runtimecore.AuthorizeRBACActionWithProvider(a.authorizer, actor, permission, scheduleID); err != nil {
		executionContext := eventmodel.ExecutionContext{CorrelationID: scheduleID, Metadata: map[string]any{"session_id": requestSessionID(r.Context())}}
		runtimecore.RecordAuthorizationDeniedAuditWithContext(a.auditRecorder, executionContext, actor, permission, scheduleID, err)
		writeJSONResponse(w, http.StatusForbidden, operatorDeniedResult(operatorAction, scheduleID, err))
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
	recordOperatorSuccessAudit(a.auditRecorder, r.Context(), eventmodel.ExecutionContext{CorrelationID: scheduleID}, actor, permission, operatorAction, scheduleID, "schedule_cancelled")
	if a.logger != nil {
		_ = a.logger.Log("info", "runtime schedule cancelled", runtimecore.LogContext{}, map[string]any{
			"actor":       actor,
			"action":      operatorAction,
			"schedule_id": scheduleID,
		})
	}
	writeJSONResponse(w, http.StatusOK, operatorScheduleResponse{
		operatorActionResult: operatorSuccessResult(operatorAction, scheduleID, "schedule_cancelled"),
		ScheduleID:           scheduleID,
	})
}

func (a *runtimeApp) handleReplies(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(a.replies.Since(0))
}

func (a *runtimeApp) handleStateCounts(w http.ResponseWriter, r *http.Request) {
	counts, err := a.runtimeStateCounts(r.Context())
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
	if err := bindOperatorRequestSession(r.Context(), a.controlState); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
	if err := a.reloadCurrentRBACAuthorizer(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	actor, err := a.requestActorID(r)
	if err != nil {
		if errors.Is(err, runtimecore.ErrRequestUnauthorized) {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	command := eventmodel.CommandInvocation{
		Name:      "admin",
		Arguments: []string{action, pluginID},
		Metadata:  map[string]any{"actor": actor},
	}
	permission := pluginadminPermission(action)
	operatorAction := operatorActionName(action, permission)
	if sessionID := requestSessionID(r.Context()); sessionID != "" {
		command.Metadata["session_id"] = sessionID
	}
	executionContext := eventmodel.ExecutionContext{
		TraceID:       fmt.Sprintf("trace-admin-%d", time.Now().UTC().UnixNano()),
		EventID:       fmt.Sprintf("evt-admin-%d", time.Now().UTC().UnixNano()),
		PluginID:      pluginID,
		CorrelationID: pluginID,
		Metadata:      map[string]any{},
	}
	if sessionID := requestSessionID(r.Context()); sessionID != "" {
		executionContext.Metadata["session_id"] = sessionID
	}
	if err := a.runtime.DispatchCommand(r.Context(), command, executionContext); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, context.Canceled) {
			status = http.StatusRequestTimeout
		} else if strings.Contains(err.Error(), "permission denied") || strings.Contains(err.Error(), "plugin scope denied") {
			status = http.StatusForbidden
			writeJSONResponse(w, status, operatorDeniedResult(operatorAction, pluginID, err))
			return
		}
		http.Error(w, err.Error(), status)
		return
	}
	state, err := a.controlState.LoadPluginEnabledState(r.Context(), pluginID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, http.StatusOK, operatorPluginStateResponse{
		operatorActionResult: operatorSuccessResult(operatorAction, pluginID, pluginEnablementReason(state.Enabled)),
		PluginID:             pluginID,
		Enabled:              boolPtr(state.Enabled),
		UpdatedAt:            state.UpdatedAt.UTC().Format(time.RFC3339),
	})
}

func (a *runtimeApp) handlePluginConfigOperator(w http.ResponseWriter, r *http.Request) {
	if err := bindOperatorRequestSession(r.Context(), a.controlState); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	pluginID, ok := parsePluginConfigPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := a.reloadCurrentRBACAuthorizer(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	actor, err := a.requestActorID(r)
	if err != nil {
		if errors.Is(err, runtimecore.ErrRequestUnauthorized) {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	binding, ok := a.pluginConfigs.Lookup(pluginID)
	if !ok {
		http.Error(w, fmt.Sprintf("plugin config operator not found for %q", pluginID), http.StatusNotFound)
		return
	}
	if err := runtimecore.AuthorizeRBACActionWithProvider(a.authorizer, actor, pluginConfigPermission, pluginID); err != nil {
		executionContext := eventmodel.ExecutionContext{PluginID: pluginID, CorrelationID: pluginID, Metadata: map[string]any{"session_id": requestSessionID(r.Context())}}
		runtimecore.RecordAuthorizationDeniedAuditWithContext(a.auditRecorder, executionContext, actor, pluginConfigPermission, pluginID, err)
		writeJSONResponse(w, http.StatusForbidden, operatorDeniedResult(operatorActionName("config", pluginConfigPermission), pluginID, err))
		return
	}
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("read plugin config: %v", err), http.StatusBadRequest)
		return
	}
	decoded, err := binding.Decode(rawBody)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.controlState.SavePluginConfig(r.Context(), pluginID, decoded.RawConfigJSON); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	recordOperatorSuccessAudit(a.auditRecorder, r.Context(), eventmodel.ExecutionContext{PluginID: pluginID, CorrelationID: pluginID}, actor, pluginConfigPermission, operatorActionName("config", pluginConfigPermission), pluginID, "plugin_config_updated")
	stored, err := a.controlState.LoadPluginConfig(r.Context(), pluginID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	persisted := true
	writeJSONResponse(w, http.StatusOK, operatorPluginConfigResponse{
		operatorActionResult: operatorSuccessResult(operatorActionName("config", pluginConfigPermission), pluginID, "plugin_config_updated"),
		PluginID:             pluginID,
		Config:               decoded.ResponseConfig,
		UpdatedAt:            stored.UpdatedAt.UTC().Format(time.RFC3339),
		Persisted:            &persisted,
		ConfigPath:           binding.ConfigPath,
	})
}

func pluginadminPermission(action string) string {
	switch strings.TrimSpace(action) {
	case "enable":
		return "plugin:enable"
	case "disable":
		return "plugin:disable"
	default:
		return ""
	}
}

func pluginEnablementReason(enabled bool) string {
	if enabled {
		return "plugin_enabled"
	}
	return "plugin_disabled"
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

func jobOperatorPermission(action string) string {
	switch action {
	case "retry":
		return jobRetryPermission
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
	return persistRuntimeEvent(ctx, a.runtime, a.smokeStore, event)
}

func persistRuntimeEvent(ctx context.Context, runtime runtimeEventDispatcher, smokeStore runtimeSmokeStore, event eventmodel.Event) (bool, error) {
	if runtime == nil {
		return false, fmt.Errorf("runtime dispatcher is required")
	}
	if smokeStore != nil && event.IdempotencyKey != "" {
		exists, err := smokeStore.HasIdempotencyKey(ctx, event.IdempotencyKey)
		if err != nil {
			return false, err
		}
		if exists {
			return true, nil
		}
	}
	if err := runtime.DispatchEvent(ctx, event); err != nil {
		return false, err
	}
	if smokeStore != nil {
		if err := smokeStore.RecordEvent(ctx, event); err != nil {
			return false, err
		}
		if event.IdempotencyKey != "" {
			if err := smokeStore.SaveIdempotencyKey(ctx, event.IdempotencyKey, event.EventID); err != nil {
				return false, err
			}
		}
	}
	return false, nil
}

func (a *runtimeApp) runtimeStateCounts(ctx context.Context) (map[string]int, error) {
	counts, err := a.state.Counts(ctx)
	if err != nil {
		return nil, err
	}
	if a.runtimeState != nil && a.runtimeState != a.state {
		runtimeCounts, err := a.runtimeState.Counts(ctx)
		if err != nil {
			return nil, fmt.Errorf("load %s execution counts: %w", runtimeExecutionBackend(a.settings), err)
		}
		for _, key := range []string{"jobs", "alerts", "schedule_plans", "workflow_instances"} {
			if value, ok := runtimeCounts[key]; ok {
				counts[key] = value
			}
		}
	}
	if a.smokeStore == nil {
		if a.controlState != nil {
			controlCounts, err := a.controlState.Counts(ctx)
			if err != nil {
				return nil, fmt.Errorf("load %s control counts: %w", runtimeControlBackend(a.settings), err)
			}
			for _, key := range runtimeControlCountKeys() {
				if value, ok := controlCounts[key]; ok {
					counts[key] = value
				}
			}
		}
		return counts, nil
	}
	if _, ok := a.smokeStore.(sqliteRuntimeSmokeStore); ok {
		if a.controlState != nil {
			controlCounts, err := a.controlState.Counts(ctx)
			if err != nil {
				return nil, fmt.Errorf("load %s control counts: %w", runtimeControlBackend(a.settings), err)
			}
			for _, key := range runtimeControlCountKeys() {
				if value, ok := controlCounts[key]; ok {
					counts[key] = value
				}
			}
		}
		return counts, nil
	}
	smokeCounts, err := a.smokeStore.Counts(ctx)
	if err != nil {
		return nil, fmt.Errorf("load %s smoke counts: %w", a.settings.SmokeStoreBackend, err)
	}
	counts["event_journal"] = smokeCounts["event_journal"]
	counts["idempotency_keys"] = smokeCounts["idempotency_keys"]
	if a.controlState != nil {
		controlCounts, err := a.controlState.Counts(ctx)
		if err != nil {
			return nil, fmt.Errorf("load %s control counts: %w", runtimeControlBackend(a.settings), err)
		}
		for _, key := range runtimeControlCountKeys() {
			if value, ok := controlCounts[key]; ok {
				counts[key] = value
			}
		}
	}
	return counts, nil
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
		IdempotencyKey: "runtime-ai:" + eventID,
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

func writeRuntimeConfigCheckResult(w io.Writer, result runtimeConfigCheckResult) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func writeRuntimeConfigCheckFailure(w io.Writer, configPath string, err error) {
	if w == nil {
		return
	}
	if err := writeRuntimeConfigCheckResult(w, runtimeConfigCheckResult{
		Status:            "error",
		Mode:              "check-config",
		ConfigPath:        configPath,
		HTTPServerStarted: false,
		Error:             err.Error(),
	}); err != nil {
		_, _ = fmt.Fprintf(w, "runtime config check failed: %v\n", err)
	}
}

func runRuntimeCLI(args []string, stdout io.Writer, stderr io.Writer, serve runtimeServeFunc) int {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if serve == nil {
		serve = http.ListenAndServe
	}

	flags := flag.NewFlagSet("runtime", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "deploy/config.dev.yaml", "path to runtime config")
	checkConfig := flags.Bool("check-config", false, "validate runtime config and exit before starting the HTTP server")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	bootstrapOutput := stdout
	if *checkConfig {
		bootstrapOutput = io.Discard
	}
	app, err := newRuntimeAppWithOutput(*configPath, bootstrapOutput)
	if err != nil {
		if *checkConfig {
			writeRuntimeConfigCheckFailure(stderr, *configPath, err)
		} else {
			_, _ = fmt.Fprintf(stderr, "start runtime app: %v\n", err)
		}
		return 1
	}

	if *checkConfig {
		closeErr := app.Close()
		if closeErr != nil {
			writeRuntimeConfigCheckFailure(stderr, *configPath, fmt.Errorf("close runtime app: %w", closeErr))
			return 1
		}
		if err := writeRuntimeConfigCheckResult(stdout, runtimeConfigCheckResult{
			Status:              "ok",
			Mode:                "check-config",
			ConfigPath:          *configPath,
			Environment:         app.config.Runtime.Environment,
			HTTPPort:            app.config.Runtime.HTTPPort,
			SQLitePath:          app.settings.SQLitePath,
			SmokeStoreBackend:   app.settings.SmokeStoreBackend,
			SchedulerIntervalMs: app.settings.SchedulerIntervalMs,
			HTTPServerStarted:   false,
		}); err != nil {
			_, _ = fmt.Fprintf(stderr, "write config check result: %v\n", err)
			return 1
		}
		return 0
	}

	addr := fmt.Sprintf(":%d", app.config.Runtime.HTTPPort)
	if err := app.logger.Log("info", "runtime app starting", runtimecore.LogContext{}, map[string]any{
		"http_port":    app.config.Runtime.HTTPPort,
		"environment":  app.config.Runtime.Environment,
		"sqlite_path":  app.settings.SQLitePath,
		"console_path": "/api/console",
		"demo_path":    configuredBotInstances(app.settings)[0].Path,
		"metrics_path": "/metrics",
	}); err != nil {
		closeErr := app.Close()
		if closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close runtime app: %w", closeErr))
		}
		_, _ = fmt.Fprintf(stderr, "log startup: %v\n", err)
		return 1
	}

	serveErr := serve(addr, app)
	closeErr := app.Close()
	if serveErr != nil {
		if closeErr != nil {
			serveErr = errors.Join(serveErr, fmt.Errorf("close runtime app: %w", closeErr))
		}
		_, _ = fmt.Fprintf(stderr, "listen and serve: %v\n", serveErr)
		return 1
	}
	if closeErr != nil {
		_, _ = fmt.Fprintf(stderr, "close runtime app: %v\n", closeErr)
		return 1
	}
	return 0
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-runtime-plugin-echo-subprocess" {
		os.Exit(runRuntimePluginEchoSubprocess(os.Stdout, os.Stderr))
	}
	os.Exit(runRuntimeCLI(os.Args[1:], os.Stdout, os.Stderr, nil))
}

func loadAppRuntimeSettings(cfg runtimecore.Config) appRuntimeSettings {
	settings := appRuntimeSettings{
		SQLitePath:          strings.TrimSpace(cfg.Runtime.SQLitePath),
		SmokeStoreBackend:   strings.ToLower(strings.TrimSpace(cfg.Runtime.SmokeStoreBackend)),
		PostgresDSN:         strings.TrimSpace(cfg.Runtime.PostgresDSN),
		SchedulerIntervalMs: cfg.Runtime.SchedulerIntervalMs,
		BotInstances:        append([]runtimecore.RuntimeBotInstance(nil), cfg.Runtime.BotInstances...),
		TraceExporter: appTraceExporterSettings{
			Enabled:  cfg.Tracing.Exporter.Enabled,
			Kind:     strings.ToLower(strings.TrimSpace(cfg.Tracing.Exporter.Kind)),
			Endpoint: strings.TrimSpace(cfg.Tracing.Exporter.Endpoint),
		},
	}
	if value := strings.TrimSpace(os.Getenv("BOT_PLATFORM_RUNTIME_SQLITE_PATH")); value != "" {
		settings.SQLitePath = value
	}
	if value := strings.TrimSpace(os.Getenv("BOT_PLATFORM_RUNTIME_SMOKE_STORE_BACKEND")); value != "" {
		settings.SmokeStoreBackend = strings.ToLower(value)
	}
	if value := strings.TrimSpace(os.Getenv("BOT_PLATFORM_RUNTIME_POSTGRES_DSN")); value != "" {
		settings.PostgresDSN = value
	}
	if value := strings.TrimSpace(os.Getenv("BOT_PLATFORM_RUNTIME_SCHEDULER_INTERVAL_MS")); value != "" {
		if interval, convErr := strconv.Atoi(value); convErr == nil {
			settings.SchedulerIntervalMs = interval
		}
	}
	if value := strings.TrimSpace(os.Getenv("BOT_PLATFORM_TRACING_EXPORTER_ENABLED")); value != "" {
		if enabled, convErr := strconv.ParseBool(value); convErr == nil {
			settings.TraceExporter.Enabled = enabled
		}
	}
	if value := strings.TrimSpace(os.Getenv("BOT_PLATFORM_TRACING_EXPORTER_KIND")); value != "" {
		settings.TraceExporter.Kind = strings.ToLower(value)
	}
	if value := strings.TrimSpace(os.Getenv("BOT_PLATFORM_TRACING_EXPORTER_ENDPOINT")); value != "" {
		settings.TraceExporter.Endpoint = value
	}
	if settings.SQLitePath == "" {
		settings.SQLitePath = "data/dev/runtime.sqlite"
	}
	if settings.SmokeStoreBackend == "" {
		settings.SmokeStoreBackend = "sqlite"
	}
	if settings.SchedulerIntervalMs <= 0 {
		settings.SchedulerIntervalMs = 100
	}
	if settings.TraceExporter.Kind == "" {
		settings.TraceExporter.Kind = "otlp"
	}
	return settings
}

func buildRuntimeTraceExporter(settings appRuntimeSettings) runtimecore.TraceExporter {
	if !settings.TraceExporter.Enabled {
		return nil
	}
	switch settings.TraceExporter.Kind {
	case "", "otlp", "test", "memory":
		return runtimecore.NewInMemoryTraceExporter()
	default:
		return nil
	}
}

func runtimeTraceSource(tracer *runtimecore.TraceRecorder, settings appRuntimeSettings) string {
	if tracer == nil || !tracer.ExporterEnabled() {
		return "runtime-trace-recorder"
	}
	kind := strings.TrimSpace(settings.TraceExporter.Kind)
	if kind == "" {
		kind = "otlp"
	}
	return "runtime-trace-recorder+" + kind + "-exporter"
}

func runtimeWorkerID() string {
	runtimeWorkerIDOnce.Do(func() {
		runtimeWorkerIDValue = "runtime-local:" + randomRuntimeWorkerSuffix()
	})
	return runtimeWorkerIDValue
}

var (
	runtimeWorkerIDOnce  sync.Once
	runtimeWorkerIDValue string
)

func randomRuntimeWorkerSuffix() string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err == nil {
		return hex.EncodeToString(buffer)
	}
	return fmt.Sprintf("ephemeral-%d", time.Now().UTC().UnixNano())
}

func openRuntimeSmokeStore(settings appRuntimeSettings, sqliteState *runtimecore.SQLiteStateStore) (runtimeSmokeStore, error) {
	switch settings.SmokeStoreBackend {
	case "", "sqlite":
		return sqliteRuntimeSmokeStore{store: sqliteState}, nil
	case "postgres":
		if settings.PostgresDSN == "" {
			return nil, fmt.Errorf("open runtime smoke store: postgres selected but runtime.postgres_dsn is empty")
		}
		store, err := runtimecore.OpenPostgresStore(context.Background(), settings.PostgresDSN)
		if err != nil {
			return nil, fmt.Errorf("open runtime smoke store: %w", err)
		}
		return postgresRuntimeSmokeStore{store: store}, nil
	default:
		return nil, fmt.Errorf("open runtime smoke store: unsupported runtime.smoke_store_backend %q", settings.SmokeStoreBackend)
	}
}

func runtimeSelectedExecutionStateStore(settings appRuntimeSettings, smokeStore runtimeSmokeStore, sqliteState *runtimecore.SQLiteStateStore) (runtimeExecutionStateStore, error) {
	if runtimeExecutionBackend(settings) != "postgres" {
		if sqliteState == nil {
			return nil, fmt.Errorf("sqlite execution state store is required")
		}
		return sqliteState, nil
	}
	postgresStore, ok := smokeStore.(postgresRuntimeSmokeStore)
	if !ok || postgresStore.store == nil {
		return nil, fmt.Errorf("selected execution state store for backend %q is unavailable", strings.TrimSpace(settings.SmokeStoreBackend))
	}
	return postgresStore.store, nil
}

func runtimeExecutionBackend(settings appRuntimeSettings) string {
	if strings.EqualFold(strings.TrimSpace(settings.SmokeStoreBackend), "postgres") {
		return "postgres"
	}
	return "sqlite"
}

func runtimeExecutionStateSource(settings appRuntimeSettings, suffix string) string {
	suffix = strings.TrimSpace(suffix)
	if suffix == "" {
		return runtimeExecutionBackend(settings)
	}
	return runtimeExecutionBackend(settings) + "-" + suffix
}

func buildAIProvider(ctx context.Context, cfg runtimecore.Config, secrets aiProviderSecretResolver, logger *runtimecore.Logger) (pluginaichat.AIProvider, error) {
	switch cfg.AIChat.Provider {
	case "", "mock":
		return aiProviderMock{}, nil
	case "openai_compat":
		if strings.TrimSpace(cfg.Secrets.AIChatAPIKeyRef) == "" {
			return nil, fmt.Errorf("%s is required when ai_chat.provider=openai_compat", runtimecore.AIChatAPIKeySecretConfigRef())
		}
		if secrets == nil {
			return nil, fmt.Errorf("secret resolver is required when ai_chat.provider=openai_compat")
		}
		contract := runtimecore.AIChatAPIKeySecretContract()
		apiKey, err := secrets.Resolve(ctx, cfg.Secrets.AIChatAPIKeyRef, contract.Consumer)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", runtimecore.AIChatAPIKeySecretConfigRef(), err)
		}
		return newAIProviderHTTP(aiProviderHTTPConfig{
			Endpoint:       cfg.AIChat.Endpoint,
			Model:          cfg.AIChat.Model,
			Timeout:        time.Duration(cfg.AIChat.RequestTimeoutMs) * time.Millisecond,
			APIKey:         apiKey,
			Logger:         logger,
			Consumer:       contract.Consumer,
			ProviderKind:   cfg.AIChat.Provider,
			RequestTimeout: cfg.AIChat.RequestTimeoutMs,
		})
	default:
		return nil, fmt.Errorf("unsupported ai_chat.provider %q", cfg.AIChat.Provider)
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
	runtimecore "github.com/ohmyopencode/bot-platform/packages/runtime-core"
)

const runtimePostgresTestDSNEnv = "BOT_PLATFORM_POSTGRES_TEST_DSN"

func writeTestConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return writeTestConfigAt(t, dir)
}

func writeTestConfigAt(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	content := "runtime:\n  environment: test\n  log_level: debug\n  http_port: 18080\n  sqlite_path: " + filepath.ToSlash(filepath.Join(dir, "runtime.sqlite")) + "\n  scheduler_interval_ms: 20\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func writeTestConfigWithTraceExporterAt(t *testing.T, dir string, enabled bool, kind string, endpoint string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	content := "runtime:\n  environment: test\n  log_level: debug\n  http_port: 18080\n  sqlite_path: " + filepath.ToSlash(filepath.Join(dir, "runtime.sqlite")) + "\n  scheduler_interval_ms: 20\n" +
		"tracing:\n" +
		"  exporter:\n" +
		"    enabled: " + strconv.FormatBool(enabled) + "\n" +
		"    kind: " + kind + "\n"
	if strings.TrimSpace(endpoint) != "" {
		content += "    endpoint: \"" + strings.ReplaceAll(endpoint, "\"", "\\\"") + "\"\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write trace exporter config: %v", err)
	}
	return path
}

func TestHelperRuntimePluginEchoSubprocess(t *testing.T) {
	if os.Getenv("GO_WANT_RUNTIME_PLUGIN_ECHO_SUBPROCESS") != "1" {
		return
	}
	os.Exit(runRuntimePluginEchoSubprocess(os.Stdout, os.Stderr))
}

func writeAIProviderConfigAt(t *testing.T, dir string, endpoint string, model string, timeoutMs int, secretRef string) string {
	t.Helper()
	if timeoutMs <= 0 {
		timeoutMs = 200
	}
	path := filepath.Join(dir, "config.yaml")
	content := "runtime:\n" +
		"  environment: test\n" +
		"  log_level: debug\n" +
		"  http_port: 18080\n" +
		"  sqlite_path: " + filepath.ToSlash(filepath.Join(dir, "runtime.sqlite")) + "\n" +
		"  scheduler_interval_ms: 20\n" +
		"ai_chat:\n" +
		"  provider: openai_compat\n" +
		"  endpoint: \"" + strings.ReplaceAll(endpoint, "\"", "\\\"") + "\"\n" +
		"  model: \"" + strings.ReplaceAll(model, "\"", "\\\"") + "\"\n" +
		"  request_timeout_ms: " + strconv.Itoa(timeoutMs) + "\n" +
		"secrets:\n" +
		"  ai_chat_api_key_ref: " + secretRef + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write ai provider config: %v", err)
	}
	return path
}

func waitForAIJobStatus(t *testing.T, app *runtimeApp, jobID string, expected runtimecore.JobStatus) runtimecore.Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		app.queue.DispatchReady(t.Context(), time.Now().UTC())
		stored, err := app.queue.Inspect(t.Context(), jobID)
		if err == nil && stored.Status == expected {
			return stored
		}
		time.Sleep(40 * time.Millisecond)
	}
	stored, err := app.queue.Inspect(t.Context(), jobID)
	if err != nil {
		t.Fatalf("inspect ai job %q: %v", jobID, err)
	}
	t.Fatalf("expected ai job %q to reach status %q, got %+v", jobID, expected, stored)
	return runtimecore.Job{}
}

func writeTestConfigWithPostgresSmokeStoreAt(t *testing.T, dir string, dsn string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	content := "runtime:\n  environment: test\n  log_level: debug\n  http_port: 18080\n  sqlite_path: " + filepath.ToSlash(filepath.Join(dir, "runtime.sqlite")) + "\n  smoke_store_backend: postgres\n  postgres_dsn: \"" + strings.ReplaceAll(dsn, "\"", "\\\"") + "\"\n  scheduler_interval_ms: 20\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func writeTestConfigWithBotInstancesAt(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	content := "runtime:\n" +
		"  environment: test\n" +
		"  log_level: debug\n" +
		"  http_port: 18080\n" +
		"  sqlite_path: " + filepath.ToSlash(filepath.Join(dir, "runtime.sqlite")) + "\n" +
		"  scheduler_interval_ms: 20\n" +
		"  bot_instances:\n" +
		"    - id: adapter-onebot-alpha\n" +
		"      adapter: onebot\n" +
		"      source: onebot-alpha\n" +
		"      platform: onebot/v11\n" +
		"      path: /demo/onebot/message\n" +
		"      demo_path: /demo/onebot/message\n" +
		"      self_id: 10001\n" +
		"    - id: adapter-onebot-beta\n" +
		"      adapter: onebot\n" +
		"      source: onebot-beta\n" +
		"      platform: onebot/v11\n" +
		"      path: /demo/onebot/message-beta\n" +
		"      demo_path: /demo/onebot/message-beta\n" +
		"    - id: adapter-webhook-main\n" +
		"      adapter: webhook\n" +
		"      source: webhook-main\n" +
		"      path: /ingress/webhook/main\n" +
		"secrets:\n" +
		"  webhook_token_ref: BOT_PLATFORM_WEBHOOK_TOKEN\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

type runtimeConsoleResponse struct {
	Plugins []struct {
		ID      string `json:"id"`
		Publish *struct {
			SourceType          string `json:"sourceType"`
			SourceURI           string `json:"sourceUri"`
			RuntimeVersionRange string `json:"runtimeVersionRange"`
		} `json:"publish"`
		ConfigStateKind       string `json:"configStateKind"`
		ConfigSource          string `json:"configSource"`
		ConfigPersisted       bool   `json:"configPersisted"`
		ConfigUpdatedAt       string `json:"configUpdatedAt"`
		EnabledStateSource    string `json:"enabledStateSource"`
		EnabledStatePersisted bool   `json:"enabledStatePersisted"`
		StatusSource          string `json:"statusSource"`
		StatusPersisted       bool   `json:"statusPersisted"`
	} `json:"plugins"`
	Adapters []struct {
		ID             string         `json:"id"`
		Adapter        string         `json:"adapter"`
		Source         string         `json:"source"`
		Config         map[string]any `json:"config"`
		StatusSource   string         `json:"statusSource"`
		ConfigSource   string         `json:"configSource"`
		Status         string         `json:"status"`
		Health         string         `json:"health"`
		Online         bool           `json:"online"`
		StatePersisted bool           `json:"statePersisted"`
	} `json:"adapters"`
	Jobs []struct {
		ID              string `json:"id"`
		Status          string `json:"status"`
		DeadLetter      bool   `json:"deadLetter"`
		WorkerID        string `json:"workerId"`
		ReasonCode      string `json:"reasonCode"`
		LeaseSummary    string `json:"leaseSummary"`
		RecoverySummary string `json:"recoverySummary"`
	} `json:"jobs"`
	Schedules []struct {
		ID string `json:"id"`
	} `json:"schedules"`
	Workflows []struct {
		ID             string         `json:"id"`
		PluginID       string         `json:"pluginId"`
		TraceID        string         `json:"traceId"`
		EventID        string         `json:"eventId"`
		RunID          string         `json:"runId"`
		CorrelationID  string         `json:"correlationId"`
		Status         string         `json:"status"`
		WaitingFor     string         `json:"waitingFor"`
		WaitingForJob  map[string]any `json:"waitingForJob"`
		LastJobResult  map[string]any `json:"lastJobResult"`
		Completed      bool           `json:"completed"`
		Compensated    bool           `json:"compensated"`
		StatusSource   string         `json:"statusSource"`
		StatePersisted bool           `json:"statePersisted"`
		RuntimeOwner   string         `json:"runtimeOwner"`
	} `json:"workflows"`
	Status struct {
		Adapters  int `json:"adapters"`
		Schedules int `json:"schedules"`
	} `json:"status"`
	Meta map[string]any `json:"meta"`
}

type runtimeHealthTestResponse struct {
	Status              string `json:"status"`
	Environment         string `json:"environment"`
	SQLitePath          string `json:"sqlite_path"`
	SchedulerIntervalMs int    `json:"scheduler_interval_ms"`
	Components          struct {
		Storage struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"storage"`
		Scheduler struct {
			Status  string `json:"status"`
			Running bool   `json:"running"`
		} `json:"scheduler"`
	} `json:"components"`
}

type operatorActionEnvelopePayload struct {
	Status   string `json:"status"`
	Action   string `json:"action"`
	Target   string `json:"target"`
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason"`
	Error    string `json:"error"`
}

type operatorPluginResponsePayload struct {
	operatorActionEnvelopePayload
	PluginID  string `json:"plugin_id"`
	Enabled   *bool  `json:"enabled"`
	UpdatedAt string `json:"updated_at"`
}

type operatorPluginConfigResponsePayload struct {
	operatorActionEnvelopePayload
	PluginID   string         `json:"plugin_id"`
	Config     map[string]any `json:"config"`
	UpdatedAt  string         `json:"updated_at"`
	Persisted  *bool          `json:"persisted"`
	ConfigPath string         `json:"config_path"`
}

type operatorJobResponsePayload struct {
	operatorActionEnvelopePayload
	JobID      string          `json:"job_id"`
	RetriedJob runtimecore.Job `json:"retried_job"`
	CurrentJob runtimecore.Job `json:"current_job"`
}

type operatorScheduleResponsePayload struct {
	operatorActionEnvelopePayload
	ScheduleID string `json:"schedule_id"`
}

type runtimeTestTraceExporter = runtimecore.InMemoryTraceExporter

func decodeRuntimeHealthResponse(t *testing.T, raw []byte) runtimeHealthTestResponse {
	t.Helper()
	var payload runtimeHealthTestResponse
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		t.Fatalf("decode runtime health payload: %v\nraw=%s", err, string(raw))
	}
	return payload
}

func readRuntimeHealthResponse(t *testing.T, app *runtimeApp) (int, runtimeHealthTestResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, req)
	return resp.Code, decodeRuntimeHealthResponse(t, resp.Body.Bytes())
}

type testRuntimeSmokeStoreSaveAttempt struct {
	Key     string
	EventID string
}

type testRuntimeSmokeStore struct {
	counts             map[string]int
	duplicateKeys      map[string]bool
	hasIdempotencyErr  error
	recordEventErr     error
	saveIdempotencyErr error
	hasCalls           []string
	recordCalls        []string
	saveCalls          []testRuntimeSmokeStoreSaveAttempt
	closeCalls         int
}

func newTestRuntimeSmokeStore() *testRuntimeSmokeStore {
	return &testRuntimeSmokeStore{
		counts: map[string]int{
			"event_journal":    0,
			"idempotency_keys": 0,
		},
		duplicateKeys: map[string]bool{},
	}
}

func (s *testRuntimeSmokeStore) RecordEvent(_ context.Context, event eventmodel.Event) error {
	s.recordCalls = append(s.recordCalls, event.EventID)
	if s.recordEventErr != nil {
		return s.recordEventErr
	}
	s.counts["event_journal"]++
	return nil
}

func (s *testRuntimeSmokeStore) SaveIdempotencyKey(_ context.Context, key string, eventID string) error {
	s.saveCalls = append(s.saveCalls, testRuntimeSmokeStoreSaveAttempt{Key: key, EventID: eventID})
	if s.saveIdempotencyErr != nil {
		return s.saveIdempotencyErr
	}
	s.counts["idempotency_keys"]++
	s.duplicateKeys[key] = true
	return nil
}

func (s *testRuntimeSmokeStore) HasIdempotencyKey(_ context.Context, key string) (bool, error) {
	s.hasCalls = append(s.hasCalls, key)
	if s.hasIdempotencyErr != nil {
		return false, s.hasIdempotencyErr
	}
	return s.duplicateKeys[key], nil
}

func (s *testRuntimeSmokeStore) Counts(_ context.Context) (map[string]int, error) {
	return map[string]int{
		"event_journal":    s.counts["event_journal"],
		"idempotency_keys": s.counts["idempotency_keys"],
	}, nil
}

func (s *testRuntimeSmokeStore) Close() error {
	s.closeCalls++
	return nil
}

type recordingDirectPluginHost struct {
	mu             sync.Mutex
	direct         runtimecore.DirectPluginHost
	eventPluginIDs []string
}

func (h *recordingDirectPluginHost) DispatchEvent(ctx context.Context, plugin pluginsdk.Plugin, event eventmodel.Event, executionContext eventmodel.ExecutionContext) error {
	h.mu.Lock()
	h.eventPluginIDs = append(h.eventPluginIDs, plugin.Manifest.ID)
	h.mu.Unlock()
	return h.direct.DispatchEvent(ctx, plugin, event, executionContext)
}

func (h *recordingDirectPluginHost) DispatchCommand(ctx context.Context, plugin pluginsdk.Plugin, command eventmodel.CommandInvocation, executionContext eventmodel.ExecutionContext) error {
	return h.direct.DispatchCommand(ctx, plugin, command, executionContext)
}

func (h *recordingDirectPluginHost) DispatchJob(ctx context.Context, plugin pluginsdk.Plugin, job pluginsdk.JobInvocation, executionContext eventmodel.ExecutionContext) error {
	return h.direct.DispatchJob(ctx, plugin, job, executionContext)
}

func (h *recordingDirectPluginHost) DispatchSchedule(ctx context.Context, plugin pluginsdk.Plugin, trigger pluginsdk.ScheduleTrigger, executionContext eventmodel.ExecutionContext) error {
	return h.direct.DispatchSchedule(ctx, plugin, trigger, executionContext)
}

func (h *recordingDirectPluginHost) EventPluginIDs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.eventPluginIDs...)
}

func runtimeDemoOneBotMessageBody(t *testing.T, messageID int64, rawMessage string) string {
	t.Helper()
	encodedRawMessage, err := json.Marshal(rawMessage)
	if err != nil {
		t.Fatalf("marshal raw onebot message: %v", err)
	}
	return `{"post_type":"message","message_type":"group","time":1712034000,"user_id":10001,"group_id":42,"message_id":` + strconv.FormatInt(messageID, 10) + `,"raw_message":` + string(encodedRawMessage) + `,"sender":{"nickname":"alice"}}`
}

func performRuntimeOneBotMessageRequest(t *testing.T, app *runtimeApp, body string) *httptest.ResponseRecorder {
	t.Helper()
	return performRuntimeOneBotMessageRequestAtPath(t, app, "/demo/onebot/message", body)
}

func performRuntimeOneBotMessageRequestAtPath(t *testing.T, app *runtimeApp, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, req)
	return resp
}

func performRuntimeWorkflowMessageRequest(t *testing.T, app *runtimeApp, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/demo/workflows/message", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, req)
	return resp
}

type runtimeWorkflowObservabilityRow struct {
	TraceID       string
	EventID       string
	RunID         string
	CorrelationID string
}

func loadRuntimeWorkflowObservabilityRow(t *testing.T, app *runtimeApp, workflowID string) runtimeWorkflowObservabilityRow {
	t.Helper()
	if app == nil || app.state == nil || app.state.DBForTests() == nil {
		t.Fatal("runtime app sqlite state db is required")
	}
	var row runtimeWorkflowObservabilityRow
	err := app.state.DBForTests().QueryRowContext(t.Context(), `
SELECT trace_id, event_id, run_id, correlation_id
FROM workflow_instances
WHERE workflow_id = ?
`, workflowID).Scan(&row.TraceID, &row.EventID, &row.RunID, &row.CorrelationID)
	if err != nil {
		t.Fatalf("load workflow observability row: %v", err)
	}
	return row
}

func assertRuntimeSmokeCounts(t *testing.T, app *runtimeApp, expectedEventJournal int, expectedIdempotencyKeys int) {
	t.Helper()
	counts, err := app.runtimeStateCounts(t.Context())
	if err != nil {
		t.Fatalf("runtime state counts: %v", err)
	}
	if counts["event_journal"] != expectedEventJournal || counts["idempotency_keys"] != expectedIdempotencyKeys {
		t.Fatalf("expected smoke counts event_journal=%d idempotency_keys=%d, got %+v", expectedEventJournal, expectedIdempotencyKeys, counts)
	}
}

func assertRegisteredAdapterBindings(t *testing.T, runtime *runtimecore.InMemoryRuntime, expected map[string]string) {
	t.Helper()
	if runtime == nil {
		t.Fatal("runtime is required")
	}
	registered := runtime.RegisteredAdapters()
	if len(registered) != len(expected) {
		t.Fatalf("expected %d registered adapters, got %d: %+v", len(expected), len(registered), registered)
	}
	actual := make(map[string]string, len(registered))
	for _, reg := range registered {
		if reg.Adapter == nil {
			t.Fatalf("expected adapter %q to keep a bound adapter instance, got nil", reg.ID)
		}
		if _, exists := actual[reg.ID]; exists {
			t.Fatalf("expected registered adapters to have unique ids, got duplicate %q in %+v", reg.ID, registered)
		}
		actual[reg.ID] = reg.Source
		if got := reg.Adapter.ID(); got != reg.ID {
			t.Fatalf("expected registered adapter %q to keep bound adapter id %q, got %q", reg.ID, reg.ID, got)
		}
		if got := reg.Adapter.Source(); got != reg.Source {
			t.Fatalf("expected registered adapter %q to keep bound adapter source %q, got %q", reg.ID, reg.Source, got)
		}
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("expected registered adapter bindings %+v, got %+v", expected, actual)
	}
}

func runtimePostgresTestDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(runtimePostgresTestDSNEnv))
	if dsn == "" {
		t.Skipf("set %s to run runtime Postgres smoke test", runtimePostgresTestDSNEnv)
	}
	return dsn
}

func newRuntimePostgresSmokeStoreApp(t *testing.T) *runtimeApp {
	t.Helper()
	app, err := newRuntimeApp(writeTestConfigWithPostgresSmokeStoreAt(t, t.TempDir(), runtimePostgresTestDSN(t)))
	if err != nil {
		t.Fatalf("new runtime app with postgres smoke store: %v", err)
	}
	return app
}

func waitForSchedulerRunningState(t *testing.T, app *runtimeApp, expected bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		running := app.scheduler != nil && app.scheduler.Running()
		if running == expected {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("scheduler running state did not reach %t", expected)
}

func readRuntimeConsoleResponse(t *testing.T, app *runtimeApp) runtimeConsoleResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected console 200, got %d: %s", resp.Code, resp.Body.String())
	}
	var payload runtimeConsoleResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode console payload: %v", err)
	}
	return payload
}

func readRuntimeConsoleResponseAsViewer(t *testing.T, app *runtimeApp, path string) runtimeConsoleResponse {
	t.Helper()
	req := consoleRequestWithViewer(path)
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected console 200, got %d: %s", resp.Code, resp.Body.String())
	}
	var payload runtimeConsoleResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode console payload: %v", err)
	}
	return payload
}

func exportedTraceSpans(t *testing.T, exporter *runtimeTestTraceExporter, traceID string) []runtimecore.ExportedSpan {
	t.Helper()
	if exporter == nil {
		return nil
	}
	return exporter.SpansByTrace(traceID)
}

func runtimeRoutedPluginHost(t *testing.T, app *runtimeApp) routedPluginHost {
	t.Helper()
	host, ok := app.runtime.PluginHost().(routedPluginHost)
	if !ok {
		t.Fatalf("expected routed plugin host, got %T", app.runtime.PluginHost())
	}
	return host
}

func runtimeSubprocessHost(t *testing.T, host routedPluginHost) *runtimecore.SubprocessPluginHost {
	t.Helper()
	subprocessHost, ok := host.subprocess.(*runtimecore.SubprocessPluginHost)
	if !ok {
		t.Fatalf("expected subprocess host route, got %T", host.subprocess)
	}
	return subprocessHost
}

func runtimeSubprocessHostForPlugin(t *testing.T, host routedPluginHost, pluginID string) *runtimecore.SubprocessPluginHost {
	t.Helper()
	subprocessHost, ok := host.hostForPlugin(pluginID).(*runtimecore.SubprocessPluginHost)
	if !ok {
		t.Fatalf("expected subprocess host route for %s, got %T", pluginID, host.hostForPlugin(pluginID))
	}
	return subprocessHost
}

func waitForRuntimeSubprocessCapture(t *testing.T, host *runtimecore.SubprocessPluginHost, predicate func(stdout string, stderr string) bool) (string, string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stdout := strings.Join(host.StdoutLines(), "\n")
		stderr := strings.Join(host.StderrLines(), "\n")
		if predicate(stdout, stderr) {
			return stdout, stderr
		}
		time.Sleep(20 * time.Millisecond)
	}
	stdout := strings.Join(host.StdoutLines(), "\n")
	stderr := strings.Join(host.StderrLines(), "\n")
	return stdout, stderr
}

func TestRuntimeAppDefaultPluginHostRoutingRoutesPluginEchoThroughSubprocess(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	host := runtimeRoutedPluginHost(t, app)
	if _, routed := host.routes["plugin-echo"]; !routed {
		t.Fatalf("expected plugin-echo to use subprocess routing by default, routes=%+v", host.routes)
	}
	if _, ok := host.defaultHost.(runtimecore.DirectPluginHost); !ok {
		t.Fatalf("expected default host to be direct, got %T", host.defaultHost)
	}
	if _, ok := host.hostForPlugin("plugin-echo").(*runtimecore.SubprocessPluginHost); !ok {
		t.Fatalf("expected plugin-echo to resolve to subprocess host, got %T", host.hostForPlugin("plugin-echo"))
	}

	resp := performRuntimeOneBotMessageRequest(t, app, runtimeDemoOneBotMessageBody(t, 9401, "hello default routing"))
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}
	var payload struct {
		Status  string        `json:"status"`
		Replies []replyRecord `json:"replies"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Status != "ok" || len(payload.Replies) != 1 || payload.Replies[0].Payload != "echo: hello default routing" {
		t.Fatalf("unexpected subprocess-routing payload %+v", payload)
	}
	if app.replies.Count() != 1 {
		t.Fatalf("expected one recorded reply, got %+v", app.replies.Since(0))
	}
	subprocessHost := runtimeSubprocessHost(t, host)
	stdout, stderr := waitForRuntimeSubprocessCapture(t, subprocessHost, func(stdout string, stderr string) bool {
		return strings.Contains(stderr, "runtime-plugin-echo-subprocess-online") && strings.Contains(stdout, `"callback":"reply_text"`)
	})
	if !strings.Contains(stderr, "runtime-plugin-echo-subprocess-online") {
		t.Fatalf("expected default routing to boot hidden subprocess entry, stderr=%s stdout=%s", stderr, stdout)
	}
	if !strings.Contains(stdout, `"callback":"reply_text"`) {
		t.Fatalf("expected default routing to reply through subprocess callback bridge, stderr=%s stdout=%s", stderr, stdout)
	}
}

func TestRuntimeAppTracingExporterDisabledKeepsLocalTraceRecorderMetadata(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	resp := performRuntimeOneBotMessageRequest(t, app, runtimeDemoOneBotMessageBody(t, 9402, "trace exporter disabled"))
	if resp.Code != http.StatusOK {
		t.Fatalf("expected onebot request 200, got %d: %s", resp.Code, resp.Body.String())
	}
	var payload struct {
		TraceID string `json:"trace_id"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode onebot response: %v", err)
	}
	if payload.TraceID == "" {
		t.Fatalf("expected trace_id in response, got %s", resp.Body.String())
	}
	if rendered := app.tracer.RenderTrace(payload.TraceID); !strings.Contains(rendered, "reply.send") && !strings.Contains(rendered, "runtime.dispatch") {
		t.Fatalf("expected local trace recorder to behave as before, got %s", rendered)
	}
	console := readRuntimeConsoleResponse(t, app)
	if got := consoleMetaString(t, console.Meta, "trace_source"); got != "runtime-trace-recorder" {
		t.Fatalf("expected default trace_source to remain runtime-trace-recorder, got %q", got)
	}
	if enabled, ok := console.Meta["trace_exporter_enabled"].(bool); !ok || enabled {
		t.Fatalf("expected trace_exporter_enabled=false, got %#v", console.Meta["trace_exporter_enabled"])
	}
	if got := consoleMetaString(t, console.Meta, "trace_exporter_kind"); got != "otlp" {
		t.Fatalf("expected default trace_exporter_kind=otlp, got %q", got)
	}
}

func TestRuntimeAppTracingExporterTestSinkReceivesCanonicalSpans(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	exporter := runtimecore.NewInMemoryTraceExporter()
	app, err := newRuntimeAppWithOptions(writeTestConfigWithTraceExporterAt(t, dir, true, "test", "memory://wave2a"), runtimeAppBuildOptions{
		traceExporter: exporter,
	})
	if err != nil {
		t.Fatalf("new runtime app with trace exporter: %v", err)
	}
	defer func() { _ = app.Close() }()

	resp := performRuntimeOneBotMessageRequest(t, app, runtimeDemoOneBotMessageBody(t, 9403, "trace exporter enabled"))
	if resp.Code != http.StatusOK {
		t.Fatalf("expected onebot request 200, got %d: %s", resp.Code, resp.Body.String())
	}
	var payload struct {
		TraceID string `json:"trace_id"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode onebot response: %v", err)
	}
	spans := exportedTraceSpans(t, exporter, payload.TraceID)
	if len(spans) == 0 {
		t.Fatal("expected exporter test sink to receive spans")
	}
	var sawRuntimeDispatch bool
	var sawPluginDispatch bool
	for _, span := range spans {
		if span.SpanID == "" || span.TraceID != payload.TraceID {
			t.Fatalf("expected exported span to carry canonical ids, got %+v", span)
		}
		switch span.SpanName {
		case "runtime.event.dispatch":
			sawRuntimeDispatch = true
		case "plugin.dispatch":
			sawPluginDispatch = true
		}
	}
	if !sawRuntimeDispatch || !sawPluginDispatch {
		t.Fatalf("expected exported canonical spans runtime.event.dispatch and plugin.dispatch, got %+v", spans)
	}
	console := readRuntimeConsoleResponse(t, app)
	if got := consoleMetaString(t, console.Meta, "trace_source"); got != "runtime-trace-recorder+test-exporter" {
		t.Fatalf("expected trace_source to advertise dual path, got %q", got)
	}
	if enabled, ok := console.Meta["trace_exporter_enabled"].(bool); !ok || !enabled {
		t.Fatalf("expected trace_exporter_enabled=true, got %#v", console.Meta["trace_exporter_enabled"])
	}
	if got := consoleMetaString(t, console.Meta, "trace_exporter_kind"); got != "test" {
		t.Fatalf("expected trace_exporter_kind=test, got %q", got)
	}
	if rendered := app.tracer.RenderTrace(payload.TraceID); !strings.Contains(rendered, "runtime.dispatch") {
		t.Fatalf("expected local trace recorder path to remain available, got %s", rendered)
	}
}

func TestRuntimeAppDefaultPluginHostRoutingRoutesOfficialS4PluginsThroughSubprocess(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	host := runtimeRoutedPluginHost(t, app)
	if len(host.routes) != 4 {
		t.Fatalf("expected exactly four official plugins to use subprocess routing in Active/1 S4, routes=%+v", host.routes)
	}
	for _, pluginID := range []string{"plugin-echo", "plugin-workflow-demo", "plugin-admin", "plugin-ai-chat"} {
		if _, routed := host.routes[pluginID]; !routed {
			t.Fatalf("expected %s to use subprocess routing by default in Active/1 S4, routes=%+v", pluginID, host.routes)
		}
		if _, ok := host.hostForPlugin(pluginID).(*runtimecore.SubprocessPluginHost); !ok {
			t.Fatalf("expected %s to resolve to subprocess host, got %T", pluginID, host.hostForPlugin(pluginID))
		}
	}

	workflowResp := performRuntimeWorkflowMessageRequest(t, app, `{"actor_id":"user-wave4","message":"start workflow"}`)
	if workflowResp.Code != http.StatusOK {
		t.Fatalf("expected workflow start 200, got %d: %s", workflowResp.Code, workflowResp.Body.String())
	}
	if !strings.Contains(workflowResp.Body.String(), `workflow started, child job queued; wait for its result, then send another message to continue`) {
		t.Fatalf("expected workflow start reply through subprocess path, got %s", workflowResp.Body.String())
	}
	stored, err := app.state.LoadWorkflowInstance(t.Context(), "workflow-user-wave4")
	if err != nil {
		t.Fatalf("load subprocess-routed workflow instance: %v", err)
	}
	if stored.PluginID != "plugin-workflow-demo" || stored.Status != runtimecore.WorkflowRuntimeStatusWaitingJob || stored.Workflow.State["greeting"] != "start workflow" {
		t.Fatalf("expected subprocess-routed workflow instance persisted, got %+v", stored)
	}
	if stored.Workflow.WaitingForJob == nil {
		t.Fatalf("expected subprocess-routed workflow to wait for child job, got %+v", stored)
	}
	subprocessHost := runtimeSubprocessHost(t, host)
	stdout, stderr := waitForRuntimeSubprocessCapture(t, subprocessHost, func(stdout string, stderr string) bool {
		return strings.Contains(stderr, "runtime-plugin-echo-subprocess-online") && strings.Contains(stdout, `"callback":"workflow_start_or_resume"`) && strings.Contains(stdout, `"callback":"reply_text"`)
	})
	if !strings.Contains(stderr, "runtime-plugin-echo-subprocess-online") {
		t.Fatalf("expected workflow subprocess routing to boot hidden subprocess entry, stderr=%s stdout=%s", stderr, stdout)
	}
	if !strings.Contains(stdout, `"callback":"workflow_start_or_resume"`) {
		t.Fatalf("expected workflow subprocess routing to use workflow callback bridge, stderr=%s stdout=%s", stderr, stdout)
	}
	if !strings.Contains(stdout, `"callback":"reply_text"`) {
		t.Fatalf("expected workflow subprocess routing to use reply callback bridge, stderr=%s stdout=%s", stderr, stdout)
	}
}

func TestRuntimeAppDefaultPluginHostRoutingCrashRecoversWorkflowSubprocessAndExposesFailureEvidence(t *testing.T) {
	dir := t.TempDir()
	configPath := writeTestConfigAt(t, dir)
	markerPath := filepath.Join(dir, "workflow-subprocess-crash-once.marker")

	var output bytes.Buffer
	app, err := newRuntimeAppWithOutputAndOptions(configPath, &output, runtimeAppBuildOptions{
		pluginHostFactory: func(replies *replyBuffer, workflowRuntime *runtimecore.WorkflowRuntime) runtimecore.PluginHost {
			launcherConfig := runtimeDefaultSubprocessLauncherConfig()
			direct := runtimecore.DirectPluginHost{}
			subprocess := runtimecore.NewSubprocessPluginHostWithErrorFactory(func(ctx context.Context) (*exec.Cmd, error) {
				cmd, err := runtimePluginProcessFactory(launcherConfig)(ctx)
				if err != nil {
					return nil, err
				}
				cmd.Env = append(cmd.Env, runtimeSubprocessCrashOncePluginEnv+"=plugin-workflow-demo", runtimeSubprocessCrashOnceMarkerEnv+"="+markerPath)
				return cmd, nil
			})
			subprocess.SetReplyTextCallback(replies.ReplyText)
			if workflowRuntime != nil {
				subprocess.SetWorkflowStartOrResumeCallback(func(ctx context.Context, request runtimecore.SubprocessWorkflowStartOrResumeRequest) (runtimecore.WorkflowTransition, error) {
					return workflowRuntime.StartOrResume(ctx, request.WorkflowID, request.PluginID, request.EventType, request.EventID, request.Initial)
				})
			}
			subprocess.SetRestartBudget(launcherConfig.restartBudget, launcherConfig.restartWindow)
			eventScopes := map[string]map[string]struct{}{
				"plugin-echo":          allowedSources("onebot", "runtime-demo-scheduler"),
				"plugin-workflow-demo": allowedSources("runtime-workflow-demo"),
			}
			return newRoutedPluginHostWithEventScopes(direct, subprocess, []string{"plugin-echo", "plugin-workflow-demo"}, eventScopes)
		},
	})
	if err != nil {
		t.Fatalf("new runtime app with crash-once subprocess seam: %v", err)
	}
	defer func() { _ = app.Close() }()

	host := runtimeRoutedPluginHost(t, app)
	if _, ok := host.hostForPlugin("plugin-workflow-demo").(*runtimecore.SubprocessPluginHost); !ok {
		t.Fatalf("expected plugin-workflow-demo subprocess route during crash recovery test, got %T", host.hostForPlugin("plugin-workflow-demo"))
	}

	failedResp := performRuntimeWorkflowMessageRequest(t, app, `{"actor_id":"user-crash","message":"crash once"}`)
	if failedResp.Code != http.StatusOK {
		t.Fatalf("expected first workflow request to recover without restarting the app, got %d: %s", failedResp.Code, failedResp.Body.String())
	}
	if !strings.Contains(failedResp.Body.String(), `workflow started, child job queued; wait for its result, then send another message to continue`) {
		t.Fatalf("expected in-request recovery to still return workflow reply, got %s", failedResp.Body.String())
	}
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("expected crash marker to prove injected subprocess crash, err=%v", err)
	}
	results := app.runtime.DispatchResults()
	if len(results) == 0 {
		t.Fatal("expected runtime dispatch results to capture subprocess crash failure")
	}
	first := results[len(results)-1]
	if first.PluginID != "plugin-workflow-demo" || first.Kind != "event" || !first.Success {
		t.Fatalf("expected workflow subprocess dispatch to recover and succeed in-request, got %+v", first)
	}
	snapshot, err := app.state.LoadPluginStatusSnapshot(t.Context(), "plugin-workflow-demo")
	if err != nil {
		t.Fatalf("load failed workflow subprocess snapshot: %v", err)
	}
	if snapshot.PluginID != "plugin-workflow-demo" || snapshot.LastDispatchKind != "event" || !snapshot.LastDispatchSuccess {
		t.Fatalf("expected recovered persisted workflow subprocess snapshot, got %+v", snapshot)
	}
	if snapshot.CurrentFailureStreak != 0 || snapshot.LastDispatchError != "" {
		t.Fatalf("expected clean persisted snapshot after in-request restart, got %+v", snapshot)
	}
	storedWorkflow, err := app.state.LoadWorkflowInstance(t.Context(), "workflow-user-crash")
	if err != nil {
		t.Fatalf("expected recovered workflow subprocess dispatch to persist workflow instance, err=%v", err)
	}
	if storedWorkflow.PluginID != "plugin-workflow-demo" || storedWorkflow.Status != runtimecore.WorkflowRuntimeStatusWaitingJob || storedWorkflow.Workflow.State["greeting"] != "crash once" {
		t.Fatalf("expected recovered workflow state after injected crash, got %+v", storedWorkflow)
	}
	storedWorkflowObservability := loadRuntimeWorkflowObservabilityRow(t, app, "workflow-user-crash")
	if statusCode, health := readRuntimeHealthResponse(t, app); statusCode != http.StatusOK || health.Status != "ok" || !health.Components.Scheduler.Running {
		t.Fatalf("expected app to stay healthy after subprocess crash without restart, status=%d payload=%+v", statusCode, health)
	}
	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsResp := httptest.NewRecorder()
	app.ServeHTTP(metricsResp, metricsReq)
	if metricsResp.Code != http.StatusOK {
		t.Fatalf("expected metrics 200 after subprocess crash, got %d: %s", metricsResp.Code, metricsResp.Body.String())
	}
	for _, expected := range []string{
		`bot_platform_subprocess_dispatch_last_duration_ms{plugin_id="plugin-workflow-demo",operation="event"}`,
		`bot_platform_subprocess_failure_total{plugin_id="plugin-workflow-demo",operation="event",failure_stage="dispatch",failure_reason="crash_after_handshake"} 1`,
		`bot_platform_subprocess_failures_total{plugin_id="plugin-workflow-demo",failure_stage="dispatch",failure_reason="crash_after_handshake"} 1`,
	} {
		if !strings.Contains(metricsResp.Body.String(), expected) {
			t.Fatalf("expected subprocess crash metrics evidence %q, got %s", expected, metricsResp.Body.String())
		}
	}
	if rendered := app.tracer.RenderTrace(storedWorkflowObservability.TraceID); !strings.Contains(rendered, `runtime.dispatch event_id=`+storedWorkflowObservability.EventID) || !strings.Contains(rendered, `plugin.invoke event_id=`+storedWorkflowObservability.EventID+` plugin_id=plugin-workflow-demo`) || !strings.Contains(rendered, `reply.send event_id=`+storedWorkflowObservability.EventID+` plugin_id=plugin-workflow-demo`) {
		t.Fatalf("expected recovered crash trace to preserve correlatable runtime, plugin, and reply spans, got %s", rendered)
	}
	combinedLogs := output.String() + strings.Join(app.logs.Lines(), "")
	crashEvidenceMatched := false
	for _, line := range strings.Split(combinedLogs, "\n") {
		if strings.Contains(line, `subprocess host dispatch failed after handshake`) && strings.Contains(line, storedWorkflowObservability.TraceID) && strings.Contains(line, storedWorkflowObservability.EventID) {
			crashEvidenceMatched = true
			break
		}
	}
	if !crashEvidenceMatched {
		t.Fatalf("expected subprocess crash logs to carry the same trace_id/event_id as the recovered workflow chain, got %s", combinedLogs)
	}
	for _, expected := range []string{"subprocess host dispatch failed after handshake", `"plugin_id":"plugin-workflow-demo"`, `"failure_stage":"dispatch"`, `"failure_reason":"crash_after_handshake"`} {
		if !strings.Contains(combinedLogs, expected) {
			t.Fatalf("expected crash failure log evidence %q, got %s", expected, combinedLogs)
		}
	}
	consoleAfterCrashReq := httptest.NewRequest(http.MethodGet, "/api/console?plugin_id=plugin-workflow-demo", nil)
	consoleAfterCrashResp := httptest.NewRecorder()
	app.ServeHTTP(consoleAfterCrashResp, consoleAfterCrashReq)
	if consoleAfterCrashResp.Code != http.StatusOK {
		t.Fatalf("expected filtered console 200 after subprocess crash recovery, got %d: %s", consoleAfterCrashResp.Code, consoleAfterCrashResp.Body.String())
	}
	for _, expected := range []string{`"id": "plugin-workflow-demo"`, `"statusLevel": "ok"`, `"statusRecovery": "last-dispatch-succeeded"`, `"lastDispatchKind": "event"`, `"lastDispatchSuccess": true`} {
		if !strings.Contains(consoleAfterCrashResp.Body.String(), expected) {
			t.Fatalf("expected workflow console payload after in-request crash recovery to include %s, got %s", expected, consoleAfterCrashResp.Body.String())
		}
	}

	childJobID := "job-workflow-demo-workflow-user-crash"
	cancelChildReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/"+childJobID+"/cancel", nil)
	cancelChildReq.Header.Set(runtimecore.ConsoleReadActorHeader, "job-operator")
	cancelChildResp := httptest.NewRecorder()
	app.ServeHTTP(cancelChildResp, cancelChildReq)
	if cancelChildResp.Code != http.StatusOK {
		t.Fatalf("cancel workflow child job through operator path: %d %s", cancelChildResp.Code, cancelChildResp.Body.String())
	}
	recoveredResp := performRuntimeWorkflowMessageRequest(t, app, `{"actor_id":"user-crash","message":"recover now"}`)
	if recoveredResp.Code != http.StatusOK {
		t.Fatalf("expected second workflow request to recover without app restart, got %d: %s", recoveredResp.Code, recoveredResp.Body.String())
	}
	if !strings.Contains(recoveredResp.Body.String(), `workflow resumed after child job failure and compensation completed`) {
		t.Fatalf("expected recovered workflow reply after subprocess restart, got %s", recoveredResp.Body.String())
	}
	recoveredResults := app.runtime.DispatchResults()
	if len(recoveredResults) < 2 {
		t.Fatalf("expected recovery dispatch to add success evidence, got %+v", recoveredResults)
	}
	recovered := recoveredResults[len(recoveredResults)-1]
	if recovered.PluginID != "plugin-workflow-demo" || recovered.Kind != "event" || !recovered.Success {
		t.Fatalf("expected recovered workflow subprocess dispatch success, got %+v", recovered)
	}
	recoveredSnapshot, err := app.state.LoadPluginStatusSnapshot(t.Context(), "plugin-workflow-demo")
	if err != nil {
		t.Fatalf("load recovered workflow subprocess snapshot: %v", err)
	}
	if !recoveredSnapshot.LastDispatchSuccess || recoveredSnapshot.CurrentFailureStreak != 0 || recoveredSnapshot.LastDispatchError != "" {
		t.Fatalf("expected persisted success snapshot after resumed workflow completion, got %+v", recoveredSnapshot)
	}
	workflowState, err := app.state.LoadWorkflowInstance(t.Context(), "workflow-user-crash")
	if err != nil {
		t.Fatalf("load recovered workflow instance: %v", err)
	}
	if workflowState.PluginID != "plugin-workflow-demo" || workflowState.Status != runtimecore.WorkflowRuntimeStatusCompleted || !workflowState.Workflow.Completed || !workflowState.Workflow.Compensated || workflowState.Workflow.State["greeting"] != "crash once" {
		t.Fatalf("expected recovered workflow dispatch to persist waiting state, got %+v", workflowState)
	}
	if jobState, _ := workflowState.Workflow.State["child-job"].(map[string]any); jobState == nil || jobState["status"] != string(runtimecore.JobStatusCancelled) {
		t.Fatalf("expected recovered workflow child-job result state after compensation, got %+v", workflowState.Workflow.State["child-job"])
	}
	recoveredConsoleReq := httptest.NewRequest(http.MethodGet, "/api/console?plugin_id=plugin-workflow-demo", nil)
	recoveredConsoleResp := httptest.NewRecorder()
	app.ServeHTTP(recoveredConsoleResp, recoveredConsoleReq)
	if recoveredConsoleResp.Code != http.StatusOK {
		t.Fatalf("expected filtered console 200 after recovery, got %d: %s", recoveredConsoleResp.Code, recoveredConsoleResp.Body.String())
	}
	for _, expected := range []string{`"id": "plugin-workflow-demo"`, `"statusLevel": "ok"`, `"statusRecovery": "last-dispatch-succeeded"`, `"lastDispatchSuccess": true`} {
		if !strings.Contains(recoveredConsoleResp.Body.String(), expected) {
			t.Fatalf("expected recovered workflow console payload to include %s, got %s", expected, recoveredConsoleResp.Body.String())
		}
	}
	subprocessHost := runtimeSubprocessHost(t, host)
	stdout, stderr := waitForRuntimeSubprocessCapture(t, subprocessHost, func(stdout string, stderr string) bool {
		return strings.Count(stderr, "runtime-plugin-echo-subprocess-online") >= 2 && strings.Contains(stdout, `"callback":"workflow_start_or_resume"`) && strings.Contains(stdout, `"callback":"reply_text"`)
	})
	if strings.Count(stderr, "runtime-plugin-echo-subprocess-online") < 2 {
		t.Fatalf("expected subprocess auto-recovery to start a second workflow subprocess without app restart, stderr=%s stdout=%s", stderr, stdout)
	}
	if !strings.Contains(stdout, `"callback":"workflow_start_or_resume"`) || !strings.Contains(stdout, `"callback":"reply_text"`) {
		t.Fatalf("expected recovered workflow subprocess to use callback bridges, stderr=%s stdout=%s", stderr, stdout)
	}
}

func TestRuntimePluginProcessFactoryAppliesLauncherGuards(t *testing.T) {
	t.Parallel()

	config := runtimeSubprocessLauncherConfig{
		workingDirectory: filepath.Join(".."),
		envAllowlist:     []string{"PATH", "SYSTEMROOT"},
	}
	factory := runtimePluginProcessFactory(config)
	_, err := factory(context.Background())
	if err == nil {
		t.Fatal("expected guarded subprocess factory to fail closed")
	}
	if !strings.Contains(err.Error(), "subprocess launch guard blocked start") || !strings.Contains(err.Error(), "escapes workspace root") {
		t.Fatalf("expected precise launch guard error, got %v", err)
	}

	workspaceRoot, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("abs workspace root: %v", err)
	}
	allowedFactory := runtimePluginProcessFactory(runtimeSubprocessLauncherConfig{
		workingDirectory: workspaceRoot,
		envAllowlist:     []string{"PATH", "SYSTEMROOT"},
	})
	allowed, err := allowedFactory(context.Background())
	if err != nil {
		t.Fatalf("expected allowed command, got %v", err)
	}
	if allowed == nil {
		t.Fatal("expected allowed command")
	}
	allowedEnv := strings.Join(allowed.Env, "\n")
	for _, forbidden := range []string{"GO_WANT_HELPER_PROCESS=1", "BOT_PLATFORM_SECRET=1"} {
		if strings.Contains(allowedEnv, forbidden) {
			t.Fatalf("expected allowlisted env to drop %q, got %v", forbidden, allowed.Env)
		}
	}
}

func TestRuntimeAppSubprocessWorkflowCallbackBridgeWorksAtSubstrateLevel(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	host := runtimeRoutedPluginHost(t, app)
	subprocessHost := runtimeSubprocessHost(t, host)
	subprocessHost.SetWorkflowStartOrResumeCallback(func(ctx context.Context, request runtimecore.SubprocessWorkflowStartOrResumeRequest) (runtimecore.WorkflowTransition, error) {
		return app.workflowRuntime.StartOrResume(ctx, request.WorkflowID, request.PluginID, request.EventType, request.EventID, request.Initial)
	})
	subprocessHost.SetAIChatQueueCallback(runtimeSubprocessAIChatCallbackBridge{queue: app.queue, sessions: app.controlState}.HandleQueue)

	workflowPlugin := testRuntimeWorkflowSubprocessPlugin(t)
	event := eventmodel.Event{
		EventID:        "evt-runtime-workflow-subprocess",
		TraceID:        "trace-runtime-workflow-subprocess",
		Source:         "runtime-workflow-demo",
		Type:           "message.received",
		Timestamp:      time.Date(2026, 4, 22, 12, 30, 0, 0, time.UTC),
		IdempotencyKey: "runtime-workflow-subprocess:1",
		Actor:          &eventmodel.Actor{ID: "user-1", Type: "user"},
		Message:        &eventmodel.Message{ID: "msg-runtime-workflow-subprocess", Text: "workflow-callback"},
	}
	if err := subprocessHost.DispatchEvent(t.Context(), workflowPlugin, event, eventmodel.ExecutionContext{TraceID: event.TraceID, EventID: event.EventID, Reply: &eventmodel.ReplyHandle{Capability: "onebot.reply", TargetID: "group-42", MessageID: event.Message.ID}}); err != nil {
		t.Fatalf("dispatch workflow subprocess event: %v", err)
	}
	stored, err := app.state.LoadWorkflowInstance(t.Context(), "workflow-user-1")
	if err != nil {
		t.Fatalf("load workflow instance: %v", err)
	}
	if stored.PluginID != "plugin-workflow-demo" || stored.Status != runtimecore.WorkflowRuntimeStatusWaitingJob || stored.Workflow.State["greeting"] != "workflow-callback" {
		t.Fatalf("expected workflow callback bridge to persist waiting runtime state, got %+v", stored)
	}
	if stored.Workflow.WaitingForJob == nil {
		t.Fatalf("expected workflow callback bridge to persist waiting child-job state, got %+v", stored)
	}
	stdout, _ := waitForRuntimeSubprocessCapture(t, subprocessHost, func(stdout string, _ string) bool {
		return strings.Contains(stdout, `"callback":"workflow_start_or_resume"`)
	})
	if !strings.Contains(stdout, `"callback":"workflow_start_or_resume"`) {
		t.Fatalf("expected workflow callback capture, got %s", stdout)
	}
}

func TestRuntimeAppDefaultPluginHostRoutingAdminEnableDisableUsesSubprocess(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeWriteActionRBACConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	host := runtimeRoutedPluginHost(t, app)
	if _, routed := host.routes["plugin-admin"]; !routed {
		t.Fatalf("expected default routing to route plugin-admin through subprocess, routes=%+v", host.routes)
	}
	if _, ok := host.hostForPlugin("plugin-admin").(*runtimecore.SubprocessPluginHost); !ok {
		t.Fatalf("expected plugin-admin to resolve to subprocess host by default, got %T", host.hostForPlugin("plugin-admin"))
	}

	disableReq := httptest.NewRequest(http.MethodPost, "/demo/plugins/plugin-echo/disable", nil)
	disableReq.Header.Set(runtimecore.ConsoleReadActorHeader, "admin-user")
	disableResp := httptest.NewRecorder()
	app.ServeHTTP(disableResp, disableReq)
	if disableResp.Code != http.StatusOK {
		t.Fatalf("expected subprocess disable operator 200, got %d: %s", disableResp.Code, disableResp.Body.String())
	}
	var disablePayload operatorPluginResponsePayload
	if err := json.Unmarshal(disableResp.Body.Bytes(), &disablePayload); err != nil {
		t.Fatalf("decode subprocess disable response: %v", err)
	}
	if disablePayload.Status != "ok" || disablePayload.Action != "plugin.disable" || disablePayload.Target != "plugin-echo" || !disablePayload.Accepted || disablePayload.Reason != "plugin_disabled" || disablePayload.Enabled == nil || *disablePayload.Enabled {
		t.Fatalf("unexpected subprocess disable payload %+v", disablePayload)
	}

	state, err := app.state.LoadPluginEnabledState(t.Context(), "plugin-echo")
	if err != nil {
		t.Fatalf("load subprocess-disabled plugin state: %v", err)
	}
	if state.Enabled {
		t.Fatalf("expected subprocess-admin disable to persist plugin disabled state, got %+v", state)
	}

	messageResp := performRuntimeOneBotMessageRequest(t, app, runtimeDemoOneBotMessageBody(t, 9501, "disabled by subprocess admin"))
	if messageResp.Code != http.StatusOK {
		t.Fatalf("expected onebot request to stay successful after subprocess disable, got %d: %s", messageResp.Code, messageResp.Body.String())
	}
	if strings.Contains(messageResp.Body.String(), "echo: disabled by subprocess admin") {
		t.Fatalf("expected disabled plugin-echo to skip replies after subprocess admin disable, got %s", messageResp.Body.String())
	}

	enableReq := httptest.NewRequest(http.MethodPost, "/demo/plugins/plugin-echo/enable", nil)
	enableReq.Header.Set(runtimecore.ConsoleReadActorHeader, "admin-user")
	enableResp := httptest.NewRecorder()
	app.ServeHTTP(enableResp, enableReq)
	if enableResp.Code != http.StatusOK {
		t.Fatalf("expected subprocess enable operator 200, got %d: %s", enableResp.Code, enableResp.Body.String())
	}
	var enablePayload operatorPluginResponsePayload
	if err := json.Unmarshal(enableResp.Body.Bytes(), &enablePayload); err != nil {
		t.Fatalf("decode subprocess enable response: %v", err)
	}
	if enablePayload.Status != "ok" || enablePayload.Action != "plugin.enable" || enablePayload.Target != "plugin-echo" || !enablePayload.Accepted || enablePayload.Reason != "plugin_enabled" || enablePayload.Enabled == nil || !*enablePayload.Enabled {
		t.Fatalf("unexpected subprocess enable payload %+v", enablePayload)
	}

	messageResp = performRuntimeOneBotMessageRequest(t, app, runtimeDemoOneBotMessageBody(t, 9502, "re-enabled by subprocess admin"))
	if messageResp.Code != http.StatusOK {
		t.Fatalf("expected onebot request after subprocess enable 200, got %d: %s", messageResp.Code, messageResp.Body.String())
	}
	if !strings.Contains(messageResp.Body.String(), "echo: re-enabled by subprocess admin") {
		t.Fatalf("expected plugin-echo reply after subprocess admin enable, got %s", messageResp.Body.String())
	}

	entries := app.audits.AuditEntries()
	if len(entries) < 2 {
		t.Fatalf("expected subprocess admin audits for disable+enable, got %+v", entries)
	}
	if entries[0].Actor != "admin-user" || entries[0].Permission != "plugin:disable" || entries[0].Action != "plugin.disable" || entries[0].Target != "plugin-echo" || !entries[0].Allowed || entries[0].Reason != "plugin_disabled" || entries[0].PluginID != "plugin-admin" {
		t.Fatalf("expected subprocess disable audit evidence, got %+v", entries[0])
	}
	if entries[1].Actor != "admin-user" || entries[1].Permission != "plugin:enable" || entries[1].Action != "plugin.enable" || entries[1].Target != "plugin-echo" || !entries[1].Allowed || entries[1].Reason != "plugin_enabled" || entries[1].PluginID != "plugin-admin" {
		t.Fatalf("expected subprocess enable audit evidence, got %+v", entries[1])
	}

	subprocessHost := runtimeSubprocessHostForPlugin(t, host, "plugin-admin")
	stdout, stderr := waitForRuntimeSubprocessCapture(t, subprocessHost, func(stdout string, stderr string) bool {
		return strings.Contains(stderr, "runtime-plugin-echo-subprocess-online") && strings.Contains(stdout, `"callback":"admin_request"`) && strings.Contains(stdout, `"action":"disable_plugin"`) && strings.Contains(stdout, `"action":"enable_plugin"`) && strings.Contains(stdout, `"action":"record_audit"`)
	})
	if !strings.Contains(stderr, "runtime-plugin-echo-subprocess-online") {
		t.Fatalf("expected default-routed admin subprocess to boot helper process, stderr=%s stdout=%s", stderr, stdout)
	}
	for _, expected := range []string{`"callback":"admin_request"`, `"action":"disable_plugin"`, `"action":"enable_plugin"`, `"action":"record_audit"`} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("expected subprocess admin callback evidence %q, stderr=%s stdout=%s", expected, stderr, stdout)
		}
	}
	for _, result := range app.runtime.DispatchResults() {
		if result.PluginID == "plugin-admin" && result.Kind == "command" && result.Success {
			return
		}
	}
	t.Fatalf("expected runtime dispatch results to record successful subprocess admin commands, got %+v", app.runtime.DispatchResults())
}

func TestRuntimeAppDefaultPluginHostRoutingAdminReplayUsesSubprocess(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeReplayRBACConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	host := runtimeRoutedPluginHost(t, app)
	if _, ok := host.hostForPlugin("plugin-admin").(*runtimecore.SubprocessPluginHost); !ok {
		t.Fatalf("expected default routing to route plugin-admin to subprocess, got %T", host.hostForPlugin("plugin-admin"))
	}

	stored := runtimeStoredReplayableEvent("evt-runtime-admin-subprocess-replay", "replay through admin subprocess")
	if err := app.state.RecordEvent(t.Context(), stored); err != nil {
		t.Fatalf("record replay source event: %v", err)
	}
	beforeReplies := app.replies.Count()

	if err := dispatchRuntimeAdminReplay(t, app, "replay-user", stored.EventID); err != nil {
		t.Fatalf("dispatch subprocess admin replay command: %v", err)
	}

	replies := app.replies.Since(beforeReplies)
	if len(replies) != 1 || replies[0].Payload != "echo: replay through admin subprocess" {
		t.Fatalf("expected subprocess admin replay to redispatch stored event, got %+v", replies)
	}
	entries := app.audits.AuditEntries()
	if len(entries) == 0 {
		t.Fatal("expected subprocess replay audit evidence")
	}
	lastEntry := entries[len(entries)-1]
	if lastEntry.Actor != "replay-user" || lastEntry.Action != "replay" || lastEntry.Target != stored.EventID || !lastEntry.Allowed || lastEntry.Permission != "plugin:replay" || lastEntry.PluginID != "plugin-admin" {
		t.Fatalf("expected subprocess replay audit entry, got %+v", lastEntry)
	}
	subprocessHost := runtimeSubprocessHostForPlugin(t, host, "plugin-admin")
	stdout, stderr := waitForRuntimeSubprocessCapture(t, subprocessHost, func(stdout string, stderr string) bool {
		return strings.Contains(stderr, "runtime-plugin-echo-subprocess-online") && strings.Contains(stdout, `"callback":"admin_request"`) && strings.Contains(stdout, `"action":"authorize"`) && strings.Contains(stdout, `"action":"replay_event"`)
	})
	if !strings.Contains(stderr, "runtime-plugin-echo-subprocess-online") {
		t.Fatalf("expected default-routed replay subprocess to boot helper process, stderr=%s stdout=%s", stderr, stdout)
	}
	for _, expected := range []string{`"callback":"admin_request"`, `"action":"authorize"`, `"action":"replay_event"`} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("expected subprocess replay callback evidence %q, stderr=%s stdout=%s", expected, stderr, stdout)
		}
	}
}

func testRuntimeWorkflowSubprocessPlugin(t *testing.T) pluginsdk.Plugin {
	t.Helper()
	plugin := testRuntimeWorkflowSubprocessDefinition()
	return plugin
}

func testRuntimeWorkflowSubprocessDefinition() pluginsdk.Plugin {
	return testRuntimeWorkflowSubprocessPluginWithID("plugin-workflow-demo")
}

func testRuntimeWorkflowSubprocessPluginWithID(pluginID string) pluginsdk.Plugin {
	return pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{
		ID:         pluginID,
		Name:       "Workflow Demo Plugin",
		Version:    "0.1.0",
		APIVersion: "v0",
		Mode:       pluginsdk.ModeSubprocess,
		Entry:      pluginsdk.PluginEntry{Module: "plugins/plugin-workflow-demo", Symbol: "Plugin"},
	}}
}

func dispatchRuntimeAdminReplay(t *testing.T, app *runtimeApp, actor string, eventID string) error {
	t.Helper()
	if strings.TrimSpace(actor) == "" {
		actor = "replay-user"
	}
	return app.runtime.DispatchCommand(t.Context(), eventmodel.CommandInvocation{
		Name:     "admin",
		Raw:      "/admin replay " + eventID,
		Metadata: map[string]any{"actor": actor},
	}, eventmodel.ExecutionContext{
		TraceID: "trace-admin-replay-" + eventID,
		EventID: "evt-admin-replay-" + eventID,
	})
}

func runtimeStoredReplayableEvent(eventID string, message string) eventmodel.Event {
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	return eventmodel.Event{
		EventID:        eventID,
		TraceID:        "trace-" + eventID,
		Source:         "onebot",
		Type:           "message.received",
		Timestamp:      now,
		IdempotencyKey: "stored:" + eventID,
		Actor:          &eventmodel.Actor{ID: "user-1", Type: "user", DisplayName: "alice"},
		Channel:        &eventmodel.Channel{ID: "group-42", Type: "group", Title: "group-42"},
		Message:        &eventmodel.Message{ID: "msg-" + eventID, Text: message},
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

type failingReplayJournalSmokeStore struct {
	runtimeSmokeStore
	loadErr error
}

func (s failingReplayJournalSmokeStore) LoadEvent(_ context.Context, _ string) (eventmodel.Event, error) {
	if s.loadErr != nil {
		return eventmodel.Event{}, s.loadErr
	}
	return eventmodel.Event{}, errors.New("replay load failed")
}

func consoleMetaString(t *testing.T, meta map[string]any, key string) string {
	t.Helper()
	value, ok := meta[key]
	if !ok {
		t.Fatalf("expected console meta to include %q", key)
	}
	text, ok := value.(string)
	if !ok {
		t.Fatalf("expected console meta %q to be a string, got %#v", key, value)
	}
	return text
}

func consoleMetaStringSlice(t *testing.T, meta map[string]any, key string) []string {
	t.Helper()
	value, ok := meta[key]
	if !ok {
		t.Fatalf("expected console meta to include %q", key)
	}
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("expected console meta %q to decode as []any, got %#v", key, value)
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			t.Fatalf("expected console meta %q item to be a string, got %#v", key, item)
		}
		result = append(result, text)
	}
	return result
}

func hasConsoleSchedule(payload runtimeConsoleResponse, scheduleID string) bool {
	for _, schedule := range payload.Schedules {
		if schedule.ID == scheduleID {
			return true
		}
	}
	return false
}

func hasConsoleAdapter(payload runtimeConsoleResponse, adapterID string) bool {
	for _, adapter := range payload.Adapters {
		if adapter.ID == adapterID {
			return true
		}
	}
	return false
}

func hasConsoleWorkflow(payload runtimeConsoleResponse, workflowID string) bool {
	for _, workflow := range payload.Workflows {
		if workflow.ID == workflowID {
			return true
		}
	}
	return false
}

func consoleAdapterByID(t *testing.T, payload runtimeConsoleResponse, adapterID string) struct {
	ID             string         `json:"id"`
	Adapter        string         `json:"adapter"`
	Source         string         `json:"source"`
	Config         map[string]any `json:"config"`
	StatusSource   string         `json:"statusSource"`
	ConfigSource   string         `json:"configSource"`
	Status         string         `json:"status"`
	Health         string         `json:"health"`
	Online         bool           `json:"online"`
	StatePersisted bool           `json:"statePersisted"`
} {
	t.Helper()
	for _, adapter := range payload.Adapters {
		if adapter.ID == adapterID {
			return adapter
		}
	}
	t.Fatalf("adapter %q not found in console payload %+v", adapterID, payload.Adapters)
	return struct {
		ID             string         `json:"id"`
		Adapter        string         `json:"adapter"`
		Source         string         `json:"source"`
		Config         map[string]any `json:"config"`
		StatusSource   string         `json:"statusSource"`
		ConfigSource   string         `json:"configSource"`
		Status         string         `json:"status"`
		Health         string         `json:"health"`
		Online         bool           `json:"online"`
		StatePersisted bool           `json:"statePersisted"`
	}{}
}

func writeConsoleRBACConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := "runtime:\n  environment: test\n  log_level: debug\n  http_port: 18080\n  sqlite_path: " + filepath.ToSlash(filepath.Join(dir, "runtime.sqlite")) + "\n  scheduler_interval_ms: 20\nrbac:\n  console_read_permission: console:read\n  actor_roles:\n    viewer-user: [viewer]\n  policies:\n    viewer:\n      permissions: [console:read]\n      plugin_scope: ['console']\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func writeScheduleCancelRBACConfig(t *testing.T) string {
	t.Helper()
	return writeScheduleCancelRBACConfigAt(t, t.TempDir(), "sqlite", "")
}

func writeScheduleCancelRBACConfigAt(t *testing.T, dir string, backend string, dsn string) string {
	t.Helper()
	var builder strings.Builder
	builder.WriteString("runtime:\n")
	builder.WriteString("  environment: test\n")
	builder.WriteString("  log_level: debug\n")
	builder.WriteString("  http_port: 18080\n")
	builder.WriteString("  sqlite_path: " + filepath.ToSlash(filepath.Join(dir, "runtime.sqlite")) + "\n")
	if strings.TrimSpace(backend) != "" && strings.TrimSpace(backend) != "sqlite" {
		builder.WriteString("  smoke_store_backend: " + strings.TrimSpace(backend) + "\n")
	}
	if strings.TrimSpace(dsn) != "" {
		builder.WriteString("  postgres_dsn: \"" + strings.ReplaceAll(dsn, "\"", "\\\"") + "\"\n")
	}
	builder.WriteString("  scheduler_interval_ms: 20\n")
	builder.WriteString("rbac:\n")
	builder.WriteString("  actor_roles:\n")
	builder.WriteString("    schedule-admin: [schedule-operator]\n")
	builder.WriteString("    viewer-user: [schedule-viewer]\n")
	builder.WriteString("  policies:\n")
	builder.WriteString("    schedule-operator:\n")
	builder.WriteString("      permissions: [schedule:cancel]\n")
	builder.WriteString("      plugin_scope: ['*']\n")
	builder.WriteString("    schedule-viewer:\n")
	builder.WriteString("      permissions: [schedule:view]\n")
	builder.WriteString("      plugin_scope: ['*']\n")
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func writeScheduleCancelRBACConfigWithPostgresSmokeStoreAt(t *testing.T, dir string, dsn string) string {
	t.Helper()
	return writeScheduleCancelRBACConfigAt(t, dir, "postgres", dsn)
}

func writeWriteActionRBACConfig(t *testing.T) string {
	t.Helper()
	return writeWriteActionRBACConfigAt(t, t.TempDir())
}

func writeWriteActionRBACConfigAt(t *testing.T, dir string) string {
	t.Helper()
	return writeWriteActionRBACConfigWithBackendAt(t, dir, "sqlite", "")
}

func writeWriteActionRBACConfigWithBackendAt(t *testing.T, dir string, backend string, dsn string) string {
	t.Helper()
	var builder strings.Builder
	builder.WriteString("runtime:\n")
	builder.WriteString("  environment: test\n")
	builder.WriteString("  log_level: debug\n")
	builder.WriteString("  http_port: 18080\n")
	builder.WriteString("  sqlite_path: " + filepath.ToSlash(filepath.Join(dir, "runtime.sqlite")) + "\n")
	if strings.TrimSpace(backend) != "" && strings.TrimSpace(backend) != "sqlite" {
		builder.WriteString("  smoke_store_backend: " + strings.TrimSpace(backend) + "\n")
	}
	if strings.TrimSpace(dsn) != "" {
		builder.WriteString("  postgres_dsn: \"" + strings.ReplaceAll(dsn, "\"", "\\\"") + "\"\n")
	}
	builder.WriteString("  scheduler_interval_ms: 20\n")
	builder.WriteString("rbac:\n")
	builder.WriteString("  actor_roles:\n")
	builder.WriteString("    admin-user: [admin]\n")
	builder.WriteString("    schedule-admin: [schedule-operator]\n")
	builder.WriteString("    job-operator: [job-operator]\n")
	builder.WriteString("    config-operator: [config-operator]\n")
	builder.WriteString("    runtime-job-runner: [runtime-job-runner]\n")
	builder.WriteString("    viewer-user: [viewer]\n")
	builder.WriteString("  policies:\n")
	builder.WriteString("    admin:\n")
	builder.WriteString("      permissions: [plugin:enable, plugin:disable]\n")
	builder.WriteString("      plugin_scope: ['*']\n")
	builder.WriteString("    schedule-operator:\n")
	builder.WriteString("      permissions: [schedule:cancel]\n")
	builder.WriteString("      plugin_scope: ['*']\n")
	builder.WriteString("    job-operator:\n")
	builder.WriteString("      permissions: [job:pause, job:resume, job:cancel, job:retry]\n")
	builder.WriteString("      plugin_scope: ['*']\n")
	builder.WriteString("    config-operator:\n")
	builder.WriteString("      permissions: [plugin:config]\n")
	builder.WriteString("      plugin_scope: ['plugin-echo']\n")
	builder.WriteString("    runtime-job-runner:\n")
	builder.WriteString("      permissions: [job:run]\n")
	builder.WriteString("      plugin_scope: ['plugin-ai-chat']\n")
	builder.WriteString("    viewer:\n")
	builder.WriteString("      permissions: [console:read]\n")
	builder.WriteString("      plugin_scope: ['console']\n")
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func writeWriteActionRBACConfigWithPostgresSmokeStoreAt(t *testing.T, dir string, dsn string) string {
	t.Helper()
	return writeWriteActionRBACConfigWithBackendAt(t, dir, "postgres", dsn)
}

func writeOperatorAuthConfig(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	content := "runtime:\n" +
		"  environment: test\n" +
		"  log_level: debug\n" +
		"  http_port: 18080\n" +
		"  sqlite_path: " + filepath.ToSlash(filepath.Join(dir, "runtime.sqlite")) + "\n" +
		"  scheduler_interval_ms: 20\n" +
		"rbac:\n" +
		"  actor_roles:\n" +
		"    admin-user: [admin]\n" +
		"  policies:\n" +
		"    admin:\n" +
		"      permissions: [console:read, plugin:enable, plugin:disable]\n" +
		"      plugin_scope: ['*']\n" +
		"  console_read_permission: console:read\n" +
		"operator_auth:\n" +
		"  tokens:\n" +
		"    - id: console-main\n" +
		"      actor_id: admin-user\n" +
		"      token_ref: BOT_PLATFORM_OPERATOR_TOKEN\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write operator auth config: %v", err)
	}
	return path
}

func writeOperatorAuthConfigWithoutConsoleReadPermission(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	content := "runtime:\n" +
		"  environment: test\n" +
		"  log_level: debug\n" +
		"  http_port: 18080\n" +
		"  sqlite_path: " + filepath.ToSlash(filepath.Join(dir, "runtime.sqlite")) + "\n" +
		"  scheduler_interval_ms: 20\n" +
		"rbac:\n" +
		"  actor_roles:\n" +
		"    admin-user: [admin]\n" +
		"  policies:\n" +
		"    admin:\n" +
		"      permissions: [plugin:enable, plugin:disable]\n" +
		"      plugin_scope: ['*']\n" +
		"operator_auth:\n" +
		"  tokens:\n" +
		"    - id: console-main\n" +
		"      actor_id: admin-user\n" +
		"      token_ref: BOT_PLATFORM_OPERATOR_TOKEN\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write operator auth config without console read permission: %v", err)
	}
	return path
}

func writeWriteActionRBACOperatorAuthConfig(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	content := "runtime:\n" +
		"  environment: test\n" +
		"  log_level: debug\n" +
		"  http_port: 18080\n" +
		"  sqlite_path: " + filepath.ToSlash(filepath.Join(dir, "runtime.sqlite")) + "\n" +
		"  scheduler_interval_ms: 20\n" +
		"rbac:\n" +
		"  console_read_permission: console:read\n" +
		"  actor_roles:\n" +
		"    admin-user: [admin]\n" +
		"    schedule-admin: [schedule-operator]\n" +
		"    job-operator: [job-operator]\n" +
		"    config-operator: [config-operator]\n" +
		"    runtime-job-runner: [runtime-job-runner]\n" +
		"    viewer-user: [viewer]\n" +
		"  policies:\n" +
		"    admin:\n" +
		"      permissions: [console:read, plugin:enable, plugin:disable]\n" +
		"      plugin_scope: ['*']\n" +
		"    schedule-operator:\n" +
		"      permissions: [schedule:cancel]\n" +
		"      plugin_scope: ['*']\n" +
		"    job-operator:\n" +
		"      permissions: [job:pause, job:resume, job:cancel, job:retry]\n" +
		"      plugin_scope: ['*']\n" +
		"    config-operator:\n" +
		"      permissions: [plugin:config]\n" +
		"      plugin_scope: ['plugin-echo']\n" +
		"    runtime-job-runner:\n" +
		"      permissions: [job:run]\n" +
		"      plugin_scope: ['plugin-ai-chat']\n" +
		"    viewer:\n" +
		"      permissions: [console:read]\n" +
		"      plugin_scope: ['console']\n" +
		"operator_auth:\n" +
		"  tokens:\n" +
		"    - id: console-main\n" +
		"      actor_id: admin-user\n" +
		"      token_ref: BOT_PLATFORM_OPERATOR_TOKEN\n" +
		"    - id: config-main\n" +
		"      actor_id: config-operator\n" +
		"      token_ref: BOT_PLATFORM_OPERATOR_CONFIG_TOKEN\n" +
		"    - id: job-main\n" +
		"      actor_id: job-operator\n" +
		"      token_ref: BOT_PLATFORM_OPERATOR_JOB_TOKEN\n" +
		"    - id: schedule-main\n" +
		"      actor_id: schedule-admin\n" +
		"      token_ref: BOT_PLATFORM_OPERATOR_SCHEDULE_TOKEN\n" +
		"    - id: viewer-main\n" +
		"      actor_id: viewer-user\n" +
		"      token_ref: BOT_PLATFORM_OPERATOR_VIEWER_TOKEN\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write operator-auth write-action config: %v", err)
	}
	return path
}

func consoleRequestWithViewer(path string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set(runtimecore.ConsoleReadActorHeader, "viewer-user")
	return req
}

func replaceRuntimeCurrentRBACState(t *testing.T, app *runtimeApp, identities []runtimecore.OperatorIdentityState, snapshot runtimecore.RBACSnapshotState) {
	t.Helper()
	if err := app.state.ReplaceCurrentRBACState(t.Context(), identities, snapshot); err != nil {
		t.Fatalf("replace current rbac state: %v", err)
	}
}

func writeReplayRBACConfigAt(t *testing.T, dir string, backend string, dsn string) string {
	t.Helper()
	var builder strings.Builder
	builder.WriteString("runtime:\n")
	builder.WriteString("  environment: test\n")
	builder.WriteString("  log_level: debug\n")
	builder.WriteString("  http_port: 18080\n")
	builder.WriteString("  sqlite_path: " + filepath.ToSlash(filepath.Join(dir, "runtime.sqlite")) + "\n")
	if strings.TrimSpace(backend) != "" && strings.TrimSpace(backend) != "sqlite" {
		builder.WriteString("  smoke_store_backend: " + strings.TrimSpace(backend) + "\n")
	}
	if strings.TrimSpace(dsn) != "" {
		builder.WriteString("  postgres_dsn: \"" + strings.ReplaceAll(dsn, "\"", "\\\"") + "\"\n")
	}
	builder.WriteString("  scheduler_interval_ms: 20\n")
	builder.WriteString("rbac:\n")
	builder.WriteString("  actor_roles:\n")
	builder.WriteString("    replay-user: [replay-operator]\n")
	builder.WriteString("  policies:\n")
	builder.WriteString("    replay-operator:\n")
	builder.WriteString("      permissions: [plugin:replay]\n")
	builder.WriteString("      event_scope: ['*']\n")
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		t.Fatalf("write replay rbac config: %v", err)
	}
	return path
}

func writeReplayRBACConfig(t *testing.T) string {
	t.Helper()
	return writeReplayRBACConfigAt(t, t.TempDir(), "sqlite", "")
}

func writeReplayRBACConfigWithPostgresSmokeStoreAt(t *testing.T, dir string, dsn string) string {
	t.Helper()
	return writeReplayRBACConfigAt(t, dir, "postgres", dsn)
}

func decodeRuntimeConfigCheckResult(t *testing.T, raw []byte) runtimeConfigCheckResult {
	t.Helper()
	var result runtimeConfigCheckResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode runtime config check result: %v\nraw=%s", err, string(raw))
	}
	return result
}

func TestRuntimeCLIConfigCheckPassesWithEnvOverrides(t *testing.T) {
	configPath := writeTestConfig(t)
	overrideSQLitePath := filepath.ToSlash(filepath.Join(t.TempDir(), "runtime-override.sqlite"))
	t.Setenv("BOT_PLATFORM_RUNTIME_SQLITE_PATH", overrideSQLitePath)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	served := false
	exitCode := runRuntimeCLI([]string{"-config", configPath, "-check-config"}, &stdout, &stderr, func(addr string, handler http.Handler) error {
		served = true
		return nil
	})
	if exitCode != 0 {
		t.Fatalf("expected config check exit code 0, got %d, stderr=%s", exitCode, stderr.String())
	}
	if served {
		t.Fatal("expected config check to exit before starting the HTTP server")
	}
	if strings.TrimSpace(stderr.String()) != "" {
		t.Fatalf("expected empty stderr for config check success, got %s", stderr.String())
	}
	result := decodeRuntimeConfigCheckResult(t, stdout.Bytes())
	if result.Status != "ok" {
		t.Fatalf("expected ok status, got %+v", result)
	}
	if result.Mode != "check-config" {
		t.Fatalf("expected mode check-config, got %+v", result)
	}
	if result.ConfigPath != configPath {
		t.Fatalf("expected config path %q, got %+v", configPath, result)
	}
	if result.SQLitePath != overrideSQLitePath {
		t.Fatalf("expected sqlite override path %q, got %+v", overrideSQLitePath, result)
	}
	if result.SmokeStoreBackend != "sqlite" {
		t.Fatalf("expected sqlite smoke store backend, got %+v", result)
	}
	if result.HTTPServerStarted {
		t.Fatalf("expected http_server_started=false, got %+v", result)
	}
}

func TestRuntimeCLIConfigCheckFailsLoudlyOnBadPostgresDSN(t *testing.T) {
	configPath := writeTestConfigWithPostgresSmokeStoreAt(t, t.TempDir(), "postgres://127.0.0.1:1/runtime_smoke?sslmode=disable")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	served := false
	exitCode := runRuntimeCLI([]string{"-config", configPath, "-check-config"}, &stdout, &stderr, func(addr string, handler http.Handler) error {
		served = true
		return nil
	})
	if exitCode == 0 {
		t.Fatal("expected config check to fail for bad postgres smoke store DSN")
	}
	if served {
		t.Fatal("expected failing config check to exit before starting the HTTP server")
	}
	if strings.TrimSpace(stdout.String()) != "" {
		t.Fatalf("expected empty stdout for config check failure, got %s", stdout.String())
	}
	result := decodeRuntimeConfigCheckResult(t, stderr.Bytes())
	if result.Status != "error" {
		t.Fatalf("expected error status, got %+v", result)
	}
	if result.Mode != "check-config" {
		t.Fatalf("expected mode check-config, got %+v", result)
	}
	for _, expected := range []string{"open runtime smoke store", "postgres"} {
		if !strings.Contains(result.Error, expected) {
			t.Fatalf("expected config check error to include %q, got %+v", expected, result)
		}
	}
	if result.HTTPServerStarted {
		t.Fatalf("expected http_server_started=false, got %+v", result)
	}
}

func TestRuntimeCLIConfigCheckFailsLoudlyWhenOpenAICompatSecretMissing(t *testing.T) {
	configPath := writeAIProviderConfigAt(t, t.TempDir(), "https://example.invalid/v1/chat/completions", "gpt-test", 250, "BOT_PLATFORM_AI_CHAT_API_KEY")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	served := false
	exitCode := runRuntimeCLI([]string{"-config", configPath, "-check-config"}, &stdout, &stderr, func(addr string, handler http.Handler) error {
		served = true
		return nil
	})
	if exitCode == 0 {
		t.Fatal("expected config check to fail when openai_compat api key secret is missing")
	}
	if served {
		t.Fatal("expected failing ai provider config check to exit before starting the HTTP server")
	}
	if strings.TrimSpace(stdout.String()) != "" {
		t.Fatalf("expected empty stdout for config check failure, got %s", stdout.String())
	}
	result := decodeRuntimeConfigCheckResult(t, stderr.Bytes())
	if result.Status != "error" || result.Mode != "check-config" {
		t.Fatalf("expected config check error result, got %+v", result)
	}
	for _, expected := range []string{"build ai chat provider", runtimecore.AIChatAPIKeySecretConfigRef(), "secret \"BOT_PLATFORM_AI_CHAT_API_KEY\" not found in environment"} {
		if !strings.Contains(result.Error, expected) {
			t.Fatalf("expected config check error to include %q, got %+v", expected, result)
		}
	}
	if result.HTTPServerStarted {
		t.Fatalf("expected http_server_started=false, got %+v", result)
	}
}

func TestRuntimeCLIConfigCheckPassesForMixedAdapterInstances(t *testing.T) {
	t.Setenv("BOT_PLATFORM_WEBHOOK_TOKEN", "test-webhook-token")
	configPath := writeTestConfigWithBotInstancesAt(t, t.TempDir())

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	served := false
	exitCode := runRuntimeCLI([]string{"-config", configPath, "-check-config"}, &stdout, &stderr, func(addr string, handler http.Handler) error {
		served = true
		return nil
	})
	if exitCode != 0 {
		t.Fatalf("expected mixed-adapter config check exit code 0, got %d, stderr=%s", exitCode, stderr.String())
	}
	if served {
		t.Fatal("expected mixed-adapter config check to exit before starting the HTTP server")
	}
	if strings.TrimSpace(stderr.String()) != "" {
		t.Fatalf("expected empty stderr for mixed-adapter config check success, got %s", stderr.String())
	}
	result := decodeRuntimeConfigCheckResult(t, stdout.Bytes())
	if result.Status != "ok" || result.Mode != "check-config" || result.ConfigPath != configPath || result.HTTPServerStarted {
		t.Fatalf("expected mixed-adapter config check success payload, got %+v", result)
	}
}

func TestRuntimeCLINormalStartupInvokesHTTPServer(t *testing.T) {
	configPath := writeTestConfig(t)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	served := false
	exitCode := runRuntimeCLI([]string{"-config", configPath}, &stdout, &stderr, func(addr string, handler http.Handler) error {
		served = true
		if addr != ":18080" {
			t.Fatalf("expected runtime to serve on :18080, got %q", addr)
		}
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("expected healthz 200 before serve, got %d: %s", resp.Code, resp.Body.String())
		}
		payload := decodeRuntimeHealthResponse(t, resp.Body.Bytes())
		if payload.Status != "ok" {
			t.Fatalf("expected healthz status ok, got %+v", payload)
		}
		if payload.Components.Storage.Status != "ok" || payload.Components.Scheduler.Status != "ok" || !payload.Components.Scheduler.Running {
			t.Fatalf("expected healthy startup health components, got %+v", payload)
		}
		return nil
	})
	if exitCode != 0 {
		t.Fatalf("expected normal startup exit code 0, got %d, stderr=%s", exitCode, stderr.String())
	}
	if !served {
		t.Fatal("expected normal startup path to invoke HTTP server")
	}
	if strings.TrimSpace(stderr.String()) != "" {
		t.Fatalf("expected empty stderr for normal startup success, got %s", stderr.String())
	}
}

func TestRuntimeAppHealthzReturnsStructuredHealthyStatus(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	waitForSchedulerRunningState(t, app, true)
	statusCode, payload := readRuntimeHealthResponse(t, app)
	if statusCode != http.StatusOK {
		t.Fatalf("expected healthz 200, got %d: %+v", statusCode, payload)
	}
	if payload.Status != "ok" {
		t.Fatalf("expected overall health ok, got %+v", payload)
	}
	if payload.Environment != "test" {
		t.Fatalf("expected environment test, got %+v", payload)
	}
	if payload.SQLitePath == "" {
		t.Fatalf("expected sqlite path in health payload, got %+v", payload)
	}
	if payload.SchedulerIntervalMs != 20 {
		t.Fatalf("expected scheduler interval 20, got %+v", payload)
	}
	if payload.Components.Storage.Status != "ok" {
		t.Fatalf("expected storage ok, got %+v", payload)
	}
	if payload.Components.Storage.Error != "" {
		t.Fatalf("expected no storage error, got %+v", payload)
	}
	if payload.Components.Scheduler.Status != "ok" || !payload.Components.Scheduler.Running {
		t.Fatalf("expected scheduler ok/running, got %+v", payload)
	}
}

func TestRuntimeAppHealthzReturnsDegradedWhenSchedulerStops(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	waitForSchedulerRunningState(t, app, true)
	app.schedulerCancel()
	waitForSchedulerRunningState(t, app, false)

	statusCode, payload := readRuntimeHealthResponse(t, app)
	if statusCode != http.StatusOK {
		t.Fatalf("expected healthz 200 for degraded runtime, got %d: %+v", statusCode, payload)
	}
	if payload.Status != "degraded" {
		t.Fatalf("expected overall health degraded, got %+v", payload)
	}
	if payload.Components.Storage.Status != "ok" {
		t.Fatalf("expected storage ok while degraded, got %+v", payload)
	}
	if payload.Components.Scheduler.Status != "degraded" || payload.Components.Scheduler.Running {
		t.Fatalf("expected scheduler degraded/not running, got %+v", payload)
	}
}

func TestRuntimeAppHealthzReturnsErrorWhenStorageProbeFails(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	waitForSchedulerRunningState(t, app, true)
	if err := app.state.Close(); err != nil {
		t.Fatalf("close sqlite state for failure probe: %v", err)
	}

	statusCode, payload := readRuntimeHealthResponse(t, app)
	if statusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected healthz 503 on storage failure, got %d: %+v", statusCode, payload)
	}
	if payload.Status != "error" {
		t.Fatalf("expected overall health error, got %+v", payload)
	}
	if payload.Components.Storage.Status != "error" {
		t.Fatalf("expected storage error status, got %+v", payload)
	}
	if payload.Components.Storage.Error == "" {
		t.Fatalf("expected storage error detail, got %+v", payload)
	}
	if payload.Components.Scheduler.Status != "ok" || !payload.Components.Scheduler.Running {
		t.Fatalf("expected scheduler state to remain visible during storage failure, got %+v", payload)
	}
	app.state = nil
	app.smokeStore = nil
}

func TestRuntimeAppDemoOneBotMessage(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	req := httptest.NewRequest(http.MethodPost, "/demo/onebot/message", strings.NewReader(`{"post_type":"message","message_type":"group","time":1712034000,"user_id":10001,"group_id":42,"message_id":9001,"raw_message":"hello runtime","sender":{"nickname":"alice"}}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	app.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}

	var payload struct {
		Status  string        `json:"status"`
		Replies []replyRecord `json:"replies"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Status != "ok" {
		t.Fatalf("unexpected status %q", payload.Status)
	}
	if len(payload.Replies) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(payload.Replies))
	}
	if payload.Replies[0].Payload != "echo: hello runtime" {
		t.Fatalf("unexpected reply payload %q", payload.Replies[0].Payload)
	}

	countsReq := httptest.NewRequest(http.MethodGet, "/demo/state/counts", nil)
	countsResp := httptest.NewRecorder()
	app.ServeHTTP(countsResp, countsReq)
	if !strings.Contains(countsResp.Body.String(), `"event_journal":1`) && !strings.Contains(countsResp.Body.String(), `"event_journal": 1`) {
		t.Fatalf("expected sqlite counts to include recorded event, got %s", countsResp.Body.String())
	}
	if !strings.Contains(countsResp.Body.String(), `"jobs":0`) && !strings.Contains(countsResp.Body.String(), `"jobs": 0`) {
		t.Fatalf("expected sqlite counts to include jobs counter, got %s", countsResp.Body.String())
	}
	if !strings.Contains(countsResp.Body.String(), `"schedule_plans":0`) && !strings.Contains(countsResp.Body.String(), `"schedule_plans": 0`) {
		t.Fatalf("expected sqlite counts to include schedule_plans counter, got %s", countsResp.Body.String())
	}
}

func TestRuntimeAppConfiguredOneBotRoutesUseConfiguredSources(t *testing.T) {
	t.Setenv("BOT_PLATFORM_WEBHOOK_TOKEN", "test-webhook-token")
	app, err := newRuntimeApp(writeTestConfigWithBotInstancesAt(t, t.TempDir()))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	alphaResp := performRuntimeOneBotMessageRequestAtPath(t, app, "/demo/onebot/message", runtimeDemoOneBotMessageBody(t, 9301, "hello alpha"))
	if alphaResp.Code != http.StatusOK {
		t.Fatalf("expected alpha route 200, got %d: %s", alphaResp.Code, alphaResp.Body.String())
	}
	var alphaPayload struct {
		Status  string        `json:"status"`
		Replies []replyRecord `json:"replies"`
	}
	if err := json.Unmarshal(alphaResp.Body.Bytes(), &alphaPayload); err != nil {
		t.Fatalf("decode alpha response: %v", err)
	}
	if alphaPayload.Status != "ok" || len(alphaPayload.Replies) != 1 || alphaPayload.Replies[0].Payload != "echo: hello alpha" {
		t.Fatalf("unexpected alpha response %+v", alphaPayload)
	}

	betaResp := performRuntimeOneBotMessageRequestAtPath(t, app, "/demo/onebot/message-beta", runtimeDemoOneBotMessageBody(t, 9302, "hello beta"))
	if betaResp.Code != http.StatusOK {
		t.Fatalf("expected beta route 200, got %d: %s", betaResp.Code, betaResp.Body.String())
	}
	var betaPayload struct {
		EventID string        `json:"event_id"`
		Status  string        `json:"status"`
		Replies []replyRecord `json:"replies"`
	}
	if err := json.Unmarshal(betaResp.Body.Bytes(), &betaPayload); err != nil {
		t.Fatalf("decode beta response: %v", err)
	}
	if betaPayload.Status != "ok" || len(betaPayload.Replies) != 1 || betaPayload.Replies[0].Payload != "echo: hello beta" {
		t.Fatalf("unexpected beta response %+v", betaPayload)
	}
	betaEvent, err := app.state.LoadEvent(t.Context(), betaPayload.EventID)
	if err != nil {
		t.Fatalf("load persisted beta event: %v", err)
	}
	if betaEvent.Source != "onebot-beta" || betaEvent.IdempotencyKey != "onebot:onebot-beta:group:9302" {
		t.Fatalf("expected persisted beta event to keep configured source/idempotency, got %+v", betaEvent)
	}
	if betaEvent.Metadata["adapter_instance_id"] != "adapter-onebot-beta" {
		t.Fatalf("expected persisted beta event to keep adapter instance id, got %+v", betaEvent.Metadata)
	}
	dispatches := app.runtime.DispatchResults()
	if len(dispatches) < 2 {
		t.Fatalf("expected dispatch evidence for configured onebot routes, got %+v", dispatches)
	}
	counts, err := app.state.Counts(t.Context())
	if err != nil {
		t.Fatalf("sqlite counts: %v", err)
	}
	if counts["event_journal"] != 2 || counts["idempotency_keys"] != 2 {
		t.Fatalf("expected two persisted onebot ingress events, got %+v", counts)
	}
	console := readRuntimeConsoleResponse(t, app)
	if got := consoleMetaStringSlice(t, console.Meta, "onebot_ingress_paths"); !reflect.DeepEqual(got, []string{"/demo/onebot/message", "/demo/onebot/message-beta"}) {
		t.Fatalf("expected onebot ingress paths for configured routes, got %+v", got)
	}
	if got := consoleMetaStringSlice(t, console.Meta, "webhook_ingress_paths"); !reflect.DeepEqual(got, []string{"/ingress/webhook/main"}) {
		t.Fatalf("expected webhook ingress path in mixed config metadata, got %+v", got)
	}
	if got := consoleMetaStringSlice(t, console.Meta, "demo_paths"); !reflect.DeepEqual(got, []string{"/demo/onebot/message", "/demo/onebot/message-beta", "/ingress/webhook/main", "/demo/workflows/message", "/demo/ai/message", "/demo/jobs/enqueue", "/demo/jobs/timeout", "/demo/jobs/{job-id}/pause", "/demo/jobs/{job-id}/resume", "/demo/jobs/{job-id}/cancel", "/demo/jobs/{job-id}/retry", "/demo/schedules/echo-delay", "/demo/schedules/{schedule-id}/cancel", "/demo/plugins/{plugin-id}/disable", "/demo/plugins/{plugin-id}/enable", "/demo/plugins/{plugin-id}/config", "/demo/replies", "/demo/state/counts"}) {
		t.Fatalf("expected mixed demo paths, got %+v", got)
	}
}

func TestRuntimeAppWebhookInstanceUsesRuntimeIntegratedIngress(t *testing.T) {
	t.Setenv("BOT_PLATFORM_WEBHOOK_TOKEN", "test-webhook-token")
	app, err := newRuntimeApp(writeTestConfigWithBotInstancesAt(t, t.TempDir()))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	req := httptest.NewRequest(http.MethodPost, "/ingress/webhook/main", strings.NewReader(`{"event_type":"message.received","source":"client-spoofed","actor_id":"svc-1","text":"hello webhook"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Token", "test-webhook-token")
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected webhook runtime ingress 200, got %d: %s", resp.Code, resp.Body.String())
	}
	var payload struct {
		Status  string `json:"status"`
		EventID string `json:"event_id"`
		TraceID string `json:"trace_id"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode webhook response: %v", err)
	}
	if payload.Status != "ok" || payload.EventID == "" || payload.TraceID == "" {
		t.Fatalf("unexpected webhook response %+v", payload)
	}
	event, err := app.state.LoadEvent(t.Context(), payload.EventID)
	if err != nil {
		t.Fatalf("load persisted webhook event: %v", err)
	}
	if event.Source != "webhook-main" || event.Type != "message.received" {
		t.Fatalf("expected runtime-integrated webhook source/type, got %+v", event)
	}
	if event.Metadata["ingress_source"] != "client-spoofed" || event.Metadata["adapter_instance_id"] != "adapter-webhook-main" {
		t.Fatalf("expected webhook metadata to preserve ingress source and instance id, got %+v", event.Metadata)
	}
	if event.IdempotencyKey == "" || !strings.Contains(event.IdempotencyKey, "webhook:webhook-main:message.received:") {
		t.Fatalf("expected webhook idempotency key to use configured source, got %+v", event)
	}
	if app.replies.Count() != 0 {
		t.Fatalf("expected webhook ingress without reply handle not to emit demo replies, got %+v", app.replies.Since(0))
	}
	dispatches := app.runtime.DispatchResults()
	if len(dispatches) < 3 {
		t.Fatalf("expected runtime dispatch evidence for webhook ingress across registered event handlers, got %+v", dispatches)
	}
	console := readRuntimeConsoleResponse(t, app)
	if got := consoleMetaStringSlice(t, console.Meta, "webhook_ingress_paths"); !reflect.DeepEqual(got, []string{"/ingress/webhook/main"}) {
		t.Fatalf("expected webhook ingress path in console meta, got %+v", got)
	}
	if !hasConsoleAdapter(console, "adapter-webhook-main") {
		t.Fatalf("expected webhook adapter instance in console payload, got %+v", console.Adapters)
	}
	adapter := consoleAdapterByID(t, console, "adapter-webhook-main")
	if adapter.Adapter != "webhook" || adapter.Source != "webhook-main" || !adapter.Online || !adapter.StatePersisted {
		t.Fatalf("expected webhook adapter console facts, got %+v", adapter)
	}
	if adapter.Config["path"] != "/ingress/webhook/main" || adapter.Config["platform"] != "webhook/http" || adapter.Config["source"] != "webhook-main" {
		t.Fatalf("expected webhook adapter config summary, got %+v", adapter.Config)
	}
}

func TestRuntimeAppWebhookInstanceRejectsUnauthorizedRequests(t *testing.T) {
	t.Setenv("BOT_PLATFORM_WEBHOOK_TOKEN", "test-webhook-token")
	app, err := newRuntimeApp(writeTestConfigWithBotInstancesAt(t, t.TempDir()))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	req := httptest.NewRequest(http.MethodPost, "/ingress/webhook/main", strings.NewReader(`{"event_type":"message.received","source":"client-spoofed","actor_id":"svc-1","text":"hello webhook"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Token", "wrong-token")
	req.RemoteAddr = "127.0.0.1:9999"
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("expected webhook runtime ingress 401, got %d: %s", resp.Code, resp.Body.String())
	}
	counts, err := app.state.Counts(t.Context())
	if err != nil {
		t.Fatalf("sqlite counts: %v", err)
	}
	if counts["event_journal"] != 0 || counts["idempotency_keys"] != 0 {
		t.Fatalf("expected unauthorized webhook ingress not to persist events, got %+v", counts)
	}
	if app.replies.Count() != 0 {
		t.Fatalf("expected unauthorized webhook ingress not to emit replies, got %+v", app.replies.Since(0))
	}
	entries := app.audits.AuditEntries()
	foundUnauthorized := false
	for _, entry := range entries {
		if entry.Action == "webhook.reject.unauthorized" && entry.Actor == "127.0.0.1:9999" {
			foundUnauthorized = true
			break
		}
	}
	if !foundUnauthorized {
		t.Fatalf("expected webhook unauthorized audit entry, got %+v", entries)
	}
}

func TestRuntimeAppSmokeStoreFailureDuplicateCheckStopsDispatch(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	stub := newTestRuntimeSmokeStore()
	stub.hasIdempotencyErr = errors.New("duplicate-check failed")
	app.smokeStore = stub

	resp := performRuntimeOneBotMessageRequest(t, app, runtimeDemoOneBotMessageBody(t, 9201, "duplicate check fails"))
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), stub.hasIdempotencyErr.Error()) {
		t.Fatalf("expected duplicate-check error in response, got %s", resp.Body.String())
	}
	if app.replies.Count() != 0 {
		t.Fatalf("expected duplicate-check failure to stop dispatch replies, got %+v", app.replies.Since(0))
	}
	if len(app.runtime.DispatchResults()) != 0 {
		t.Fatalf("expected duplicate-check failure before dispatch, got %+v", app.runtime.DispatchResults())
	}
	if len(stub.hasCalls) != 1 {
		t.Fatalf("expected one duplicate-check attempt, got %+v", stub.hasCalls)
	}
	if len(stub.recordCalls) != 0 || len(stub.saveCalls) != 0 {
		t.Fatalf("expected duplicate-check failure to avoid writes, recordCalls=%+v saveCalls=%+v", stub.recordCalls, stub.saveCalls)
	}
	assertRuntimeSmokeCounts(t, app, 0, 0)
}

func TestRuntimeAppSmokeStoreFailureRecordEventLeavesCountsFlat(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	stub := newTestRuntimeSmokeStore()
	stub.recordEventErr = errors.New("record-event failed")
	app.smokeStore = stub

	resp := performRuntimeOneBotMessageRequest(t, app, runtimeDemoOneBotMessageBody(t, 9202, "record event fails"))
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), stub.recordEventErr.Error()) {
		t.Fatalf("expected record-event error in response, got %s", resp.Body.String())
	}
	if app.replies.Count() != 1 {
		t.Fatalf("expected dispatch to happen before record-event failure, got replies %+v", app.replies.Since(0))
	}
	if len(app.runtime.DispatchResults()) == 0 {
		t.Fatal("expected dispatch results before record-event failure")
	}
	if len(stub.hasCalls) != 1 || len(stub.recordCalls) != 1 {
		t.Fatalf("expected duplicate check then one record-event attempt, hasCalls=%+v recordCalls=%+v", stub.hasCalls, stub.recordCalls)
	}
	if len(stub.saveCalls) != 0 {
		t.Fatalf("expected record-event failure to stop before idempotency write, got %+v", stub.saveCalls)
	}
	assertRuntimeSmokeCounts(t, app, 0, 0)
}

func TestRuntimeAppSmokeStoreFailureSaveIdempotencyExposesAttempt(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	stub := newTestRuntimeSmokeStore()
	stub.saveIdempotencyErr = errors.New("save-idempotency failed")
	app.smokeStore = stub

	resp := performRuntimeOneBotMessageRequest(t, app, runtimeDemoOneBotMessageBody(t, 9203, "save idempotency fails"))
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), stub.saveIdempotencyErr.Error()) {
		t.Fatalf("expected save-idempotency error in response, got %s", resp.Body.String())
	}
	if app.replies.Count() != 1 {
		t.Fatalf("expected dispatch to happen before save-idempotency failure, got replies %+v", app.replies.Since(0))
	}
	if len(app.runtime.DispatchResults()) == 0 {
		t.Fatal("expected dispatch results before save-idempotency failure")
	}
	if len(stub.hasCalls) != 1 || len(stub.recordCalls) != 1 || len(stub.saveCalls) != 1 {
		t.Fatalf("expected duplicate check, event record, and one idempotency attempt, hasCalls=%+v recordCalls=%+v saveCalls=%+v", stub.hasCalls, stub.recordCalls, stub.saveCalls)
	}
	if stub.saveCalls[0].Key == "" || stub.saveCalls[0].EventID == "" {
		t.Fatalf("expected idempotency attempt details to be visible, got %+v", stub.saveCalls[0])
	}
	if stub.saveCalls[0].EventID != stub.recordCalls[0] {
		t.Fatalf("expected idempotency attempt to target recorded event %q, got %+v", stub.recordCalls[0], stub.saveCalls[0])
	}
	assertRuntimeSmokeCounts(t, app, 1, 0)
}

func TestRuntimeAppPostgresSmokeStoreStartupFailsLoudlyOnBadDSN(t *testing.T) {
	t.Parallel()

	configPath := writeTestConfigWithPostgresSmokeStoreAt(t, t.TempDir(), "postgres://127.0.0.1:1/runtime_smoke?sslmode=disable")
	_, err := newRuntimeApp(configPath)
	if err == nil {
		t.Fatal("expected postgres-selected startup to fail")
	}
	for _, expected := range []string{"open runtime smoke store", "postgres"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("expected startup error to include %q, got %v", expected, err)
		}
	}
}

func TestRuntimeAppDemoOneBotMessageWithPostgresSmokeStore(t *testing.T) {
	app := newRuntimePostgresSmokeStoreApp(t)
	defer func() { _ = app.Close() }()

	beforeCounts, err := app.runtimeStateCounts(t.Context())
	if err != nil {
		t.Fatalf("runtime state counts before request: %v", err)
	}
	messageID := time.Now().UnixNano()
	messageBody := runtimeDemoOneBotMessageBody(t, messageID, "hello postgres runtime")
	resp := performRuntimeOneBotMessageRequest(t, app, messageBody)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}
	var payload struct {
		Status    string        `json:"status"`
		Duplicate bool          `json:"duplicate"`
		Replies   []replyRecord `json:"replies"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Status != "ok" || payload.Duplicate {
		t.Fatalf("expected successful non-duplicate response, got %+v", payload)
	}
	if len(payload.Replies) != 1 || payload.Replies[0].Payload != "echo: hello postgres runtime" {
		t.Fatalf("expected postgres smoke path reply, got %+v", payload.Replies)
	}

	countsReq := httptest.NewRequest(http.MethodGet, "/demo/state/counts", nil)
	countsResp := httptest.NewRecorder()
	app.ServeHTTP(countsResp, countsReq)
	if countsResp.Code != http.StatusOK {
		t.Fatalf("expected counts 200, got %d: %s", countsResp.Code, countsResp.Body.String())
	}
	afterCounts, err := app.runtimeStateCounts(t.Context())
	if err != nil {
		t.Fatalf("runtime state counts after request: %v", err)
	}
	if afterCounts["event_journal"] != beforeCounts["event_journal"]+1 {
		t.Fatalf("expected postgres-backed event_journal count to increase by one, before=%+v after=%+v raw=%s", beforeCounts, afterCounts, countsResp.Body.String())
	}
	if afterCounts["idempotency_keys"] != beforeCounts["idempotency_keys"]+1 {
		t.Fatalf("expected postgres-backed idempotency count to increase by one, before=%+v after=%+v raw=%s", beforeCounts, afterCounts, countsResp.Body.String())
	}
	if !strings.Contains(countsResp.Body.String(), `"jobs":0`) && !strings.Contains(countsResp.Body.String(), `"jobs": 0`) {
		t.Fatalf("expected sqlite jobs counter to remain present, got %s", countsResp.Body.String())
	}

	dupReq := httptest.NewRequest(http.MethodPost, "/demo/onebot/message", strings.NewReader(messageBody))
	dupReq.Header.Set("Content-Type", "application/json")
	dupResp := httptest.NewRecorder()
	app.ServeHTTP(dupResp, dupReq)
	if dupResp.Code != http.StatusOK {
		t.Fatalf("expected duplicate request 200, got %d: %s", dupResp.Code, dupResp.Body.String())
	}
	if !strings.Contains(dupResp.Body.String(), `"duplicate":true`) && !strings.Contains(dupResp.Body.String(), `"duplicate": true`) {
		t.Fatalf("expected duplicate response from postgres idempotency store, got %s", dupResp.Body.String())
	}
	afterDuplicateCounts, err := app.runtimeStateCounts(t.Context())
	if err != nil {
		t.Fatalf("runtime state counts after duplicate request: %v", err)
	}
	if afterDuplicateCounts["event_journal"] != afterCounts["event_journal"] {
		t.Fatalf("expected duplicate request to leave postgres-backed event_journal flat, afterFirst=%+v afterDuplicate=%+v", afterCounts, afterDuplicateCounts)
	}
	if afterDuplicateCounts["idempotency_keys"] != afterCounts["idempotency_keys"] {
		t.Fatalf("expected duplicate request to leave postgres-backed idempotency flat, afterFirst=%+v afterDuplicate=%+v", afterCounts, afterDuplicateCounts)
	}

	sqliteCounts, err := app.state.Counts(t.Context())
	if err != nil {
		t.Fatalf("sqlite counts: %v", err)
	}
	if sqliteCounts["event_journal"] != 0 || sqliteCounts["idempotency_keys"] != 0 {
		t.Fatalf("expected sqlite to remain out of smoke persistence path when postgres selected, got %+v", sqliteCounts)
	}
}

func TestRuntimeAppDemoOneBotMessageWithPostgresSmokeStoreAndDirectHost(t *testing.T) {
	dir := t.TempDir()
	directHost := &recordingDirectPluginHost{}
	app, err := newRuntimeAppWithOptions(writeTestConfigWithPostgresSmokeStoreAt(t, dir, runtimePostgresTestDSN(t)), runtimeAppBuildOptions{
		pluginHostFactory: func(*replyBuffer, *runtimecore.WorkflowRuntime) runtimecore.PluginHost {
			return directHost
		},
	})
	if err != nil {
		t.Fatalf("new runtime app with postgres smoke store and direct host: %v", err)
	}
	defer func() { _ = app.Close() }()

	host, ok := app.runtime.PluginHost().(*recordingDirectPluginHost)
	if !ok || host != directHost {
		t.Fatalf("expected injected direct host, got %T", app.runtime.PluginHost())
	}
	if _, ok := app.runtimeState.(*runtimecore.PostgresStore); !ok {
		t.Fatalf("expected runtimeState to stay postgres-backed, got %T", app.runtimeState)
	}
	if _, ok := app.smokeStore.(postgresRuntimeSmokeStore); !ok {
		t.Fatalf("expected postgres smoke store under direct host seam, got %T", app.smokeStore)
	}

	beforeCounts, err := app.runtimeStateCounts(t.Context())
	if err != nil {
		t.Fatalf("runtime state counts before direct-host request: %v", err)
	}

	messageID := time.Now().UnixNano()
	messageBody := runtimeDemoOneBotMessageBody(t, messageID, "hello postgres direct host")
	resp := performRuntimeOneBotMessageRequest(t, app, messageBody)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected direct-host onebot request 200, got %d: %s", resp.Code, resp.Body.String())
	}
	var payload struct {
		Status    string        `json:"status"`
		Duplicate bool          `json:"duplicate"`
		Replies   []replyRecord `json:"replies"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode direct-host response: %v", err)
	}
	if payload.Status != "ok" || payload.Duplicate {
		t.Fatalf("expected successful non-duplicate direct-host response, got %+v", payload)
	}
	if len(payload.Replies) != 1 || payload.Replies[0].Payload != "echo: hello postgres direct host" {
		t.Fatalf("expected direct-host reply payload, got %+v", payload.Replies)
	}
	if app.replies.Count() != 1 {
		t.Fatalf("expected one recorded reply through direct host path, got %+v", app.replies.Since(0))
	}
	directDispatches := directHost.EventPluginIDs()
	if len(directDispatches) == 0 {
		t.Fatal("expected direct host to observe event dispatches")
	}
	pluginEchoSeen := false
	for _, pluginID := range directDispatches {
		if pluginID == "plugin-echo" {
			pluginEchoSeen = true
			break
		}
	}
	if !pluginEchoSeen {
		t.Fatalf("expected direct host path to dispatch plugin-echo, got %+v", directDispatches)
	}

	afterCounts, err := app.runtimeStateCounts(t.Context())
	if err != nil {
		t.Fatalf("runtime state counts after direct-host request: %v", err)
	}
	if afterCounts["event_journal"] != beforeCounts["event_journal"]+1 {
		t.Fatalf("expected postgres event_journal to increase by one under direct host, before=%+v after=%+v", beforeCounts, afterCounts)
	}
	if afterCounts["idempotency_keys"] != beforeCounts["idempotency_keys"]+1 {
		t.Fatalf("expected postgres idempotency_keys to increase by one under direct host, before=%+v after=%+v", beforeCounts, afterCounts)
	}

	duplicateResp := performRuntimeOneBotMessageRequest(t, app, messageBody)
	if duplicateResp.Code != http.StatusOK {
		t.Fatalf("expected duplicate direct-host request 200, got %d: %s", duplicateResp.Code, duplicateResp.Body.String())
	}
	var duplicatePayload struct {
		Status    string        `json:"status"`
		Duplicate bool          `json:"duplicate"`
		Replies   []replyRecord `json:"replies"`
	}
	if err := json.Unmarshal(duplicateResp.Body.Bytes(), &duplicatePayload); err != nil {
		t.Fatalf("decode duplicate direct-host response: %v", err)
	}
	if duplicatePayload.Status != "ok" || !duplicatePayload.Duplicate {
		t.Fatalf("expected duplicate=true for second direct-host request, got %+v", duplicatePayload)
	}

	afterDuplicateCounts, err := app.runtimeStateCounts(t.Context())
	if err != nil {
		t.Fatalf("runtime state counts after duplicate direct-host request: %v", err)
	}
	if afterDuplicateCounts["event_journal"] != afterCounts["event_journal"] {
		t.Fatalf("expected duplicate direct-host request to leave postgres event_journal flat, afterFirst=%+v afterDuplicate=%+v", afterCounts, afterDuplicateCounts)
	}
	if afterDuplicateCounts["idempotency_keys"] != afterCounts["idempotency_keys"] {
		t.Fatalf("expected duplicate direct-host request to leave postgres idempotency_keys flat, afterFirst=%+v afterDuplicate=%+v", afterCounts, afterDuplicateCounts)
	}

	sqliteCounts, err := app.state.Counts(t.Context())
	if err != nil {
		t.Fatalf("sqlite counts under postgres direct-host test: %v", err)
	}
	if sqliteCounts["event_journal"] != 0 || sqliteCounts["idempotency_keys"] != 0 {
		t.Fatalf("expected sqlite smoke tables to remain empty under postgres-selected direct host, got %+v", sqliteCounts)
	}

	console := readRuntimeConsoleResponse(t, app)
	if got := consoleMetaString(t, console.Meta, "smoke_store_backend"); got != "postgres" {
		t.Fatalf("expected smoke_store_backend=postgres, got %q", got)
	}
	if got := consoleMetaString(t, console.Meta, "job_read_model"); got != "postgres" {
		t.Fatalf("expected job_read_model=postgres under direct host, got %q", got)
	}
	if got := consoleMetaString(t, console.Meta, "job_status_source"); got != "postgres-jobs" {
		t.Fatalf("expected job_status_source=postgres-jobs under direct host, got %q", got)
	}
	if got := consoleMetaString(t, console.Meta, "schedule_read_model"); got != "postgres" {
		t.Fatalf("expected schedule_read_model=postgres under direct host, got %q", got)
	}
	if got := consoleMetaString(t, console.Meta, "schedule_status_source"); got != "postgres-schedule-plans" {
		t.Fatalf("expected schedule_status_source=postgres-schedule-plans under direct host, got %q", got)
	}
	if got := consoleMetaString(t, console.Meta, "workflow_read_model"); got != "postgres-workflow-instances" {
		t.Fatalf("expected workflow_read_model=postgres-workflow-instances under direct host, got %q", got)
	}
	if got := consoleMetaString(t, console.Meta, "workflow_status_source"); got != "postgres-workflow-instances" {
		t.Fatalf("expected workflow_status_source=postgres-workflow-instances under direct host, got %q", got)
	}
}

func TestRuntimeAppDemoOneBotMessageWithSQLiteSmokeStoreAndDirectHost(t *testing.T) {
	dir := t.TempDir()
	directHost := &recordingDirectPluginHost{}
	app, err := newRuntimeAppWithOptions(writeTestConfigAt(t, dir), runtimeAppBuildOptions{
		pluginHostFactory: func(*replyBuffer, *runtimecore.WorkflowRuntime) runtimecore.PluginHost {
			return directHost
		},
	})
	if err != nil {
		t.Fatalf("new runtime app with sqlite smoke store and direct host: %v", err)
	}
	defer func() { _ = app.Close() }()

	host, ok := app.runtime.PluginHost().(*recordingDirectPluginHost)
	if !ok || host != directHost {
		t.Fatalf("expected injected direct host, got %T", app.runtime.PluginHost())
	}
	if _, ok := app.runtimeState.(*runtimecore.SQLiteStateStore); !ok {
		t.Fatalf("expected runtimeState to stay sqlite-backed, got %T", app.runtimeState)
	}
	if _, ok := app.smokeStore.(sqliteRuntimeSmokeStore); !ok {
		t.Fatalf("expected sqlite smoke store under direct host seam, got %T", app.smokeStore)
	}

	beforeCounts, err := app.runtimeStateCounts(t.Context())
	if err != nil {
		t.Fatalf("runtime state counts before direct-host sqlite request: %v", err)
	}

	messageID := time.Now().UnixNano()
	messageBody := runtimeDemoOneBotMessageBody(t, messageID, "hello sqlite direct host")
	resp := performRuntimeOneBotMessageRequest(t, app, messageBody)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected direct-host sqlite onebot request 200, got %d: %s", resp.Code, resp.Body.String())
	}
	var payload struct {
		Status    string        `json:"status"`
		Duplicate bool          `json:"duplicate"`
		Replies   []replyRecord `json:"replies"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode direct-host sqlite response: %v", err)
	}
	if payload.Status != "ok" || payload.Duplicate {
		t.Fatalf("expected successful non-duplicate sqlite direct-host response, got %+v", payload)
	}
	if len(payload.Replies) != 1 || payload.Replies[0].Payload != "echo: hello sqlite direct host" {
		t.Fatalf("expected sqlite direct-host reply payload, got %+v", payload.Replies)
	}
	if app.replies.Count() != 1 {
		t.Fatalf("expected one recorded reply through sqlite direct host path, got %+v", app.replies.Since(0))
	}
	directDispatches := directHost.EventPluginIDs()
	if len(directDispatches) == 0 {
		t.Fatal("expected sqlite direct host to observe event dispatches")
	}
	pluginEchoSeen := false
	for _, pluginID := range directDispatches {
		if pluginID == "plugin-echo" {
			pluginEchoSeen = true
			break
		}
	}
	if !pluginEchoSeen {
		t.Fatalf("expected sqlite direct host path to dispatch plugin-echo, got %+v", directDispatches)
	}

	afterCounts, err := app.runtimeStateCounts(t.Context())
	if err != nil {
		t.Fatalf("runtime state counts after direct-host sqlite request: %v", err)
	}
	if afterCounts["event_journal"] != beforeCounts["event_journal"]+1 {
		t.Fatalf("expected sqlite event_journal to increase by one under direct host, before=%+v after=%+v", beforeCounts, afterCounts)
	}
	if afterCounts["idempotency_keys"] != beforeCounts["idempotency_keys"]+1 {
		t.Fatalf("expected sqlite idempotency_keys to increase by one under direct host, before=%+v after=%+v", beforeCounts, afterCounts)
	}

	duplicateResp := performRuntimeOneBotMessageRequest(t, app, messageBody)
	if duplicateResp.Code != http.StatusOK {
		t.Fatalf("expected duplicate sqlite direct-host request 200, got %d: %s", duplicateResp.Code, duplicateResp.Body.String())
	}
	var duplicatePayload struct {
		Status    string        `json:"status"`
		Duplicate bool          `json:"duplicate"`
		Replies   []replyRecord `json:"replies"`
	}
	if err := json.Unmarshal(duplicateResp.Body.Bytes(), &duplicatePayload); err != nil {
		t.Fatalf("decode duplicate sqlite direct-host response: %v", err)
	}
	if duplicatePayload.Status != "ok" || !duplicatePayload.Duplicate {
		t.Fatalf("expected duplicate=true for second sqlite direct-host request, got %+v", duplicatePayload)
	}

	afterDuplicateCounts, err := app.runtimeStateCounts(t.Context())
	if err != nil {
		t.Fatalf("runtime state counts after duplicate sqlite direct-host request: %v", err)
	}
	if afterDuplicateCounts["event_journal"] != afterCounts["event_journal"] {
		t.Fatalf("expected duplicate sqlite direct-host request to leave event_journal flat, afterFirst=%+v afterDuplicate=%+v", afterCounts, afterDuplicateCounts)
	}
	if afterDuplicateCounts["idempotency_keys"] != afterCounts["idempotency_keys"] {
		t.Fatalf("expected duplicate sqlite direct-host request to leave idempotency_keys flat, afterFirst=%+v afterDuplicate=%+v", afterCounts, afterDuplicateCounts)
	}

	console := readRuntimeConsoleResponse(t, app)
	if got := consoleMetaString(t, console.Meta, "smoke_store_backend"); got != "sqlite" {
		t.Fatalf("expected smoke_store_backend=sqlite, got %q", got)
	}
	if got := consoleMetaString(t, console.Meta, "job_read_model"); got != "sqlite" {
		t.Fatalf("expected job_read_model=sqlite under direct host, got %q", got)
	}
	if got := consoleMetaString(t, console.Meta, "job_status_source"); got != "sqlite-jobs" {
		t.Fatalf("expected job_status_source=sqlite-jobs under direct host, got %q", got)
	}
	if got := consoleMetaString(t, console.Meta, "schedule_read_model"); got != "sqlite" {
		t.Fatalf("expected schedule_read_model=sqlite under direct host, got %q", got)
	}
	if got := consoleMetaString(t, console.Meta, "schedule_status_source"); got != "sqlite-schedule-plans" {
		t.Fatalf("expected schedule_status_source=sqlite-schedule-plans under direct host, got %q", got)
	}
	if got := consoleMetaString(t, console.Meta, "workflow_read_model"); got != "sqlite-workflow-instances" {
		t.Fatalf("expected workflow_read_model=sqlite-workflow-instances under direct host, got %q", got)
	}
	if got := consoleMetaString(t, console.Meta, "workflow_status_source"); got != "sqlite-workflow-instances" {
		t.Fatalf("expected workflow_status_source=sqlite-workflow-instances under direct host, got %q", got)
	}
}

func TestRuntimeAppPostgresSmokeStoreClosedCountsEndpointFailsLoudly(t *testing.T) {
	app := newRuntimePostgresSmokeStoreApp(t)
	defer func() { _ = app.Close() }()

	if err := app.smokeStore.Close(); err != nil {
		t.Fatalf("close postgres smoke store: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/demo/state/counts", nil)
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, req)

	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("expected counts 500 after closing postgres smoke store, got %d: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "load postgres smoke counts") {
		t.Fatalf("expected counts failure to identify postgres smoke counts path, got %s", resp.Body.String())
	}
	if !strings.Contains(strings.ToLower(resp.Body.String()), "closed") {
		t.Fatalf("expected counts failure to mention closed smoke store, got %s", resp.Body.String())
	}
}

func TestRuntimeAppPostgresSmokeStoreClosedHealthzShowsStorageError(t *testing.T) {
	app := newRuntimePostgresSmokeStoreApp(t)
	defer func() { _ = app.Close() }()

	waitForSchedulerRunningState(t, app, true)
	if err := app.smokeStore.Close(); err != nil {
		t.Fatalf("close postgres smoke store: %v", err)
	}

	statusCode, payload := readRuntimeHealthResponse(t, app)
	if statusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected healthz 503 after closing postgres smoke store, got %d: %+v", statusCode, payload)
	}
	if payload.Status != "error" {
		t.Fatalf("expected overall health error, got %+v", payload)
	}
	if payload.Components.Storage.Status != "error" {
		t.Fatalf("expected storage error after closing postgres smoke store, got %+v", payload)
	}
	if !strings.Contains(payload.Components.Storage.Error, "load postgres smoke counts") {
		t.Fatalf("expected storage error to identify postgres smoke counts path, got %+v", payload)
	}
	if payload.Components.Scheduler.Status != "ok" || !payload.Components.Scheduler.Running {
		t.Fatalf("expected scheduler visibility to remain ok/running during postgres smoke failure, got %+v", payload)
	}
}

func TestRuntimeAppConsoleReflectsRuntimeState(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	req := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	resp := httptest.NewRecorder()

	app.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "plugin-echo") {
		t.Fatalf("expected console payload to include plugin-echo, got %s", resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"adapters": 1`) {
		t.Fatalf("expected console payload to report one adapter, got %s", resp.Body.String())
	}
	for _, expected := range []string{`"id": "adapter-onebot-demo"`, `"adapter": "onebot"`, `"source": "onebot"`, `"status": "registered"`, `"health": "ready"`, `"online": true`, `"statePersisted": true`, `"adapter_read_model": "sqlite-adapter-instances"`, `"adapter_state_persisted": true`, `"adapter_operator_scope": "already-registered adapters only"`, `"adapter_status_model": "persisted-registered-instance-status"`} {
		if !strings.Contains(resp.Body.String(), expected) {
			t.Fatalf("expected console payload to include %s, got %s", expected, resp.Body.String())
		}
	}
	if !strings.Contains(resp.Body.String(), `"plugins": 4`) {
		t.Fatalf("expected console payload to report four registered plugins, got %s", resp.Body.String())
	}
	for _, expected := range []string{`"publish": {`, `"sourceType": "git"`, `"sourceUri": "https://github.com/ohmyopencode/bot-platform/tree/main/plugins/plugin-echo"`, `"runtimeVersionRange": "\u003e=0.1.0 \u003c1.0.0"`} {
		if !strings.Contains(resp.Body.String(), expected) {
			t.Fatalf("expected console payload to include %s, got %s", expected, resp.Body.String())
		}
	}
	if !strings.Contains(resp.Body.String(), `"runtime_entry": "apps/runtime"`) {
		t.Fatalf("expected console payload to include runtime meta, got %s", resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"scheduler_running": true`) {
		t.Fatalf("expected console payload to include scheduler_running=true, got %s", resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"ai_job_dispatcher_registered": true`) {
		t.Fatalf("expected console payload to include ai_job_dispatcher_registered=true, got %s", resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"job_read_model": "sqlite"`) || !strings.Contains(resp.Body.String(), `"schedule_read_model": "sqlite"`) {
		t.Fatalf("expected console payload to include sqlite read model metadata, got %s", resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"plugin_read_model": "runtime-registry+sqlite-plugin-status-snapshot"`) {
		t.Fatalf("expected console payload to include plugin_read_model=runtime-registry+sqlite-plugin-status-snapshot, got %s", resp.Body.String())
	}
	for _, expected := range []string{`"plugin_config_state_read_model": "runtime-registry+sqlite-plugin-config"`, `"plugin_config_state_kind": "plugin-owned-persisted-input"`, `"plugin_config_state_persisted": true`, `"plugin_config_operator_actions": [`, `"plugin_config_operator_scope": "plugins with app-local persisted config bindings only"`, `"plugin_enabled_state_read_model": "runtime-registry+sqlite-plugin-enabled-overlay"`, `"plugin_enabled_state_persisted": true`, `"plugin_operator_scope": "already-registered plugins only"`, `"workflow_read_model": "sqlite-workflow-instances"`, `"workflow_status_source": "sqlite-workflow-instances"`, `"workflow_status_persisted": true`, `"workflow_runtime_owner": "runtime-core"`, `"workflow_child_job_resume_path": "runtime-owned queued job terminal outcome -\u003e workflow resume"`, `"workflow_reference_demo": "event trigger -\u003e workflow wait -\u003e child job -\u003e suspend/restore -\u003e compensation"`, `"/demo/plugins/{plugin-id}/enable"`, `"/demo/plugins/{plugin-id}/disable"`, `"/demo/plugins/{plugin-id}/config"`, `"/demo/jobs/{job-id}/pause"`, `"/demo/jobs/{job-id}/resume"`, `"/demo/jobs/{job-id}/cancel"`, `"/demo/jobs/{job-id}/retry"`, `"/demo/schedules/{schedule-id}/cancel"`, `"job_operator_scope": "queued jobs for pause|resume|cancel, dead-letter jobs for retry"`, `"console_mode": "read+operator-plugin-enable-disable+plugin-config+job-control+schedule-cancel"`, `"enabled": true`, `"enabledStateSource": "runtime-default-enabled"`, `"enabledStatePersisted": false`, `"configStateKind": "plugin-owned-persisted-input"`, `"configPersisted": false`} {
		if !strings.Contains(resp.Body.String(), expected) {
			t.Fatalf("expected console payload to include %s, got %s", expected, resp.Body.String())
		}
	}
	console := readRuntimeConsoleResponse(t, app)
	expectedDemoPaths := []string{
		"/demo/onebot/message",
		"/demo/workflows/message",
		"/demo/ai/message",
		"/demo/jobs/enqueue",
		"/demo/jobs/timeout",
		"/demo/jobs/{job-id}/pause",
		"/demo/jobs/{job-id}/resume",
		"/demo/jobs/{job-id}/cancel",
		"/demo/jobs/{job-id}/retry",
		"/demo/schedules/echo-delay",
		"/demo/schedules/{schedule-id}/cancel",
		"/demo/plugins/{plugin-id}/disable",
		"/demo/plugins/{plugin-id}/enable",
		"/demo/plugins/{plugin-id}/config",
		"/demo/replies",
		"/demo/state/counts",
	}
	if got := consoleMetaString(t, console.Meta, "console_mode"); got != "read+operator-plugin-enable-disable+plugin-config+job-control+schedule-cancel" {
		t.Fatalf("expected console_mode to advertise plugin-config capability, got %q", got)
	}
	if got := consoleMetaString(t, console.Meta, "plugin_config_operator_scope"); got != "plugins with app-local persisted config bindings only" {
		t.Fatalf("expected plugin_config_operator_scope to describe binding-driven capability, got %q", got)
	}
	if got := consoleMetaStringSlice(t, console.Meta, "plugin_config_operator_actions"); !reflect.DeepEqual(got, []string{"/demo/plugins/{plugin-id}/config"}) {
		t.Fatalf("expected exact plugin_config_operator_actions, got %+v", got)
	}
	if got := consoleMetaStringSlice(t, console.Meta, "job_operator_actions"); !reflect.DeepEqual(got, []string{"/demo/jobs/{job-id}/pause", "/demo/jobs/{job-id}/resume", "/demo/jobs/{job-id}/cancel", "/demo/jobs/{job-id}/retry"}) {
		t.Fatalf("expected exact job_operator_actions, got %+v", got)
	}
	if got := consoleMetaString(t, console.Meta, "job_operator_scope"); got != "queued jobs for pause|resume|cancel, dead-letter jobs for retry" {
		t.Fatalf("expected job_operator_scope to describe retry capability, got %q", got)
	}
	if got := consoleMetaStringSlice(t, console.Meta, "demo_paths"); !reflect.DeepEqual(got, expectedDemoPaths) {
		t.Fatalf("expected exact demo_paths, got %+v", got)
	}
	if got := consoleMetaStringSlice(t, console.Meta, "onebot_ingress_paths"); !reflect.DeepEqual(got, []string{"/demo/onebot/message"}) {
		t.Fatalf("expected onebot ingress paths, got %+v", got)
	}
	if got := consoleMetaStringSlice(t, console.Meta, "webhook_ingress_paths"); len(got) != 0 {
		t.Fatalf("expected no webhook ingress paths in default config, got %+v", got)
	}
	if !strings.Contains(resp.Body.String(), `"plugin_dispatch_source": "sqlite-plugin-status-snapshot+runtime-dispatch-results"`) {
		t.Fatalf("expected console payload to include plugin_dispatch_source=sqlite-plugin-status-snapshot+runtime-dispatch-results, got %s", resp.Body.String())
	}
	for _, expected := range []string{`"plugin_status_source": "runtime-registry+sqlite-plugin-status-snapshot+runtime-dispatch-results"`, `"plugin_status_evidence_model": "manifest-static-or-last-persisted-plugin-snapshot-with-live-overlay"`, `"plugin_dispatch_kind_visibility": "last-persisted-or-live-dispatch-kind"`, `"plugin_recovery_visibility": "last-dispatch-failed|last-dispatch-succeeded|recovered-after-failure|no-runtime-evidence"`, `"plugin_status_staleness": "static-registration|persisted-snapshot|persisted-snapshot+live-overlay|process-local-volatile"`, `"plugin_status_staleness_reason": "persisted plugin snapshots survive restart while current-process live overlay remains explicitly distinguished from the stored snapshot"`, `"plugin_runtime_state_live": true`, `"rbac_capability_surface": "read-only declaration of current authorization and adjacent dispatch-boundary facts"`, `"rbac_read_model_scope": "current runtime authorizer entrypoints, adjacent dispatch contract/filter boundaries, deny audit taxonomy, and known system gaps"`, `"rbac_current_state": "persisted-runtime-current-snapshot"`, `"rbac_system_model_state": "not-complete-global-rbac-authn-or-audit-system"`, `"rbac_current_authorization_paths_count": 9`, `"rbac_deny_audit_scope": "authorizer deny paths only"`, `"rbac_manifest_permission_gate_audited": false`, `"rbac_manifest_permission_gate_boundary": "independent dispatch contract check; not part of deny audit taxonomy"`, `"rbac_job_target_plugin_filter_boundary": "dispatch filter only; not an authorizer entrypoint or deny audit taxonomy item"`, `"rbac_console_read_permission": false`, `"rbac_console_read_actor_header": "X-Bot-Platform-Actor"`, `"secrets_provider": "env"`, `"secrets_runtime_owned_ref_prefix": "BOT_PLATFORM_"`, `"rollout_record_store": "sqlite-current-runtime-rollout-operations"`} {
		if !strings.Contains(resp.Body.String(), expected) {
			t.Fatalf("expected console payload to include %s, got %s", expected, resp.Body.String())
		}
	}
	for _, expected := range []string{`"admin-command-runtime-authorizer"`, `"event-metadata-runtime-authorizer"`, `"job-metadata-runtime-authorizer"`, `"schedule-metadata-runtime-authorizer"`, `"schedule-operator-runtime-authorizer"`, `"job-operator-runtime-authorizer"`, `"plugin-config-runtime-authorizer"`, `"plugin-admin-current-authorizer-provider"`, `"dispatch-manifest-permission-gate"`, `"job-target-plugin-filter"`, `"console-read-authorizer"`, `"permission_denied"`, `"plugin_scope_denied"`, `"unified-authentication"`, `"unified-resource-model"`, `"independent-authorization-read-model"`, `"actor"`, `"permission"`, `"target_plugin_id"`, `"console read authorization is optional and only enforced when rbac.console_read_permission is configured"`, `"console read authorization uses bearer-backed request identity when operator_auth.tokens is configured; otherwise it falls back to the X-Bot-Platform-Actor header"`, `"manifest permission gate remains a separate dispatch contract check and does not emit deny audit entries"`, `"target_plugin_id remains a dispatch filter, not a global RBAC resource kind"`, `"current slice persists and reloads a single runtime snapshot but does not add login/authn UX or a global resource hierarchy"`} {
		if !strings.Contains(resp.Body.String(), expected) {
			t.Fatalf("expected console payload to include RBAC declaration detail %s, got %s", expected, resp.Body.String())
		}
	}
	for _, expected := range []string{`"secrets.webhook_token_ref"`, `"adapter-webhook.NewWithSecretRef"`, `"secret.read"`, `"secret-write-api"`, `generic secret resolution failures`, `"/admin prepare \u003cplugin-id\u003e"`, `"prepared-record-required"`, `"replay_record_read_model": "sqlite-replay-operation-records"`, `"rollout_record_read_model": "sqlite-rollout-operation-records"`, `"rollout_head_read_model": "sqlite-rollout-heads"`, `"job_status_source": "sqlite-jobs"`, `"schedule_status_source": "sqlite-schedule-plans"`} {
		if !strings.Contains(resp.Body.String(), expected) {
			t.Fatalf("expected console payload to include %s, got %s", expected, resp.Body.String())
		}
	}
	for _, expected := range []string{`"plugin_status_persisted": true`, `"job_status_persisted": true`, `"schedule_status_persisted": true`} {
		if !strings.Contains(resp.Body.String(), expected) {
			t.Fatalf("expected console payload to include %s, got %s", expected, resp.Body.String())
		}
	}
	if !strings.Contains(resp.Body.String(), `"mixed_read_model": true`) {
		t.Fatalf("expected console payload to include mixed_read_model=true, got %s", resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"snapshot_atomic": true`) {
		t.Fatalf("expected console payload to include snapshot_atomic=true, got %s", resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"generated_at":`) {
		t.Fatalf("expected console payload to include generated_at timestamp, got %s", resp.Body.String())
	}
	pluginLifecycleReq := httptest.NewRequest(http.MethodGet, "/api/console?plugin_id=plugin-admin", nil)
	pluginLifecycleResp := httptest.NewRecorder()
	app.ServeHTTP(pluginLifecycleResp, pluginLifecycleReq)
	if pluginLifecycleResp.Code != http.StatusOK {
		t.Fatalf("expected filtered plugin lifecycle console 200, got %d: %s", pluginLifecycleResp.Code, pluginLifecycleResp.Body.String())
	}
	for _, expected := range []string{`"id": "plugin-admin"`, `"statusLevel": "registered"`, `"statusRecovery": "no-runtime-evidence"`, `"statusStaleness": "static-registration"`} {
		if !strings.Contains(pluginLifecycleResp.Body.String(), expected) {
			t.Fatalf("expected filtered plugin lifecycle payload to include %s, got %s", expected, pluginLifecycleResp.Body.String())
		}
	}
}

func TestRuntimeConsoleExposesRolloutHeadCurrentTruthAlongsideHistory(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	updatedAt := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	if err := app.controlState.SaveRolloutOperationRecord(context.Background(), runtimecore.RolloutOperationRecord{
		OperationID:      "rollout-op-runtime-console-1",
		PluginID:         "plugin-echo",
		Action:           "canary",
		CurrentVersion:   "0.1.0",
		CandidateVersion: "0.2.0-candidate",
		Status:           "canarying",
		OccurredAt:       updatedAt,
		UpdatedAt:        updatedAt,
	}); err != nil {
		t.Fatalf("save rollout operation record: %v", err)
	}
	if err := app.controlState.SaveRolloutHead(context.Background(), runtimecore.RolloutHeadState{
		PluginID:        "plugin-echo",
		Stable:          runtimecore.RolloutSnapshotState{Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess},
		Active:          runtimecore.RolloutSnapshotState{Version: "0.2.0-candidate", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess},
		Candidate:       &runtimecore.RolloutSnapshotState{Version: "0.2.0-candidate", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess},
		Phase:           string(pluginsdk.RolloutPhaseCanary),
		Status:          string(pluginsdk.RolloutStatusCanarying),
		LastOperationID: "rollout-op-runtime-console-1",
		UpdatedAt:       updatedAt,
	}); err != nil {
		t.Fatalf("save rollout head: %v", err)
	}

	consoleReq := httptest.NewRequest(http.MethodGet, "/api/console?plugin_id=plugin-echo", nil)
	consoleResp := httptest.NewRecorder()
	app.ServeHTTP(consoleResp, consoleReq)
	if consoleResp.Code != http.StatusOK {
		t.Fatalf("expected console 200, got %d: %s", consoleResp.Code, consoleResp.Body.String())
	}
	for _, expected := range []string{`"rolloutHeads": [`, `"pluginId": "plugin-echo"`, `"phase": "canary"`, `"status": "canarying"`, `"stable": {`, `"active": {`, `"candidate": {`, `"version": "0.2.0-candidate"`, `"lastOperationId": "rollout-op-runtime-console-1"`, `"stateSource": "sqlite-rollout-heads"`, `"summary": "rollout head canary canarying for plugin-echo`, `"rolloutOps": [`, `"action": "canary"`, `"rollout_head_read_model": "sqlite-rollout-heads"`} {
		if !strings.Contains(consoleResp.Body.String(), expected) {
			t.Fatalf("expected runtime console rollout payload to include %q, got %s", expected, consoleResp.Body.String())
		}
	}
}

func TestRuntimeAppOperatorDisablePersistsOverlaySkipsDispatchAndReEnables(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	disableReq := httptest.NewRequest(http.MethodPost, "/demo/plugins/plugin-echo/disable", nil)
	disableReq.Header.Set(runtimecore.ConsoleReadActorHeader, "admin-user")
	disableResp := httptest.NewRecorder()
	app.ServeHTTP(disableResp, disableReq)
	if disableResp.Code != http.StatusOK {
		t.Fatalf("expected disable operator 200, got %d: %s", disableResp.Code, disableResp.Body.String())
	}
	var disablePayload operatorPluginResponsePayload
	if err := json.Unmarshal(disableResp.Body.Bytes(), &disablePayload); err != nil {
		t.Fatalf("decode disable response: %v", err)
	}
	if disablePayload.Status != "ok" || disablePayload.Action != "plugin.disable" || disablePayload.Target != "plugin-echo" || !disablePayload.Accepted || disablePayload.Reason != "plugin_disabled" || disablePayload.PluginID != "plugin-echo" || disablePayload.Enabled == nil || *disablePayload.Enabled {
		t.Fatalf("expected normalized disable response, got %+v", disablePayload)
	}

	state, err := app.state.LoadPluginEnabledState(t.Context(), "plugin-echo")
	if err != nil {
		t.Fatalf("load disabled plugin state: %v", err)
	}
	if state.Enabled {
		t.Fatalf("expected persisted plugin state disabled, got %+v", state)
	}
	counts, err := app.state.Counts(t.Context())
	if err != nil {
		t.Fatalf("sqlite counts: %v", err)
	}
	if counts["plugin_enabled_overlays"] != 1 {
		t.Fatalf("expected one persisted plugin enabled overlay, got %+v", counts)
	}

	messageReq := httptest.NewRequest(http.MethodPost, "/demo/onebot/message", strings.NewReader(`{"post_type":"message","message_type":"group","time":1712034000,"user_id":10001,"group_id":42,"message_id":9101,"raw_message":"disabled plugin should skip","sender":{"nickname":"alice"}}`))
	messageReq.Header.Set("Content-Type", "application/json")
	messageResp := httptest.NewRecorder()
	app.ServeHTTP(messageResp, messageReq)
	if messageResp.Code != http.StatusOK {
		t.Fatalf("expected disabled plugin event dispatch to keep app response successful, got %d: %s", messageResp.Code, messageResp.Body.String())
	}
	if strings.Contains(messageResp.Body.String(), "echo: disabled plugin should skip") {
		t.Fatalf("expected disabled plugin not to emit echo reply, got %s", messageResp.Body.String())
	}
	if app.replies.Count() != 0 {
		t.Fatalf("expected disabled plugin to suppress replies, got %+v", app.replies.Since(0))
	}
	if strings.Contains(messageResp.Body.String(), `"PluginID":"plugin-echo"`) || strings.Contains(messageResp.Body.String(), `"PluginID": "plugin-echo"`) {
		t.Fatalf("expected disabled plugin to be absent from dispatch results, got %s", messageResp.Body.String())
	}

	consoleReq := httptest.NewRequest(http.MethodGet, "/api/console?plugin_id=plugin-echo", nil)
	consoleResp := httptest.NewRecorder()
	app.ServeHTTP(consoleResp, consoleReq)
	for _, expected := range []string{`"id": "plugin-echo"`, `"enabled": false`, `"enabledStateSource": "sqlite-plugin-enabled-overlay"`, `"enabledStatePersisted": true`, `"enabledStateUpdatedAt":`} {
		if !strings.Contains(consoleResp.Body.String(), expected) {
			t.Fatalf("expected disabled plugin console payload to include %q, got %s", expected, consoleResp.Body.String())
		}
	}

	enableReq := httptest.NewRequest(http.MethodPost, "/demo/plugins/plugin-echo/enable", nil)
	enableReq.Header.Set(runtimecore.ConsoleReadActorHeader, "admin-user")
	enableResp := httptest.NewRecorder()
	app.ServeHTTP(enableResp, enableReq)
	if enableResp.Code != http.StatusOK {
		t.Fatalf("expected enable operator 200, got %d: %s", enableResp.Code, enableResp.Body.String())
	}
	var enablePayload operatorPluginResponsePayload
	if err := json.Unmarshal(enableResp.Body.Bytes(), &enablePayload); err != nil {
		t.Fatalf("decode enable response: %v", err)
	}
	if enablePayload.Status != "ok" || enablePayload.Action != "plugin.enable" || enablePayload.Target != "plugin-echo" || !enablePayload.Accepted || enablePayload.Reason != "plugin_enabled" || enablePayload.PluginID != "plugin-echo" || enablePayload.Enabled == nil || !*enablePayload.Enabled {
		t.Fatalf("expected normalized enable response, got %+v", enablePayload)
	}

	messageReq2 := httptest.NewRequest(http.MethodPost, "/demo/onebot/message", strings.NewReader(`{"post_type":"message","message_type":"group","time":1712034001,"user_id":10001,"group_id":42,"message_id":9102,"raw_message":"re-enabled plugin runs","sender":{"nickname":"alice"}}`))
	messageReq2.Header.Set("Content-Type", "application/json")
	messageResp2 := httptest.NewRecorder()
	app.ServeHTTP(messageResp2, messageReq2)
	if messageResp2.Code != http.StatusOK {
		t.Fatalf("expected re-enabled dispatch 200, got %d: %s", messageResp2.Code, messageResp2.Body.String())
	}
	if !strings.Contains(messageResp2.Body.String(), "echo: re-enabled plugin runs") {
		t.Fatalf("expected re-enabled plugin to reply again, got %s", messageResp2.Body.String())
	}

	entries := app.audits.AuditEntries()
	if len(entries) < 2 {
		t.Fatalf("expected admin operator audits for disable/enable, got %+v", entries)
	}
	if entries[0].Actor != "admin-user" || entries[0].Permission != "plugin:disable" || entries[0].Action != "plugin.disable" || entries[0].Target != "plugin-echo" || !entries[0].Allowed || entries[0].Reason != "plugin_disabled" || entries[0].PluginID != "plugin-admin" || entries[0].CorrelationID != "plugin-echo" || entries[0].ErrorCategory != "operator" || entries[0].ErrorCode != "plugin_disabled" {
		t.Fatalf("expected first audit entry for disable, got %+v", entries[0])
	}
	if entries[1].Actor != "admin-user" || entries[1].Permission != "plugin:enable" || entries[1].Action != "plugin.enable" || entries[1].Target != "plugin-echo" || !entries[1].Allowed || entries[1].Reason != "plugin_enabled" || entries[1].PluginID != "plugin-admin" || entries[1].CorrelationID != "plugin-echo" || entries[1].ErrorCategory != "operator" || entries[1].ErrorCode != "plugin_enabled" {
		t.Fatalf("expected second audit entry for enable, got %+v", entries[1])
	}
}

func TestRuntimeAppPluginEchoConfigOperatorRejectsInvalidConfig(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeWriteActionRBACConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	req := httptest.NewRequest(http.MethodPost, "/demo/plugins/plugin-echo/config", strings.NewReader(`{"prefix":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(runtimecore.ConsoleReadActorHeader, "config-operator")
	resp := httptest.NewRecorder()

	app.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `plugin-echo config property "prefix" must be a string`) {
		t.Fatalf("expected caller-facing prefix validation error, got %s", resp.Body.String())
	}
	if _, err := app.state.LoadPluginConfig(t.Context(), "plugin-echo"); err == nil {
		t.Fatal("expected invalid config not to be persisted")
	}
	if entries := app.audits.AuditEntries(); len(entries) != 0 {
		t.Fatalf("expected invalid config to avoid allow/deny audit records, got %+v", entries)
	}
}

func TestRuntimeAppReloadsPersistedPluginEchoConfigAfterRestart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := writeWriteActionRBACConfigAt(t, dir)

	app, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}

	configReq := httptest.NewRequest(http.MethodPost, "/demo/plugins/plugin-echo/config", strings.NewReader(`{"prefix":"persisted: "}`))
	configReq.Header.Set("Content-Type", "application/json")
	configReq.Header.Set(runtimecore.ConsoleReadActorHeader, "config-operator")
	configResp := httptest.NewRecorder()
	app.ServeHTTP(configResp, configReq)
	if configResp.Code != http.StatusOK {
		t.Fatalf("expected config operator 200, got %d: %s", configResp.Code, configResp.Body.String())
	}
	stored, err := app.state.LoadPluginConfig(t.Context(), "plugin-echo")
	if err != nil {
		t.Fatalf("load persisted plugin config: %v", err)
	}
	if string(stored.RawConfig) != `{"prefix":"persisted: "}` {
		t.Fatalf("expected persisted raw config, got %s", string(stored.RawConfig))
	}
	if err := app.Close(); err != nil {
		t.Fatalf("close first app: %v", err)
	}

	restarted, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("restart runtime app: %v", err)
	}
	defer func() { _ = restarted.Close() }()
	restartedHost := runtimeRoutedPluginHost(t, restarted)
	if _, routed := restartedHost.routes["plugin-echo"]; !routed {
		t.Fatalf("expected restarted runtime to keep plugin-echo on subprocess route, routes=%+v", restartedHost.routes)
	}

	messageReq := httptest.NewRequest(http.MethodPost, "/demo/onebot/message", strings.NewReader(`{"post_type":"message","message_type":"group","time":1712034000,"user_id":10001,"group_id":42,"message_id":9901,"raw_message":"hello restart","sender":{"nickname":"alice"}}`))
	messageReq.Header.Set("Content-Type", "application/json")
	messageResp := httptest.NewRecorder()
	restarted.ServeHTTP(messageResp, messageReq)

	if messageResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", messageResp.Code, messageResp.Body.String())
	}
	if !strings.Contains(messageResp.Body.String(), "persisted: hello restart") {
		t.Fatalf("expected restarted runtime to use persisted echo prefix, got %s", messageResp.Body.String())
	}
	restartedSubprocessHost := runtimeSubprocessHost(t, restartedHost)
	restartedStdout, restartedStderr := waitForRuntimeSubprocessCapture(t, restartedSubprocessHost, func(stdout string, stderr string) bool {
		return strings.Contains(stderr, "runtime-plugin-echo-subprocess-online") && strings.Contains(stdout, `"callback":"reply_text"`)
	})
	if !strings.Contains(restartedStderr, "runtime-plugin-echo-subprocess-online") {
		t.Fatalf("expected restarted runtime to boot echo subprocess, stderr=%s stdout=%s", restartedStderr, restartedStdout)
	}
	if !strings.Contains(restartedStdout, `"callback":"reply_text"`) {
		t.Fatalf("expected restarted runtime to send persisted echo reply through subprocess callback bridge, stderr=%s stdout=%s", restartedStderr, restartedStdout)
	}

	consoleReq := consoleRequestWithViewer("/api/console?plugin_id=plugin-echo")
	consoleResp := httptest.NewRecorder()
	restarted.ServeHTTP(consoleResp, consoleReq)
	if consoleResp.Code != http.StatusOK {
		t.Fatalf("expected console 200 after restart, got %d: %s", consoleResp.Code, consoleResp.Body.String())
	}
	for _, expected := range []string{
		`"id": "plugin-echo"`,
		`"publish": {`,
		`"sourceType": "git"`,
		`"sourceUri": "https://github.com/ohmyopencode/bot-platform/tree/main/plugins/plugin-echo"`,
		`"runtimeVersionRange": "\u003e=0.1.0 \u003c1.0.0"`,
		`"configStateKind": "plugin-owned-persisted-input"`,
		`"configSource": "sqlite-plugin-config"`,
		`"configPersisted": true`,
		`"enabledStateSource": "runtime-default-enabled"`,
		`"statusPersisted": true`,
	} {
		if !strings.Contains(consoleResp.Body.String(), expected) {
			t.Fatalf("expected restarted console payload to include %q, got %s", expected, consoleResp.Body.String())
		}
	}
	if !strings.Contains(consoleResp.Body.String(), `"configUpdatedAt":`) {
		t.Fatalf("expected restarted console payload to include configUpdatedAt, got %s", consoleResp.Body.String())
	}
	consoleReq = consoleRequestWithViewer("/api/console")
	consoleResp = httptest.NewRecorder()
	restarted.ServeHTTP(consoleResp, consoleReq)
	if consoleResp.Code != http.StatusOK {
		t.Fatalf("expected console 200 after restart, got %d: %s", consoleResp.Code, consoleResp.Body.String())
	}
	var console runtimeConsoleResponse
	if err := json.Unmarshal(consoleResp.Body.Bytes(), &console); err != nil {
		t.Fatalf("decode console payload: %v", err)
	}
	var pluginFound bool
	for _, plugin := range console.Plugins {
		if plugin.ID != "plugin-echo" {
			continue
		}
		pluginFound = true
		if plugin.Publish == nil || plugin.Publish.SourceType != "git" || plugin.Publish.SourceURI != "https://github.com/ohmyopencode/bot-platform/tree/main/plugins/plugin-echo" || plugin.Publish.RuntimeVersionRange != ">=0.1.0 <1.0.0" {
			t.Fatalf("expected plugin publish metadata after restart, got %+v", plugin.Publish)
		}
		if plugin.ConfigStateKind != "plugin-owned-persisted-input" || plugin.ConfigSource != "sqlite-plugin-config" || !plugin.ConfigPersisted || plugin.ConfigUpdatedAt == "" {
			t.Fatalf("expected persisted plugin config contract after restart, got %+v", plugin)
		}
		if plugin.EnabledStateSource != "runtime-default-enabled" || plugin.EnabledStatePersisted {
			t.Fatalf("expected enabled overlay to stay distinct from persisted plugin config, got %+v", plugin)
		}
		if plugin.StatusSource == "" || !plugin.StatusPersisted {
			t.Fatalf("expected status snapshot to stay distinct from persisted plugin config, got %+v", plugin)
		}
	}
	if !pluginFound {
		t.Fatalf("expected plugin-echo in restarted console payload, got %+v", console.Plugins)
	}
}

func TestRuntimeAppDefaultPluginHostRoutingBlocksDisallowedPluginEchoSourcesBeforeSubprocess(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	host := runtimeRoutedPluginHost(t, app)
	if _, routed := host.routes["plugin-echo"]; !routed {
		t.Fatalf("expected plugin-echo to use subprocess routing by default, routes=%+v", host.routes)
	}
	if err := app.runtime.DispatchEvent(t.Context(), eventmodel.Event{
		EventID:        "evt-disallowed-plugin-echo-source",
		TraceID:        "trace-disallowed-plugin-echo-source",
		Source:         "runtime-ai",
		Type:           "message.received",
		Timestamp:      time.Now().UTC(),
		IdempotencyKey: "idempotency-disallowed-plugin-echo-source",
		Actor:          &eventmodel.Actor{ID: "user-1", Type: "user", DisplayName: "alice"},
		Channel:        &eventmodel.Channel{ID: "group-42", Type: "group", Title: "group-42"},
		Message:        &eventmodel.Message{ID: "msg-disallowed-plugin-echo-source", Text: "should stay blocked"},
		Reply: &eventmodel.ReplyHandle{
			Capability: "onebot.reply",
			TargetID:   "group-42",
			MessageID:  "msg-disallowed-plugin-echo-source",
			Metadata: map[string]any{
				"message_type": "group",
				"group_id":     42,
				"user_id":      10001,
			},
		},
	}); err != nil {
		t.Fatalf("dispatch disallowed plugin-echo event: %v", err)
	}
	if app.replies.Count() != 0 {
		t.Fatalf("expected disallowed source to produce no replies, got %+v", app.replies.Since(0))
	}
	subprocessHost := runtimeSubprocessHost(t, host)
	stdout, stderr := waitForRuntimeSubprocessCapture(t, subprocessHost, func(stdout string, stderr string) bool {
		return strings.TrimSpace(stdout) != "" || strings.TrimSpace(stderr) != ""
	})
	if strings.TrimSpace(stdout) != "" || strings.TrimSpace(stderr) != "" {
		t.Fatalf("expected disallowed source to avoid subprocess boot, stderr=%s stdout=%s", stderr, stdout)
	}
}

func TestRuntimeAppWorkflowDemoPersistsAndRecoversAcrossRestart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	exporter := runtimecore.NewInMemoryTraceExporter()
	configPath := writeTestConfigWithTraceExporterAt(t, dir, true, "test", "memory://wave5a-workflow")

	app, err := newRuntimeAppWithOptions(configPath, runtimeAppBuildOptions{traceExporter: exporter})
	if err != nil {
		t.Fatalf(`new runtime app: %v`, err)
	}
	startResp := performRuntimeWorkflowMessageRequest(t, app, `{"actor_id":"user-1","message":"start workflow"}`)
	if startResp.Code != http.StatusOK {
		t.Fatalf(`expected workflow start 200, got %d: %s`, startResp.Code, startResp.Body.String())
	}
	if !strings.Contains(startResp.Body.String(), `workflow started, child job queued; wait for its result, then send another message to continue`) {
		t.Fatalf(`expected workflow start reply, got %s`, startResp.Body.String())
	}
	started, err := app.state.LoadWorkflowInstance(t.Context(), `workflow-user-1`)
	if err != nil {
		t.Fatalf(`load started workflow instance: %v`, err)
	}
	if started.PluginID != `plugin-workflow-demo` || started.Status != runtimecore.WorkflowRuntimeStatusWaitingJob {
		t.Fatalf(`expected waiting persisted workflow after first request, got %+v`, started)
	}
	if started.Workflow.WaitingForJob == nil || started.Workflow.State[`greeting`] != `start workflow` || started.Workflow.Completed {
		t.Fatalf(`expected workflow runtime to persist waiting child-job state after first request, got %+v`, started.Workflow)
	}
	startedObservability := loadRuntimeWorkflowObservabilityRow(t, app, `workflow-user-1`)
	if startedObservability.TraceID == `` || startedObservability.EventID == `` || startedObservability.RunID == `` || startedObservability.CorrelationID == `` {
		t.Fatalf(`expected started workflow to persist observability ids, got %+v`, startedObservability)
	}
	startedTraceID := startedObservability.TraceID
	startedEventID := startedObservability.EventID
	startedRunID := startedObservability.RunID
	startedCorrelationID := startedObservability.CorrelationID
	var startedPayload struct {
		EventID    string        `json:"event_id"`
		TraceID    string        `json:"trace_id"`
		WorkflowID string        `json:"workflow_id"`
		Replies    []replyRecord `json:"replies"`
	}
	if err := json.Unmarshal(startResp.Body.Bytes(), &startedPayload); err != nil {
		t.Fatalf(`decode workflow start response: %v`, err)
	}
	if startedPayload.EventID != startedEventID || startedPayload.TraceID != startedTraceID || startedPayload.WorkflowID != `workflow-user-1` {
		t.Fatalf(`expected workflow start response ids to match persisted workflow observability row, got response=%+v persisted=%+v`, startedPayload, startedObservability)
	}
	if len(startedPayload.Replies) != 1 || startedPayload.Replies[0].Payload != `workflow started, child job queued; wait for its result, then send another message to continue` {
		t.Fatalf(`expected workflow start response to expose reply payload, got %+v`, startedPayload.Replies)
	}
	if rendered := app.tracer.RenderTrace(startedTraceID); !strings.Contains(rendered, `runtime.dispatch event_id=`+startedEventID) || !strings.Contains(rendered, `plugin_host.dispatch event_id=`+startedEventID+` plugin_id=plugin-workflow-demo`) || !strings.Contains(rendered, `reply.send event_id=`+startedEventID+` plugin_id=plugin-workflow-demo`) {
		t.Fatalf(`expected workflow start trace to correlate runtime, plugin host, and reply spans, got %s`, rendered)
	}
	startedExported := exportedTraceSpans(t, exporter, startedTraceID)
	startedSpanNames := map[string]bool{}
	for _, span := range startedExported {
		startedSpanNames[span.SpanName] = true
	}
	for _, spanName := range []string{"runtime.event.dispatch", "plugin.dispatch", "plugin_host.dispatch", "reply.dispatch"} {
		if !startedSpanNames[spanName] {
			t.Fatalf(`expected exported workflow start trace to include %q, got %+v`, spanName, startedExported)
		}
	}
	entries := app.logs.Lines()
	matchedStartReply := false
	for _, line := range entries {
		if strings.Contains(line, `runtime demo reply recorded`) && strings.Contains(line, startedTraceID) && strings.Contains(line, startedEventID) && strings.Contains(line, startedRunID) && strings.Contains(line, startedCorrelationID) && strings.Contains(line, `plugin-workflow-demo`) {
			matchedStartReply = true
			break
		}
	}
	if !matchedStartReply {
		t.Fatalf(`expected workflow start reply log to recover five observability ids, got %+v`, entries)
	}
	if err := app.Close(); err != nil {
		t.Fatalf(`close first app: %v`, err)
	}

	restarted, err := newRuntimeAppWithOptions(configPath, runtimeAppBuildOptions{traceExporter: exporter})
	if err != nil {
		t.Fatalf(`restart runtime app: %v`, err)
	}
	defer func() { _ = restarted.Close() }()
	recovery := restarted.workflowRuntime.LastRecoverySnapshot()
	if recovery.TotalWorkflows != 1 || recovery.RecoveredWorkflows != 1 || recovery.StatusCounts[runtimecore.WorkflowRuntimeStatusWaitingJob] != 1 {
		t.Fatalf(`expected workflow runtime recovery snapshot after restart, got %+v`, recovery)
	}
	childJobID := "job-workflow-demo-workflow-user-1"
	cancelChildReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/"+childJobID+"/cancel", nil)
	cancelChildReq.Header.Set(runtimecore.ConsoleReadActorHeader, "job-operator")
	cancelChildResp := httptest.NewRecorder()
	restarted.ServeHTTP(cancelChildResp, cancelChildReq)
	if cancelChildResp.Code != http.StatusOK {
		t.Fatalf(`cancel workflow child job before resume through operator path: %d %s`, cancelChildResp.Code, cancelChildResp.Body.String())
	}
	resumeResp := performRuntimeWorkflowMessageRequest(t, restarted, `{"actor_id":"user-1","message":"continue"}`)
	if resumeResp.Code != http.StatusOK {
		t.Fatalf(`expected workflow resume 200, got %d: %s`, resumeResp.Code, resumeResp.Body.String())
	}
	if !strings.Contains(resumeResp.Body.String(), `workflow resumed after child job failure and compensation completed`) {
		t.Fatalf(`expected workflow resume reply, got %s`, resumeResp.Body.String())
	}
	completed, err := restarted.state.LoadWorkflowInstance(t.Context(), `workflow-user-1`)
	if err != nil {
		t.Fatalf(`load completed workflow instance: %v`, err)
	}
	if completed.Status != runtimecore.WorkflowRuntimeStatusCompleted || !completed.Workflow.Completed || !completed.Workflow.Compensated {
		t.Fatalf(`expected completed persisted workflow after restart resume, got %+v`, completed)
	}
	completedObservability := loadRuntimeWorkflowObservabilityRow(t, restarted, `workflow-user-1`)
	if completedObservability.TraceID != startedTraceID || completedObservability.EventID != startedEventID || completedObservability.RunID != startedRunID || completedObservability.CorrelationID != startedCorrelationID {
		t.Fatalf(`expected workflow resume to keep origin observability ids stable, got %+v`, completedObservability)
	}
	if completed.LastEventID == completedObservability.EventID {
		t.Fatalf(`expected workflow cursor to track latest trigger while origin event_id stays stable, got %+v`, completed)
	}
	restartedEntries := restarted.logs.Lines()
	matchedResumeReply := false
	for _, line := range restartedEntries {
		if strings.Contains(line, `runtime demo reply recorded`) && strings.Contains(line, completedObservability.TraceID) && strings.Contains(line, completedObservability.EventID) && strings.Contains(line, completedObservability.RunID) && strings.Contains(line, completedObservability.CorrelationID) && strings.Contains(line, `plugin-workflow-demo`) {
			matchedResumeReply = true
			break
		}
	}
	if !matchedResumeReply {
		t.Fatalf(`expected workflow resume reply log to correlate back to origin ids, got %+v`, restartedEntries)
	}
	var resumedPayload struct {
		EventID    string        `json:"event_id"`
		TraceID    string        `json:"trace_id"`
		WorkflowID string        `json:"workflow_id"`
		Replies    []replyRecord `json:"replies"`
	}
	if err := json.Unmarshal(resumeResp.Body.Bytes(), &resumedPayload); err != nil {
		t.Fatalf(`decode workflow resume response: %v`, err)
	}
	if resumedPayload.EventID != completed.LastEventID || resumedPayload.WorkflowID != `workflow-user-1` {
		t.Fatalf(`expected workflow resume response to expose latest trigger event and stable workflow id, got response=%+v completed=%+v`, resumedPayload, completed)
	}
	if resumedPayload.TraceID == `` || resumedPayload.TraceID == startedTraceID {
		t.Fatalf(`expected workflow resume response to carry the live trigger trace while persistence keeps origin trace stable, got start=%q resume=%+v`, startedTraceID, resumedPayload)
	}
	if len(resumedPayload.Replies) != 1 || resumedPayload.Replies[0].Payload != `workflow resumed after child job failure and compensation completed` {
		t.Fatalf(`expected workflow resume response to expose reply payload, got %+v`, resumedPayload.Replies)
	}
	if rendered := restarted.tracer.RenderTrace(completedObservability.TraceID); !strings.Contains(rendered, `reply.send event_id=`+startedEventID+` plugin_id=plugin-workflow-demo`) {
		t.Fatalf(`expected origin workflow trace to preserve correlation into resumed reply span, got %s`, rendered)
	}
	if resumedRendered := restarted.tracer.RenderTrace(resumedPayload.TraceID); !strings.Contains(resumedRendered, `runtime.dispatch event_id=`+resumedPayload.EventID) || !strings.Contains(resumedRendered, `plugin_host.dispatch event_id=`+resumedPayload.EventID+` plugin_id=plugin-workflow-demo`) {
		t.Fatalf(`expected live resume trace to show dispatch through current runtime surfaces, got %s`, resumedRendered)
	}
	resumedExported := exportedTraceSpans(t, exporter, resumedPayload.TraceID)
	resumedSpanNames := map[string]bool{}
	for _, span := range resumedExported {
		resumedSpanNames[span.SpanName] = true
	}
	for _, spanName := range []string{"runtime.event.dispatch", "plugin.dispatch", "plugin_host.dispatch"} {
		if !resumedSpanNames[spanName] {
			t.Fatalf(`expected exported workflow resume trace to include %q, got %+v`, spanName, resumedExported)
		}
	}
	originExported := exportedTraceSpans(t, exporter, completedObservability.TraceID)
	originReplyExported := false
	for _, span := range originExported {
		if span.SpanName == "reply.dispatch" && span.EventID == startedEventID && span.PluginID == "plugin-workflow-demo" {
			originReplyExported = true
			break
		}
	}
	if !originReplyExported {
		t.Fatalf(`expected exporter path to retain resumed reply correlation on origin workflow trace, got %+v`, originExported)
	}
	consoleReq := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	consoleResp := httptest.NewRecorder()
	restarted.ServeHTTP(consoleResp, consoleReq)
	if consoleResp.Code != http.StatusOK {
		t.Fatalf(`expected console 200 after workflow recovery, got %d: %s`, consoleResp.Code, consoleResp.Body.String())
	}
	if !strings.Contains(consoleResp.Body.String(), `"workflow_read_model": "sqlite-workflow-instances"`) {
		t.Fatalf(`expected console meta to expose workflow read model, got %s`, consoleResp.Body.String())
	}
	if !strings.Contains(consoleResp.Body.String(), `"trace_source": "runtime-trace-recorder+test-exporter"`) {
		t.Fatalf(`expected console meta to expose local exporter path, got %s`, consoleResp.Body.String())
	}
	var console runtimeConsoleResponse
	if err := json.Unmarshal(consoleResp.Body.Bytes(), &console); err != nil {
		t.Fatalf(`decode console payload: %v`, err)
	}
	if !hasConsoleWorkflow(console, `workflow-user-1`) {
		t.Fatalf(`expected console payload to expose persisted workflow instance, got %+v`, console.Workflows)
	}
	for _, workflow := range console.Workflows {
		if workflow.ID != `workflow-user-1` {
			continue
		}
		if workflow.PluginID != `plugin-workflow-demo` || workflow.Status != `completed` || !workflow.Completed || !workflow.Compensated {
			t.Fatalf(`expected completed workflow facts in console payload, got %+v`, workflow)
		}
		if workflow.LastJobResult == nil || workflow.LastJobResult[`status`] != string(runtimecore.JobStatusCancelled) {
			t.Fatalf(`expected console workflow payload to expose last child job result, got %+v`, workflow)
		}
		if workflow.TraceID != startedTraceID || workflow.EventID != startedEventID || workflow.RunID != startedRunID || workflow.CorrelationID != startedCorrelationID {
			t.Fatalf(`expected console workflow payload to expose stable origin observability ids, got %+v`, workflow)
		}
		if workflow.StatusSource != `sqlite-workflow-instances` || !workflow.StatePersisted || workflow.RuntimeOwner != `runtime-core` {
			t.Fatalf(`expected workflow console provenance metadata, got %+v`, workflow)
		}
		return
	}
	t.Fatalf(`expected workflow-user-1 in console payload, got %+v`, console.Workflows)
}

func TestRuntimeAppWorkflowDemoRestoresAcrossRestartWithPostgresSelectedBackend(t *testing.T) {
	dir := t.TempDir()
	configPath := writeTestConfigWithPostgresSmokeStoreAt(t, dir, runtimePostgresTestDSN(t))

	app, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("new runtime app with postgres workflow restore config: %v", err)
	}
	startResp := performRuntimeWorkflowMessageRequest(t, app, `{"actor_id":"postgres-restore-user","message":"start workflow"}`)
	if startResp.Code != http.StatusOK {
		t.Fatalf("expected workflow start 200, got %d: %s", startResp.Code, startResp.Body.String())
	}
	if !strings.Contains(startResp.Body.String(), `workflow started, child job queued; wait for its result, then send another message to continue`) {
		t.Fatalf("expected workflow start reply, got %s", startResp.Body.String())
	}

	workflowID := "workflow-postgres-restore-user"
	postgresStore, ok := app.runtimeState.(*runtimecore.PostgresStore)
	if !ok {
		t.Fatalf("expected postgres runtime state for workflow restore path, got %T", app.runtimeState)
	}
	started, err := postgresStore.LoadWorkflowInstance(t.Context(), workflowID)
	if err != nil {
		t.Fatalf("load postgres workflow instance before restart: %v", err)
	}
	if started.PluginID != "plugin-workflow-demo" || started.Status != runtimecore.WorkflowRuntimeStatusWaitingJob {
		t.Fatalf("expected waiting postgres workflow before restart, got %+v", started)
	}
	if started.Workflow.WaitingForJob == nil || started.Workflow.State["greeting"] != "start workflow" || started.Workflow.Completed {
		t.Fatalf("expected persisted postgres workflow waiting child-job state, got %+v", started.Workflow)
	}
	startedTraceID := started.TraceID
	startedEventID := started.EventID
	startedRunID := started.RunID
	startedCorrelationID := started.CorrelationID
	if startedTraceID == "" || startedEventID == "" || startedRunID == "" || startedCorrelationID == "" {
		t.Fatalf("expected postgres workflow observability ids before restart, got %+v", started)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("close first app: %v", err)
	}

	restarted, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("restart runtime app with postgres workflow restore config: %v", err)
	}
	defer func() { _ = restarted.Close() }()

	recovery := restarted.workflowRuntime.LastRecoverySnapshot()
	if recovery.TotalWorkflows != 1 || recovery.RecoveredWorkflows != 1 || recovery.StatusCounts[runtimecore.WorkflowRuntimeStatusWaitingJob] != 1 {
		t.Fatalf("expected postgres workflow recovery snapshot after restart, got %+v", recovery)
	}
	cancelChildReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/job-workflow-demo-workflow-postgres-restore-user/cancel", nil)
	cancelChildReq.Header.Set(runtimecore.ConsoleReadActorHeader, "job-operator")
	cancelChildResp := httptest.NewRecorder()
	restarted.ServeHTTP(cancelChildResp, cancelChildReq)
	if cancelChildResp.Code != http.StatusOK {
		t.Fatalf("cancel postgres workflow child job before resume through operator path: %d %s", cancelChildResp.Code, cancelChildResp.Body.String())
	}
	resumeResp := performRuntimeWorkflowMessageRequest(t, restarted, `{"actor_id":"postgres-restore-user","message":"continue"}`)
	if resumeResp.Code != http.StatusOK {
		t.Fatalf("expected workflow resume 200, got %d: %s", resumeResp.Code, resumeResp.Body.String())
	}
	if !strings.Contains(resumeResp.Body.String(), `workflow resumed after child job failure and compensation completed`) {
		t.Fatalf("expected workflow resume reply, got %s", resumeResp.Body.String())
	}

	restartedPostgresStore, ok := restarted.runtimeState.(*runtimecore.PostgresStore)
	if !ok {
		t.Fatalf("expected restarted runtime state to remain postgres-backed, got %T", restarted.runtimeState)
	}
	completed, err := restartedPostgresStore.LoadWorkflowInstance(t.Context(), workflowID)
	if err != nil {
		t.Fatalf("load postgres workflow instance after restart: %v", err)
	}
	if completed.Status != runtimecore.WorkflowRuntimeStatusCompleted || !completed.Workflow.Completed || !completed.Workflow.Compensated {
		t.Fatalf("expected completed postgres workflow after restart resume, got %+v", completed)
	}
	if completed.TraceID != startedTraceID || completed.EventID != startedEventID || completed.RunID != startedRunID || completed.CorrelationID != startedCorrelationID {
		t.Fatalf("expected postgres workflow origin observability ids to stay stable, got %+v", completed)
	}
	if completed.LastEventID == completed.EventID {
		t.Fatalf("expected postgres workflow cursor to move to latest trigger while origin event id stays stable, got %+v", completed)
	}

	counts, err := restarted.runtimeStateCounts(t.Context())
	if err != nil {
		t.Fatalf("runtime state counts after postgres workflow restore: %v", err)
	}
	if counts["workflow_instances"] < 1 {
		t.Fatalf("expected postgres workflow counts after restore, got %+v", counts)
	}
	sqliteCounts, err := restarted.state.Counts(t.Context())
	if err != nil {
		t.Fatalf("sqlite counts after postgres workflow restore: %v", err)
	}
	if sqliteCounts["workflow_instances"] != 0 {
		t.Fatalf("expected sqlite workflow table to remain empty under postgres restore path, got %+v", sqliteCounts)
	}

	console := readRuntimeConsoleResponseAsViewer(t, restarted, "/api/console?plugin_id=plugin-workflow-demo")
	if got := consoleMetaString(t, console.Meta, "workflow_read_model"); got != "postgres-workflow-instances" {
		t.Fatalf("expected workflow_read_model=postgres-workflow-instances, got %q", got)
	}
	if got := consoleMetaString(t, console.Meta, "workflow_status_source"); got != "postgres-workflow-instances" {
		t.Fatalf("expected workflow_status_source=postgres-workflow-instances, got %q", got)
	}
	for _, workflow := range console.Workflows {
		if workflow.ID != workflowID {
			continue
		}
		if workflow.PluginID != "plugin-workflow-demo" || workflow.Status != "completed" || !workflow.Completed || !workflow.Compensated {
			t.Fatalf("expected postgres console workflow facts after restore, got %+v", workflow)
		}
		if workflow.LastJobResult == nil || workflow.LastJobResult[`status`] != string(runtimecore.JobStatusCancelled) {
			t.Fatalf("expected postgres console workflow child-job result after restore, got %+v", workflow)
		}
		if workflow.TraceID != startedTraceID || workflow.EventID != startedEventID || workflow.RunID != startedRunID || workflow.CorrelationID != startedCorrelationID {
			t.Fatalf("expected postgres console workflow origin observability ids after restore, got %+v", workflow)
		}
		if workflow.StatusSource != "postgres-workflow-instances" || !workflow.StatePersisted || workflow.RuntimeOwner != "runtime-core" {
			t.Fatalf("expected postgres console workflow provenance after restore, got %+v", workflow)
		}
		return
	}
	t.Fatalf("expected %s in postgres console payload after restore, got %+v", workflowID, console.Workflows)
}

func TestRuntimeAppWorkflowDemoStartsFreshRunForSameActorAfterCompletion(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeWriteActionRBACConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	startResp := performRuntimeWorkflowMessageRequest(t, app, `{"actor_id":"repeat-actor","message":"first run"}`)
	if startResp.Code != http.StatusOK {
		t.Fatalf("expected first workflow start 200, got %d: %s", startResp.Code, startResp.Body.String())
	}
	cancelFirstReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/job-workflow-demo-workflow-repeat-actor/cancel", nil)
	cancelFirstReq.Header.Set(runtimecore.ConsoleReadActorHeader, "job-operator")
	cancelFirstResp := httptest.NewRecorder()
	app.ServeHTTP(cancelFirstResp, cancelFirstReq)
	if cancelFirstResp.Code != http.StatusOK {
		t.Fatalf("expected first child job cancel 200, got %d: %s", cancelFirstResp.Code, cancelFirstResp.Body.String())
	}
	continueResp := performRuntimeWorkflowMessageRequest(t, app, `{"actor_id":"repeat-actor","message":"complete first run"}`)
	if continueResp.Code != http.StatusOK {
		t.Fatalf("expected first workflow continue 200, got %d: %s", continueResp.Code, continueResp.Body.String())
	}
	if !strings.Contains(continueResp.Body.String(), `workflow resumed after child job failure and compensation completed`) {
		t.Fatalf("expected compensated completion reply, got %s", continueResp.Body.String())
	}

	secondStartResp := performRuntimeWorkflowMessageRequest(t, app, `{"actor_id":"repeat-actor","message":"second run"}`)
	if secondStartResp.Code != http.StatusOK {
		t.Fatalf("expected second workflow start 200, got %d: %s", secondStartResp.Code, secondStartResp.Body.String())
	}
	var secondPayload struct {
		WorkflowID string `json:"workflow_id"`
	}
	if err := json.Unmarshal(secondStartResp.Body.Bytes(), &secondPayload); err != nil {
		t.Fatalf("decode second workflow start response: %v", err)
	}
	if secondPayload.WorkflowID == `workflow-repeat-actor` || !strings.HasPrefix(secondPayload.WorkflowID, `workflow-repeat-actor-run-`) {
		t.Fatalf("expected fresh workflow run id for repeated actor, got %+v", secondPayload)
	}
	console := readRuntimeConsoleResponseAsViewer(t, app, "/api/console?plugin_id=plugin-workflow-demo")
	freshWaiting := false
	completedBase := false
	for _, workflow := range console.Workflows {
		switch workflow.ID {
		case `workflow-repeat-actor`:
			if workflow.Status == `completed` {
				completedBase = true
			}
		default:
			if workflow.ID == secondPayload.WorkflowID && workflow.Status == `waiting_job` && workflow.WaitingForJob != nil {
				freshWaiting = true
			}
		}
	}
	if !completedBase || !freshWaiting {
		t.Fatalf("expected completed base run plus fresh waiting run in console payload, got %+v", console.Workflows)
	}
}

func TestRuntimeAppConsoleReturnsForbiddenWhenConsoleReadAuthorizationDenies(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeConsoleRBACConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	req := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	resp := httptest.NewRecorder()

	app.ServeHTTP(resp, req)

	if resp.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "permission denied") {
		t.Fatalf("expected forbidden response to mention permission denied, got %s", resp.Body.String())
	}
	entries := app.audits.AuditEntries()
	if len(entries) != 1 {
		t.Fatalf("expected runtime app to record one deny audit entry, got %+v", entries)
	}
	if entries[0].Action != "console.read" || entries[0].Permission != "console:read" || entries[0].Target != "console" || entries[0].Allowed || entries[0].Actor != "" || entries[0].Reason != "permission_denied" {
		t.Fatalf("expected runtime app deny audit entry, got %+v", entries[0])
	}
}

func TestRuntimeAppConsoleAllowsConfiguredActorHeader(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeConsoleRBACConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	req := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	req.Header.Set(runtimecore.ConsoleReadActorHeader, "viewer-user")
	resp := httptest.NewRecorder()

	app.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "plugin-echo") {
		t.Fatalf("expected allowed console response to include plugin-echo, got %s", resp.Body.String())
	}
}

func TestRuntimeAppConsoleReadHotReloadsPersistedRBACSnapshotWithoutRestart(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeConsoleRBACConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	allowedReq := consoleRequestWithViewer("/api/console")
	allowedResp := httptest.NewRecorder()
	app.ServeHTTP(allowedResp, allowedReq)
	if allowedResp.Code != http.StatusOK {
		t.Fatalf("expected hot-reload seed console read 200, got %d: %s", allowedResp.Code, allowedResp.Body.String())
	}

	replaceRuntimeCurrentRBACState(t, app, []runtimecore.OperatorIdentityState{{ActorID: "viewer-user", Roles: []string{"viewer-no-console"}}}, runtimecore.RBACSnapshotState{
		SnapshotKey:           runtimecore.CurrentRBACSnapshotKey,
		ConsoleReadPermission: "console:read",
		Policies: map[string]pluginsdk.AuthorizationPolicy{
			"viewer-no-console": {Permissions: []string{"job:retry"}, PluginScope: []string{"*"}},
		},
	})

	deniedReq := consoleRequestWithViewer("/api/console")
	deniedResp := httptest.NewRecorder()
	app.ServeHTTP(deniedResp, deniedReq)
	if deniedResp.Code != http.StatusForbidden {
		t.Fatalf("expected same-process hot-reloaded console read 403, got %d: %s", deniedResp.Code, deniedResp.Body.String())
	}
	if !strings.Contains(deniedResp.Body.String(), "permission denied") {
		t.Fatalf("expected hot-reloaded console denial to mention permission denied, got %s", deniedResp.Body.String())
	}
	entries := app.audits.AuditEntries()
	if len(entries) != 1 || entries[0].Actor != "viewer-user" || entries[0].Action != "console.read" || entries[0].Permission != "console:read" || entries[0].Target != "console" || entries[0].Allowed || entries[0].Reason != "permission_denied" {
		t.Fatalf("expected console hot-reload deny audit entry, got %+v", entries)
	}
}

func TestRuntimeAppPluginConfigHotReloadsPersistedRBACSnapshotWithoutRestart(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeWriteActionRBACConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	allowedReq := httptest.NewRequest(http.MethodPost, "/demo/plugins/plugin-echo/config", strings.NewReader(`{"prefix":"allowed before reload: "}`))
	allowedReq.Header.Set("Content-Type", "application/json")
	allowedReq.Header.Set(runtimecore.ConsoleReadActorHeader, "config-operator")
	allowedResp := httptest.NewRecorder()
	app.ServeHTTP(allowedResp, allowedReq)
	if allowedResp.Code != http.StatusOK {
		t.Fatalf("expected hot-reload seed config update 200, got %d: %s", allowedResp.Code, allowedResp.Body.String())
	}

	replaceRuntimeCurrentRBACState(t, app, []runtimecore.OperatorIdentityState{{ActorID: "config-operator", Roles: []string{"viewer"}}}, runtimecore.RBACSnapshotState{
		SnapshotKey:           runtimecore.CurrentRBACSnapshotKey,
		ConsoleReadPermission: "console:read",
		Policies: map[string]pluginsdk.AuthorizationPolicy{
			"viewer": {Permissions: []string{"console:read"}, PluginScope: []string{"console"}},
		},
	})

	deniedReq := httptest.NewRequest(http.MethodPost, "/demo/plugins/plugin-echo/config", strings.NewReader(`{"prefix":"denied after reload: "}`))
	deniedReq.Header.Set("Content-Type", "application/json")
	deniedReq.Header.Set(runtimecore.ConsoleReadActorHeader, "config-operator")
	deniedResp := httptest.NewRecorder()
	app.ServeHTTP(deniedResp, deniedReq)
	if deniedResp.Code != http.StatusForbidden {
		t.Fatalf("expected same-process hot-reloaded config update 403, got %d: %s", deniedResp.Code, deniedResp.Body.String())
	}
	if !strings.Contains(deniedResp.Body.String(), "permission denied") {
		t.Fatalf("expected hot-reloaded config denial to mention permission denied, got %s", deniedResp.Body.String())
	}
	stored, err := app.state.LoadPluginConfig(t.Context(), "plugin-echo")
	if err != nil {
		t.Fatalf("load persisted plugin config after hot reload: %v", err)
	}
	if string(stored.RawConfig) != `{"prefix":"allowed before reload: "}` {
		t.Fatalf("expected denied hot-reload config update not to overwrite persisted config, got %s", string(stored.RawConfig))
	}
	entries := app.audits.AuditEntries()
	if len(entries) != 2 {
		t.Fatalf("expected allow+deny config audit entries, got %+v", entries)
	}
	last := entries[len(entries)-1]
	if last.Actor != "config-operator" || last.Action != "plugin.config" || last.Permission != "plugin:config" || last.Target != "plugin-echo" || last.Allowed || last.Reason != "permission_denied" {
		t.Fatalf("expected hot-reload denied config audit entry, got %+v", last)
	}
}

func TestRuntimeAppRetryHotReloadsPersistedRBACSnapshotWithoutRestart(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeWriteActionRBACConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	jobAllowed := runtimecore.NewJob("job-dead-retry-hot-reload-allowed", "ai.chat", 0, 30*time.Second)
	jobAllowed.TraceID = "trace-job-dead-retry-hot-reload-allowed"
	jobAllowed.EventID = "evt-job-dead-retry-hot-reload-allowed"
	jobAllowed.Correlation = "runtime-ai:user-hot-reload-allowed:retry"
	if err := applyDemoAIJobContract(&jobAllowed, "retry hot reload allowed", "user-hot-reload-allowed"); err != nil {
		t.Fatalf("apply allowed demo ai job contract: %v", err)
	}
	if err := app.queue.Enqueue(t.Context(), jobAllowed); err != nil {
		t.Fatalf("enqueue allowed ai retry seed job: %v", err)
	}
	timeoutAllowedReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/timeout?id="+jobAllowed.ID, nil)
	timeoutAllowedResp := httptest.NewRecorder()
	app.ServeHTTP(timeoutAllowedResp, timeoutAllowedReq)
	if timeoutAllowedResp.Code != http.StatusOK {
		t.Fatalf("expected allowed seed timeout 200, got %d: %s", timeoutAllowedResp.Code, timeoutAllowedResp.Body.String())
	}

	retryAllowedReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/"+jobAllowed.ID+"/retry", nil)
	retryAllowedReq.Header.Set(runtimecore.ConsoleReadActorHeader, "job-operator")
	retryAllowedResp := httptest.NewRecorder()
	app.ServeHTTP(retryAllowedResp, retryAllowedReq)
	if retryAllowedResp.Code != http.StatusOK {
		t.Fatalf("expected hot-reload seed retry 200, got %d: %s", retryAllowedResp.Code, retryAllowedResp.Body.String())
	}

	replaceRuntimeCurrentRBACState(t, app, []runtimecore.OperatorIdentityState{{ActorID: "job-operator", Roles: []string{"viewer"}}}, runtimecore.RBACSnapshotState{
		SnapshotKey:           runtimecore.CurrentRBACSnapshotKey,
		ConsoleReadPermission: "console:read",
		Policies: map[string]pluginsdk.AuthorizationPolicy{
			"viewer": {Permissions: []string{"console:read"}, PluginScope: []string{"console"}},
		},
	})

	jobDenied := runtimecore.NewJob("job-dead-retry-hot-reload-denied", "ai.chat", 0, 30*time.Second)
	jobDenied.TraceID = "trace-job-dead-retry-hot-reload-denied"
	jobDenied.EventID = "evt-job-dead-retry-hot-reload-denied"
	jobDenied.Correlation = "runtime-ai:user-hot-reload-denied:retry"
	if err := applyDemoAIJobContract(&jobDenied, "retry hot reload denied", "user-hot-reload-denied"); err != nil {
		t.Fatalf("apply denied demo ai job contract: %v", err)
	}
	if err := app.queue.Enqueue(t.Context(), jobDenied); err != nil {
		t.Fatalf("enqueue denied ai retry seed job: %v", err)
	}
	timeoutDeniedReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/timeout?id="+jobDenied.ID, nil)
	timeoutDeniedResp := httptest.NewRecorder()
	app.ServeHTTP(timeoutDeniedResp, timeoutDeniedReq)
	if timeoutDeniedResp.Code != http.StatusOK {
		t.Fatalf("expected denied seed timeout 200, got %d: %s", timeoutDeniedResp.Code, timeoutDeniedResp.Body.String())
	}

	retryDeniedReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/"+jobDenied.ID+"/retry", nil)
	retryDeniedReq.Header.Set(runtimecore.ConsoleReadActorHeader, "job-operator")
	retryDeniedResp := httptest.NewRecorder()
	app.ServeHTTP(retryDeniedResp, retryDeniedReq)
	if retryDeniedResp.Code != http.StatusForbidden {
		t.Fatalf("expected same-process hot-reloaded retry 403, got %d: %s", retryDeniedResp.Code, retryDeniedResp.Body.String())
	}
	if !strings.Contains(retryDeniedResp.Body.String(), "permission denied") {
		t.Fatalf("expected hot-reloaded retry denial to mention permission denied, got %s", retryDeniedResp.Body.String())
	}
	storedDeniedJob, err := app.queue.Inspect(t.Context(), jobDenied.ID)
	if err != nil {
		t.Fatalf("inspect denied hot-reload retry job: %v", err)
	}
	if storedDeniedJob.Status != runtimecore.JobStatusDead || !storedDeniedJob.DeadLetter {
		t.Fatalf("expected hot-reloaded denied retry to preserve dead-letter state, got %+v", storedDeniedJob)
	}
	entries := app.audits.AuditEntries()
	if len(entries) < 2 {
		t.Fatalf("expected allow+deny retry audit entries, got %+v", entries)
	}
	last := entries[len(entries)-1]
	if last.Actor != "job-operator" || last.Action != "job.retry" || last.Permission != "job:retry" || last.Target != jobDenied.ID || last.Allowed || last.Reason != "permission_denied" {
		t.Fatalf("expected hot-reload denied retry audit entry, got %+v", last)
	}
}

func TestRuntimeAppScheduleCancelHotReloadsPersistedRBACSnapshotWithoutRestart(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeScheduleCancelRBACConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	createAllowedReq := httptest.NewRequest(http.MethodPost, "/demo/schedules/echo-delay", strings.NewReader(`{"id":"schedule-hot-reload-allowed","delay_ms":500,"message":"cancel allowed"}`))
	createAllowedReq.Header.Set("Content-Type", "application/json")
	createAllowedResp := httptest.NewRecorder()
	app.ServeHTTP(createAllowedResp, createAllowedReq)
	if createAllowedResp.Code != http.StatusOK {
		t.Fatalf("expected allowed seed schedule create 200, got %d: %s", createAllowedResp.Code, createAllowedResp.Body.String())
	}

	cancelAllowedReq := httptest.NewRequest(http.MethodPost, "/demo/schedules/schedule-hot-reload-allowed/cancel", nil)
	cancelAllowedReq.Header.Set(runtimecore.ConsoleReadActorHeader, "schedule-admin")
	cancelAllowedResp := httptest.NewRecorder()
	app.ServeHTTP(cancelAllowedResp, cancelAllowedReq)
	if cancelAllowedResp.Code != http.StatusOK {
		t.Fatalf("expected hot-reload seed schedule cancel 200, got %d: %s", cancelAllowedResp.Code, cancelAllowedResp.Body.String())
	}

	replaceRuntimeCurrentRBACState(t, app, []runtimecore.OperatorIdentityState{{ActorID: "schedule-admin", Roles: []string{"schedule-viewer"}}}, runtimecore.RBACSnapshotState{
		SnapshotKey: runtimecore.CurrentRBACSnapshotKey,
		Policies: map[string]pluginsdk.AuthorizationPolicy{
			"schedule-viewer": {Permissions: []string{"schedule:view"}, PluginScope: []string{"*"}},
		},
	})

	createDeniedReq := httptest.NewRequest(http.MethodPost, "/demo/schedules/echo-delay", strings.NewReader(`{"id":"schedule-hot-reload-denied","delay_ms":500,"message":"cancel denied"}`))
	createDeniedReq.Header.Set("Content-Type", "application/json")
	createDeniedResp := httptest.NewRecorder()
	app.ServeHTTP(createDeniedResp, createDeniedReq)
	if createDeniedResp.Code != http.StatusOK {
		t.Fatalf("expected denied seed schedule create 200, got %d: %s", createDeniedResp.Code, createDeniedResp.Body.String())
	}

	cancelDeniedReq := httptest.NewRequest(http.MethodPost, "/demo/schedules/schedule-hot-reload-denied/cancel", nil)
	cancelDeniedReq.Header.Set(runtimecore.ConsoleReadActorHeader, "schedule-admin")
	cancelDeniedResp := httptest.NewRecorder()
	app.ServeHTTP(cancelDeniedResp, cancelDeniedReq)
	if cancelDeniedResp.Code != http.StatusForbidden {
		t.Fatalf("expected same-process hot-reloaded schedule cancel 403, got %d: %s", cancelDeniedResp.Code, cancelDeniedResp.Body.String())
	}
	if !strings.Contains(cancelDeniedResp.Body.String(), "permission denied") {
		t.Fatalf("expected hot-reloaded schedule cancel denial to mention permission denied, got %s", cancelDeniedResp.Body.String())
	}
	if _, err := app.state.LoadSchedulePlan(t.Context(), "schedule-hot-reload-denied"); err != nil {
		t.Fatalf("expected denied hot-reload schedule cancel to preserve persisted plan, got %v", err)
	}
	entries := app.audits.AuditEntries()
	if len(entries) < 2 {
		t.Fatalf("expected allow+deny schedule cancel audit entries, got %+v", entries)
	}
	last := entries[len(entries)-1]
	if last.Actor != "schedule-admin" || last.Action != "schedule.cancel" || last.Permission != "schedule:cancel" || last.Target != "schedule-hot-reload-denied" || last.Allowed || last.Reason != "permission_denied" {
		t.Fatalf("expected hot-reload denied schedule cancel audit entry, got %+v", last)
	}
}

func TestRuntimeAppPluginOperatorHotReloadsPersistedRBACSnapshotWithoutRestart(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeWriteActionRBACConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	replaceRuntimeCurrentRBACState(t, app, []runtimecore.OperatorIdentityState{{ActorID: "admin-user", Roles: []string{"admin"}}}, runtimecore.RBACSnapshotState{
		SnapshotKey: runtimecore.CurrentRBACSnapshotKey,
		Policies: map[string]pluginsdk.AuthorizationPolicy{
			"admin": {Permissions: []string{"plugin:enable", "plugin:disable"}, PluginScope: []string{"*"}},
		},
	})

	disableReq := httptest.NewRequest(http.MethodPost, "/demo/plugins/plugin-echo/disable", nil)
	disableReq.Header.Set(runtimecore.ConsoleReadActorHeader, "admin-user")
	disableResp := httptest.NewRecorder()
	app.ServeHTTP(disableResp, disableReq)
	if disableResp.Code != http.StatusOK {
		t.Fatalf("expected hot-reload seed plugin disable 200, got %d: %s", disableResp.Code, disableResp.Body.String())
	}
	state, err := app.state.LoadPluginEnabledState(t.Context(), "plugin-echo")
	if err != nil {
		t.Fatalf("load disabled plugin state after hot-reload seed: %v", err)
	}
	if state.Enabled {
		t.Fatalf("expected plugin to be disabled by hot-reload seed request, got %+v", state)
	}

	replaceRuntimeCurrentRBACState(t, app, []runtimecore.OperatorIdentityState{{ActorID: "admin-user", Roles: []string{"viewer"}}}, runtimecore.RBACSnapshotState{
		SnapshotKey:           runtimecore.CurrentRBACSnapshotKey,
		ConsoleReadPermission: "console:read",
		Policies: map[string]pluginsdk.AuthorizationPolicy{
			"viewer": {Permissions: []string{"console:read"}, PluginScope: []string{"console"}},
		},
	})

	enableReq := httptest.NewRequest(http.MethodPost, "/demo/plugins/plugin-echo/enable", nil)
	enableReq.Header.Set(runtimecore.ConsoleReadActorHeader, "admin-user")
	enableResp := httptest.NewRecorder()
	app.ServeHTTP(enableResp, enableReq)
	if enableResp.Code != http.StatusForbidden {
		t.Fatalf("expected same-process hot-reloaded plugin enable 403, got %d: %s", enableResp.Code, enableResp.Body.String())
	}
	var deniedEnablePayload operatorActionEnvelopePayload
	if err := json.Unmarshal(enableResp.Body.Bytes(), &deniedEnablePayload); err != nil {
		t.Fatalf("decode denied plugin enable response: %v", err)
	}
	if deniedEnablePayload.Status != "forbidden" || deniedEnablePayload.Action != "plugin.enable" || deniedEnablePayload.Target != "plugin-echo" || deniedEnablePayload.Accepted || deniedEnablePayload.Reason != "permission_denied" || deniedEnablePayload.Error != "permission denied" {
		t.Fatalf("expected normalized denied plugin enable response, got %+v", deniedEnablePayload)
	}
	if !strings.Contains(enableResp.Body.String(), "permission denied") {
		t.Fatalf("expected hot-reloaded plugin enable denial to mention permission denied, got %s", enableResp.Body.String())
	}
	state, err = app.state.LoadPluginEnabledState(t.Context(), "plugin-echo")
	if err != nil {
		t.Fatalf("load plugin state after hot-reload denied enable: %v", err)
	}
	if state.Enabled {
		t.Fatalf("expected denied hot-reload enable to preserve disabled plugin state, got %+v", state)
	}
	entries := app.audits.AuditEntries()
	if len(entries) < 2 {
		t.Fatalf("expected allow+deny plugin operator audit entries, got %+v", entries)
	}
	last := entries[len(entries)-1]
	if last.Actor != "admin-user" || last.Action != "plugin.enable" || last.Permission != "plugin:enable" || last.Target != "plugin-echo" || last.Allowed || last.Reason != "permission_denied" || last.PluginID != "plugin-echo" || last.CorrelationID != "plugin-echo" || last.ErrorCategory != "authorization" || last.ErrorCode != "permission_denied" {
		t.Fatalf("expected hot-reload denied plugin enable audit entry, got %+v", last)
	}
}

func TestRuntimeAppRestartReloadsPersistedRBACSnapshotAndIdentities(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := writeWriteActionRBACConfigAt(t, dir)

	app, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	counts, err := app.state.Counts(t.Context())
	if err != nil {
		t.Fatalf("state counts before restart: %v", err)
	}
	if counts["operator_identities"] == 0 || counts["rbac_snapshots"] != 1 {
		t.Fatalf("expected persisted rbac state before restart, got %+v", counts)
	}
	identity, err := app.state.LoadOperatorIdentity(t.Context(), "config-operator")
	if err != nil {
		t.Fatalf("load persisted operator identity: %v", err)
	}
	if identity.ActorID != "config-operator" || len(identity.Roles) != 1 || identity.Roles[0] != "config-operator" {
		t.Fatalf("expected persisted config-operator identity, got %+v", identity)
	}
	snapshot, err := app.state.LoadRBACSnapshot(t.Context(), runtimecore.CurrentRBACSnapshotKey)
	if err != nil {
		t.Fatalf("load persisted rbac snapshot: %v", err)
	}
	if snapshot.Policies["config-operator"].Permissions[0] != "plugin:config" {
		t.Fatalf("expected persisted config-operator policy, got %+v", snapshot.Policies)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("close first app: %v", err)
	}

	restarted, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("restart runtime app: %v", err)
	}
	defer func() { _ = restarted.Close() }()

	consoleReq := consoleRequestWithViewer("/api/console")
	consoleResp := httptest.NewRecorder()
	restarted.ServeHTTP(consoleResp, consoleReq)
	if consoleResp.Code != http.StatusOK {
		t.Fatalf("expected restarted console 200, got %d: %s", consoleResp.Code, consoleResp.Body.String())
	}

	deniedConfigReq := httptest.NewRequest(http.MethodPost, "/demo/plugins/plugin-echo/config", strings.NewReader(`{"prefix":"denied after restart: "}`))
	deniedConfigReq.Header.Set("Content-Type", "application/json")
	deniedConfigResp := httptest.NewRecorder()
	restarted.ServeHTTP(deniedConfigResp, deniedConfigReq)
	if deniedConfigResp.Code != http.StatusForbidden {
		t.Fatalf("expected headerless config update 403 after restart, got %d: %s", deniedConfigResp.Code, deniedConfigResp.Body.String())
	}

	allowedConfigReq := httptest.NewRequest(http.MethodPost, "/demo/plugins/plugin-echo/config", strings.NewReader(`{"prefix":"persisted after restart: "}`))
	allowedConfigReq.Header.Set("Content-Type", "application/json")
	allowedConfigReq.Header.Set(runtimecore.ConsoleReadActorHeader, "config-operator")
	allowedConfigResp := httptest.NewRecorder()
	restarted.ServeHTTP(allowedConfigResp, allowedConfigReq)
	if allowedConfigResp.Code != http.StatusOK {
		t.Fatalf("expected allowed config update after restart, got %d: %s", allowedConfigResp.Code, allowedConfigResp.Body.String())
	}
	stored, err := restarted.state.LoadPluginConfig(t.Context(), "plugin-echo")
	if err != nil {
		t.Fatalf("load persisted plugin config after restart: %v", err)
	}
	if string(stored.RawConfig) != `{"prefix":"persisted after restart: "}` {
		t.Fatalf("expected restarted runtime to keep using persisted rbac snapshot, got %s", string(stored.RawConfig))
	}
	entries := restarted.audits.AuditEntries()
	if len(entries) < 2 {
		t.Fatalf("expected deny + allow audit entries after restart, got %+v", entries)
	}
	if entries[0].Actor != "" || entries[0].Action != "plugin.config" || entries[0].Permission != "plugin:config" || entries[0].Target != "plugin-echo" || entries[0].Allowed || entries[0].Reason != "permission_denied" {
		t.Fatalf("expected denied config audit after restart, got %+v", entries[0])
	}
	last := entries[len(entries)-1]
	if last.Actor != "config-operator" || last.Action != "plugin.config" || last.Permission != "plugin:config" || last.Target != "plugin-echo" || !last.Allowed || last.Reason != "plugin_config_updated" || last.ErrorCategory != "operator" || last.ErrorCode != "plugin_config_updated" {
		t.Fatalf("expected allowed config audit after restart, got %+v", last)
	}
}

func TestRuntimeAppPostgresSelectedOperatorWritePathsCoverPersistedOperatorSurface(t *testing.T) {
	dir := t.TempDir()
	configPath := writeWriteActionRBACConfigWithPostgresSmokeStoreAt(t, dir, runtimePostgresTestDSN(t))

	app, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("new runtime app with postgres operator config: %v", err)
	}
	defer func() { _ = app.Close() }()

	postgresStore, ok := app.runtimeState.(*runtimecore.PostgresStore)
	if !ok {
		t.Fatalf("expected postgres runtime state for operator matrix, got %T", app.runtimeState)
	}

	disableReq := httptest.NewRequest(http.MethodPost, "/demo/plugins/plugin-echo/disable", nil)
	disableReq.Header.Set(runtimecore.ConsoleReadActorHeader, "admin-user")
	disableResp := httptest.NewRecorder()
	app.ServeHTTP(disableResp, disableReq)
	if disableResp.Code != http.StatusOK {
		t.Fatalf("expected postgres disable operator 200, got %d: %s", disableResp.Code, disableResp.Body.String())
	}
	var postgresDisablePayload operatorPluginResponsePayload
	if err := json.Unmarshal(disableResp.Body.Bytes(), &postgresDisablePayload); err != nil {
		t.Fatalf("decode postgres disable response: %v", err)
	}
	if postgresDisablePayload.Status != "ok" || postgresDisablePayload.Action != "plugin.disable" || postgresDisablePayload.Target != "plugin-echo" || !postgresDisablePayload.Accepted || postgresDisablePayload.Reason != "plugin_disabled" || postgresDisablePayload.PluginID != "plugin-echo" || postgresDisablePayload.Enabled == nil || *postgresDisablePayload.Enabled {
		t.Fatalf("expected normalized postgres disable response, got %+v", postgresDisablePayload)
	}
	disabledState, err := postgresStore.LoadPluginEnabledState(t.Context(), "plugin-echo")
	if err != nil {
		t.Fatalf("load postgres disabled plugin state: %v", err)
	}
	if disabledState.Enabled {
		t.Fatalf("expected postgres plugin enabled overlay to persist disabled state, got %+v", disabledState)
	}
	if _, err := app.state.LoadPluginEnabledState(t.Context(), "plugin-echo"); err == nil {
		t.Fatal("expected sqlite plugin enabled overlay table to stay empty under postgres selection")
	}

	messageReq := httptest.NewRequest(http.MethodPost, "/demo/onebot/message", strings.NewReader(`{"post_type":"message","message_type":"group","time":1712034000,"user_id":10001,"group_id":42,"message_id":9101,"raw_message":"disabled plugin should skip","sender":{"nickname":"alice"}}`))
	messageReq.Header.Set("Content-Type", "application/json")
	messageResp := httptest.NewRecorder()
	app.ServeHTTP(messageResp, messageReq)
	if messageResp.Code != http.StatusOK {
		t.Fatalf("expected postgres disabled-plugin dispatch 200, got %d: %s", messageResp.Code, messageResp.Body.String())
	}
	if strings.Contains(messageResp.Body.String(), "echo: disabled plugin should skip") {
		t.Fatalf("expected disabled postgres plugin not to emit echo reply, got %s", messageResp.Body.String())
	}
	if app.replies.Count() != 0 {
		t.Fatalf("expected disabled postgres plugin to suppress replies, got %+v", app.replies.Since(0))
	}

	enableReq := httptest.NewRequest(http.MethodPost, "/demo/plugins/plugin-echo/enable", nil)
	enableReq.Header.Set(runtimecore.ConsoleReadActorHeader, "admin-user")
	enableResp := httptest.NewRecorder()
	app.ServeHTTP(enableResp, enableReq)
	if enableResp.Code != http.StatusOK {
		t.Fatalf("expected postgres enable operator 200, got %d: %s", enableResp.Code, enableResp.Body.String())
	}
	var postgresEnablePayload operatorPluginResponsePayload
	if err := json.Unmarshal(enableResp.Body.Bytes(), &postgresEnablePayload); err != nil {
		t.Fatalf("decode postgres enable response: %v", err)
	}
	if postgresEnablePayload.Status != "ok" || postgresEnablePayload.Action != "plugin.enable" || postgresEnablePayload.Target != "plugin-echo" || !postgresEnablePayload.Accepted || postgresEnablePayload.Reason != "plugin_enabled" || postgresEnablePayload.PluginID != "plugin-echo" || postgresEnablePayload.Enabled == nil || !*postgresEnablePayload.Enabled {
		t.Fatalf("expected normalized postgres enable response, got %+v", postgresEnablePayload)
	}
	enabledState, err := postgresStore.LoadPluginEnabledState(t.Context(), "plugin-echo")
	if err != nil {
		t.Fatalf("load postgres re-enabled plugin state: %v", err)
	}
	if !enabledState.Enabled {
		t.Fatalf("expected postgres plugin enabled overlay to persist enabled state, got %+v", enabledState)
	}

	configReq := httptest.NewRequest(http.MethodPost, "/demo/plugins/plugin-echo/config", strings.NewReader(`{"prefix":"postgres persisted: "}`))
	configReq.Header.Set("Content-Type", "application/json")
	configReq.Header.Set(runtimecore.ConsoleReadActorHeader, "config-operator")
	configResp := httptest.NewRecorder()
	app.ServeHTTP(configResp, configReq)
	if configResp.Code != http.StatusOK {
		t.Fatalf("expected postgres config operator 200, got %d: %s", configResp.Code, configResp.Body.String())
	}
	var postgresConfigPayload operatorPluginConfigResponsePayload
	if err := json.Unmarshal(configResp.Body.Bytes(), &postgresConfigPayload); err != nil {
		t.Fatalf("decode postgres config response: %v", err)
	}
	if postgresConfigPayload.Status != "ok" || postgresConfigPayload.Action != "plugin.config" || postgresConfigPayload.Target != "plugin-echo" || !postgresConfigPayload.Accepted || postgresConfigPayload.Reason != "plugin_config_updated" || postgresConfigPayload.PluginID != "plugin-echo" || postgresConfigPayload.Persisted == nil || !*postgresConfigPayload.Persisted {
		t.Fatalf("expected normalized postgres config response, got %+v", postgresConfigPayload)
	}

	configState, err := postgresStore.LoadPluginConfig(t.Context(), "plugin-echo")
	if err != nil {
		t.Fatalf("load postgres plugin config: %v", err)
	}
	if string(configState.RawConfig) != `{"prefix":"postgres persisted: "}` {
		t.Fatalf("expected postgres plugin config persistence, got %s", string(configState.RawConfig))
	}
	if _, err := app.state.LoadPluginConfig(t.Context(), "plugin-echo"); err == nil {
		t.Fatal("expected sqlite plugin config table to stay empty under postgres selection")
	}

	job := runtimecore.NewJob("job-postgres-dead-retry", "ai.chat", 0, 30*time.Second)
	job.TraceID = "trace-job-postgres-dead-retry"
	job.EventID = "evt-job-postgres-dead-retry"
	job.Correlation = "runtime-ai:user-postgres-retry:hello retry"
	if err := applyDemoAIJobContract(&job, "hello retry", "user-postgres-retry"); err != nil {
		t.Fatalf("apply demo ai job contract: %v", err)
	}
	if err := app.queue.Enqueue(t.Context(), job); err != nil {
		t.Fatalf("enqueue postgres retry seed job: %v", err)
	}
	timeoutReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/timeout?id=job-postgres-dead-retry", nil)
	timeoutResp := httptest.NewRecorder()
	app.ServeHTTP(timeoutResp, timeoutReq)
	if timeoutResp.Code != http.StatusOK {
		t.Fatalf("expected postgres timeout 200, got %d: %s", timeoutResp.Code, timeoutResp.Body.String())
	}
	retryReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/job-postgres-dead-retry/retry", nil)
	retryReq.Header.Set(runtimecore.ConsoleReadActorHeader, "job-operator")
	retryResp := httptest.NewRecorder()
	app.ServeHTTP(retryResp, retryReq)
	if retryResp.Code != http.StatusOK {
		t.Fatalf("expected postgres retry operator 200, got %d: %s", retryResp.Code, retryResp.Body.String())
	}
	var postgresRetryPayload operatorJobResponsePayload
	if err := json.Unmarshal(retryResp.Body.Bytes(), &postgresRetryPayload); err != nil {
		t.Fatalf("decode postgres retry response: %v", err)
	}
	if postgresRetryPayload.Status != "ok" || postgresRetryPayload.Action != "job.retry" || postgresRetryPayload.Target != "job-postgres-dead-retry" || !postgresRetryPayload.Accepted || postgresRetryPayload.Reason != "job_dead_letter_retried" || postgresRetryPayload.JobID != "job-postgres-dead-retry" {
		t.Fatalf("expected normalized postgres retry response, got %+v", postgresRetryPayload)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stored, inspectErr := app.queue.Inspect(t.Context(), "job-postgres-dead-retry")
		if inspectErr != nil {
			t.Fatalf("inspect postgres retried job: %v", inspectErr)
		}
		if stored.Status == runtimecore.JobStatusDone {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	retriedJob, err := app.queue.Inspect(t.Context(), "job-postgres-dead-retry")
	if err != nil {
		t.Fatalf("inspect postgres retried job after dispatch: %v", err)
	}
	if retriedJob.Status != runtimecore.JobStatusDone || retriedJob.DeadLetter {
		t.Fatalf("expected retried postgres job to leave dead-letter state and complete, got %+v", retriedJob)
	}
	storedJob, err := postgresStore.LoadJob(t.Context(), "job-postgres-dead-retry")
	if err != nil {
		t.Fatalf("load postgres retried job: %v", err)
	}
	if storedJob.Status != runtimecore.JobStatusDone || storedJob.DeadLetter {
		t.Fatalf("expected postgres retried job persistence after operator retry, got %+v", storedJob)
	}
	if len(app.replies.Since(0)) == 0 {
		t.Fatal("expected postgres retried ai job to record a reply through runtime/plugin path")
	}

	createReq := httptest.NewRequest(http.MethodPost, "/demo/schedules/echo-delay", strings.NewReader(`{"id":"schedule-postgres-cancel","delay_ms":60000,"message":"cancel in postgres"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createResp := httptest.NewRecorder()
	app.ServeHTTP(createResp, createReq)
	if createResp.Code != http.StatusOK {
		t.Fatalf("expected postgres schedule create 200, got %d: %s", createResp.Code, createResp.Body.String())
	}
	if _, err := postgresStore.LoadSchedulePlan(t.Context(), "schedule-postgres-cancel"); err != nil {
		t.Fatalf("load postgres schedule before cancel: %v", err)
	}

	cancelReq := httptest.NewRequest(http.MethodPost, "/demo/schedules/schedule-postgres-cancel/cancel", nil)
	cancelReq.Header.Set(runtimecore.ConsoleReadActorHeader, "schedule-admin")
	cancelResp := httptest.NewRecorder()
	app.ServeHTTP(cancelResp, cancelReq)
	if cancelResp.Code != http.StatusOK {
		t.Fatalf("expected postgres cancel operator 200, got %d: %s", cancelResp.Code, cancelResp.Body.String())
	}
	var postgresCancelPayload operatorScheduleResponsePayload
	if err := json.Unmarshal(cancelResp.Body.Bytes(), &postgresCancelPayload); err != nil {
		t.Fatalf("decode postgres cancel response: %v", err)
	}
	if postgresCancelPayload.Status != "ok" || postgresCancelPayload.Action != "schedule.cancel" || postgresCancelPayload.Target != "schedule-postgres-cancel" || !postgresCancelPayload.Accepted || postgresCancelPayload.Reason != "schedule_cancelled" || postgresCancelPayload.ScheduleID != "schedule-postgres-cancel" {
		t.Fatalf("expected normalized postgres cancel response, got %+v", postgresCancelPayload)
	}
	if _, err := postgresStore.LoadSchedulePlan(t.Context(), "schedule-postgres-cancel"); err == nil {
		t.Fatal("expected cancelled postgres schedule to be deleted from selected backend")
	}
	if _, err := app.state.LoadSchedulePlan(t.Context(), "schedule-postgres-cancel"); err == nil {
		t.Fatal("expected sqlite schedule table to stay empty under postgres selection")
	}

	counts, err := app.runtimeStateCounts(t.Context())
	if err != nil {
		t.Fatalf("runtime state counts after postgres operator writes: %v", err)
	}
	if counts["plugin_enabled_overlays"] != 1 {
		t.Fatalf("expected postgres control state to persist one plugin enabled overlay, got %+v", counts)
	}
	if counts["plugin_configs"] != 1 {
		t.Fatalf("expected postgres control state to persist one plugin config, got %+v", counts)
	}
	if counts["jobs"] < 1 {
		t.Fatalf("expected postgres execution state to persist retried job, got %+v", counts)
	}
	if counts["alerts"] != 0 {
		t.Fatalf("expected postgres retry path to clear persisted alerts, got %+v", counts)
	}
	if counts["schedule_plans"] != 0 {
		t.Fatalf("expected postgres schedule count to return to zero after cancel, got %+v", counts)
	}
	sqliteCounts, err := app.state.Counts(t.Context())
	if err != nil {
		t.Fatalf("sqlite counts after postgres operator writes: %v", err)
	}
	if sqliteCounts["plugin_enabled_overlays"] != 0 || sqliteCounts["plugin_configs"] != 0 || sqliteCounts["jobs"] != 0 || sqliteCounts["alerts"] != 0 || sqliteCounts["schedule_plans"] != 0 {
		t.Fatalf("expected sqlite control/runtime tables to stay empty under postgres operator paths, got %+v", sqliteCounts)
	}

	console := readRuntimeConsoleResponseAsViewer(t, app, "/api/console?plugin_id=plugin-echo")
	if got := consoleMetaString(t, console.Meta, "plugin_enabled_state_read_model"); got != "runtime-registry+postgres-plugin-enabled-overlay" {
		t.Fatalf("expected plugin_enabled_state_read_model=runtime-registry+postgres-plugin-enabled-overlay, got %q", got)
	}
	if got := consoleMetaString(t, console.Meta, "plugin_config_read_model"); got != "postgres-plugin-config" {
		t.Fatalf("expected plugin_config_read_model=postgres-plugin-config, got %q", got)
	}
	if got := consoleMetaString(t, console.Meta, "job_read_model"); got != "postgres" {
		t.Fatalf("expected job_read_model=postgres, got %q", got)
	}
	if got := consoleMetaString(t, console.Meta, "job_status_source"); got != "postgres-jobs" {
		t.Fatalf("expected job_status_source=postgres-jobs, got %q", got)
	}
	if got := consoleMetaString(t, console.Meta, "schedule_read_model"); got != "postgres" {
		t.Fatalf("expected schedule_read_model=postgres, got %q", got)
	}
	if got := consoleMetaString(t, console.Meta, "schedule_status_source"); got != "postgres-schedule-plans" {
		t.Fatalf("expected schedule_status_source=postgres-schedule-plans, got %q", got)
	}
	pluginFound := false
	for _, plugin := range console.Plugins {
		if plugin.ID != "plugin-echo" {
			continue
		}
		pluginFound = true
		if plugin.EnabledStateSource != "postgres-plugin-enabled-overlay" || !plugin.EnabledStatePersisted {
			t.Fatalf("expected postgres console plugin enabled-state provenance, got %+v", plugin)
		}
		if plugin.ConfigStateKind != "plugin-owned-persisted-input" || plugin.ConfigSource != "postgres-plugin-config" || !plugin.ConfigPersisted || plugin.ConfigUpdatedAt == "" {
			t.Fatalf("expected postgres console plugin config provenance, got %+v", plugin)
		}
	}
	if !pluginFound {
		t.Fatalf("expected plugin-echo in postgres console payload, got %+v", console.Plugins)
	}
	consoleReq := consoleRequestWithViewer("/api/console?plugin_id=plugin-echo")
	consoleResp := httptest.NewRecorder()
	app.ServeHTTP(consoleResp, consoleReq)
	if consoleResp.Code != http.StatusOK {
		t.Fatalf("expected filtered postgres console 200, got %d: %s", consoleResp.Code, consoleResp.Body.String())
	}
	if !strings.Contains(consoleResp.Body.String(), `"prefix": "postgres persisted: "`) && !strings.Contains(consoleResp.Body.String(), `"prefix":"postgres persisted: "`) {
		t.Fatalf("expected postgres console payload to expose persisted plugin config value, got %s", consoleResp.Body.String())
	}
	for _, expected := range []string{`"enabled": true`, `"enabledStateSource": "postgres-plugin-enabled-overlay"`, `"enabledStatePersisted": true`} {
		if !strings.Contains(consoleResp.Body.String(), expected) && !strings.Contains(consoleResp.Body.String(), strings.ReplaceAll(expected, `:`, `: `)) {
			t.Fatalf("expected postgres console payload to expose persisted plugin enabled value %s, got %s", expected, consoleResp.Body.String())
		}
	}
	if hasConsoleSchedule(console, "schedule-postgres-cancel") {
		t.Fatalf("expected cancelled postgres schedule to disappear from console read model, got %+v", console.Schedules)
	}
	jobFound := false
	for _, job := range console.Jobs {
		if job.ID == "job-postgres-dead-retry" && job.Status == "done" && !job.DeadLetter {
			jobFound = true
			break
		}
	}
	if !jobFound {
		t.Fatalf("expected postgres console payload to expose retried job, got %+v", console.Jobs)
	}
	if strings.Contains(consoleResp.Body.String(), `"objectId": "job-postgres-dead-retry"`) || strings.Contains(consoleResp.Body.String(), `"objectId":"job-postgres-dead-retry"`) {
		t.Fatalf("expected postgres retry path to clear console alert, got %s", consoleResp.Body.String())
	}

	audits := app.audits.AuditEntries()
	if len(audits) < 5 {
		t.Fatalf("expected postgres operator writes to record audit evidence, got %+v", audits)
	}
	disableAuditFound := false
	enableAuditFound := false
	configAuditFound := false
	retryAuditFound := false
	cancelAuditFound := false
	for _, entry := range audits {
		if entry.Actor == "admin-user" && entry.Action == "plugin.disable" && entry.Permission == "plugin:disable" && entry.Target == "plugin-echo" && entry.PluginID == "plugin-admin" && entry.CorrelationID == "plugin-echo" && entry.Allowed && entry.Reason == "plugin_disabled" && entry.ErrorCategory == "operator" && entry.ErrorCode == "plugin_disabled" {
			disableAuditFound = true
		}
		if entry.Actor == "admin-user" && entry.Action == "plugin.enable" && entry.Permission == "plugin:enable" && entry.Target == "plugin-echo" && entry.PluginID == "plugin-admin" && entry.CorrelationID == "plugin-echo" && entry.Allowed && entry.Reason == "plugin_enabled" && entry.ErrorCategory == "operator" && entry.ErrorCode == "plugin_enabled" {
			enableAuditFound = true
		}
		if entry.Actor == "config-operator" && entry.Action == "plugin.config" && entry.Permission == "plugin:config" && entry.Target == "plugin-echo" && entry.Allowed && entry.Reason == "plugin_config_updated" && entry.ErrorCategory == "operator" && entry.ErrorCode == "plugin_config_updated" {
			configAuditFound = true
		}
		if entry.Actor == "job-operator" && entry.Action == "job.retry" && entry.Permission == "job:retry" && entry.Target == "job-postgres-dead-retry" && entry.Allowed && entry.Reason == "job_dead_letter_retried" && entry.ErrorCategory == "operator" && entry.ErrorCode == "job_dead_letter_retried" {
			retryAuditFound = true
		}
		if entry.Actor == "schedule-admin" && entry.Action == "schedule.cancel" && entry.Permission == "schedule:cancel" && entry.Target == "schedule-postgres-cancel" && entry.Allowed && entry.Reason == "schedule_cancelled" && entry.ErrorCategory == "operator" && entry.ErrorCode == "schedule_cancelled" {
			cancelAuditFound = true
		}
	}
	if !disableAuditFound || !enableAuditFound || !configAuditFound || !retryAuditFound || !cancelAuditFound {
		t.Fatalf("expected postgres operator audit entries for disable+enable+config+retry+cancel, got %+v", audits)
	}
}

func TestRuntimeAppSkipsDuplicateIdempotencyKey(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeWriteActionRBACConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	body := `{"post_type":"message","message_type":"group","time":1712034000,"user_id":10001,"group_id":42,"message_id":9001,"raw_message":"hello runtime","sender":{"nickname":"alice"}}`
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/demo/onebot/message", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		app.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
		}
	}

	counts, err := app.runtimeStateCounts(t.Context())
	if err != nil {
		t.Fatalf("runtime state counts: %v", err)
	}
	if counts["event_journal"] != 1 || counts["idempotency_keys"] != 1 {
		t.Fatalf("expected one persisted event and one idempotency key, got %+v", counts)
	}
}

func TestRuntimeAppOutOfOrderOneBotMessagesStayDistinctWhileDuplicateStillShortCircuits(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	type oneBotMessageResponse struct {
		Status    string `json:"status"`
		EventID   string `json:"event_id"`
		Duplicate bool   `json:"duplicate"`
	}

	buildBody := func(messageID int64, unixSeconds int64, rawMessage string) string {
		t.Helper()
		encodedRawMessage, err := json.Marshal(rawMessage)
		if err != nil {
			t.Fatalf("marshal raw onebot message: %v", err)
		}
		return `{"post_type":"message","message_type":"group","time":` + strconv.FormatInt(unixSeconds, 10) + `,"user_id":10001,"group_id":42,"message_id":` + strconv.FormatInt(messageID, 10) + `,"raw_message":` + string(encodedRawMessage) + `,"sender":{"nickname":"alice"}}`
	}

	decodeResponse := func(name string, resp *httptest.ResponseRecorder) oneBotMessageResponse {
		t.Helper()
		if resp.Code != http.StatusOK {
			t.Fatalf("expected %s request 200, got %d: %s", name, resp.Code, resp.Body.String())
		}
		var payload oneBotMessageResponse
		if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode %s response: %v", name, err)
		}
		return payload
	}

	const (
		laterMessageID     int64 = 9102
		earlierMessageID   int64 = 9101
		laterMessageTime   int64 = 1712034002
		earlierMessageTime int64 = 1712034001
	)

	laterBody := buildBody(laterMessageID, laterMessageTime, "later logical message")
	earlierBody := buildBody(earlierMessageID, earlierMessageTime, "earlier logical message")

	laterResp := decodeResponse("later-arrives-first", performRuntimeOneBotMessageRequest(t, app, laterBody))
	if laterResp.Status != "ok" || laterResp.EventID == "" || laterResp.Duplicate {
		t.Fatalf("expected later-arrives-first response to be successful and non-duplicate, got %+v", laterResp)
	}

	earlierResp := decodeResponse("earlier-arrives-second", performRuntimeOneBotMessageRequest(t, app, earlierBody))
	if earlierResp.Status != "ok" || earlierResp.EventID == "" || earlierResp.Duplicate {
		t.Fatalf("expected earlier-arrives-second response to be successful and non-duplicate, got %+v", earlierResp)
	}
	if laterResp.EventID == earlierResp.EventID {
		t.Fatalf("expected out-of-order distinct messages to persist as distinct events, got later=%+v earlier=%+v", laterResp, earlierResp)
	}

	laterEvent, err := app.state.LoadEvent(t.Context(), laterResp.EventID)
	if err != nil {
		t.Fatalf("load persisted later event: %v", err)
	}
	earlierEvent, err := app.state.LoadEvent(t.Context(), earlierResp.EventID)
	if err != nil {
		t.Fatalf("load persisted earlier event: %v", err)
	}

	if laterEvent.EventID == earlierEvent.EventID || laterEvent.IdempotencyKey == earlierEvent.IdempotencyKey {
		t.Fatalf("expected persisted out-of-order messages to remain distinct facts, got later=%+v earlier=%+v", laterEvent, earlierEvent)
	}
	if laterEvent.Message == nil || laterEvent.Message.ID != "msg-"+strconv.FormatInt(laterMessageID, 10) || laterEvent.Message.Text != "later logical message" {
		t.Fatalf("expected persisted later event to keep posted message fact, got %+v", laterEvent)
	}
	if earlierEvent.Message == nil || earlierEvent.Message.ID != "msg-"+strconv.FormatInt(earlierMessageID, 10) || earlierEvent.Message.Text != "earlier logical message" {
		t.Fatalf("expected persisted earlier event to keep posted message fact, got %+v", earlierEvent)
	}
	if laterEvent.IdempotencyKey != "onebot:onebot:group:"+strconv.FormatInt(laterMessageID, 10) {
		t.Fatalf("expected persisted later event idempotency key to match source/message id boundary, got %+v", laterEvent)
	}
	if earlierEvent.IdempotencyKey != "onebot:onebot:group:"+strconv.FormatInt(earlierMessageID, 10) {
		t.Fatalf("expected persisted earlier event idempotency key to match source/message id boundary, got %+v", earlierEvent)
	}
	if !laterEvent.Timestamp.Equal(time.Unix(laterMessageTime, 0).UTC()) {
		t.Fatalf("expected persisted later event timestamp to reflect payload time, got %+v", laterEvent)
	}
	if !earlierEvent.Timestamp.Equal(time.Unix(earlierMessageTime, 0).UTC()) {
		t.Fatalf("expected persisted earlier event timestamp to reflect payload time, got %+v", earlierEvent)
	}
	if !laterEvent.Timestamp.After(earlierEvent.Timestamp) {
		t.Fatalf("expected persisted timestamps to reflect payload chronology rather than arrival order, got later=%s earlier=%s", laterEvent.Timestamp, earlierEvent.Timestamp)
	}

	assertRuntimeSmokeCounts(t, app, 2, 2)

	duplicateResp := decodeResponse("true-duplicate", performRuntimeOneBotMessageRequest(t, app, laterBody))
	if duplicateResp.Status != "ok" || !duplicateResp.Duplicate {
		t.Fatalf("expected reposted original body to short-circuit as duplicate, got %+v", duplicateResp)
	}

	assertRuntimeSmokeCounts(t, app, 2, 2)
}

func TestRuntimeAppReplayUsesSQLiteSelectedBackendEventJournal(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeReplayRBACConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	if app.replay == nil {
		t.Fatal("expected runtime app to provide non-nil replay service")
	}
	stored := runtimeStoredReplayableEvent("evt-runtime-replay-sqlite", "replay from sqlite")
	if err := app.state.RecordEvent(t.Context(), stored); err != nil {
		t.Fatalf("record sqlite replay source event: %v", err)
	}
	beforeReplies := app.replies.Count()
	beforeDispatches := len(app.runtime.DispatchResults())

	if err := dispatchRuntimeAdminReplay(t, app, "replay-user", stored.EventID); err != nil {
		t.Fatalf("dispatch sqlite replay admin command: %v", err)
	}

	replies := app.replies.Since(beforeReplies)
	if len(replies) != 1 || replies[0].Payload != "echo: replay from sqlite" {
		t.Fatalf("expected sqlite replay to redispatch stored event, got %+v", replies)
	}
	if len(app.runtime.DispatchResults()) <= beforeDispatches {
		t.Fatalf("expected replay to add runtime dispatch evidence, got %+v", app.runtime.DispatchResults())
	}
	entries := app.audits.AuditEntries()
	if len(entries) == 0 {
		t.Fatal("expected replay audit evidence")
	}
	lastEntry := entries[len(entries)-1]
	if lastEntry.Actor != "replay-user" || lastEntry.Action != "replay" || lastEntry.Target != stored.EventID || !lastEntry.Allowed || lastEntry.Permission != "plugin:replay" {
		t.Fatalf("expected allowed replay audit entry, got %+v", lastEntry)
	}
}

func TestRuntimeAppReplayUsesPostgresSelectedBackendEventJournal(t *testing.T) {
	dir := t.TempDir()
	app, err := newRuntimeApp(writeReplayRBACConfigWithPostgresSmokeStoreAt(t, dir, runtimePostgresTestDSN(t)))
	if err != nil {
		t.Fatalf("new runtime app with postgres replay config: %v", err)
	}
	defer func() { _ = app.Close() }()

	stored := runtimeStoredReplayableEvent("evt-runtime-replay-postgres", "replay from postgres")
	postgresStore, ok := app.smokeStore.(postgresRuntimeSmokeStore)
	if !ok {
		t.Fatalf("expected postgres-selected smoke store, got %T", app.smokeStore)
	}
	if err := postgresStore.RecordEvent(t.Context(), stored); err != nil {
		t.Fatalf("record postgres replay source event: %v", err)
	}
	beforeReplies := app.replies.Count()

	if err := dispatchRuntimeAdminReplay(t, app, "replay-user", stored.EventID); err != nil {
		t.Fatalf("dispatch postgres replay admin command: %v", err)
	}

	replies := app.replies.Since(beforeReplies)
	if len(replies) != 1 || replies[0].Payload != "echo: replay from postgres" {
		t.Fatalf("expected postgres replay to redispatch stored event, got %+v", replies)
	}
	sqliteCounts, err := app.state.Counts(t.Context())
	if err != nil {
		t.Fatalf("sqlite counts: %v", err)
	}
	if sqliteCounts["event_journal"] != 0 {
		t.Fatalf("expected sqlite event journal to remain empty under postgres replay selection, got %+v", sqliteCounts)
	}
	loadedFromPostgres, err := postgresStore.LoadEvent(t.Context(), stored.EventID)
	if err != nil {
		t.Fatalf("load replay source event from postgres: %v", err)
	}
	if loadedFromPostgres.EventID != stored.EventID || loadedFromPostgres.Message == nil || loadedFromPostgres.Message.Text != stored.Message.Text {
		t.Fatalf("expected replay source event to be loaded from postgres journal, got %+v", loadedFromPostgres)
	}
}

func TestRuntimeAppPostgresSelectedBackendOwnsExecutionStateMainPath(t *testing.T) {
	dir := t.TempDir()
	app, err := newRuntimeApp(writeTestConfigWithPostgresSmokeStoreAt(t, dir, runtimePostgresTestDSN(t)))
	if err != nil {
		t.Fatalf("new runtime app with postgres execution state: %v", err)
	}
	defer func() { _ = app.Close() }()

	if _, ok := app.runtimeState.(*runtimecore.PostgresStore); !ok {
		t.Fatalf("expected runtimeState to be postgres-backed when postgres selected, got %T", app.runtimeState)
	}
	if _, ok := app.smokeStore.(postgresRuntimeSmokeStore); !ok {
		t.Fatalf("expected postgres-selected smoke store, got %T", app.smokeStore)
	}

	jobReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/enqueue", strings.NewReader(`{"id":"job-postgres-main-path","type":"demo.echo","correlation_id":"corr-postgres-main-path"}`))
	jobReq.Header.Set("Content-Type", "application/json")
	jobResp := httptest.NewRecorder()
	app.ServeHTTP(jobResp, jobReq)
	if jobResp.Code != http.StatusOK {
		t.Fatalf("expected enqueue 200, got %d: %s", jobResp.Code, jobResp.Body.String())
	}

	scheduleReq := httptest.NewRequest(http.MethodPost, "/demo/schedules/echo-delay", strings.NewReader(`{"id":"schedule-postgres-main-path","delay_ms":60000,"message":"hello postgres schedule"}`))
	scheduleReq.Header.Set("Content-Type", "application/json")
	scheduleResp := httptest.NewRecorder()
	app.ServeHTTP(scheduleResp, scheduleReq)
	if scheduleResp.Code != http.StatusOK {
		t.Fatalf("expected schedule register 200, got %d: %s", scheduleResp.Code, scheduleResp.Body.String())
	}

	workflowResp := performRuntimeWorkflowMessageRequest(t, app, `{"actor_id":"postgres-user","message":"hello postgres workflow"}`)
	if workflowResp.Code != http.StatusOK {
		t.Fatalf("expected workflow start 200, got %d: %s", workflowResp.Code, workflowResp.Body.String())
	}

	console := readRuntimeConsoleResponse(t, app)
	if got := consoleMetaString(t, console.Meta, "job_read_model"); got != "postgres" {
		t.Fatalf("expected job_read_model=postgres, got %q", got)
	}
	if got := consoleMetaString(t, console.Meta, "schedule_read_model"); got != "postgres" {
		t.Fatalf("expected schedule_read_model=postgres, got %q", got)
	}
	if got := consoleMetaString(t, console.Meta, "job_status_source"); got != "postgres-jobs" {
		t.Fatalf("expected job_status_source=postgres-jobs, got %q", got)
	}
	if got := consoleMetaString(t, console.Meta, "schedule_status_source"); got != "postgres-schedule-plans" {
		t.Fatalf("expected schedule_status_source=postgres-schedule-plans, got %q", got)
	}
	if got := consoleMetaString(t, console.Meta, "workflow_read_model"); got != "postgres-workflow-instances" {
		t.Fatalf("expected workflow_read_model=postgres-workflow-instances, got %q", got)
	}
	if got := consoleMetaString(t, console.Meta, "workflow_status_source"); got != "postgres-workflow-instances" {
		t.Fatalf("expected workflow_status_source=postgres-workflow-instances, got %q", got)
	}

	jobFound := false
	for _, job := range console.Jobs {
		if job.ID == "job-postgres-main-path" {
			jobFound = true
			break
		}
	}
	if !jobFound {
		t.Fatalf("expected postgres execution-state console to expose queued job, got %+v", console.Jobs)
	}
	if !hasConsoleSchedule(console, "schedule-postgres-main-path") {
		t.Fatalf("expected postgres execution-state console to expose schedule, got %+v", console.Schedules)
	}
	workflowID := "workflow-postgres-user"
	workflowFound := false
	for _, workflow := range console.Workflows {
		if workflow.ID != workflowID {
			continue
		}
		workflowFound = true
		if workflow.PluginID != "plugin-workflow-demo" || workflow.StatusSource != "postgres-workflow-instances" || !workflow.StatePersisted {
			t.Fatalf("expected postgres workflow console provenance, got %+v", workflow)
		}
		break
	}
	if !workflowFound {
		t.Fatalf("expected postgres execution-state console to expose workflow instance, got %+v", console.Workflows)
	}

	counts, err := app.runtimeStateCounts(t.Context())
	if err != nil {
		t.Fatalf("runtime state counts: %v", err)
	}
	if counts["jobs"] < 1 || counts["schedule_plans"] < 1 || counts["workflow_instances"] < 1 {
		t.Fatalf("expected postgres execution-state counts for jobs/schedules/workflows, got %+v", counts)
	}

	sqliteCounts, err := app.state.Counts(t.Context())
	if err != nil {
		t.Fatalf("sqlite counts: %v", err)
	}
	if sqliteCounts["jobs"] != 0 || sqliteCounts["schedule_plans"] != 0 || sqliteCounts["workflow_instances"] != 0 {
		t.Fatalf("expected sqlite execution-state tables to stay empty under postgres selection, got %+v", sqliteCounts)
	}

	postgresStore := app.runtimeState.(*runtimecore.PostgresStore)
	storedJob, err := postgresStore.LoadJob(t.Context(), "job-postgres-main-path")
	if err != nil {
		t.Fatalf("load postgres execution-state job: %v", err)
	}
	if storedJob.ID != "job-postgres-main-path" || storedJob.Status != runtimecore.JobStatusPending {
		t.Fatalf("expected postgres job persistence via main runtime path, got %+v", storedJob)
	}
	storedSchedule, err := postgresStore.LoadSchedulePlan(t.Context(), "schedule-postgres-main-path")
	if err != nil {
		t.Fatalf("load postgres execution-state schedule: %v", err)
	}
	if storedSchedule.Plan.ID != "schedule-postgres-main-path" || storedSchedule.Plan.EventType != "message.received" {
		t.Fatalf("expected postgres schedule persistence via main runtime path, got %+v", storedSchedule)
	}
	storedWorkflow, err := postgresStore.LoadWorkflowInstance(t.Context(), workflowID)
	if err != nil {
		t.Fatalf("load postgres execution-state workflow: %v", err)
	}
	if storedWorkflow.WorkflowID != workflowID || storedWorkflow.PluginID != "plugin-workflow-demo" || storedWorkflow.Status != runtimecore.WorkflowRuntimeStatusWaitingEvent {
		t.Fatalf("expected postgres workflow persistence via main runtime path, got %+v", storedWorkflow)
	}
}

func TestRuntimeAppReplayMissingEventSurfacesSelectedBackendNotFound(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeReplayRBACConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	err = dispatchRuntimeAdminReplay(t, app, "replay-user", "evt-missing-runtime-replay")
	if err == nil {
		t.Fatal("expected replay of missing event to fail")
	}
	if !strings.Contains(err.Error(), "load event journal") {
		t.Fatalf("expected selected backend not-found to surface journal read error, got %v", err)
	}
	if app.replies.Count() != 0 {
		t.Fatalf("expected missing replay source not to dispatch replies, got %+v", app.replies.Since(0))
	}
	entries := app.audits.AuditEntries()
	if len(entries) == 0 {
		t.Fatal("expected replay failure audit evidence")
	}
	lastEntry := entries[len(entries)-1]
	if lastEntry.Action != "replay" || lastEntry.Target != "evt-missing-runtime-replay" || !lastEntry.Allowed || lastEntry.Reason != "replay_failed" {
		t.Fatalf("expected replay missing-event audit entry, got %+v", lastEntry)
	}
}

func TestRuntimeAppReplayBackendFailureSurfacesRuntimeAdminError(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeReplayRBACConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	failingStore := failingReplayJournalSmokeStore{
		runtimeSmokeStore: app.smokeStore,
		loadErr:           errors.New("backend replay reader unavailable"),
	}
	app.smokeStore = failingStore
	app.replay.journal = failingStore

	err = dispatchRuntimeAdminReplay(t, app, "replay-user", "evt-runtime-replay-backend-fail")
	if err == nil {
		t.Fatal("expected replay backend failure to bubble through runtime admin path")
	}
	if !strings.Contains(err.Error(), "backend replay reader unavailable") {
		t.Fatalf("expected replay backend failure in command error, got %v", err)
	}
	if app.replies.Count() != 0 {
		t.Fatalf("expected replay backend failure not to dispatch replies, got %+v", app.replies.Since(0))
	}
	entries := app.audits.AuditEntries()
	if len(entries) == 0 {
		t.Fatal("expected replay backend failure audit evidence")
	}
	lastEntry := entries[len(entries)-1]
	if lastEntry.Action != "replay" || lastEntry.Target != "evt-runtime-replay-backend-fail" || !lastEntry.Allowed || lastEntry.Reason != "replay_failed" {
		t.Fatalf("expected replay backend failure audit entry, got %+v", lastEntry)
	}
}

func TestRuntimeAppJobQueueAppearsInConsole(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	req := httptest.NewRequest(http.MethodPost, "/demo/jobs/enqueue", strings.NewReader(`{"id":"job-demo-1"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}

	consoleReq := consoleRequestWithViewer("/api/console")
	consoleResp := httptest.NewRecorder()
	app.ServeHTTP(consoleResp, consoleReq)
	if !strings.Contains(consoleResp.Body.String(), "job-demo-1") {
		t.Fatalf("expected console to include job-demo-1, got %s", consoleResp.Body.String())
	}
}

func TestRuntimeAppJobTimeoutTransitionsFromPending(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	enqueueReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/enqueue", strings.NewReader(`{"id":"job-timeout-1"}`))
	enqueueReq.Header.Set("Content-Type", "application/json")
	enqueueResp := httptest.NewRecorder()
	app.ServeHTTP(enqueueResp, enqueueReq)
	if enqueueResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", enqueueResp.Code, enqueueResp.Body.String())
	}

	timeoutReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/timeout?id=job-timeout-1", nil)
	timeoutResp := httptest.NewRecorder()
	app.ServeHTTP(timeoutResp, timeoutReq)
	if timeoutResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", timeoutResp.Code, timeoutResp.Body.String())
	}
	if !strings.Contains(timeoutResp.Body.String(), `"status":"retrying"`) {
		t.Fatalf("expected timeout endpoint to transition job to retrying, got %s", timeoutResp.Body.String())
	}
}

func TestRuntimeAppDeadLetterAlertAppearsInConsole(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	enqueueReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/enqueue", strings.NewReader(`{"id":"job-alert-console","correlation_id":"corr-alert-console","max_retries":0}`))
	enqueueReq.Header.Set("Content-Type", "application/json")
	enqueueResp := httptest.NewRecorder()
	app.ServeHTTP(enqueueResp, enqueueReq)
	if enqueueResp.Code != http.StatusOK {
		t.Fatalf("expected enqueue 200, got %d: %s", enqueueResp.Code, enqueueResp.Body.String())
	}

	timeoutReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/timeout?id=job-alert-console", nil)
	timeoutResp := httptest.NewRecorder()
	app.ServeHTTP(timeoutResp, timeoutReq)
	if timeoutResp.Code != http.StatusOK {
		t.Fatalf("expected timeout 200, got %d: %s", timeoutResp.Code, timeoutResp.Body.String())
	}
	if !strings.Contains(timeoutResp.Body.String(), `"status":"dead"`) || !strings.Contains(timeoutResp.Body.String(), `"deadLetter":true`) {
		t.Fatalf("expected timeout endpoint to dead-letter demo job, got %s", timeoutResp.Body.String())
	}

	consoleReq := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	consoleResp := httptest.NewRecorder()
	app.ServeHTTP(consoleResp, consoleReq)
	if consoleResp.Code != http.StatusOK {
		t.Fatalf("expected console 200, got %d: %s", consoleResp.Code, consoleResp.Body.String())
	}
	var payload struct {
		Jobs []struct {
			ID         string `json:"id"`
			Status     string `json:"status"`
			DeadLetter bool   `json:"deadLetter"`
		} `json:"jobs"`
		Alerts []struct {
			ID               string `json:"id"`
			ObjectID         string `json:"objectId"`
			FailureType      string `json:"failureType"`
			LatestReason     string `json:"latestReason"`
			Correlation      string `json:"correlation"`
			FirstOccurredAt  string `json:"firstOccurredAt"`
			LatestOccurredAt string `json:"latestOccurredAt"`
		} `json:"alerts"`
	}
	if err := json.Unmarshal(consoleResp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode console payload: %v", err)
	}
	jobVisible := false
	for _, job := range payload.Jobs {
		if job.ID == "job-alert-console" && job.Status == "dead" && job.DeadLetter {
			jobVisible = true
			break
		}
	}
	if !jobVisible {
		t.Fatalf("expected dead-letter job to remain visible in /api/console, got %+v", payload.Jobs)
	}
	if len(payload.Alerts) != 1 {
		t.Fatalf("expected one dead-letter alert in console payload, got %+v", payload.Alerts)
	}
	alert := payload.Alerts[0]
	if alert.ObjectID != "job-alert-console" || alert.FailureType != "job.dead_letter" || alert.LatestReason != "timeout" || alert.Correlation != "corr-alert-console" {
		t.Fatalf("expected minimal dead-letter alert payload in console response, got %+v", alert)
	}
	if alert.FirstOccurredAt == "" || alert.LatestOccurredAt == "" {
		t.Fatalf("expected dead-letter alert timestamps in console response, got %+v", alert)
	}
	counts, err := app.state.Counts(t.Context())
	if err != nil {
		t.Fatalf("sqlite counts: %v", err)
	}
	if counts["alerts"] != 1 {
		t.Fatalf("expected one persisted alert row after demo dead-letter flow, got %+v", counts)
	}
}

func TestRuntimeAppRetryDeadLetterOperatorRequeuesJobClearsConsoleAlertAndRecordsDistinctAudit(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeWriteActionRBACConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	job := runtimecore.NewJob("job-dead-retry-console", "ai.chat", 0, 30*time.Second)
	job.TraceID = "trace-job-dead-retry-console"
	job.EventID = "evt-job-dead-retry-console"
	job.Correlation = "runtime-ai:user-retry:hello retry"
	if err := applyDemoAIJobContract(&job, "hello retry", "user-retry"); err != nil {
		t.Fatalf("apply demo ai job contract: %v", err)
	}
	if err := app.queue.Enqueue(t.Context(), job); err != nil {
		t.Fatalf("enqueue ai retry seed job: %v", err)
	}

	timeoutReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/timeout?id=job-dead-retry-console", nil)
	timeoutResp := httptest.NewRecorder()
	app.ServeHTTP(timeoutResp, timeoutReq)
	if timeoutResp.Code != http.StatusOK {
		t.Fatalf("expected timeout 200, got %d: %s", timeoutResp.Code, timeoutResp.Body.String())
	}
	if !strings.Contains(timeoutResp.Body.String(), `"status":"dead"`) || !strings.Contains(timeoutResp.Body.String(), `"deadLetter":true`) {
		t.Fatalf("expected dead-letter response before retry, got %s", timeoutResp.Body.String())
	}

	retryReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/job-dead-retry-console/retry", nil)
	retryReq.Header.Set(runtimecore.ConsoleReadActorHeader, "job-operator")
	retryResp := httptest.NewRecorder()
	app.ServeHTTP(retryResp, retryReq)
	if retryResp.Code != http.StatusOK {
		t.Fatalf("expected retry operator 200, got %d: %s", retryResp.Code, retryResp.Body.String())
	}
	var retryPayload operatorJobResponsePayload
	if err := json.Unmarshal(retryResp.Body.Bytes(), &retryPayload); err != nil {
		t.Fatalf("decode retry response: %v", err)
	}
	if retryPayload.Status != "ok" || retryPayload.Action != "job.retry" || retryPayload.Target != "job-dead-retry-console" || !retryPayload.Accepted || retryPayload.Reason != "job_dead_letter_retried" || retryPayload.JobID != "job-dead-retry-console" {
		t.Fatalf("expected normalized retry response, got %+v", retryPayload)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stored, inspectErr := app.queue.Inspect(t.Context(), "job-dead-retry-console")
		if inspectErr != nil {
			t.Fatalf("inspect retried job: %v", inspectErr)
		}
		if stored.Status == runtimecore.JobStatusDone {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	stored, err := app.queue.Inspect(t.Context(), "job-dead-retry-console")
	if err != nil {
		t.Fatalf("inspect retried job after dispatch: %v", err)
	}
	if stored.Status != runtimecore.JobStatusDone || stored.DeadLetter {
		t.Fatalf("expected retried job to leave dead-letter state and complete through runtime dispatch, got %+v", stored)
	}
	if len(app.replies.Since(0)) == 0 {
		t.Fatal("expected retried ai job to record a reply through the existing runtime/plugin path")
	}

	consoleReq := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	consoleResp := httptest.NewRecorder()
	app.ServeHTTP(consoleResp, consoleReq)
	if consoleResp.Code != http.StatusOK {
		t.Fatalf("expected console 200 after retry, got %d: %s", consoleResp.Code, consoleResp.Body.String())
	}
	var payload struct {
		Jobs []struct {
			ID         string `json:"id"`
			Status     string `json:"status"`
			DeadLetter bool   `json:"deadLetter"`
		} `json:"jobs"`
		Alerts []struct {
			ObjectID string `json:"objectId"`
		} `json:"alerts"`
		Audits []struct {
			Actor         string `json:"actor"`
			Action        string `json:"action"`
			Target        string `json:"target"`
			Allowed       bool   `json:"allowed"`
			Reason        string `json:"reason"`
			TraceID       string `json:"trace_id"`
			EventID       string `json:"event_id"`
			PluginID      string `json:"plugin_id"`
			RunID         string `json:"run_id"`
			CorrelationID string `json:"correlation_id"`
			ErrorCategory string `json:"error_category"`
			ErrorCode     string `json:"error_code"`
		} `json:"audits"`
	}
	if err := json.Unmarshal(consoleResp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode console payload: %v", err)
	}
	jobVisible := false
	for _, job := range payload.Jobs {
		if job.ID == "job-dead-retry-console" && job.Status == "done" && !job.DeadLetter {
			jobVisible = true
			break
		}
	}
	if !jobVisible {
		t.Fatalf("expected console to show retried job as non-dead, got %+v", payload.Jobs)
	}
	for _, alert := range payload.Alerts {
		if alert.ObjectID == "job-dead-retry-console" {
			t.Fatalf("expected stale dead-letter alert to be removed from console, got %+v", payload.Alerts)
		}
	}
	auditVisible := false
	for _, entry := range payload.Audits {
		if entry.Actor == "job-operator" && entry.Action == "job.retry" && entry.Target == "job-dead-retry-console" && entry.Allowed && entry.Reason == "job_dead_letter_retried" && entry.TraceID == "trace-job-dead-retry-console" && entry.EventID == "evt-job-dead-retry-console" && entry.PluginID == "" && entry.RunID == "run-evt-job-dead-retry-console" && entry.CorrelationID == "runtime-ai:user-retry:hello retry" && entry.ErrorCategory == "operator" && entry.ErrorCode == "job_dead_letter_retried" {
			auditVisible = true
			break
		}
	}
	if !auditVisible {
		t.Fatalf("expected distinct retry audit entry in console payload, got %+v", payload.Audits)
	}
	entries := app.audits.AuditEntries()
	if len(entries) == 0 {
		t.Fatal("expected retry operator to record audit evidence")
	}
	lastEntry := entries[len(entries)-1]
	if lastEntry.Actor != "job-operator" || lastEntry.Permission != "job:retry" || lastEntry.Action != "job.retry" || lastEntry.Target != "job-dead-retry-console" || !lastEntry.Allowed || lastEntry.Reason != "job_dead_letter_retried" || lastEntry.ErrorCategory != "operator" || lastEntry.ErrorCode != "job_dead_letter_retried" {
		t.Fatalf("expected distinct retry audit entry, got %+v", lastEntry)
	}
	if lastEntry.TraceID != "trace-job-dead-retry-console" || lastEntry.EventID != "evt-job-dead-retry-console" || lastEntry.RunID != "run-evt-job-dead-retry-console" || lastEntry.CorrelationID != "runtime-ai:user-retry:hello retry" {
		t.Fatalf("expected retry audit observability fields, got %+v", lastEntry)
	}
	if lastEntry.Action == "replay" || strings.Contains(lastEntry.Reason, "replay") {
		t.Fatalf("expected retry audit to remain distinct from replay, got %+v", lastEntry)
	}
	counts, err := app.state.Counts(t.Context())
	if err != nil {
		t.Fatalf("sqlite counts after retry: %v", err)
	}
	if counts["alerts"] != 0 {
		t.Fatalf("expected retry to resolve persisted dead-letter alert, got %+v", counts)
	}
}

func TestRuntimeAppConsoleReadsPersistedAuditEntriesFromSQLiteState(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	entry := pluginsdk.AuditEntry{
		Actor:         "schedule-admin",
		Permission:    "schedule:cancel",
		Action:        "schedule.cancel",
		Target:        "schedule-persisted-audit",
		Allowed:       true,
		Reason:        "schedule_cancelled",
		TraceID:       "trace-persisted-console-audit",
		EventID:       "evt-persisted-console-audit",
		PluginID:      "plugin-scheduler",
		RunID:         "run-persisted-console-audit",
		CorrelationID: "corr-persisted-console-audit",
		ErrorCategory: "operator",
		ErrorCode:     "schedule_cancelled",
		OccurredAt:    "2026-04-21T09:00:00Z",
	}
	if err := app.auditRecorder.RecordAudit(entry); err != nil {
		t.Fatalf("save persisted audit: %v", err)
	}

	consoleReq := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	consoleResp := httptest.NewRecorder()
	app.ServeHTTP(consoleResp, consoleReq)
	if consoleResp.Code != http.StatusOK {
		t.Fatalf("expected console 200, got %d: %s", consoleResp.Code, consoleResp.Body.String())
	}
	for _, expected := range []string{`"trace_id": "trace-persisted-console-audit"`, `"event_id": "evt-persisted-console-audit"`, `"plugin_id": "plugin-scheduler"`, `"run_id": "run-persisted-console-audit"`, `"correlation_id": "corr-persisted-console-audit"`, `"error_category": "operator"`, `"error_code": "schedule_cancelled"`} {
		if !strings.Contains(consoleResp.Body.String(), expected) {
			t.Fatalf("expected console persisted audit payload to include %q, got %s", expected, consoleResp.Body.String())
		}
	}
}

func TestRuntimeAppRetryDeadLetterOperatorRejectsDuplicateAndInvalidRequestsSafely(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeWriteActionRBACConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	job := runtimecore.NewJob("job-dead-retry-once", "ai.chat", 0, 30*time.Second)
	job.TraceID = "trace-job-dead-retry-once"
	job.EventID = "evt-job-dead-retry-once"
	job.Correlation = "runtime-ai:user-retry-once:retry once"
	if err := applyDemoAIJobContract(&job, "retry once", "user-retry-once"); err != nil {
		t.Fatalf("apply demo ai job contract: %v", err)
	}
	if err := app.queue.Enqueue(t.Context(), job); err != nil {
		t.Fatalf("enqueue ai retry-once seed job: %v", err)
	}

	timeoutReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/timeout?id=job-dead-retry-once", nil)
	timeoutResp := httptest.NewRecorder()
	app.ServeHTTP(timeoutResp, timeoutReq)
	if timeoutResp.Code != http.StatusOK {
		t.Fatalf("expected timeout 200, got %d: %s", timeoutResp.Code, timeoutResp.Body.String())
	}

	firstRetryReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/job-dead-retry-once/retry", nil)
	firstRetryReq.Header.Set(runtimecore.ConsoleReadActorHeader, "job-operator")
	firstRetryResp := httptest.NewRecorder()
	app.ServeHTTP(firstRetryResp, firstRetryReq)
	if firstRetryResp.Code != http.StatusOK {
		t.Fatalf("expected first retry 200, got %d: %s", firstRetryResp.Code, firstRetryResp.Body.String())
	}

	secondRetryReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/job-dead-retry-once/retry", nil)
	secondRetryReq.Header.Set(runtimecore.ConsoleReadActorHeader, "job-operator")
	secondRetryResp := httptest.NewRecorder()
	app.ServeHTTP(secondRetryResp, secondRetryReq)
	if secondRetryResp.Code != http.StatusBadRequest {
		t.Fatalf("expected duplicate retry to be rejected with 400, got %d: %s", secondRetryResp.Code, secondRetryResp.Body.String())
	}
	if !strings.Contains(secondRetryResp.Body.String(), "is not dead-lettered") {
		t.Fatalf("expected duplicate retry rejection to explain state, got %s", secondRetryResp.Body.String())
	}

	missingRetryReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/job-missing-retry/retry", nil)
	missingRetryReq.Header.Set(runtimecore.ConsoleReadActorHeader, "job-operator")
	missingRetryResp := httptest.NewRecorder()
	app.ServeHTTP(missingRetryResp, missingRetryReq)
	if missingRetryResp.Code != http.StatusNotFound {
		t.Fatalf("expected missing retry to return 404, got %d: %s", missingRetryResp.Code, missingRetryResp.Body.String())
	}
	if !strings.Contains(missingRetryResp.Body.String(), "job not found") {
		t.Fatalf("expected missing retry response to mention job not found, got %s", missingRetryResp.Body.String())
	}
	entries := app.audits.AuditEntries()
	allowedRetries := 0
	for _, entry := range entries {
		if entry.Action == "job.retry" && entry.Target == "job-dead-retry-once" && entry.Allowed {
			allowedRetries++
		}
	}
	if allowedRetries != 1 {
		t.Fatalf("expected exactly one accepted retry audit entry, got %+v", entries)
	}
}

func TestRuntimeAppRetryDeadLetterOperatorReturnsForbiddenAndRecordsDeniedAudit(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeWriteActionRBACConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	job := runtimecore.NewJob("job-dead-retry-denied", "ai.chat", 0, 30*time.Second)
	job.TraceID = "trace-job-dead-retry-denied"
	job.EventID = "evt-job-dead-retry-denied"
	job.Correlation = "runtime-ai:user-retry-denied:deny retry"
	if err := applyDemoAIJobContract(&job, "deny retry", "user-retry-denied"); err != nil {
		t.Fatalf("apply demo ai job contract: %v", err)
	}
	if err := app.queue.Enqueue(t.Context(), job); err != nil {
		t.Fatalf("enqueue denied ai retry seed job: %v", err)
	}

	timeoutReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/timeout?id=job-dead-retry-denied", nil)
	timeoutResp := httptest.NewRecorder()
	app.ServeHTTP(timeoutResp, timeoutReq)
	if timeoutResp.Code != http.StatusOK {
		t.Fatalf("expected timeout 200, got %d: %s", timeoutResp.Code, timeoutResp.Body.String())
	}

	retryReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/job-dead-retry-denied/retry", nil)
	retryReq.Header.Set(runtimecore.ConsoleReadActorHeader, "viewer-user")
	retryResp := httptest.NewRecorder()
	app.ServeHTTP(retryResp, retryReq)
	if retryResp.Code != http.StatusForbidden {
		t.Fatalf("expected denied retry 403, got %d: %s", retryResp.Code, retryResp.Body.String())
	}
	var deniedRetryPayload operatorActionEnvelopePayload
	if err := json.Unmarshal(retryResp.Body.Bytes(), &deniedRetryPayload); err != nil {
		t.Fatalf("decode denied retry response: %v", err)
	}
	if deniedRetryPayload.Status != "forbidden" || deniedRetryPayload.Action != "job.retry" || deniedRetryPayload.Target != "job-dead-retry-denied" || deniedRetryPayload.Accepted || deniedRetryPayload.Reason != "permission_denied" || deniedRetryPayload.Error != "permission denied" {
		t.Fatalf("expected normalized denied retry response, got %+v", deniedRetryPayload)
	}
	if !strings.Contains(retryResp.Body.String(), "permission denied") {
		t.Fatalf("expected denied retry response to mention permission denied, got %s", retryResp.Body.String())
	}
	stored, err := app.queue.Inspect(t.Context(), "job-dead-retry-denied")
	if err != nil {
		t.Fatalf("inspect denied retry job: %v", err)
	}
	if stored.Status != runtimecore.JobStatusDead || !stored.DeadLetter {
		t.Fatalf("expected denied retry to preserve dead-letter job state, got %+v", stored)
	}
	entries := app.audits.AuditEntries()
	if len(entries) != 1 {
		t.Fatalf("expected one denied retry audit entry, got %+v", entries)
	}
	if entries[0].Actor != "viewer-user" || entries[0].Action != "job.retry" || entries[0].Permission != "job:retry" || entries[0].Target != "job-dead-retry-denied" || entries[0].Allowed || entries[0].Reason != "permission_denied" || entries[0].ErrorCategory != "authorization" || entries[0].ErrorCode != "permission_denied" {
		t.Fatalf("expected denied retry audit entry, got %+v", entries[0])
	}
	if entries[0].SessionID != "" {
		t.Fatalf("expected denied retry without bearer auth to omit session id, got %+v", entries[0])
	}
}

func TestRuntimeAppRetryDeadLetterOperatorFailsClosedWithoutActorHeaderUnderConfiguredRBAC(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeWriteActionRBACConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	job := runtimecore.NewJob("job-dead-retry-missing-actor", "ai.chat", 0, 30*time.Second)
	job.TraceID = "trace-job-dead-retry-missing-actor"
	job.EventID = "evt-job-dead-retry-missing-actor"
	job.Correlation = "runtime-ai:user-retry-missing-actor:deny retry"
	if err := applyDemoAIJobContract(&job, "deny retry", "user-retry-missing-actor"); err != nil {
		t.Fatalf("apply demo ai job contract: %v", err)
	}
	if err := app.queue.Enqueue(t.Context(), job); err != nil {
		t.Fatalf("enqueue ai retry seed job: %v", err)
	}

	timeoutReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/timeout?id=job-dead-retry-missing-actor", nil)
	timeoutResp := httptest.NewRecorder()
	app.ServeHTTP(timeoutResp, timeoutReq)
	if timeoutResp.Code != http.StatusOK {
		t.Fatalf("expected timeout 200, got %d: %s", timeoutResp.Code, timeoutResp.Body.String())
	}

	retryReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/job-dead-retry-missing-actor/retry", nil)
	retryResp := httptest.NewRecorder()
	app.ServeHTTP(retryResp, retryReq)
	if retryResp.Code != http.StatusForbidden {
		t.Fatalf("expected headerless retry 403, got %d: %s", retryResp.Code, retryResp.Body.String())
	}
	if !strings.Contains(retryResp.Body.String(), "permission denied") {
		t.Fatalf("expected headerless retry response to mention permission denied, got %s", retryResp.Body.String())
	}
	entries := app.audits.AuditEntries()
	if len(entries) != 1 || entries[0].Actor != "" || entries[0].Action != "job.retry" || entries[0].Permission != "job:retry" || entries[0].Target != "job-dead-retry-missing-actor" || entries[0].Allowed || entries[0].Reason != "permission_denied" {
		t.Fatalf("expected headerless retry deny audit entry, got %+v", entries)
	}
	if len(entries) == 1 && entries[0].SessionID != "" {
		t.Fatalf("expected headerless retry deny audit entry to omit session id, got %+v", entries[0])
	}
}

func TestRuntimeAppJobPauseResumeCancelOperatorsPersistQueueControlAndAudits(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeWriteActionRBACConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	pending := runtimecore.NewJob("job-operator-pause-resume", "demo.echo", 1, 30*time.Second)
	if err := app.queue.Enqueue(t.Context(), pending); err != nil {
		t.Fatalf("enqueue pause/resume job: %v", err)
	}

	pauseReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/"+pending.ID+"/pause", nil)
	pauseReq.Header.Set(runtimecore.ConsoleReadActorHeader, "job-operator")
	pauseResp := httptest.NewRecorder()
	app.ServeHTTP(pauseResp, pauseReq)
	if pauseResp.Code != http.StatusOK {
		t.Fatalf("expected pause operator 200, got %d: %s", pauseResp.Code, pauseResp.Body.String())
	}
	var pausePayload operatorJobResponsePayload
	if err := json.Unmarshal(pauseResp.Body.Bytes(), &pausePayload); err != nil {
		t.Fatalf("decode pause response: %v", err)
	}
	if pausePayload.Status != "ok" || pausePayload.Action != "job.pause" || pausePayload.Target != pending.ID || !pausePayload.Accepted || pausePayload.Reason != "job_paused" {
		t.Fatalf("expected normalized pause response, got %+v", pausePayload)
	}
	pausedJob, err := app.queue.Inspect(t.Context(), pending.ID)
	if err != nil {
		t.Fatalf("inspect paused job: %v", err)
	}
	if pausedJob.Status != runtimecore.JobStatusPaused {
		t.Fatalf("expected paused job state, got %+v", pausedJob)
	}

	resumeReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/"+pending.ID+"/resume", nil)
	resumeReq.Header.Set(runtimecore.ConsoleReadActorHeader, "job-operator")
	resumeResp := httptest.NewRecorder()
	app.ServeHTTP(resumeResp, resumeReq)
	if resumeResp.Code != http.StatusOK {
		t.Fatalf("expected resume operator 200, got %d: %s", resumeResp.Code, resumeResp.Body.String())
	}
	var resumePayload operatorJobResponsePayload
	if err := json.Unmarshal(resumeResp.Body.Bytes(), &resumePayload); err != nil {
		t.Fatalf("decode resume response: %v", err)
	}
	if resumePayload.Status != "ok" || resumePayload.Action != "job.resume" || resumePayload.Target != pending.ID || !resumePayload.Accepted || resumePayload.Reason != "job_resumed" {
		t.Fatalf("expected normalized resume response, got %+v", resumePayload)
	}
	resumedJob, err := app.queue.Inspect(t.Context(), pending.ID)
	if err != nil {
		t.Fatalf("inspect resumed job: %v", err)
	}
	if resumedJob.Status != runtimecore.JobStatusPending {
		t.Fatalf("expected resumed pending job state, got %+v", resumedJob)
	}

	retrying := runtimecore.NewJob("job-operator-cancel", "ai.chat", 1, 30*time.Second)
	retrying.TraceID = "trace-job-operator-cancel"
	retrying.EventID = "evt-job-operator-cancel"
	retrying.Correlation = "runtime-ai:user-operator-cancel:cancel"
	if err := applyDemoAIJobContract(&retrying, "cancel me", "user-operator-cancel"); err != nil {
		t.Fatalf("apply demo ai job contract: %v", err)
	}
	if err := app.queue.Enqueue(t.Context(), retrying); err != nil {
		t.Fatalf("enqueue cancel job: %v", err)
	}
	if _, err := app.queue.MarkRunning(t.Context(), retrying.ID); err != nil {
		t.Fatalf("mark running cancel job: %v", err)
	}
	if _, err := app.queue.Fail(t.Context(), retrying.ID, "boom"); err != nil {
		t.Fatalf("move cancel job to retrying: %v", err)
	}

	cancelReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/"+retrying.ID+"/cancel", nil)
	cancelReq.Header.Set(runtimecore.ConsoleReadActorHeader, "job-operator")
	cancelResp := httptest.NewRecorder()
	app.ServeHTTP(cancelResp, cancelReq)
	if cancelResp.Code != http.StatusOK {
		t.Fatalf("expected cancel operator 200, got %d: %s", cancelResp.Code, cancelResp.Body.String())
	}
	var cancelPayload operatorJobResponsePayload
	if err := json.Unmarshal(cancelResp.Body.Bytes(), &cancelPayload); err != nil {
		t.Fatalf("decode cancel response: %v", err)
	}
	if cancelPayload.Status != "ok" || cancelPayload.Action != "job.cancel" || cancelPayload.Target != retrying.ID || !cancelPayload.Accepted || cancelPayload.Reason != "job_cancelled" {
		t.Fatalf("expected normalized cancel response, got %+v", cancelPayload)
	}
	cancelledJob, err := app.queue.Inspect(t.Context(), retrying.ID)
	if err != nil {
		t.Fatalf("inspect cancelled job: %v", err)
	}
	if cancelledJob.Status != runtimecore.JobStatusCancelled {
		t.Fatalf("expected cancelled job state, got %+v", cancelledJob)
	}

	entries := app.audits.AuditEntries()
	if len(entries) != 3 {
		t.Fatalf("expected pause+resume+cancel audits, got %+v", entries)
	}
	if entries[0].Action != "job.pause" || entries[0].Permission != "job:pause" || !entries[0].Allowed || entries[0].Reason != "job_paused" {
		t.Fatalf("expected pause audit entry, got %+v", entries[0])
	}
	if entries[1].Action != "job.resume" || entries[1].Permission != "job:resume" || !entries[1].Allowed || entries[1].Reason != "job_resumed" {
		t.Fatalf("expected resume audit entry, got %+v", entries[1])
	}
	if entries[2].Action != "job.cancel" || entries[2].Permission != "job:cancel" || !entries[2].Allowed || entries[2].Reason != "job_cancelled" {
		t.Fatalf("expected cancel audit entry, got %+v", entries[2])
	}
	console := readRuntimeConsoleResponseAsViewer(t, app, "/api/console?job_query=job-operator")
	if got := consoleMetaStringSlice(t, console.Meta, "job_operator_actions"); !reflect.DeepEqual(got, []string{"/demo/jobs/{job-id}/pause", "/demo/jobs/{job-id}/resume", "/demo/jobs/{job-id}/cancel", "/demo/jobs/{job-id}/retry"}) {
		t.Fatalf("expected job operator actions in console meta, got %+v", got)
	}
}

func TestRuntimeAppDefaultDevConfigJobOperatorAllowsPauseResumeCancel(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.dev.yaml")
	raw, err := os.ReadFile(filepath.Join(`D:\Repositories\Elysia-Bot`, `deploy`, `config.dev.yaml`))
	if err != nil {
		t.Fatalf("read deploy config: %v", err)
	}
	content := strings.ReplaceAll(string(raw), "data/dev/runtime.sqlite", filepath.ToSlash(filepath.Join(dir, "runtime.sqlite")))
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write isolated deploy config: %v", err)
	}
	t.Setenv("BOT_PLATFORM_OPERATOR_TOKEN", "default-dev-admin")
	t.Setenv("BOT_PLATFORM_OPERATOR_CONFIG_TOKEN", "default-dev-config")
	t.Setenv("BOT_PLATFORM_OPERATOR_JOB_TOKEN", "default-dev-job")
	t.Setenv("BOT_PLATFORM_OPERATOR_SCHEDULE_TOKEN", "default-dev-schedule")
	t.Setenv("BOT_PLATFORM_OPERATOR_VIEWER_TOKEN", "default-dev-viewer")

	app, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("new runtime app from deploy config: %v", err)
	}
	defer func() { _ = app.Close() }()

	job := runtimecore.NewJob("job-default-dev-control", "demo.echo", 1, 30*time.Second)
	if err := app.queue.Enqueue(t.Context(), job); err != nil {
		t.Fatalf("enqueue default-dev control job: %v", err)
	}
	headers := map[string]string{"Authorization": "Bearer default-dev-job"}
	for _, tc := range []struct {
		path       string
		wantAction string
	}{
		{path: "/demo/jobs/" + job.ID + "/pause", wantAction: "job.pause"},
		{path: "/demo/jobs/" + job.ID + "/resume", wantAction: "job.resume"},
	} {
		req := httptest.NewRequest(http.MethodPost, tc.path, nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp := httptest.NewRecorder()
		app.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("expected %s 200 under default dev config, got %d: %s", tc.wantAction, resp.Code, resp.Body.String())
		}
		if !strings.Contains(resp.Body.String(), tc.wantAction) {
			t.Fatalf("expected %s response envelope, got %s", tc.wantAction, resp.Body.String())
		}
	}
	if _, err := app.queue.MarkRunning(t.Context(), job.ID); err != nil {
		t.Fatalf("mark default-dev control job running: %v", err)
	}
	if _, err := app.queue.Fail(t.Context(), job.ID, "boom"); err != nil {
		t.Fatalf("move default-dev control job to retrying: %v", err)
	}
	cancelReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/"+job.ID+"/cancel", nil)
	cancelReq.Header.Set("Authorization", "Bearer default-dev-job")
	cancelResp := httptest.NewRecorder()
	app.ServeHTTP(cancelResp, cancelReq)
	if cancelResp.Code != http.StatusOK {
		t.Fatalf("expected job.cancel 200 under default dev config, got %d: %s", cancelResp.Code, cancelResp.Body.String())
	}
	if !strings.Contains(cancelResp.Body.String(), `job.cancel`) {
		t.Fatalf("expected cancel response envelope, got %s", cancelResp.Body.String())
	}
}

func TestRuntimeAppPluginEchoConfigOperatorPersistsConfigWithStableEnvelopeAndAllowAudit(t *testing.T) {
	t.Parallel()

	configPath := writeWriteActionRBACConfig(t)
	app, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	configReq := httptest.NewRequest(http.MethodPost, "/demo/plugins/plugin-echo/config", strings.NewReader(`{"prefix":"persisted: "}`))
	configReq.Header.Set("Content-Type", "application/json")
	configReq.Header.Set(runtimecore.ConsoleReadActorHeader, "config-operator")
	configResp := httptest.NewRecorder()
	app.ServeHTTP(configResp, configReq)
	if configResp.Code != http.StatusOK {
		t.Fatalf("expected config operator 200, got %d: %s", configResp.Code, configResp.Body.String())
	}
	var configPayload operatorPluginConfigResponsePayload
	if err := json.Unmarshal(configResp.Body.Bytes(), &configPayload); err != nil {
		t.Fatalf("decode config response: %v", err)
	}
	if configPayload.Status != "ok" || configPayload.Action != "plugin.config" || configPayload.Target != "plugin-echo" || !configPayload.Accepted || configPayload.Reason != "plugin_config_updated" || configPayload.PluginID != "plugin-echo" || configPayload.Persisted == nil || !*configPayload.Persisted {
		t.Fatalf("expected normalized config response, got %+v", configPayload)
	}
	stored, err := app.state.LoadPluginConfig(t.Context(), "plugin-echo")
	if err != nil {
		t.Fatalf("load persisted plugin config: %v", err)
	}
	if string(stored.RawConfig) != `{"prefix":"persisted: "}` {
		t.Fatalf("expected persisted raw config, got %s", string(stored.RawConfig))
	}
	entries := app.audits.AuditEntries()
	if len(entries) != 1 {
		t.Fatalf("expected one allow audit entry for config update, got %+v", entries)
	}
	if entries[0].Actor != "config-operator" || entries[0].Action != "plugin.config" || entries[0].Permission != "plugin:config" || entries[0].Target != "plugin-echo" || !entries[0].Allowed || entries[0].Reason != "plugin_config_updated" || entries[0].ErrorCategory != "operator" || entries[0].ErrorCode != "plugin_config_updated" {
		t.Fatalf("expected config update audit entry, got %+v", entries[0])
	}
	if entries[0].SessionID != "" {
		t.Fatalf("expected actor-header config update to omit session id without bearer auth, got %+v", entries[0])
	}
}

func TestRuntimeAppConsoleReadsPersistedAuditSessionIDFromSQLiteState(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	entry := pluginsdk.AuditEntry{
		Actor:         "schedule-admin",
		Permission:    "schedule:cancel",
		Action:        "schedule.cancel",
		Target:        "schedule-persisted-audit-session",
		Allowed:       true,
		Reason:        "schedule_cancelled",
		TraceID:       "trace-persisted-console-audit-session",
		EventID:       "evt-persisted-console-audit-session",
		PluginID:      "plugin-scheduler",
		RunID:         "run-persisted-console-audit-session",
		SessionID:     "session-operator-bearer-schedule-admin",
		CorrelationID: "corr-persisted-console-audit-session",
		ErrorCategory: "operator",
		ErrorCode:     "schedule_cancelled",
		OccurredAt:    "2026-04-21T09:10:00Z",
	}
	if err := app.auditRecorder.RecordAudit(entry); err != nil {
		t.Fatalf("save persisted audit with session: %v", err)
	}

	consoleReq := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	consoleResp := httptest.NewRecorder()
	app.ServeHTTP(consoleResp, consoleReq)
	if consoleResp.Code != http.StatusOK {
		t.Fatalf("expected console 200, got %d: %s", consoleResp.Code, consoleResp.Body.String())
	}
	if !strings.Contains(consoleResp.Body.String(), `"session_id": "session-operator-bearer-schedule-admin"`) {
		t.Fatalf("expected console persisted audit payload to include session_id, got %s", consoleResp.Body.String())
	}

	var payload struct {
		Audits []struct {
			Target    string `json:"target"`
			SessionID string `json:"session_id"`
		} `json:"audits"`
	}
	if err := json.Unmarshal(consoleResp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode console payload with audits: %v", err)
	}
	found := false
	for _, audit := range payload.Audits {
		if audit.Target == entry.Target && audit.SessionID == entry.SessionID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected console audits to round-trip persisted session_id, got %+v", payload.Audits)
	}
}

func TestRuntimeAppPluginEchoConfigOperatorReturnsForbiddenAndRecordsDeniedAudit(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeWriteActionRBACConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	configReq := httptest.NewRequest(http.MethodPost, "/demo/plugins/plugin-echo/config", strings.NewReader(`{"prefix":"denied: "}`))
	configReq.Header.Set("Content-Type", "application/json")
	configReq.Header.Set(runtimecore.ConsoleReadActorHeader, "viewer-user")
	configResp := httptest.NewRecorder()
	app.ServeHTTP(configResp, configReq)
	if configResp.Code != http.StatusForbidden {
		t.Fatalf("expected denied config update 403, got %d: %s", configResp.Code, configResp.Body.String())
	}
	var deniedConfigPayload operatorActionEnvelopePayload
	if err := json.Unmarshal(configResp.Body.Bytes(), &deniedConfigPayload); err != nil {
		t.Fatalf("decode denied config response: %v", err)
	}
	if deniedConfigPayload.Status != "forbidden" || deniedConfigPayload.Action != "plugin.config" || deniedConfigPayload.Target != "plugin-echo" || deniedConfigPayload.Accepted || deniedConfigPayload.Reason != "permission_denied" || deniedConfigPayload.Error != "permission denied" {
		t.Fatalf("expected normalized denied config response, got %+v", deniedConfigPayload)
	}
	if !strings.Contains(configResp.Body.String(), "permission denied") {
		t.Fatalf("expected denied config update response to mention permission denied, got %s", configResp.Body.String())
	}
	if _, err := app.state.LoadPluginConfig(t.Context(), "plugin-echo"); err == nil {
		t.Fatal("expected denied config update not to persist plugin config")
	}
	entries := app.audits.AuditEntries()
	if len(entries) != 1 {
		t.Fatalf("expected one denied config audit entry, got %+v", entries)
	}
	if entries[0].Actor != "viewer-user" || entries[0].Action != "plugin.config" || entries[0].Permission != "plugin:config" || entries[0].Target != "plugin-echo" || entries[0].Allowed || entries[0].Reason != "permission_denied" || entries[0].ErrorCategory != "authorization" || entries[0].ErrorCode != "permission_denied" {
		t.Fatalf("expected denied config audit entry, got %+v", entries[0])
	}
	if entries[0].SessionID != "" {
		t.Fatalf("expected denied config audit without bearer auth to omit session id, got %+v", entries[0])
	}
}

func TestRuntimeAppBindsBearerOperatorIdentityIntoSessionStoreAndAudit(t *testing.T) {
	t.Setenv("BOT_PLATFORM_OPERATOR_TOKEN", "opaque-operator-token")
	app, err := newRuntimeApp(writeOperatorAuthConfig(t, t.TempDir()))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	req := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	req.Header.Set("Authorization", "Bearer opaque-operator-token")
	req.Header.Set(runtimecore.ConsoleReadActorHeader, "admin-user")
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected bearer-bound console read 200, got %d: %s", resp.Code, resp.Body.String())
	}

	stored, err := app.state.LoadSession(t.Context(), runtimecore.OperatorBearerSessionID("admin-user"))
	if err != nil {
		t.Fatalf("load persisted operator session: %v", err)
	}
	if stored.SessionID != runtimecore.OperatorBearerSessionID("admin-user") || stored.PluginID != runtimecore.OperatorAuthSessionPluginID {
		t.Fatalf("unexpected persisted operator session identity %+v", stored)
	}
	if stored.State["actor_id"] != "admin-user" || stored.State["token_id"] != "console-main" || stored.State["auth_method"] != runtimecore.RequestIdentityAuthMethodBearer {
		t.Fatalf("unexpected persisted operator session state %+v", stored.State)
	}
	listed, err := app.state.ListSessions(t.Context())
	if err != nil {
		t.Fatalf("list persisted sessions: %v", err)
	}
	if len(listed) != 1 || listed[0].SessionID != stored.SessionID {
		t.Fatalf("expected one persisted bearer-bound session, got %+v", listed)
	}
	counts, err := app.state.Counts(t.Context())
	if err != nil {
		t.Fatalf("sqlite counts after bearer-bound console read: %v", err)
	}
	if counts["sessions"] != 1 {
		t.Fatalf("expected one persisted operator-auth session row, got %+v", counts)
	}

	allowReq := httptest.NewRequest(http.MethodPost, "/demo/plugins/plugin-echo/disable", nil)
	allowReq.Header.Set("Authorization", "Bearer opaque-operator-token")
	allowReq.Header.Set(runtimecore.ConsoleReadActorHeader, "admin-user")
	allowResp := httptest.NewRecorder()
	app.ServeHTTP(allowResp, allowReq)
	if allowResp.Code != http.StatusOK {
		t.Fatalf("expected bearer-bound plugin disable 200, got %d: %s", allowResp.Code, allowResp.Body.String())
	}
	entries := app.audits.AuditEntries()
	if len(entries) == 0 {
		t.Fatal("expected bearer-bound plugin disable to record audit evidence")
	}
	last := entries[len(entries)-1]
	if last.Action != "plugin.disable" || last.Target != "plugin-echo" || !last.Allowed || last.SessionID != runtimecore.OperatorBearerSessionID("admin-user") || last.Reason != "plugin_disabled" || last.ErrorCategory != "operator" || last.ErrorCode != "plugin_disabled" {
		t.Fatalf("expected allowed operator audit to carry bearer session id, got %+v", last)
	}

	denyReq := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	denyReq.Header.Set("Authorization", "Bearer opaque-operator-token")
	denyReq.Header.Set(runtimecore.ConsoleReadActorHeader, "admin-user")
	replaceRuntimeCurrentRBACState(t, app, []runtimecore.OperatorIdentityState{{ActorID: "admin-user", Roles: []string{"admin"}}}, runtimecore.RBACSnapshotState{SnapshotKey: runtimecore.CurrentRBACSnapshotKey, ConsoleReadPermission: "console:read", Policies: map[string]pluginsdk.AuthorizationPolicy{"admin": {Permissions: []string{"plugin:enable", "plugin:disable"}, PluginScope: []string{"*"}}}})
	denyResp := httptest.NewRecorder()
	app.ServeHTTP(denyResp, denyReq)
	if denyResp.Code != http.StatusForbidden {
		t.Fatalf("expected bearer-bound console read deny 403, got %d: %s", denyResp.Code, denyResp.Body.String())
	}
	entries = app.audits.AuditEntries()
	if len(entries) == 0 {
		t.Fatal("expected bearer-bound denied console read to record audit evidence")
	}
	last = entries[len(entries)-1]
	if last.Action != "console.read" || last.SessionID != runtimecore.OperatorBearerSessionID("admin-user") {
		t.Fatalf("expected denied console audit to carry bearer session id, got %+v", last)
	}
	if !strings.Contains(denyResp.Body.String(), "permission denied") {
		t.Fatalf("expected bearer-bound denied console read response to mention permission denied, got %s", denyResp.Body.String())
	}
}

func TestRuntimeAppConsoleRequiresBearerAuthWhenOperatorAuthConfigured(t *testing.T) {
	t.Setenv("BOT_PLATFORM_OPERATOR_TOKEN", "opaque-operator-token")
	app, err := newRuntimeApp(writeOperatorAuthConfig(t, t.TempDir()))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()
	baselineAudits := len(app.audits.AuditEntries())

	missingReq := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	missingReq.Header.Set(runtimecore.ConsoleReadActorHeader, "admin-user")
	missingResp := httptest.NewRecorder()
	app.ServeHTTP(missingResp, missingReq)
	if missingResp.Code != http.StatusUnauthorized {
		t.Fatalf("expected header-only console read 401 when operator auth is configured, got %d: %s", missingResp.Code, missingResp.Body.String())
	}
	if !strings.Contains(missingResp.Body.String(), "unauthorized") {
		t.Fatalf("expected header-only console read response to mention unauthorized, got %s", missingResp.Body.String())
	}
	if len(app.audits.AuditEntries()) != baselineAudits {
		t.Fatalf("expected unauthorized console read not to add deny audit entries, got %+v", app.audits.AuditEntries())
	}

	invalidReq := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	invalidReq.Header.Set("Authorization", "Bearer invalid-operator-token")
	invalidReq.Header.Set(runtimecore.ConsoleReadActorHeader, "admin-user")
	invalidResp := httptest.NewRecorder()
	app.ServeHTTP(invalidResp, invalidReq)
	if invalidResp.Code != http.StatusUnauthorized {
		t.Fatalf("expected invalid bearer console read 401, got %d: %s", invalidResp.Code, invalidResp.Body.String())
	}
	if !strings.Contains(invalidResp.Body.String(), "unauthorized") {
		t.Fatalf("expected invalid bearer console read response to mention unauthorized, got %s", invalidResp.Body.String())
	}
	if len(app.audits.AuditEntries()) != baselineAudits {
		t.Fatalf("expected invalid bearer console read not to add deny audit entries, got %+v", app.audits.AuditEntries())
	}
	counts, err := app.state.Counts(t.Context())
	if err != nil {
		t.Fatalf("sqlite counts after unauthorized console reads: %v", err)
	}
	if counts["sessions"] != 0 {
		t.Fatalf("expected unauthorized console reads not to persist operator sessions, got %+v", counts)
	}
}

func TestRuntimeAppConsoleRequiresBearerAuthWhenOperatorAuthConfiguredWithoutConsoleReadPermission(t *testing.T) {
	t.Setenv("BOT_PLATFORM_OPERATOR_TOKEN", "opaque-operator-token")
	app, err := newRuntimeApp(writeOperatorAuthConfigWithoutConsoleReadPermission(t, t.TempDir()))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()
	baselineAudits := len(app.audits.AuditEntries())

	missingReq := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	missingReq.Header.Set(runtimecore.ConsoleReadActorHeader, "admin-user")
	missingResp := httptest.NewRecorder()
	app.ServeHTTP(missingResp, missingReq)
	if missingResp.Code != http.StatusUnauthorized {
		t.Fatalf("expected console read without console_read_permission and without bearer auth to return 401, got %d: %s", missingResp.Code, missingResp.Body.String())
	}

	invalidReq := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	invalidReq.Header.Set("Authorization", "Bearer invalid-operator-token")
	invalidResp := httptest.NewRecorder()
	app.ServeHTTP(invalidResp, invalidReq)
	if invalidResp.Code != http.StatusUnauthorized {
		t.Fatalf("expected console read without console_read_permission and with invalid bearer auth to return 401, got %d: %s", invalidResp.Code, invalidResp.Body.String())
	}

	allowedReq := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	allowedReq.Header.Set("Authorization", "Bearer opaque-operator-token")
	allowedResp := httptest.NewRecorder()
	app.ServeHTTP(allowedResp, allowedReq)
	if allowedResp.Code != http.StatusOK {
		t.Fatalf("expected console read without console_read_permission and with valid bearer auth to return 200, got %d: %s", allowedResp.Code, allowedResp.Body.String())
	}
	if len(app.audits.AuditEntries()) != baselineAudits {
		t.Fatalf("expected missing/invalid bearer console requests without console_read_permission not to add audit entries, got %+v", app.audits.AuditEntries())
	}
}

func TestRuntimeAppOperatorWriteRoutesRequireBearerAuthWhenOperatorAuthConfigured(t *testing.T) {
	t.Setenv("BOT_PLATFORM_OPERATOR_TOKEN", "opaque-operator-token")
	t.Setenv("BOT_PLATFORM_OPERATOR_CONFIG_TOKEN", "opaque-config-token")
	t.Setenv("BOT_PLATFORM_OPERATOR_JOB_TOKEN", "opaque-job-token")
	t.Setenv("BOT_PLATFORM_OPERATOR_SCHEDULE_TOKEN", "opaque-schedule-token")
	t.Setenv("BOT_PLATFORM_OPERATOR_VIEWER_TOKEN", "opaque-viewer-token")
	app, err := newRuntimeApp(writeWriteActionRBACOperatorAuthConfig(t, t.TempDir()))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	createScheduleReq := httptest.NewRequest(http.MethodPost, "/demo/schedules/echo-delay", strings.NewReader(`{"id":"schedule-operator-auth","delay_ms":500,"message":"cancel me"}`))
	createScheduleReq.Header.Set("Content-Type", "application/json")
	createScheduleResp := httptest.NewRecorder()
	app.ServeHTTP(createScheduleResp, createScheduleReq)
	if createScheduleResp.Code != http.StatusOK {
		t.Fatalf("expected schedule seed create 200, got %d: %s", createScheduleResp.Code, createScheduleResp.Body.String())
	}

	job := runtimecore.NewJob("job-operator-auth-retry", "ai.chat", 0, 30*time.Second)
	job.TraceID = "trace-job-operator-auth-retry"
	job.EventID = "evt-job-operator-auth-retry"
	job.Correlation = "runtime-ai:user-operator-auth:retry"
	if err := applyDemoAIJobContract(&job, "retry operator auth", "user-operator-auth"); err != nil {
		t.Fatalf("apply demo ai job contract: %v", err)
	}
	if err := app.queue.Enqueue(t.Context(), job); err != nil {
		t.Fatalf("enqueue operator-auth retry seed job: %v", err)
	}
	timeoutReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/timeout?id="+job.ID, nil)
	timeoutResp := httptest.NewRecorder()
	app.ServeHTTP(timeoutResp, timeoutReq)
	if timeoutResp.Code != http.StatusOK {
		t.Fatalf("expected retry seed timeout 200, got %d: %s", timeoutResp.Code, timeoutResp.Body.String())
	}
	baselineAudits := len(app.audits.AuditEntries())

	tests := []struct {
		name string
		path string
		body string
	}{
		{name: "plugin disable", path: "/demo/plugins/plugin-echo/disable"},
		{name: "plugin config", path: "/demo/plugins/plugin-echo/config", body: `{"prefix":"unauthorized: "}`},
		{name: "job pause", path: "/demo/jobs/" + job.ID + "/pause"},
		{name: "job resume", path: "/demo/jobs/" + job.ID + "/resume"},
		{name: "job cancel", path: "/demo/jobs/" + job.ID + "/cancel"},
		{name: "job retry", path: "/demo/jobs/" + job.ID + "/retry"},
		{name: "schedule cancel", path: "/demo/schedules/schedule-operator-auth/cancel"},
	}
	for _, tc := range tests {
		req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
		if tc.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set(runtimecore.ConsoleReadActorHeader, "admin-user")
		resp := httptest.NewRecorder()
		app.ServeHTTP(resp, req)
		if resp.Code != http.StatusUnauthorized {
			t.Fatalf("expected %s without bearer auth to return 401, got %d: %s", tc.name, resp.Code, resp.Body.String())
		}
		if !strings.Contains(resp.Body.String(), "unauthorized") {
			t.Fatalf("expected %s without bearer auth to mention unauthorized, got %s", tc.name, resp.Body.String())
		}
	}
	if len(app.audits.AuditEntries()) != baselineAudits {
		t.Fatalf("expected unauthorized operator writes not to add deny audit entries, got %+v", app.audits.AuditEntries())
	}
	counts, err := app.state.Counts(t.Context())
	if err != nil {
		t.Fatalf("sqlite counts after unauthorized operator writes: %v", err)
	}
	if counts["sessions"] != 0 {
		t.Fatalf("expected unauthorized operator writes not to persist operator sessions, got %+v", counts)
	}
}

func TestRuntimeAppOperatorWriteRoutesReturnForbiddenForAuthenticatedRBACDenials(t *testing.T) {
	t.Setenv("BOT_PLATFORM_OPERATOR_TOKEN", "opaque-operator-token")
	t.Setenv("BOT_PLATFORM_OPERATOR_CONFIG_TOKEN", "opaque-config-token")
	t.Setenv("BOT_PLATFORM_OPERATOR_JOB_TOKEN", "opaque-job-token")
	t.Setenv("BOT_PLATFORM_OPERATOR_SCHEDULE_TOKEN", "opaque-schedule-token")
	t.Setenv("BOT_PLATFORM_OPERATOR_VIEWER_TOKEN", "opaque-viewer-token")
	app, err := newRuntimeApp(writeWriteActionRBACOperatorAuthConfig(t, t.TempDir()))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	createScheduleReq := httptest.NewRequest(http.MethodPost, "/demo/schedules/echo-delay", strings.NewReader(`{"id":"schedule-bearer-denied","delay_ms":500,"message":"cancel denied"}`))
	createScheduleReq.Header.Set("Content-Type", "application/json")
	createScheduleResp := httptest.NewRecorder()
	app.ServeHTTP(createScheduleResp, createScheduleReq)
	if createScheduleResp.Code != http.StatusOK {
		t.Fatalf("expected schedule seed create 200, got %d: %s", createScheduleResp.Code, createScheduleResp.Body.String())
	}

	job := runtimecore.NewJob("job-bearer-denied-retry", "ai.chat", 0, 30*time.Second)
	job.TraceID = "trace-job-bearer-denied-retry"
	job.EventID = "evt-job-bearer-denied-retry"
	job.Correlation = "runtime-ai:user-bearer-denied:retry"
	if err := applyDemoAIJobContract(&job, "retry bearer denied", "user-bearer-denied"); err != nil {
		t.Fatalf("apply demo ai job contract: %v", err)
	}
	if err := app.queue.Enqueue(t.Context(), job); err != nil {
		t.Fatalf("enqueue bearer-denied retry seed job: %v", err)
	}
	timeoutReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/timeout?id="+job.ID, nil)
	timeoutResp := httptest.NewRecorder()
	app.ServeHTTP(timeoutResp, timeoutReq)
	if timeoutResp.Code != http.StatusOK {
		t.Fatalf("expected retry seed timeout 200, got %d: %s", timeoutResp.Code, timeoutResp.Body.String())
	}

	consoleReq := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	consoleReq.Header.Set("Authorization", "Bearer opaque-viewer-token")
	consoleResp := httptest.NewRecorder()
	app.ServeHTTP(consoleResp, consoleReq)
	if consoleResp.Code != http.StatusOK {
		t.Fatalf("expected viewer bearer console read 200, got %d: %s", consoleResp.Code, consoleResp.Body.String())
	}
	baselineAudits := len(app.audits.AuditEntries())

	pluginReq := httptest.NewRequest(http.MethodPost, "/demo/plugins/plugin-echo/disable", nil)
	pluginReq.Header.Set("Authorization", "Bearer opaque-viewer-token")
	pluginReq.Header.Set(runtimecore.ConsoleReadActorHeader, "admin-user")
	pluginResp := httptest.NewRecorder()
	app.ServeHTTP(pluginResp, pluginReq)
	if pluginResp.Code != http.StatusForbidden {
		t.Fatalf("expected bearer-authenticated plugin disable denial 403, got %d: %s", pluginResp.Code, pluginResp.Body.String())
	}
	var deniedPluginPayload operatorActionEnvelopePayload
	if err := json.Unmarshal(pluginResp.Body.Bytes(), &deniedPluginPayload); err != nil {
		t.Fatalf("decode bearer-authenticated plugin disable denial: %v", err)
	}
	if deniedPluginPayload.Status != "forbidden" || deniedPluginPayload.Action != "plugin.disable" || deniedPluginPayload.Target != "plugin-echo" || deniedPluginPayload.Accepted || deniedPluginPayload.Reason != "permission_denied" || deniedPluginPayload.Error != "permission denied" {
		t.Fatalf("expected normalized bearer-authenticated plugin disable denial, got %+v", deniedPluginPayload)
	}

	configReq := httptest.NewRequest(http.MethodPost, "/demo/plugins/plugin-echo/config", strings.NewReader(`{"prefix":"forbidden: "}`))
	configReq.Header.Set("Content-Type", "application/json")
	configReq.Header.Set("Authorization", "Bearer opaque-viewer-token")
	configReq.Header.Set(runtimecore.ConsoleReadActorHeader, "config-operator")
	configResp := httptest.NewRecorder()
	app.ServeHTTP(configResp, configReq)
	if configResp.Code != http.StatusForbidden {
		t.Fatalf("expected bearer-authenticated config denial 403, got %d: %s", configResp.Code, configResp.Body.String())
	}

	retryReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/"+job.ID+"/retry", nil)
	retryReq.Header.Set("Authorization", "Bearer opaque-viewer-token")
	retryReq.Header.Set(runtimecore.ConsoleReadActorHeader, "job-operator")
	retryResp := httptest.NewRecorder()
	app.ServeHTTP(retryResp, retryReq)
	if retryResp.Code != http.StatusForbidden {
		t.Fatalf("expected bearer-authenticated retry denial 403, got %d: %s", retryResp.Code, retryResp.Body.String())
	}

	cancelReq := httptest.NewRequest(http.MethodPost, "/demo/schedules/schedule-bearer-denied/cancel", nil)
	cancelReq.Header.Set("Authorization", "Bearer opaque-viewer-token")
	cancelReq.Header.Set(runtimecore.ConsoleReadActorHeader, "schedule-admin")
	cancelResp := httptest.NewRecorder()
	app.ServeHTTP(cancelResp, cancelReq)
	if cancelResp.Code != http.StatusForbidden {
		t.Fatalf("expected bearer-authenticated cancel denial 403, got %d: %s", cancelResp.Code, cancelResp.Body.String())
	}

	for name, resp := range map[string]*httptest.ResponseRecorder{
		"plugin disable":  pluginResp,
		"plugin config":   configResp,
		"job retry":       retryResp,
		"schedule cancel": cancelResp,
	} {
		if !strings.Contains(resp.Body.String(), "permission denied") {
			t.Fatalf("expected %s denial to mention permission denied, got %s", name, resp.Body.String())
		}
	}

	entries := app.audits.AuditEntries()
	deniedEntries := entries[baselineAudits:]
	if len(deniedEntries) != 4 {
		t.Fatalf("expected four bearer-authenticated denied operator audits, got %+v", entries)
	}
	for _, entry := range deniedEntries {
		if entry.Actor != "viewer-user" || entry.SessionID != runtimecore.OperatorBearerSessionID("viewer-user") || entry.Allowed || entry.Reason != "permission_denied" || entry.ErrorCategory != "authorization" || entry.ErrorCode != "permission_denied" {
			t.Fatalf("expected bearer-authenticated deny audit to use viewer request identity, got %+v", entry)
		}
	}
	if _, err := app.state.LoadPluginConfig(t.Context(), "plugin-echo"); err == nil {
		t.Fatal("expected forbidden bearer config update not to persist plugin config")
	}
	storedJob, err := app.queue.Inspect(t.Context(), job.ID)
	if err != nil {
		t.Fatalf("inspect bearer-denied retry job: %v", err)
	}
	if storedJob.Status != runtimecore.JobStatusDead || !storedJob.DeadLetter {
		t.Fatalf("expected forbidden bearer retry to preserve dead-letter job, got %+v", storedJob)
	}
	if _, err := app.state.LoadSchedulePlan(t.Context(), "schedule-bearer-denied"); err != nil {
		t.Fatalf("expected forbidden bearer cancel to preserve schedule, got %v", err)
	}
}

func TestRuntimeAppPluginConfigOperatorReturnsNotFoundForUnboundPlugin(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeWriteActionRBACConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	configReq := httptest.NewRequest(http.MethodPost, "/demo/plugins/plugin-admin/config", strings.NewReader(`{"prefix":"ignored: "}`))
	configReq.Header.Set("Content-Type", "application/json")
	configReq.Header.Set(runtimecore.ConsoleReadActorHeader, "config-operator")
	configResp := httptest.NewRecorder()
	app.ServeHTTP(configResp, configReq)

	if configResp.Code != http.StatusNotFound {
		t.Fatalf("expected unbound plugin config operator 404, got %d: %s", configResp.Code, configResp.Body.String())
	}
	if len(app.audits.AuditEntries()) != 0 {
		t.Fatalf("expected unbound plugin config operator not to record auth or allow audit, got %+v", app.audits.AuditEntries())
	}
}

func TestRuntimeAppPluginConfigOperatorRequiresBearerAuthBeforeUnboundPluginLookup(t *testing.T) {
	t.Setenv("BOT_PLATFORM_OPERATOR_TOKEN", "opaque-operator-token")
	t.Setenv("BOT_PLATFORM_OPERATOR_CONFIG_TOKEN", "opaque-config-token")
	t.Setenv("BOT_PLATFORM_OPERATOR_JOB_TOKEN", "opaque-job-token")
	t.Setenv("BOT_PLATFORM_OPERATOR_SCHEDULE_TOKEN", "opaque-schedule-token")
	t.Setenv("BOT_PLATFORM_OPERATOR_VIEWER_TOKEN", "opaque-viewer-token")
	app, err := newRuntimeApp(writeWriteActionRBACOperatorAuthConfig(t, t.TempDir()))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()
	baselineAudits := len(app.audits.AuditEntries())

	unauthReq := httptest.NewRequest(http.MethodPost, "/demo/plugins/plugin-admin/config", strings.NewReader(`{"prefix":"ignored: "}`))
	unauthReq.Header.Set("Content-Type", "application/json")
	unauthResp := httptest.NewRecorder()
	app.ServeHTTP(unauthResp, unauthReq)
	if unauthResp.Code != http.StatusUnauthorized {
		t.Fatalf("expected unbound plugin config without bearer auth to return 401, got %d: %s", unauthResp.Code, unauthResp.Body.String())
	}
	if !strings.Contains(unauthResp.Body.String(), "unauthorized") {
		t.Fatalf("expected unbound plugin config without bearer auth to mention unauthorized, got %s", unauthResp.Body.String())
	}
	if len(app.audits.AuditEntries()) != baselineAudits {
		t.Fatalf("expected unauthorized unbound plugin config request not to add audit entries, got %+v", app.audits.AuditEntries())
	}

	authReq := httptest.NewRequest(http.MethodPost, "/demo/plugins/plugin-admin/config", strings.NewReader(`{"prefix":"ignored: "}`))
	authReq.Header.Set("Content-Type", "application/json")
	authReq.Header.Set("Authorization", "Bearer opaque-config-token")
	authResp := httptest.NewRecorder()
	app.ServeHTTP(authResp, authReq)
	if authResp.Code != http.StatusNotFound {
		t.Fatalf("expected authenticated unbound plugin config request to return 404, got %d: %s", authResp.Code, authResp.Body.String())
	}
	if len(app.audits.AuditEntries()) != baselineAudits {
		t.Fatalf("expected authenticated unbound plugin config request not to add audit entries, got %+v", app.audits.AuditEntries())
	}
}

func TestRuntimeAppDelayScheduleTriggersEchoReply(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	req := httptest.NewRequest(http.MethodPost, "/demo/schedules/echo-delay", strings.NewReader(`{"id":"schedule-demo-1","delay_ms":30,"message":"scheduled hello"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}

	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		repliesReq := httptest.NewRequest(http.MethodGet, "/demo/replies", nil)
		repliesResp := httptest.NewRecorder()
		app.ServeHTTP(repliesResp, repliesReq)
		if strings.Contains(repliesResp.Body.String(), "scheduled hello") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected scheduled echo reply to be recorded")
}

func TestRuntimeAppPersistsDelaySchedulePlan(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	req := httptest.NewRequest(http.MethodPost, "/demo/schedules/echo-delay", strings.NewReader(`{"id":"schedule-store-1","delay_ms":500,"message":"persisted hello"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}

	stored, err := app.state.LoadSchedulePlan(t.Context(), "schedule-store-1")
	if err != nil {
		t.Fatalf("load persisted schedule plan: %v", err)
	}
	if stored.Plan.ID != "schedule-store-1" || stored.Plan.Kind != runtimecore.ScheduleKindDelay {
		t.Fatalf("expected persisted delay schedule, got %+v", stored)
	}
	if stored.Plan.Metadata["message_text"] != "persisted hello" {
		t.Fatalf("expected persisted schedule metadata, got %+v", stored.Plan.Metadata)
	}
	if stored.DueAt == nil {
		t.Fatalf("expected persisted dueAt for delay schedule, got %+v", stored)
	}

	counts, err := app.state.Counts(t.Context())
	if err != nil {
		t.Fatalf("sqlite counts: %v", err)
	}
	if counts["schedule_plans"] != 1 {
		t.Fatalf("expected one persisted schedule plan, got %+v", counts)
	}

	consoleReq := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	consoleResp := httptest.NewRecorder()
	app.ServeHTTP(consoleResp, consoleReq)
	for _, expected := range []string{"schedule-store-1", `"schedules": 1`, `"eventType": "message.received"`, `"jobs": 0`, `"dueAtSource": "persisted-state"`, `"dueAtEvidence": "persisted-schedule-due-at"`, `"dueAtPersisted": true`} {
		if !strings.Contains(consoleResp.Body.String(), expected) {
			t.Fatalf("expected console payload to include %q, got %s", expected, consoleResp.Body.String())
		}
	}
}

func TestRuntimeAppCancelScheduleOperatorRemovesPersistedPlanFromConsoleAndRecordsEvidence(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeScheduleCancelRBACConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	createReq := httptest.NewRequest(http.MethodPost, "/demo/schedules/echo-delay", strings.NewReader(`{"id":"schedule-cancel-1","delay_ms":500,"message":"cancel me"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createResp := httptest.NewRecorder()
	app.ServeHTTP(createResp, createReq)
	if createResp.Code != http.StatusOK {
		t.Fatalf("expected create schedule 200, got %d: %s", createResp.Code, createResp.Body.String())
	}

	before := readRuntimeConsoleResponse(t, app)
	if before.Status.Schedules != 1 || !hasConsoleSchedule(before, "schedule-cancel-1") {
		t.Fatalf("expected console to show one schedule before cancel, got %+v", before)
	}

	cancelReq := httptest.NewRequest(http.MethodPost, "/demo/schedules/schedule-cancel-1/cancel", nil)
	cancelReq.Header.Set(runtimecore.ConsoleReadActorHeader, "schedule-admin")
	cancelResp := httptest.NewRecorder()
	app.ServeHTTP(cancelResp, cancelReq)
	if cancelResp.Code != http.StatusOK {
		t.Fatalf("expected cancel operator 200, got %d: %s", cancelResp.Code, cancelResp.Body.String())
	}
	var cancelPayload operatorScheduleResponsePayload
	if err := json.Unmarshal(cancelResp.Body.Bytes(), &cancelPayload); err != nil {
		t.Fatalf("decode cancel response: %v", err)
	}
	if cancelPayload.Status != "ok" || cancelPayload.Action != "schedule.cancel" || cancelPayload.Target != "schedule-cancel-1" || !cancelPayload.Accepted || cancelPayload.Reason != "schedule_cancelled" || cancelPayload.ScheduleID != "schedule-cancel-1" {
		t.Fatalf("expected normalized cancel response, got %+v", cancelPayload)
	}

	if _, err := app.state.LoadSchedulePlan(t.Context(), "schedule-cancel-1"); err == nil {
		t.Fatal("expected cancelled schedule to be deleted from persistence")
	}
	counts, err := app.state.Counts(t.Context())
	if err != nil {
		t.Fatalf("sqlite counts: %v", err)
	}
	if counts["schedule_plans"] != 0 {
		t.Fatalf("expected no persisted schedules after cancel, got %+v", counts)
	}

	after := readRuntimeConsoleResponse(t, app)
	if after.Status.Schedules != 0 || hasConsoleSchedule(after, "schedule-cancel-1") {
		t.Fatalf("expected cancelled schedule to disappear from console read side, got %+v", after)
	}

	entries := app.audits.AuditEntries()
	if len(entries) == 0 {
		t.Fatal("expected cancel operator to record audit evidence")
	}
	lastEntry := entries[len(entries)-1]
	if lastEntry.Action != "schedule.cancel" || lastEntry.Target != "schedule-cancel-1" || !lastEntry.Allowed || lastEntry.Actor != "schedule-admin" || lastEntry.Permission != "schedule:cancel" || lastEntry.Reason != "schedule_cancelled" || lastEntry.ErrorCategory != "operator" || lastEntry.ErrorCode != "schedule_cancelled" {
		t.Fatalf("expected cancel audit entry, got %+v", lastEntry)
	}
	logs := app.logs.Lines()
	matchedLog := false
	for _, line := range logs {
		if strings.Contains(line, "runtime schedule cancelled") && strings.Contains(line, "schedule-cancel-1") && strings.Contains(line, "schedule-admin") {
			matchedLog = true
			break
		}
	}
	if !matchedLog {
		t.Fatalf("expected cancel operator log evidence, got %+v", logs)
	}

	notFoundReq := httptest.NewRequest(http.MethodPost, "/demo/schedules/schedule-cancel-1/cancel", nil)
	notFoundReq.Header.Set(runtimecore.ConsoleReadActorHeader, "schedule-admin")
	notFoundResp := httptest.NewRecorder()
	app.ServeHTTP(notFoundResp, notFoundReq)
	if notFoundResp.Code != http.StatusNotFound {
		t.Fatalf("expected second cancel to return 404, got %d: %s", notFoundResp.Code, notFoundResp.Body.String())
	}
	if !strings.Contains(notFoundResp.Body.String(), "schedule not found") {
		t.Fatalf("expected 404 response to mention schedule not found, got %s", notFoundResp.Body.String())
	}
}

func TestRuntimeAppCancelScheduleOperatorReturnsForbiddenAndRecordsDeniedAudit(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeScheduleCancelRBACConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	createReq := httptest.NewRequest(http.MethodPost, "/demo/schedules/echo-delay", strings.NewReader(`{"id":"schedule-cancel-denied","delay_ms":500,"message":"deny me"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createResp := httptest.NewRecorder()
	app.ServeHTTP(createResp, createReq)
	if createResp.Code != http.StatusOK {
		t.Fatalf("expected create schedule 200, got %d: %s", createResp.Code, createResp.Body.String())
	}

	cancelReq := httptest.NewRequest(http.MethodPost, "/demo/schedules/schedule-cancel-denied/cancel", nil)
	cancelReq.Header.Set(runtimecore.ConsoleReadActorHeader, "viewer-user")
	cancelResp := httptest.NewRecorder()
	app.ServeHTTP(cancelResp, cancelReq)
	if cancelResp.Code != http.StatusForbidden {
		t.Fatalf("expected cancel operator 403, got %d: %s", cancelResp.Code, cancelResp.Body.String())
	}
	var deniedCancelPayload operatorActionEnvelopePayload
	if err := json.Unmarshal(cancelResp.Body.Bytes(), &deniedCancelPayload); err != nil {
		t.Fatalf("decode denied cancel response: %v", err)
	}
	if deniedCancelPayload.Status != "forbidden" || deniedCancelPayload.Action != "schedule.cancel" || deniedCancelPayload.Target != "schedule-cancel-denied" || deniedCancelPayload.Accepted || deniedCancelPayload.Reason != "permission_denied" || deniedCancelPayload.Error != "permission denied" {
		t.Fatalf("expected normalized denied cancel response, got %+v", deniedCancelPayload)
	}
	if !strings.Contains(cancelResp.Body.String(), "permission denied") {
		t.Fatalf("expected forbidden cancel response to mention permission denied, got %s", cancelResp.Body.String())
	}

	if _, err := app.state.LoadSchedulePlan(t.Context(), "schedule-cancel-denied"); err != nil {
		t.Fatalf("expected denied cancel to preserve persisted schedule, got %v", err)
	}
	console := readRuntimeConsoleResponse(t, app)
	if console.Status.Schedules != 1 || !hasConsoleSchedule(console, "schedule-cancel-denied") {
		t.Fatalf("expected denied cancel to preserve console schedule read model, got %+v", console)
	}

	entries := app.audits.AuditEntries()
	if len(entries) != 1 {
		t.Fatalf("expected one denied cancel audit entry, got %+v", entries)
	}
	if entries[0].Actor != "viewer-user" || entries[0].Action != "schedule.cancel" || entries[0].Permission != "schedule:cancel" || entries[0].Target != "schedule-cancel-denied" || entries[0].Allowed || entries[0].Reason != "permission_denied" || entries[0].ErrorCategory != "authorization" || entries[0].ErrorCode != "permission_denied" {
		t.Fatalf("expected denied cancel audit entry, got %+v", entries[0])
	}
	for _, line := range app.logs.Lines() {
		if strings.Contains(line, "runtime schedule cancelled") && strings.Contains(line, "schedule-cancel-denied") {
			t.Fatalf("expected denied cancel not to emit success log, got %+v", app.logs.Lines())
		}
	}
}

func TestRuntimeAppCancelScheduleOperatorFailsClosedWithoutActorHeaderUnderConfiguredRBAC(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeScheduleCancelRBACConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	createReq := httptest.NewRequest(http.MethodPost, "/demo/schedules/echo-delay", strings.NewReader(`{"id":"schedule-cancel-missing-actor","delay_ms":500,"message":"deny me"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createResp := httptest.NewRecorder()
	app.ServeHTTP(createResp, createReq)
	if createResp.Code != http.StatusOK {
		t.Fatalf("expected create schedule 200, got %d: %s", createResp.Code, createResp.Body.String())
	}

	cancelReq := httptest.NewRequest(http.MethodPost, "/demo/schedules/schedule-cancel-missing-actor/cancel", nil)
	cancelResp := httptest.NewRecorder()
	app.ServeHTTP(cancelResp, cancelReq)
	if cancelResp.Code != http.StatusForbidden {
		t.Fatalf("expected headerless cancel 403, got %d: %s", cancelResp.Code, cancelResp.Body.String())
	}
	var headerlessCancelPayload operatorActionEnvelopePayload
	if err := json.Unmarshal(cancelResp.Body.Bytes(), &headerlessCancelPayload); err != nil {
		t.Fatalf("decode headerless cancel response: %v", err)
	}
	if headerlessCancelPayload.Status != "forbidden" || headerlessCancelPayload.Action != "schedule.cancel" || headerlessCancelPayload.Target != "schedule-cancel-missing-actor" || headerlessCancelPayload.Accepted || headerlessCancelPayload.Reason != "permission_denied" || headerlessCancelPayload.Error != "permission denied" {
		t.Fatalf("expected normalized headerless cancel response, got %+v", headerlessCancelPayload)
	}
	if !strings.Contains(cancelResp.Body.String(), "permission denied") {
		t.Fatalf("expected headerless cancel response to mention permission denied, got %s", cancelResp.Body.String())
	}
	entries := app.audits.AuditEntries()
	if len(entries) != 1 || entries[0].Actor != "" || entries[0].Action != "schedule.cancel" || entries[0].Permission != "schedule:cancel" || entries[0].Target != "schedule-cancel-missing-actor" || entries[0].Allowed || entries[0].Reason != "permission_denied" || entries[0].ErrorCategory != "authorization" || entries[0].ErrorCode != "permission_denied" {
		t.Fatalf("expected headerless cancel deny audit entry, got %+v", entries)
	}
}

func TestRuntimeAppCancelScheduleOperatorDeletionSurvivesRestart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := writeTestConfigAt(t, dir)

	app, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}

	createReq := httptest.NewRequest(http.MethodPost, "/demo/schedules/echo-delay", strings.NewReader(`{"id":"schedule-cancel-restart","delay_ms":500,"message":"cancel before restart"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createResp := httptest.NewRecorder()
	app.ServeHTTP(createResp, createReq)
	if createResp.Code != http.StatusOK {
		t.Fatalf("expected create schedule 200, got %d: %s", createResp.Code, createResp.Body.String())
	}

	cancelReq := httptest.NewRequest(http.MethodPost, "/demo/schedules/schedule-cancel-restart/cancel", nil)
	cancelReq.Header.Set(runtimecore.ConsoleReadActorHeader, "schedule-admin")
	cancelResp := httptest.NewRecorder()
	app.ServeHTTP(cancelResp, cancelReq)
	if cancelResp.Code != http.StatusOK {
		t.Fatalf("expected cancel operator 200, got %d: %s", cancelResp.Code, cancelResp.Body.String())
	}
	if err := app.Close(); err != nil {
		t.Fatalf("close first app: %v", err)
	}

	restarted, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("restart runtime app: %v", err)
	}
	defer func() { _ = restarted.Close() }()

	if _, err := restarted.state.LoadSchedulePlan(t.Context(), "schedule-cancel-restart"); err == nil {
		t.Fatal("expected cancelled schedule to stay deleted after restart")
	}
	consoleReq := consoleRequestWithViewer("/api/console")
	consoleResp := httptest.NewRecorder()
	restarted.ServeHTTP(consoleResp, consoleReq)
	if consoleResp.Code != http.StatusOK {
		t.Fatalf("expected restarted console 200, got %d: %s", consoleResp.Code, consoleResp.Body.String())
	}
	var console runtimeConsoleResponse
	if err := json.Unmarshal(consoleResp.Body.Bytes(), &console); err != nil {
		t.Fatalf("decode console payload: %v", err)
	}
	if console.Status.Schedules != 0 || hasConsoleSchedule(console, "schedule-cancel-restart") {
		t.Fatalf("expected restarted console to stay clear of deleted schedule, got %+v", console)
	}
}

func TestRuntimeAppConsoleJobsComeFromPersistedState(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := writeTestConfigAt(t, dir)

	app, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}

	job := runtimecore.NewJob("job-console-persisted-runtime", "ai.chat", 1, 30*time.Second)
	job.TraceID = "trace-console-runtime"
	job.EventID = "evt-console-runtime"
	job.Correlation = "corr-console-runtime"
	if err := app.state.SaveJob(t.Context(), job); err != nil {
		t.Fatalf("save job directly to state: %v", err)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("close first app: %v", err)
	}

	restarted, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("restart runtime app: %v", err)
	}
	defer func() { _ = restarted.Close() }()

	consoleReq := consoleRequestWithViewer("/api/console")
	consoleResp := httptest.NewRecorder()
	restarted.ServeHTTP(consoleResp, consoleReq)
	for _, expected := range []string{"job-console-persisted-runtime", `"jobs": 1`, "corr-console-runtime", `"recoveredJobs": 0`, `"job_recovery_total_jobs": 1`} {
		if !strings.Contains(consoleResp.Body.String(), expected) {
			t.Fatalf("expected persisted job in console payload %q, got %s", expected, consoleResp.Body.String())
		}
	}
}

func TestRuntimeAppConsoleReadsPersistedPluginSnapshotAfterRestart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := writeTestConfigAt(t, dir)

	app, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = app.Close()
		}
	}()

	req := httptest.NewRequest(http.MethodPost, "/demo/onebot/message", strings.NewReader(`{"post_type":"message","message_type":"group","time":1712034000,"user_id":10001,"group_id":42,"message_id":9002,"raw_message":"persist plugin snapshot","sender":{"nickname":"alice"}}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}

	counts, err := app.state.Counts(t.Context())
	if err != nil {
		t.Fatalf("sqlite counts: %v", err)
	}
	if counts["plugin_status_snapshots"] < 1 {
		t.Fatalf("expected at least one persisted plugin snapshot before restart, got %+v", counts)
	}
	snapshot, err := app.state.LoadPluginStatusSnapshot(t.Context(), "plugin-echo")
	if err != nil {
		t.Fatalf("load persisted plugin-echo snapshot: %v", err)
	}
	if snapshot.PluginID != "plugin-echo" || snapshot.LastDispatchKind != "event" || !snapshot.LastDispatchSuccess {
		t.Fatalf("expected persisted plugin-echo success snapshot before restart, got %+v", snapshot)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("close first app: %v", err)
	}
	closed = true

	restarted, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("restart runtime app: %v", err)
	}
	defer func() { _ = restarted.Close() }()

	consoleReq := httptest.NewRequest(http.MethodGet, "/api/console?plugin_id=plugin-echo", nil)
	consoleResp := httptest.NewRecorder()
	restarted.ServeHTTP(consoleResp, consoleReq)
	for _, expected := range []string{
		`"id": "plugin-echo"`,
		`"statusSource": "runtime-registry+sqlite-plugin-status-snapshot"`,
		`"statusEvidence": "persisted-plugin-status-snapshot"`,
		`"runtimeStateLive": false`,
		`"statusPersisted": true`,
		`"statusRecovery": "last-dispatch-succeeded"`,
		`"statusStaleness": "persisted-snapshot"`,
		`"lastDispatchKind": "event"`,
		`"lastDispatchSuccess": true`,
	} {
		if !strings.Contains(consoleResp.Body.String(), expected) {
			t.Fatalf("expected persisted plugin snapshot in console payload %q, got %s", expected, consoleResp.Body.String())
		}
	}
}

func TestRuntimeAppConsoleReadsPersistedAdapterInstanceAfterRestart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := writeTestConfigAt(t, dir)

	app, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	counts, err := app.state.Counts(t.Context())
	if err != nil {
		t.Fatalf("sqlite counts: %v", err)
	}
	if counts["adapter_instances"] != 1 {
		t.Fatalf("expected one persisted adapter instance before restart, got %+v", counts)
	}
	state, err := app.state.LoadAdapterInstance(t.Context(), "adapter-onebot-demo")
	if err != nil {
		t.Fatalf("load persisted adapter instance: %v", err)
	}
	if state.Adapter != "onebot" || state.Source != "onebot" || state.Status != "registered" || state.Health != "ready" || !state.Online {
		t.Fatalf("expected persisted onebot adapter facts before restart, got %+v", state)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("close first app: %v", err)
	}

	restarted, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("restart runtime app: %v", err)
	}
	defer func() { _ = restarted.Close() }()

	consoleReq := consoleRequestWithViewer("/api/console")
	consoleResp := httptest.NewRecorder()
	restarted.ServeHTTP(consoleResp, consoleReq)
	if consoleResp.Code != http.StatusOK {
		t.Fatalf("expected console 200, got %d: %s", consoleResp.Code, consoleResp.Body.String())
	}
	var console runtimeConsoleResponse
	if err := json.Unmarshal(consoleResp.Body.Bytes(), &console); err != nil {
		t.Fatalf("decode console payload: %v", err)
	}
	if console.Status.Adapters != 1 || !hasConsoleAdapter(console, "adapter-onebot-demo") {
		t.Fatalf("expected restarted console to expose persisted adapter instance, got %+v", console)
	}
	adapter := console.Adapters[0]
	if adapter.Adapter != "onebot" || adapter.Source != "onebot" || adapter.Status != "registered" || adapter.Health != "ready" || !adapter.Online || !adapter.StatePersisted {
		t.Fatalf("expected restarted console adapter facts, got %+v", adapter)
	}
	consoleReq = consoleRequestWithViewer("/api/console")
	consoleResp = httptest.NewRecorder()
	restarted.ServeHTTP(consoleResp, consoleReq)
	for _, expected := range []string{`"id": "adapter-onebot-demo"`, `"adapter": "onebot"`, `"status": "registered"`, `"health": "ready"`, `"online": true`, `"statePersisted": true`, `"adapter_read_model": "sqlite-adapter-instances"`} {
		if !strings.Contains(consoleResp.Body.String(), expected) {
			t.Fatalf("expected restarted console payload to include %q, got %s", expected, consoleResp.Body.String())
		}
	}
}

func TestRuntimeAppLoadsConfiguredBotInstancesAndExposesThemAfterRestart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := writeTestConfigWithBotInstancesAt(t, dir)

	app, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	counts, err := app.state.Counts(t.Context())
	if err != nil {
		t.Fatalf("sqlite counts: %v", err)
	}
	if counts["adapter_instances"] != 3 {
		t.Fatalf("expected three persisted adapter instances before restart, got %+v", counts)
	}
	alphaState, err := app.state.LoadAdapterInstance(t.Context(), "adapter-onebot-alpha")
	if err != nil {
		t.Fatalf("load alpha adapter instance: %v", err)
	}
	if alphaState.Adapter != "onebot" || alphaState.Source != "onebot-alpha" || alphaState.Status != "registered" || alphaState.Health != "ready" || !alphaState.Online {
		t.Fatalf("expected persisted alpha adapter facts before restart, got %+v", alphaState)
	}
	betaState, err := app.state.LoadAdapterInstance(t.Context(), "adapter-onebot-beta")
	if err != nil {
		t.Fatalf("load beta adapter instance: %v", err)
	}
	if betaState.Adapter != "onebot" || betaState.Source != "onebot-beta" || betaState.Status != "registered" || betaState.Health != "ready" || !betaState.Online {
		t.Fatalf("expected persisted beta adapter facts before restart, got %+v", betaState)
	}
	webhookState, err := app.state.LoadAdapterInstance(t.Context(), "adapter-webhook-main")
	if err != nil {
		t.Fatalf("load webhook adapter instance: %v", err)
	}
	if webhookState.Adapter != "webhook" || webhookState.Source != "webhook-main" || webhookState.Status != "registered" || webhookState.Health != "ready" || !webhookState.Online {
		t.Fatalf("expected persisted webhook adapter facts before restart, got %+v", webhookState)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("close first app: %v", err)
	}

	restarted, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("restart runtime app: %v", err)
	}
	defer func() { _ = restarted.Close() }()

	consoleReq := consoleRequestWithViewer("/api/console")
	consoleResp := httptest.NewRecorder()
	restarted.ServeHTTP(consoleResp, consoleReq)
	if consoleResp.Code != http.StatusOK {
		t.Fatalf("expected console 200, got %d: %s", consoleResp.Code, consoleResp.Body.String())
	}
	var console runtimeConsoleResponse
	if err := json.Unmarshal(consoleResp.Body.Bytes(), &console); err != nil {
		t.Fatalf("decode console payload: %v", err)
	}
	if console.Status.Adapters != 3 || !hasConsoleAdapter(console, "adapter-onebot-alpha") || !hasConsoleAdapter(console, "adapter-onebot-beta") || !hasConsoleAdapter(console, "adapter-webhook-main") {
		t.Fatalf("expected restarted console to expose mixed configured adapter instances, got %+v", console)
	}
	alpha := consoleAdapterByID(t, console, "adapter-onebot-alpha")
	if alpha.Adapter != "onebot" || alpha.Source != "onebot-alpha" || alpha.Status != "registered" || alpha.Health != "ready" || !alpha.Online || !alpha.StatePersisted {
		t.Fatalf("expected alpha console adapter facts, got %+v", alpha)
	}
	if alpha.StatusSource != "sqlite-adapter-instances" || alpha.ConfigSource != "sqlite-adapter-instances" {
		t.Fatalf("expected alpha adapter read-model sources, got %+v", alpha)
	}
	if alpha.Config["demo_path"] != "/demo/onebot/message" || alpha.Config["platform"] != "onebot/v11" || alpha.Config["source"] != "onebot-alpha" {
		t.Fatalf("expected alpha adapter config metadata, got %+v", alpha.Config)
	}
	if selfID, ok := alpha.Config["self_id"].(float64); !ok || selfID != 10001 {
		t.Fatalf("expected alpha self_id 10001, got %+v", alpha.Config)
	}
	beta := consoleAdapterByID(t, console, "adapter-onebot-beta")
	if beta.Adapter != "onebot" || beta.Source != "onebot-beta" || beta.Status != "registered" || beta.Health != "ready" || !beta.Online || !beta.StatePersisted {
		t.Fatalf("expected beta console adapter facts, got %+v", beta)
	}
	if beta.StatusSource != "sqlite-adapter-instances" || beta.ConfigSource != "sqlite-adapter-instances" {
		t.Fatalf("expected beta adapter read-model sources, got %+v", beta)
	}
	if beta.Config["demo_path"] != "/demo/onebot/message-beta" || beta.Config["platform"] != "onebot/v11" || beta.Config["source"] != "onebot-beta" {
		t.Fatalf("expected beta adapter config metadata, got %+v", beta.Config)
	}
	if _, exists := beta.Config["self_id"]; exists {
		t.Fatalf("expected beta config to omit optional self_id, got %+v", beta.Config)
	}
	webhook := consoleAdapterByID(t, console, "adapter-webhook-main")
	if webhook.Adapter != "webhook" || webhook.Source != "webhook-main" || webhook.Status != "registered" || webhook.Health != "ready" || !webhook.Online || !webhook.StatePersisted {
		t.Fatalf("expected webhook console adapter facts, got %+v", webhook)
	}
	if webhook.StatusSource != "sqlite-adapter-instances" || webhook.ConfigSource != "sqlite-adapter-instances" {
		t.Fatalf("expected webhook adapter read-model sources, got %+v", webhook)
	}
	if webhook.Config["path"] != "/ingress/webhook/main" || webhook.Config["platform"] != "webhook/http" || webhook.Config["source"] != "webhook-main" {
		t.Fatalf("expected webhook adapter config metadata, got %+v", webhook.Config)
	}
	consoleReq = consoleRequestWithViewer("/api/console")
	consoleResp = httptest.NewRecorder()
	restarted.ServeHTTP(consoleResp, consoleReq)
	for _, expected := range []string{`"id": "adapter-onebot-alpha"`, `"id": "adapter-onebot-beta"`, `"id": "adapter-webhook-main"`, `"source": "onebot-alpha"`, `"source": "onebot-beta"`, `"source": "webhook-main"`, `"demo_path": "/demo/onebot/message"`, `"demo_path": "/demo/onebot/message-beta"`, `"path": "/ingress/webhook/main"`, `"self_id": 10001`, `"adapter_read_model": "sqlite-adapter-instances"`} {
		if !strings.Contains(consoleResp.Body.String(), expected) {
			t.Fatalf("expected restarted console payload to include %q, got %s", expected, consoleResp.Body.String())
		}
	}
	if got := len(restarted.runtime.RegisteredAdapters()); got != 3 {
		t.Fatalf("expected three registered adapters after restart, got %d", got)
	}
}

func TestRuntimeAppConfiguredOneBotAndWebhookIngressWorkAfterRestart(t *testing.T) {
	t.Setenv("BOT_PLATFORM_WEBHOOK_TOKEN", "test-webhook-token")

	dir := t.TempDir()
	configPath := writeTestConfigWithBotInstancesAt(t, dir)

	app, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	counts, err := app.state.Counts(t.Context())
	if err != nil {
		t.Fatalf("sqlite counts before restart: %v", err)
	}
	if counts["adapter_instances"] != 3 {
		t.Fatalf("expected three persisted adapter instances before restart, got %+v", counts)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("close first app: %v", err)
	}

	restarted, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("restart runtime app: %v", err)
	}
	defer func() { _ = restarted.Close() }()

	oneBotMessageID := int64(9312)
	oneBotResp := performRuntimeOneBotMessageRequestAtPath(t, restarted, "/demo/onebot/message-beta", runtimeDemoOneBotMessageBody(t, oneBotMessageID, "hello beta after restart"))
	if oneBotResp.Code != http.StatusOK {
		t.Fatalf("expected restarted onebot route 200, got %d: %s", oneBotResp.Code, oneBotResp.Body.String())
	}
	var oneBotPayload struct {
		Status  string `json:"status"`
		EventID string `json:"event_id"`
	}
	if err := json.Unmarshal(oneBotResp.Body.Bytes(), &oneBotPayload); err != nil {
		t.Fatalf("decode restarted onebot response: %v", err)
	}
	if oneBotPayload.Status != "ok" || oneBotPayload.EventID == "" {
		t.Fatalf("unexpected restarted onebot response %+v", oneBotPayload)
	}
	oneBotEvent, err := restarted.state.LoadEvent(t.Context(), oneBotPayload.EventID)
	if err != nil {
		t.Fatalf("load restarted onebot event: %v", err)
	}
	if oneBotEvent.Source != "onebot-beta" {
		t.Fatalf("expected restarted onebot event source onebot-beta, got %+v", oneBotEvent)
	}
	expectedOneBotIdempotencyKey := "onebot:onebot-beta:group:" + strconv.FormatInt(oneBotMessageID, 10)
	if oneBotEvent.IdempotencyKey != expectedOneBotIdempotencyKey {
		t.Fatalf("expected restarted onebot idempotency key %q, got %+v", expectedOneBotIdempotencyKey, oneBotEvent)
	}
	if oneBotEvent.Metadata["adapter_instance_id"] != "adapter-onebot-beta" {
		t.Fatalf("expected restarted onebot metadata to keep adapter instance id, got %+v", oneBotEvent.Metadata)
	}

	webhookReq := httptest.NewRequest(http.MethodPost, "/ingress/webhook/main", strings.NewReader(`{"event_type":"message.received","source":"client-spoofed","actor_id":"svc-1","text":"hello webhook after restart"}`))
	webhookReq.Header.Set("Content-Type", "application/json")
	webhookReq.Header.Set("X-Webhook-Token", "test-webhook-token")
	webhookResp := httptest.NewRecorder()
	restarted.ServeHTTP(webhookResp, webhookReq)
	if webhookResp.Code != http.StatusOK {
		t.Fatalf("expected restarted webhook ingress 200, got %d: %s", webhookResp.Code, webhookResp.Body.String())
	}
	var webhookPayload struct {
		Status  string `json:"status"`
		EventID string `json:"event_id"`
		TraceID string `json:"trace_id"`
	}
	if err := json.Unmarshal(webhookResp.Body.Bytes(), &webhookPayload); err != nil {
		t.Fatalf("decode restarted webhook response: %v", err)
	}
	if webhookPayload.Status != "ok" || webhookPayload.EventID == "" || webhookPayload.TraceID == "" {
		t.Fatalf("unexpected restarted webhook response %+v", webhookPayload)
	}
	webhookEvent, err := restarted.state.LoadEvent(t.Context(), webhookPayload.EventID)
	if err != nil {
		t.Fatalf("load restarted webhook event: %v", err)
	}
	if webhookEvent.Source != "webhook-main" {
		t.Fatalf("expected restarted webhook event source webhook-main, got %+v", webhookEvent)
	}
	if webhookEvent.Metadata["ingress_source"] != "client-spoofed" {
		t.Fatalf("expected restarted webhook metadata to keep ingress_source, got %+v", webhookEvent.Metadata)
	}
	if webhookEvent.Metadata["adapter_instance_id"] != "adapter-webhook-main" {
		t.Fatalf("expected restarted webhook metadata to keep adapter instance id, got %+v", webhookEvent.Metadata)
	}
	if !strings.HasPrefix(webhookEvent.IdempotencyKey, "webhook:webhook-main:message.received:") {
		t.Fatalf("expected restarted webhook idempotency key prefix, got %+v", webhookEvent)
	}

	consoleReq := consoleRequestWithViewer("/api/console")
	consoleResp := httptest.NewRecorder()
	restarted.ServeHTTP(consoleResp, consoleReq)
	if consoleResp.Code != http.StatusOK {
		t.Fatalf("expected restarted console 200, got %d: %s", consoleResp.Code, consoleResp.Body.String())
	}
	var console runtimeConsoleResponse
	if err := json.Unmarshal(consoleResp.Body.Bytes(), &console); err != nil {
		t.Fatalf("decode restarted console payload: %v", err)
	}
	if console.Status.Adapters != 3 || !hasConsoleAdapter(console, "adapter-onebot-alpha") || !hasConsoleAdapter(console, "adapter-onebot-beta") || !hasConsoleAdapter(console, "adapter-webhook-main") {
		t.Fatalf("expected restarted console to expose all configured adapter rows, got %+v", console)
	}
	assertRegisteredAdapterBindings(t, restarted.runtime, map[string]string{
		"adapter-onebot-alpha": "onebot-alpha",
		"adapter-onebot-beta":  "onebot-beta",
		"adapter-webhook-main": "webhook-main",
	})
}

func TestRuntimeAppRestoresPersistedDelayScheduleAfterRestart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := writeTestConfigAt(t, dir)

	app, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/demo/schedules/echo-delay", strings.NewReader(`{"id":"schedule-restart-delay","delay_ms":80,"message":"restored schedule hello"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}
	if err := app.Close(); err != nil {
		t.Fatalf("close first app: %v", err)
	}

	restarted, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("restart runtime app: %v", err)
	}
	defer func() { _ = restarted.Close() }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		restarted.queue.DispatchReady(t.Context(), time.Now().UTC())
		repliesReq := httptest.NewRequest(http.MethodGet, "/demo/replies", nil)
		repliesResp := httptest.NewRecorder()
		restarted.ServeHTTP(repliesResp, repliesReq)
		if strings.Contains(repliesResp.Body.String(), "restored schedule hello") {
			removeDeadline := time.Now().Add(200 * time.Millisecond)
			for {
				if _, err := restarted.state.LoadSchedulePlan(t.Context(), "schedule-restart-delay"); err != nil {
					return
				}
				if time.Now().After(removeDeadline) {
					t.Fatal("expected restored delay schedule to be deleted after firing")
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected restored delay schedule reply after restart")
}

func TestRuntimeAppConsoleAndMetricsExposeScheduleRecoveryAfterRestart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := writeTestConfigAt(t, dir)

	app, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	createdAt := time.Now().UTC()
	if _, err := app.state.DBForTests().ExecContext(t.Context(), `
	INSERT INTO schedule_plans (
	  schedule_id, kind, cron_expr, delay_ms, execute_at, due_at, due_at_evidence, source, event_type,
	  metadata_json, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "schedule-recovery-missing-dueat", string(runtimecore.ScheduleKindDelay), "", int64((5*time.Second)/time.Millisecond), nil, nil, "", "runtime-demo-scheduler", "message.received", `{}`, createdAt.Format(time.RFC3339Nano), createdAt.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert persisted schedule plan without dueAt: %v", err)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("close first app: %v", err)
	}

	restarted, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("restart runtime app: %v", err)
	}
	defer func() { _ = restarted.Close() }()

	stored, err := restarted.state.LoadSchedulePlan(t.Context(), "schedule-recovery-missing-dueat")
	if err != nil {
		t.Fatalf("load repaired persisted schedule plan: %v", err)
	}
	wantDueAt := createdAt.Add(5 * time.Second)
	if stored.DueAt == nil || !stored.DueAt.Equal(wantDueAt) {
		t.Fatalf("expected restored schedule dueAt to persist as %s, got %+v", wantDueAt, stored)
	}

	consoleReq := consoleRequestWithViewer("/api/console")
	consoleResp := httptest.NewRecorder()
	restarted.ServeHTTP(consoleResp, consoleReq)
	for _, expected := range []string{
		`"schedule_recovery_source": "runtime-startup-restore"`,
		`"schedule_recovery_recovered_schedules": 1`,
		`"schedule_recovery_total_schedules": 1`,
		`"dueAtSource": "startup-recovery"`,
		`"dueAtEvidence": "recovered-schedule-due-at"`,
		`"dueAtPersisted": true`,
		`"scheduleKinds": {`,
		`"delay": 1`,
		`"recoveredSchedules": 1`,
		`"totalSchedules": 1`,
	} {
		if !strings.Contains(consoleResp.Body.String(), expected) {
			t.Fatalf("expected console payload to include %q, got %s", expected, consoleResp.Body.String())
		}
	}

	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsResp := httptest.NewRecorder()
	restarted.ServeHTTP(metricsResp, metricsReq)
	if !strings.Contains(metricsResp.Body.String(), "bot_platform_schedule_recoveries_total 1") {
		t.Fatalf("expected metrics payload to include schedule recoveries counter, got %s", metricsResp.Body.String())
	}
}

func TestRuntimeAppAIMessageQueuesAndReplies(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	req := httptest.NewRequest(http.MethodPost, "/demo/ai/message", strings.NewReader(`{"prompt":"hello ai","user_id":"user-ai-1"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		repliesReq := httptest.NewRequest(http.MethodGet, "/demo/replies", nil)
		repliesResp := httptest.NewRecorder()
		app.ServeHTTP(repliesResp, repliesReq)
		if strings.Contains(repliesResp.Body.String(), "AI: hello ai") {
			stored, inspectErr := app.queue.Inspect(t.Context(), extractAIJobID(resp.Body.String()))
			if inspectErr != nil {
				t.Fatalf("inspect ai job: %v", inspectErr)
			}
			if stored.Status != runtimecore.JobStatusDone {
				t.Fatalf("expected ai job done, got %+v", stored)
			}
			entries := app.logs.Lines()
			matched := false
			for _, line := range entries {
				if strings.Contains(line, "runtime demo reply recorded") && strings.Contains(line, stored.TraceID) && strings.Contains(line, stored.EventID) && strings.Contains(line, stored.Correlation) {
					matched = true
					break
				}
			}
			if !matched {
				t.Fatalf("expected reply log to keep trace/event/correlation context, got %+v", entries)
			}
			if rendered := app.tracer.RenderTrace(stored.TraceID); !strings.Contains(rendered, "reply.send") {
				t.Fatalf("expected reply.send span on success trace, got %s", rendered)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("expected AI reply to be recorded")
}

func TestRuntimeAppAIMessageAllowsRepeatedSamePromptRequests(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	reqBody := `{"prompt":"repeat me","user_id":"user-ai-repeat"}`
	firstReq := httptest.NewRequest(http.MethodPost, "/demo/ai/message", strings.NewReader(reqBody))
	firstReq.Header.Set("Content-Type", "application/json")
	firstResp := httptest.NewRecorder()
	app.ServeHTTP(firstResp, firstReq)
	if firstResp.Code != http.StatusOK {
		t.Fatalf("expected first request 200, got %d: %s", firstResp.Code, firstResp.Body.String())
	}
	if strings.Contains(firstResp.Body.String(), `"duplicate":true`) || strings.Contains(firstResp.Body.String(), `"duplicate": true`) {
		t.Fatalf("expected first repeated prompt request not to short-circuit as duplicate, got %s", firstResp.Body.String())
	}
	firstJobID := extractAIJobID(firstResp.Body.String())
	firstStored := waitForAIJobStatus(t, app, firstJobID, runtimecore.JobStatusDone)

	secondReq := httptest.NewRequest(http.MethodPost, "/demo/ai/message", strings.NewReader(reqBody))
	secondReq.Header.Set("Content-Type", "application/json")
	secondResp := httptest.NewRecorder()
	app.ServeHTTP(secondResp, secondReq)
	if secondResp.Code != http.StatusOK {
		t.Fatalf("expected second request 200, got %d: %s", secondResp.Code, secondResp.Body.String())
	}
	if strings.Contains(secondResp.Body.String(), `"duplicate":true`) || strings.Contains(secondResp.Body.String(), `"duplicate": true`) {
		t.Fatalf("expected second repeated prompt request not to short-circuit as duplicate, got %s", secondResp.Body.String())
	}
	secondJobID := extractAIJobID(secondResp.Body.String())
	if secondJobID == firstJobID {
		t.Fatalf("expected repeated prompt request to create a distinct job, got first=%q second=%q", firstJobID, secondJobID)
	}
	secondStored := waitForAIJobStatus(t, app, secondJobID, runtimecore.JobStatusDone)
	if firstStored.EventID == secondStored.EventID || firstStored.TraceID == secondStored.TraceID {
		t.Fatalf("expected repeated prompt request to produce distinct event/trace identity, first=%+v second=%+v", firstStored, secondStored)
	}

	repliesReq := httptest.NewRequest(http.MethodGet, "/demo/replies", nil)
	repliesResp := httptest.NewRecorder()
	app.ServeHTTP(repliesResp, repliesReq)
	if strings.Count(repliesResp.Body.String(), "AI: repeat me") != 2 {
		t.Fatalf("expected repeated prompt requests to record two replies, got %s", repliesResp.Body.String())
	}
	counts, err := app.runtimeStateCounts(t.Context())
	if err != nil {
		t.Fatalf("runtime state counts after repeated ai message requests: %v", err)
	}
	if counts["event_journal"] != 2 || counts["idempotency_keys"] != 2 {
		t.Fatalf("expected repeated ai message requests to persist two events and two distinct idempotency keys, got %+v", counts)
	}
}

func TestRuntimeAppDemoJobsEnqueueRejectsAIChatType(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	req := httptest.NewRequest(http.MethodPost, "/demo/jobs/enqueue", strings.NewReader(`{"id":"job-ai-chat-enqueue-rejected","type":"ai.chat","prompt":"blocked","user_id":"user-ai-blocked"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected ai.chat generic enqueue rejection 400, got %d: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "/demo/ai/message") {
		t.Fatalf("expected ai.chat enqueue rejection to point callers to /demo/ai/message, got %s", resp.Body.String())
	}
	if _, err := app.queue.Inspect(t.Context(), "job-ai-chat-enqueue-rejected"); err == nil {
		t.Fatal("expected rejected ai.chat enqueue request not to create a queued provider job")
	}
	if len(app.replies.Since(0)) != 0 {
		t.Fatalf("expected rejected ai.chat enqueue request not to dispatch replies, got %+v", app.replies.Since(0))
	}
}

func TestRuntimeAppAIMessageQueuesAndRepliesWithOpenAICompatProvider(t *testing.T) {
	apiKeyRef := "BOT_PLATFORM_AI_CHAT_API_KEY_TEST_SUCCESS"
	t.Setenv(apiKeyRef, "secret-openai-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST request, got %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret-openai-key" {
			t.Fatalf("expected bearer auth header, got %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("expected json content type, got %q", got)
		}
		var payload struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode openai compat request: %v", err)
		}
		if payload.Model != "gpt-test" {
			t.Fatalf("expected model gpt-test, got %+v", payload)
		}
		if len(payload.Messages) != 1 || payload.Messages[0].Role != "user" || payload.Messages[0].Content != "hello from real provider" {
			t.Fatalf("unexpected openai compat request payload %+v", payload)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"openai compat says hi"}}]}`))
	}))
	defer server.Close()

	app, err := newRuntimeApp(writeAIProviderConfigAt(t, t.TempDir(), server.URL, "gpt-test", 500, apiKeyRef))
	if err != nil {
		t.Fatalf("new runtime app with openai compat provider: %v", err)
	}
	defer func() { _ = app.Close() }()

	req := httptest.NewRequest(http.MethodPost, "/demo/ai/message", strings.NewReader(`{"prompt":"hello from real provider","user_id":"user-ai-openai"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}
	jobID := extractAIJobID(resp.Body.String())
	stored := waitForAIJobStatus(t, app, jobID, runtimecore.JobStatusDone)
	if stored.ReasonCode != "" {
		t.Fatalf("expected success path to avoid failure reason code, got %+v", stored)
	}
	repliesReq := httptest.NewRequest(http.MethodGet, "/demo/replies", nil)
	repliesResp := httptest.NewRecorder()
	app.ServeHTTP(repliesResp, repliesReq)
	if !strings.Contains(repliesResp.Body.String(), "openai compat says hi") {
		t.Fatalf("expected openai compat reply to be recorded, got %s", repliesResp.Body.String())
	}
	if rendered := app.tracer.RenderTrace(stored.TraceID); !strings.Contains(rendered, "reply.send") {
		t.Fatalf("expected reply.send span on openai compat success trace, got %s", rendered)
	}
	console := readRuntimeConsoleResponse(t, app)
	if got := consoleMetaString(t, console.Meta, "ai_chat_provider"); got != "openai_compat" {
		t.Fatalf("expected console ai_chat_provider=openai_compat, got %q", got)
	}
	secretReads := 0
	for _, entry := range app.audits.AuditEntries() {
		if entry.Action == "secret.read" && entry.Target == apiKeyRef && entry.Allowed {
			secretReads++
		}
	}
	if secretReads != 1 {
		t.Fatalf("expected one successful ai api key secret audit, got %+v", app.audits.AuditEntries())
	}
	matchedProviderLog := false
	for _, line := range app.logs.Lines() {
		if strings.Contains(line, "runtime ai provider configured") && strings.Contains(line, `"provider":"openai_compat"`) && strings.Contains(line, `"model":"gpt-test"`) {
			matchedProviderLog = true
			break
		}
	}
	if !matchedProviderLog {
		t.Fatalf("expected provider configuration log, got %+v", app.logs.Lines())
	}
}

func TestRuntimeAppAIMessageOpenAICompatRejectsRedirectBeforeFollowing(t *testing.T) {
	apiKeyRef := "BOT_PLATFORM_AI_CHAT_API_KEY_TEST_REDIRECT"
	t.Setenv(apiKeyRef, "secret-openai-key")

	redirectTargetHits := 0
	redirectTargetSawAuthorization := false
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectTargetHits++
		if strings.TrimSpace(r.Header.Get("Authorization")) != "" {
			redirectTargetSawAuthorization = true
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"redirect target should not be reached"}}]}`))
	}))
	defer redirectTarget.Close()

	redirectSource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget.URL, http.StatusTemporaryRedirect)
	}))
	defer redirectSource.Close()

	app, err := newRuntimeApp(writeAIProviderConfigAt(t, t.TempDir(), redirectSource.URL, "gpt-redirect", 500, apiKeyRef))
	if err != nil {
		t.Fatalf("new runtime app with redirecting openai compat provider: %v", err)
	}
	defer func() { _ = app.Close() }()

	req := httptest.NewRequest(http.MethodPost, "/demo/ai/message", strings.NewReader(`{"prompt":"follow redirect please","user_id":"user-ai-openai-redirect"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}

	jobID := extractAIJobID(resp.Body.String())
	stored := waitForAIJobStatus(t, app, jobID, runtimecore.JobStatusDead)
	if redirectTargetHits != 0 {
		t.Fatalf("expected redirected target never to be hit, hits=%d authorization=%v", redirectTargetHits, redirectTargetSawAuthorization)
	}
	if redirectTargetSawAuthorization {
		t.Fatal("expected redirected target never to receive authorization header")
	}
	if !strings.Contains(strings.ToLower(stored.LastError), "redirects are not allowed") {
		t.Fatalf("expected redirect rejection detail on job, got %+v", stored)
	}

	repliesReq := httptest.NewRequest(http.MethodGet, "/demo/replies", nil)
	repliesResp := httptest.NewRecorder()
	app.ServeHTTP(repliesResp, repliesReq)
	if !strings.Contains(strings.ToLower(repliesResp.Body.String()), "redirects are not allowed") {
		t.Fatalf("expected redirect rejection failure feedback reply, got %s", repliesResp.Body.String())
	}

	matchedRedirectLog := false
	for _, line := range app.logs.Lines() {
		if strings.Contains(line, "runtime ai provider request failed") && strings.Contains(strings.ToLower(line), "redirects are not allowed") {
			matchedRedirectLog = true
			break
		}
	}
	if !matchedRedirectLog {
		t.Fatalf("expected redirect rejection provider log, got %+v", app.logs.Lines())
	}
}

func TestRuntimeAppAIQueueDispatcherUsesRuntimeDispatchPath(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	req := httptest.NewRequest(http.MethodPost, "/demo/ai/message", strings.NewReader(`{"prompt":"dispatch through runtime","user_id":"user-ai-dispatch"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}

	jobID := extractAIJobID(resp.Body.String())
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		app.queue.DispatchReady(t.Context(), time.Now().UTC())
		stored, inspectErr := app.queue.Inspect(t.Context(), jobID)
		if inspectErr == nil && stored.Status == runtimecore.JobStatusDone {
			entries := app.logs.Lines()
			for _, line := range entries {
				if strings.Contains(line, "runtime dispatch started") && strings.Contains(line, "\"dispatch_kind\":\"job\"") && strings.Contains(line, jobID) {
					return
				}
			}
			t.Fatalf("expected runtime job dispatch log for %s, got %+v", jobID, entries)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("expected AI queue dispatcher to complete job through runtime dispatch path")
}

func TestRuntimeAppDefaultPluginHostRoutingAIMessageEnqueuesExpectedJobAndSessionState(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	host := runtimeRoutedPluginHost(t, app)
	if _, routed := host.routes["plugin-ai-chat"]; !routed {
		t.Fatalf("expected default routing to route plugin-ai-chat through subprocess, routes=%+v", host.routes)
	}
	if _, ok := host.hostForPlugin("plugin-ai-chat").(*runtimecore.SubprocessPluginHost); !ok {
		t.Fatalf("expected plugin-ai-chat to resolve to subprocess host by default, got %T", host.hostForPlugin("plugin-ai-chat"))
	}

	event := aiEvent("hello from default subprocess route", "user-ai-subprocess-event")
	duplicate, err := app.persistAndDispatchEvent(t.Context(), event)
	if err != nil {
		t.Fatalf("dispatch subprocess ai-chat event: %v", err)
	}
	if duplicate {
		t.Fatalf("expected default-routed ai-chat event not to be treated as duplicate")
	}

	jobID := "job-ai-chat-" + event.EventID
	stored, err := app.queue.Inspect(t.Context(), jobID)
	if err != nil {
		t.Fatalf("inspect queued ai subprocess job: %v", err)
	}
	if stored.Status != runtimecore.JobStatusPending {
		t.Fatalf("expected ai subprocess event path to enqueue pending job, got %+v", stored)
	}
	prompt, _ := stored.Payload["prompt"].(string)
	if prompt != "hello from default subprocess route" {
		t.Fatalf("expected queued prompt from subprocess event path, got %+v", stored.Payload)
	}
	replyHandle, _ := stored.Payload["reply_handle"].(map[string]any)
	if capability, _ := replyHandle["capability"].(string); capability != "onebot.reply" {
		t.Fatalf("expected queued reply handle from subprocess event path, got %+v", stored.Payload)
	}
	session, err := app.state.LoadSession(t.Context(), "session-user-ai-subprocess-event")
	if err != nil {
		t.Fatalf("load subprocess ai-chat session: %v", err)
	}
	if session.PluginID != "plugin-ai-chat" {
		t.Fatalf("expected subprocess ai-chat session plugin id, got %+v", session)
	}
	if got, _ := session.State["last_prompt"].(string); got != "hello from default subprocess route" {
		t.Fatalf("expected subprocess ai-chat session state to persist last prompt, got %+v", session)
	}
	if len(app.replies.Since(0)) != 0 {
		t.Fatalf("expected ai subprocess event path not to reply before job execution, got %+v", app.replies.Since(0))
	}

	subprocessHost := runtimeSubprocessHostForPlugin(t, host, "plugin-ai-chat")
	stdout, stderr := waitForRuntimeSubprocessCapture(t, subprocessHost, func(stdout string, stderr string) bool {
		return strings.Contains(stderr, "runtime-plugin-echo-subprocess-online") && strings.Contains(stdout, `"callback":"ai_chat_queue"`) && strings.Contains(stdout, `"action":"enqueue"`) && strings.Contains(stdout, `"callback":"ai_chat_session"`)
	})
	if !strings.Contains(stderr, "runtime-plugin-echo-subprocess-online") {
		t.Fatalf("expected default-routed ai-chat subprocess to boot helper process, stderr=%s stdout=%s", stderr, stdout)
	}
	for _, expected := range []string{`"callback":"ai_chat_queue"`, `"action":"enqueue"`, `"callback":"ai_chat_session"`} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("expected subprocess ai-chat event callback evidence %q, stderr=%s stdout=%s", expected, stderr, stdout)
		}
	}
}

func TestRuntimeAppDefaultPluginHostRoutingAIMessageCompletesJobAndRepliesThroughSubprocess(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	host := runtimeRoutedPluginHost(t, app)
	if _, ok := host.hostForPlugin("plugin-ai-chat").(*runtimecore.SubprocessPluginHost); !ok {
		t.Fatalf("expected plugin-ai-chat subprocess route by default, got %T", host.hostForPlugin("plugin-ai-chat"))
	}

	req := httptest.NewRequest(http.MethodPost, "/demo/ai/message", strings.NewReader(`{"prompt":"hello from subprocess job","user_id":"user-ai-subprocess-job"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}

	jobID := extractAIJobID(resp.Body.String())
	stored := waitForAIJobStatus(t, app, jobID, runtimecore.JobStatusDone)
	if stored.Status != runtimecore.JobStatusDone {
		t.Fatalf("expected subprocess ai-chat job path done status, got %+v", stored)
	}
	replies := app.replies.Since(0)
	if len(replies) == 0 || replies[len(replies)-1].Payload != "AI: hello from subprocess job" {
		t.Fatalf("expected subprocess ai-chat job path to reply through runtime reply buffer, got %+v", replies)
	}
	entries := app.logs.Lines()
	matched := false
	for _, line := range entries {
		if strings.Contains(line, "runtime dispatch started") && strings.Contains(line, `"dispatch_kind":"job"`) && strings.Contains(line, jobID) {
			matched = true
			break
		}
	}
	if !matched {
		t.Fatalf("expected runtime dispatch log for subprocess ai-chat job, got %+v", entries)
	}

	subprocessHost := runtimeSubprocessHostForPlugin(t, host, "plugin-ai-chat")
	stdout, stderr := waitForRuntimeSubprocessCapture(t, subprocessHost, func(stdout string, stderr string) bool {
		return strings.Contains(stderr, "runtime-plugin-echo-subprocess-online") && strings.Contains(stdout, `"callback":"ai_chat_queue"`) && strings.Contains(stdout, `"action":"inspect"`) && strings.Contains(stdout, `"action":"complete"`) && strings.Contains(stdout, `"callback":"ai_chat_provider"`) && strings.Contains(stdout, `"callback":"reply_text"`)
	})
	if !strings.Contains(stderr, "runtime-plugin-echo-subprocess-online") {
		t.Fatalf("expected default-routed ai-chat subprocess to boot helper process, stderr=%s stdout=%s", stderr, stdout)
	}
	for _, expected := range []string{`"callback":"ai_chat_queue"`, `"action":"inspect"`, `"action":"complete"`, `"callback":"ai_chat_provider"`, `"callback":"reply_text"`} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("expected subprocess ai-chat job callback evidence %q, stderr=%s stdout=%s", expected, stderr, stdout)
		}
	}
}

func TestRuntimeAppInvalidAIReplyHandleTransitionsJobOutOfPending(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	job := runtimecore.NewJob("job-ai-invalid-reply", "ai.chat", 0, 30*time.Second)
	job.Payload = map[string]any{"prompt": "hello ai"}
	if err := app.queue.Enqueue(t.Context(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		app.queue.DispatchReady(t.Context(), time.Now().UTC())
		stored, inspectErr := app.queue.Inspect(t.Context(), job.ID)
		if inspectErr != nil {
			t.Fatalf("inspect ai job: %v", inspectErr)
		}
		if stored.Status == runtimecore.JobStatusDead {
			if stored.LastError == "" {
				t.Fatalf("expected invalid reply handle failure reason, got %+v", stored)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("expected invalid AI reply handle to move job out of pending")
}

func TestRuntimeAppDispatchFailureTransitionsAIJobOutOfPending(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	job := runtimecore.NewJob("job-ai-dispatch-missing-plugin", "ai.chat", 0, 30*time.Second)
	job.Payload = map[string]any{
		"prompt": "hello ai",
		"dispatch": map[string]any{
			"permission":       "job:run",
			"target_plugin_id": "plugin-missing",
		},
		"reply_handle": map[string]any{
			"capability": "onebot.reply",
			"target_id":  "group-42",
		},
	}
	if err := app.queue.Enqueue(t.Context(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		app.queue.DispatchReady(t.Context(), time.Now().UTC())
		stored, inspectErr := app.queue.Inspect(t.Context(), job.ID)
		if inspectErr != nil {
			t.Fatalf("inspect ai job: %v", inspectErr)
		}
		if stored.Status == runtimecore.JobStatusDead {
			if stored.LastError == "" {
				t.Fatalf("expected dispatch failure reason, got %+v", stored)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("expected dispatch failure to move AI job out of pending")
}

func TestRuntimeAppQueueDispatcherUsesRuntimeMethod(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	job := runtimecore.NewJob("job-ai-bridge-direct", "ai.chat", 0, 30*time.Second)
	job.TraceID = "trace-ai-bridge-direct"
	job.EventID = "evt-ai-bridge-direct"
	job.Correlation = "runtime-ai:user-bridge:hello ai"
	job.Payload = map[string]any{
		"prompt": "hello ai",
		"dispatch": map[string]any{
			"permission":       "job:run",
			"target_plugin_id": "plugin-ai-chat",
		},
		"reply_handle": map[string]any{
			"capability": "onebot.reply",
			"target_id":  "group-42",
		},
	}
	if err := app.queue.Enqueue(t.Context(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	stored, err := app.queue.Inspect(t.Context(), job.ID)
	if err != nil {
		t.Fatalf("inspect job: %v", err)
	}
	dispatcher := queuedRuntimeJobDispatcher{runtime: app.runtime, queue: app.queue}
	if err := dispatcher.DispatchQueuedJob(t.Context(), stored); err != nil {
		t.Fatalf("dispatch queued job through queue dispatcher: %v", err)
	}

	updated, err := app.queue.Inspect(t.Context(), job.ID)
	if err != nil {
		t.Fatalf("inspect updated job: %v", err)
	}
	if updated.Status != runtimecore.JobStatusDone {
		t.Fatalf("expected direct bridge dispatch to complete job, got %+v", updated)
	}
	if len(app.replies.Since(0)) == 0 {
		t.Fatal("expected direct queue dispatcher dispatch to record a reply")
	}
}

func TestRuntimeAppConsoleShowsWorkerLeaseFactsForRunningJob(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	job := runtimecore.NewJob("job-console-running-worker", "demo.echo", 1, 30*time.Second)
	if err := app.queue.Enqueue(t.Context(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	if _, err := app.queue.MarkRunning(t.Context(), job.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}

	payload := readRuntimeConsoleResponse(t, app)
	for _, consoleJob := range payload.Jobs {
		if consoleJob.ID == job.ID {
			if consoleJob.WorkerID == "" || !strings.Contains(consoleJob.LeaseSummary, "worker=") {
				t.Fatalf("expected console worker lease facts for running job, got %+v", consoleJob)
			}
			return
		}
	}
	t.Fatalf("expected running job in console payload, got %+v", payload.Jobs)
}

func TestRuntimeAppConsoleMetaExposesNonHostnameWorkerIdentity(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	payload := readRuntimeConsoleResponse(t, app)
	runtimeWorkerID, _ := payload.Meta["runtime_worker_id"].(string)
	jobWorkerIdentity, _ := payload.Meta["job_worker_identity"].(string)
	if runtimeWorkerID == "" || jobWorkerIdentity == "" {
		t.Fatalf("expected worker identity visibility in console meta, got %+v", payload.Meta)
	}
	if runtimeWorkerID != jobWorkerIdentity {
		t.Fatalf("expected console meta worker identity fields to agree, got runtime=%q job=%q", runtimeWorkerID, jobWorkerIdentity)
	}
	hostname, _ := os.Hostname()
	if hostname != "" && strings.Contains(strings.ToLower(runtimeWorkerID), strings.ToLower(strings.TrimSpace(hostname))) {
		t.Fatalf("expected worker identity to avoid hostname disclosure, got worker_id=%q hostname=%q", runtimeWorkerID, hostname)
	}
	if !strings.HasPrefix(runtimeWorkerID, "runtime-local:") {
		t.Fatalf("expected runtime-local worker identity prefix, got %q", runtimeWorkerID)
	}
}

func TestJobQueueDispatchReadyIgnoresJobTypeWithoutDispatcher(t *testing.T) {
	t.Parallel()

	queue := runtimecore.NewJobQueue()
	job := runtimecore.NewJob("job-no-dispatcher", "demo.echo", 0, 30*time.Second)
	if err := queue.Enqueue(t.Context(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	queue.DispatchReady(t.Context(), time.Now().UTC())
	stored, err := queue.Inspect(t.Context(), job.ID)
	if err != nil {
		t.Fatalf("inspect job: %v", err)
	}
	if stored.Status != runtimecore.JobStatusPending {
		t.Fatalf("expected pending job without dispatcher, got %+v", stored)
	}
}

func TestRuntimeAppAIMessageFailureFeedback(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	req := httptest.NewRequest(http.MethodPost, "/demo/ai/message", strings.NewReader(`{"prompt":"please fail","user_id":"user-ai-2"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}

	jobID := extractAIJobID(resp.Body.String())
	deadline := time.Now().Add(2500 * time.Millisecond)
	for time.Now().Before(deadline) {
		app.queue.DispatchReady(t.Context(), time.Now().UTC())
		repliesReq := httptest.NewRequest(http.MethodGet, "/demo/replies", nil)
		repliesResp := httptest.NewRecorder()
		app.ServeHTTP(repliesResp, repliesReq)
		if strings.Contains(repliesResp.Body.String(), "AI request failed: mock upstream failure") {
			stored, inspectErr := app.queue.Inspect(t.Context(), jobID)
			if inspectErr != nil {
				t.Fatalf("inspect ai job: %v", inspectErr)
			}
			if stored.Status != runtimecore.JobStatusDead {
				t.Fatalf("expected dead ai job, got %+v", stored)
			}
			entries := app.logs.Lines()
			matched := false
			for _, line := range entries {
				if strings.Contains(line, "runtime demo reply recorded") && strings.Contains(line, stored.TraceID) && strings.Contains(line, stored.EventID) && strings.Contains(line, stored.Correlation) {
					matched = true
					break
				}
			}
			if !matched {
				t.Fatalf("expected failure feedback reply log to keep trace/event/correlation context, got %+v", entries)
			}
			if rendered := app.tracer.RenderTrace(stored.TraceID); !strings.Contains(rendered, "reply.send") {
				t.Fatalf("expected reply.send span on failure trace, got %s", rendered)
			}
			return
		}
		time.Sleep(80 * time.Millisecond)
	}
	t.Fatal("expected AI failure feedback to be recorded")
}

func TestRuntimeAppAIMessageOpenAICompatFailureUsesExistingDeadLetterPath(t *testing.T) {
	apiKeyRef := "BOT_PLATFORM_AI_CHAT_API_KEY_TEST_FAILURE"
	t.Setenv(apiKeyRef, "secret-openai-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream overloaded", http.StatusBadGateway)
	}))
	defer server.Close()

	app, err := newRuntimeApp(writeAIProviderConfigAt(t, t.TempDir(), server.URL, "gpt-fail", 500, apiKeyRef))
	if err != nil {
		t.Fatalf("new runtime app with failing openai compat provider: %v", err)
	}
	defer func() { _ = app.Close() }()

	req := httptest.NewRequest(http.MethodPost, "/demo/ai/message", strings.NewReader(`{"prompt":"please fail for real provider","user_id":"user-ai-openai-fail"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}
	jobID := extractAIJobID(resp.Body.String())
	stored := waitForAIJobStatus(t, app, jobID, runtimecore.JobStatusDead)
	if !stored.DeadLetter || stored.ReasonCode != runtimecore.JobReasonCodeExecutionDead {
		t.Fatalf("expected execution dead-letter semantics for provider failure, got %+v", stored)
	}
	if !strings.Contains(stored.LastError, "status 502") || !strings.Contains(stored.LastError, "upstream overloaded") {
		t.Fatalf("expected provider failure detail on job, got %+v", stored)
	}
	repliesReq := httptest.NewRequest(http.MethodGet, "/demo/replies", nil)
	repliesResp := httptest.NewRecorder()
	app.ServeHTTP(repliesResp, repliesReq)
	if !strings.Contains(repliesResp.Body.String(), "AI request failed: openai_compat request failed: status 502: upstream overloaded") {
		t.Fatalf("expected dead-letter failure feedback reply, got %s", repliesResp.Body.String())
	}
	matchedDispatchLog := false
	matchedProviderErrorLog := false
	for _, line := range app.logs.Lines() {
		if strings.Contains(line, "runtime queued job dispatch failed") && strings.Contains(line, jobID) && strings.Contains(line, "status 502") {
			matchedDispatchLog = true
		}
		if strings.Contains(line, "runtime ai provider returned non-success response") && strings.Contains(line, `"status_code":502`) {
			matchedProviderErrorLog = true
		}
	}
	if !matchedDispatchLog {
		t.Fatalf("expected queued dispatch failure log for provider error, got %+v", app.logs.Lines())
	}
	if !matchedProviderErrorLog {
		t.Fatalf("expected provider error log for non-2xx response, got %+v", app.logs.Lines())
	}
	alerts, err := app.state.ListAlerts(t.Context())
	if err != nil {
		t.Fatalf("load alerts after provider dead letter: %v", err)
	}
	foundDeadLetterAlert := false
	for _, alert := range alerts {
		if alert.ObjectID == jobID && alert.FailureType == "job.dead_letter" && strings.Contains(alert.LatestReason, "status 502") {
			foundDeadLetterAlert = true
			break
		}
	}
	if !foundDeadLetterAlert {
		t.Fatalf("expected dead-letter alert for provider failure, got %+v", alerts)
	}
	if rendered := app.tracer.RenderTrace(stored.TraceID); !strings.Contains(rendered, "reply.send") {
		t.Fatalf("expected reply.send span on provider dead-letter trace, got %s", rendered)
	}
}

func TestRuntimeAppAIMessageOpenAICompatTimeoutUsesExistingDeadLetterPath(t *testing.T) {
	apiKeyRef := "BOT_PLATFORM_AI_CHAT_API_KEY_TEST_TIMEOUT"
	t.Setenv(apiKeyRef, "secret-openai-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"too slow"}}]}`))
	}))
	defer server.Close()

	app, err := newRuntimeApp(writeAIProviderConfigAt(t, t.TempDir(), server.URL, "gpt-timeout", 50, apiKeyRef))
	if err != nil {
		t.Fatalf("new runtime app with timeout openai compat provider: %v", err)
	}
	defer func() { _ = app.Close() }()

	req := httptest.NewRequest(http.MethodPost, "/demo/ai/message", strings.NewReader(`{"prompt":"timeout please","user_id":"user-ai-openai-timeout"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}
	jobID := extractAIJobID(resp.Body.String())
	stored := waitForAIJobStatus(t, app, jobID, runtimecore.JobStatusDead)
	if !stored.DeadLetter || stored.ReasonCode != runtimecore.JobReasonCodeExecutionDead {
		t.Fatalf("expected timeout path to dead-letter through existing execution semantics, got %+v", stored)
	}
	if !strings.Contains(strings.ToLower(stored.LastError), "context deadline exceeded") {
		t.Fatalf("expected timeout detail on job, got %+v", stored)
	}
	repliesReq := httptest.NewRequest(http.MethodGet, "/demo/replies", nil)
	repliesResp := httptest.NewRecorder()
	app.ServeHTTP(repliesResp, repliesReq)
	if !strings.Contains(strings.ToLower(repliesResp.Body.String()), "context deadline exceeded") {
		t.Fatalf("expected timeout failure feedback reply, got %s", repliesResp.Body.String())
	}
	matchedProviderTimeoutLog := false
	for _, line := range app.logs.Lines() {
		if strings.Contains(line, "runtime ai provider request failed") && strings.Contains(strings.ToLower(line), "context deadline exceeded") {
			matchedProviderTimeoutLog = true
			break
		}
	}
	if !matchedProviderTimeoutLog {
		t.Fatalf("expected provider timeout log, got %+v", app.logs.Lines())
	}
}

func TestRuntimeAppRestoresPersistedJobsAfterRestart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := writeTestConfigAt(t, dir)

	app, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}

	enqueueReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/enqueue", strings.NewReader(`{"id":"job-restart-visible"}`))
	enqueueReq.Header.Set("Content-Type", "application/json")
	enqueueResp := httptest.NewRecorder()
	app.ServeHTTP(enqueueResp, enqueueReq)
	if enqueueResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", enqueueResp.Code, enqueueResp.Body.String())
	}
	if err := app.Close(); err != nil {
		t.Fatalf("close first app: %v", err)
	}

	restarted, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("restart runtime app: %v", err)
	}
	defer func() { _ = restarted.Close() }()

	stored, err := restarted.queue.Inspect(t.Context(), "job-restart-visible")
	if err != nil {
		t.Fatalf("inspect restored job: %v", err)
	}
	if stored.Status != runtimecore.JobStatusPending {
		t.Fatalf("expected restored job to remain pending, got %+v", stored)
	}

	consoleReq := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	consoleResp := httptest.NewRecorder()
	restarted.ServeHTTP(consoleResp, consoleReq)
	if !strings.Contains(consoleResp.Body.String(), "job-restart-visible") {
		t.Fatalf("expected console to include restored job, got %s", consoleResp.Body.String())
	}
}

func TestRuntimeAppRestartRecoversRunningJobState(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := writeTestConfigAt(t, dir)

	app, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}

	job := runtimecore.NewJob("job-restart-running", "ai.chat", 1, 30*time.Second)
	if err := app.queue.Enqueue(t.Context(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	if _, err := app.queue.MarkRunning(t.Context(), job.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	runningBeforeClose, err := app.queue.Inspect(t.Context(), job.ID)
	if err != nil {
		t.Fatalf("inspect running job before restart: %v", err)
	}
	if runningBeforeClose.WorkerID == "" || runningBeforeClose.LeaseAcquiredAt == nil || runningBeforeClose.HeartbeatAt == nil {
		t.Fatalf("expected running job ownership facts before restart, got %+v", runningBeforeClose)
	}
	if runningBeforeClose.LeaseExpiresAt == nil {
		t.Fatalf("expected lease expiry before restart, got %+v", runningBeforeClose)
	}
	expiredLeaseAt := time.Now().UTC().Add(-1 * time.Second)
	runningBeforeClose.LeaseExpiresAt = &expiredLeaseAt
	if err := app.state.SaveJob(t.Context(), runningBeforeClose); err != nil {
		t.Fatalf("persist expired lease before restart: %v", err)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("close first app: %v", err)
	}

	restarted, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("restart runtime app: %v", err)
	}
	defer func() { _ = restarted.Close() }()

	restored, err := restarted.queue.Inspect(t.Context(), job.ID)
	if err != nil {
		t.Fatalf("inspect restored running job: %v", err)
	}
	if restored.Status != runtimecore.JobStatusRetrying || restored.RetryCount != 1 || restored.NextRunAt == nil {
		t.Fatalf("expected running job to recover as retrying, got %+v", restored)
	}
	if restored.ReasonCode != runtimecore.JobReasonCodeWorkerAbandoned || !strings.Contains(restored.LastError, "lease abandoned") {
		t.Fatalf("expected restart recovery reason, got %+v", restored)
	}
	consoleReq := consoleRequestWithViewer("/api/console")
	consoleResp := httptest.NewRecorder()
	restarted.ServeHTTP(consoleResp, consoleReq)
	if consoleResp.Code != http.StatusOK {
		t.Fatalf("expected console 200, got %d: %s", consoleResp.Code, consoleResp.Body.String())
	}
	var payload runtimeConsoleResponse
	if err := json.Unmarshal(consoleResp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode console payload: %v", err)
	}
	jobVisible := false
	for _, consoleJob := range payload.Jobs {
		if consoleJob.ID == "job-restart-running" {
			jobVisible = true
			if consoleJob.ReasonCode != "worker_abandoned" || !strings.Contains(consoleJob.RecoverySummary, "reason_code=worker_abandoned") {
				t.Fatalf("expected console recovery reason visibility, got %+v", consoleJob)
			}
			break
		}
	}
	if !jobVisible {
		t.Fatalf("expected restored job in console payload, got %+v", payload.Jobs)
	}
}

func TestRuntimeAppRestartRecoveryIsVisibleInLogsMetricsAndTrace(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := writeTestConfigAt(t, dir)

	app, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}

	job := runtimecore.NewJob("job-restart-observe", "ai.chat", 1, 30*time.Second)
	job.TraceID = "trace-restart-observe"
	job.EventID = "evt-restart-observe"
	job.Correlation = "corr-restart-observe"
	if err := app.queue.Enqueue(t.Context(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	if _, err := app.queue.MarkRunning(t.Context(), job.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("close first app: %v", err)
	}

	restarted, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("restart runtime app: %v", err)
	}
	defer func() { _ = restarted.Close() }()

	restored, err := restarted.queue.Inspect(t.Context(), job.ID)
	if err != nil {
		t.Fatalf("inspect restored job: %v", err)
	}
	if restored.Status != runtimecore.JobStatusRetrying {
		t.Fatalf("expected restored retrying job, got %+v", restored)
	}

	logs := restarted.logs.Lines()
	foundSummary := false
	foundRecovered := false
	for _, line := range logs {
		if strings.Contains(line, "job queue restored from persistence") && strings.Contains(line, `"recovered_jobs":1`) && strings.Contains(line, `"retrying_jobs":1`) {
			foundSummary = true
		}
		if strings.Contains(line, "job.recovered") && strings.Contains(line, restored.TraceID) && strings.Contains(line, restored.EventID) && strings.Contains(line, restored.Correlation) {
			foundRecovered = true
		}
	}
	if !foundSummary || !foundRecovered {
		t.Fatalf("expected recovery logs and summary, got %+v", logs)
	}

	if rendered := restarted.tracer.RenderTrace(restored.TraceID); !strings.Contains(rendered, "job.lifecycle") {
		t.Fatalf("expected recovery trace to contain job.lifecycle span, got %s", rendered)
	}
	metricsOutput := restarted.metrics.RenderPrometheus()
	if !strings.Contains(metricsOutput, `bot_platform_job_recoveries_total 1`) {
		t.Fatalf("expected recovery metric after restart, got %s", metricsOutput)
	}
	if !strings.Contains(metricsOutput, `bot_platform_job_status_total{status="retrying"} 1`) {
		t.Fatalf("expected retrying metric after recovery, got %s", metricsOutput)
	}
}

func TestRuntimeAppRestartRecoveryKeepsRuntimeRestartReasonWhenLeaseNotExpired(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := writeTestConfigAt(t, dir)

	app, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}

	job := runtimecore.NewJob("job-restart-runtime-reason", "ai.chat", 1, 30*time.Second)
	if err := app.queue.Enqueue(t.Context(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	if _, err := app.queue.MarkRunning(t.Context(), job.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("close first app: %v", err)
	}

	restarted, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("restart runtime app: %v", err)
	}
	defer func() { _ = restarted.Close() }()

	restored, err := restarted.queue.Inspect(t.Context(), job.ID)
	if err != nil {
		t.Fatalf("inspect restored job: %v", err)
	}
	if restored.ReasonCode != runtimecore.JobReasonCodeRuntimeRestart || !strings.Contains(restored.LastError, "runtime restarted") {
		t.Fatalf("expected runtime restart classification for unexpired lease, got %+v", restored)
	}
}

func extractAIJobID(raw string) string {
	var payload struct {
		JobID string `json:"job_id"`
	}
	_ = json.Unmarshal([]byte(raw), &payload)
	return payload.JobID
}

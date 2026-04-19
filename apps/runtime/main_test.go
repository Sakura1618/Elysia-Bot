package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
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
		ID             string `json:"id"`
		PluginID       string `json:"pluginId"`
		Status         string `json:"status"`
		WaitingFor     string `json:"waitingFor"`
		Completed      bool   `json:"completed"`
		Compensated    bool   `json:"compensated"`
		StatusSource   string `json:"statusSource"`
		StatePersisted bool   `json:"statePersisted"`
		RuntimeOwner   string `json:"runtimeOwner"`
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
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := "runtime:\n  environment: test\n  log_level: debug\n  http_port: 18080\n  sqlite_path: " + filepath.ToSlash(filepath.Join(dir, "runtime.sqlite")) + "\n  scheduler_interval_ms: 20\nrbac:\n  actor_roles:\n    schedule-admin: [schedule-operator]\n    viewer-user: [schedule-viewer]\n  policies:\n    schedule-operator:\n      permissions: [schedule:cancel]\n      plugin_scope: ['*']\n    schedule-viewer:\n      permissions: [schedule:view]\n      plugin_scope: ['*']\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func writeWriteActionRBACConfig(t *testing.T) string {
	t.Helper()
	return writeWriteActionRBACConfigAt(t, t.TempDir())
}

func writeWriteActionRBACConfigAt(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	content := "runtime:\n  environment: test\n  log_level: debug\n  http_port: 18080\n  sqlite_path: " + filepath.ToSlash(filepath.Join(dir, "runtime.sqlite")) + "\n  scheduler_interval_ms: 20\nrbac:\n  actor_roles:\n    job-operator: [job-operator]\n    config-operator: [config-operator]\n    runtime-job-runner: [runtime-job-runner]\n    viewer-user: [viewer]\n  policies:\n    job-operator:\n      permissions: [job:retry]\n      plugin_scope: ['*']\n    config-operator:\n      permissions: [plugin:config]\n      plugin_scope: ['plugin-echo']\n    runtime-job-runner:\n      permissions: [job:run]\n      plugin_scope: ['plugin-ai-chat']\n    viewer:\n      permissions: [console:read]\n      plugin_scope: ['console']\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
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
	if got := consoleMetaStringSlice(t, console.Meta, "demo_paths"); !reflect.DeepEqual(got, []string{"/demo/onebot/message", "/demo/onebot/message-beta", "/ingress/webhook/main", "/demo/workflows/message", "/demo/ai/message", "/demo/jobs/enqueue", "/demo/jobs/timeout", "/demo/jobs/{job-id}/retry", "/demo/schedules/echo-delay", "/demo/schedules/{schedule-id}/cancel", "/demo/plugins/{plugin-id}/disable", "/demo/plugins/{plugin-id}/enable", "/demo/plugins/{plugin-id}/config", "/demo/replies", "/demo/state/counts"}) {
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
	for _, expected := range []string{`"plugin_config_state_read_model": "runtime-registry+sqlite-plugin-config"`, `"plugin_config_state_kind": "plugin-owned-persisted-input"`, `"plugin_config_state_persisted": true`, `"plugin_config_operator_actions": [`, `"plugin_config_operator_scope": "plugins with app-local persisted config bindings only"`, `"plugin_enabled_state_read_model": "runtime-registry+sqlite-plugin-enabled-overlay"`, `"plugin_enabled_state_persisted": true`, `"plugin_operator_scope": "already-registered plugins only"`, `"workflow_read_model": "sqlite-workflow-instances"`, `"workflow_status_source": "sqlite-workflow-instances"`, `"workflow_status_persisted": true`, `"workflow_runtime_owner": "runtime-core"`, `"/demo/plugins/{plugin-id}/enable"`, `"/demo/plugins/{plugin-id}/disable"`, `"/demo/plugins/{plugin-id}/config"`, `"/demo/schedules/{schedule-id}/cancel"`, `"console_mode": "read+operator-plugin-enable-disable+plugin-config"`, `"enabled": true`, `"enabledStateSource": "runtime-default-enabled"`, `"enabledStatePersisted": false`, `"configStateKind": "plugin-owned-persisted-input"`, `"configPersisted": false`} {
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
		"/demo/jobs/{job-id}/retry",
		"/demo/schedules/echo-delay",
		"/demo/schedules/{schedule-id}/cancel",
		"/demo/plugins/{plugin-id}/disable",
		"/demo/plugins/{plugin-id}/enable",
		"/demo/plugins/{plugin-id}/config",
		"/demo/replies",
		"/demo/state/counts",
	}
	if got := consoleMetaString(t, console.Meta, "console_mode"); got != "read+operator-plugin-enable-disable+plugin-config" {
		t.Fatalf("expected console_mode to advertise plugin-config capability, got %q", got)
	}
	if got := consoleMetaString(t, console.Meta, "plugin_config_operator_scope"); got != "plugins with app-local persisted config bindings only" {
		t.Fatalf("expected plugin_config_operator_scope to describe binding-driven capability, got %q", got)
	}
	if got := consoleMetaStringSlice(t, console.Meta, "plugin_config_operator_actions"); !reflect.DeepEqual(got, []string{"/demo/plugins/{plugin-id}/config"}) {
		t.Fatalf("expected exact plugin_config_operator_actions, got %+v", got)
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
	for _, expected := range []string{`"admin-command-runtime-authorizer"`, `"event-metadata-runtime-authorizer"`, `"job-metadata-runtime-authorizer"`, `"schedule-metadata-runtime-authorizer"`, `"schedule-operator-runtime-authorizer"`, `"job-operator-runtime-authorizer"`, `"plugin-config-runtime-authorizer"`, `"plugin-admin-current-authorizer-provider"`, `"dispatch-manifest-permission-gate"`, `"job-target-plugin-filter"`, `"console-read-authorizer"`, `"permission_denied"`, `"plugin_scope_denied"`, `"unified-authentication"`, `"unified-resource-model"`, `"independent-authorization-read-model"`, `"actor"`, `"permission"`, `"target_plugin_id"`, `"console read authorization is optional and only enforced when rbac.console_read_permission is configured"`, `"console read authorization currently reads actor only from the X-Bot-Platform-Actor header"`, `"manifest permission gate remains a separate dispatch contract check and does not emit deny audit entries"`, `"target_plugin_id remains a dispatch filter, not a global RBAC resource kind"`, `"current slice persists and reloads a single runtime snapshot but does not add login/authn UX or a global resource hierarchy"`} {
		if !strings.Contains(resp.Body.String(), expected) {
			t.Fatalf("expected console payload to include RBAC declaration detail %s, got %s", expected, resp.Body.String())
		}
	}
	for _, expected := range []string{`"secrets.webhook_token_ref"`, `"adapter-webhook.NewWithSecretRef"`, `"secret.read"`, `"secret-write-api"`, `generic secret resolution failures`, `"/admin prepare \u003cplugin-id\u003e"`, `"prepared-record-required"`, `"replay_record_read_model": "sqlite-replay-operation-records"`, `"rollout_record_read_model": "sqlite-rollout-operation-records"`, `"job_status_source": "sqlite-jobs"`, `"schedule_status_source": "sqlite-schedule-plans"`} {
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
	if !strings.Contains(disableResp.Body.String(), `"plugin_id":"plugin-echo"`) && !strings.Contains(disableResp.Body.String(), `"plugin_id": "plugin-echo"`) {
		t.Fatalf("expected disable response to include plugin id, got %s", disableResp.Body.String())
	}
	if !strings.Contains(disableResp.Body.String(), `"enabled":false`) && !strings.Contains(disableResp.Body.String(), `"enabled": false`) {
		t.Fatalf("expected disable response to confirm disabled state, got %s", disableResp.Body.String())
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
	if !strings.Contains(enableResp.Body.String(), `"enabled":true`) && !strings.Contains(enableResp.Body.String(), `"enabled": true`) {
		t.Fatalf("expected enable response to confirm enabled state, got %s", enableResp.Body.String())
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
	if entries[0].Action != "disable" || entries[0].Target != "plugin-echo" || !entries[0].Allowed {
		t.Fatalf("expected first audit entry for disable, got %+v", entries[0])
	}
	if entries[1].Action != "enable" || entries[1].Target != "plugin-echo" || !entries[1].Allowed {
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

func TestRuntimeAppWorkflowDemoPersistsAndRecoversAcrossRestart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := writeTestConfigAt(t, dir)

	app, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf(`new runtime app: %v`, err)
	}
	startResp := performRuntimeWorkflowMessageRequest(t, app, `{"actor_id":"user-1","message":"start workflow"}`)
	if startResp.Code != http.StatusOK {
		t.Fatalf(`expected workflow start 200, got %d: %s`, startResp.Code, startResp.Body.String())
	}
	if !strings.Contains(startResp.Body.String(), `workflow started, please send another message to continue`) {
		t.Fatalf(`expected workflow start reply, got %s`, startResp.Body.String())
	}
	started, err := app.state.LoadWorkflowInstance(t.Context(), `workflow-user-1`)
	if err != nil {
		t.Fatalf(`load started workflow instance: %v`, err)
	}
	if started.PluginID != `plugin-workflow-demo` || started.Status != runtimecore.WorkflowRuntimeStatusWaitingEvent {
		t.Fatalf(`expected waiting persisted workflow after first request, got %+v`, started)
	}
	if started.Workflow.WaitingFor != `message.received` || started.Workflow.State[`greeting`] != `start workflow` || started.Workflow.Completed {
		t.Fatalf(`expected workflow runtime to persist waiting state after first request, got %+v`, started.Workflow)
	}
	if err := app.Close(); err != nil {
		t.Fatalf(`close first app: %v`, err)
	}

	restarted, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf(`restart runtime app: %v`, err)
	}
	defer func() { _ = restarted.Close() }()
	recovery := restarted.workflowRuntime.LastRecoverySnapshot()
	if recovery.TotalWorkflows != 1 || recovery.RecoveredWorkflows != 1 || recovery.StatusCounts[runtimecore.WorkflowRuntimeStatusWaitingEvent] != 1 {
		t.Fatalf(`expected workflow runtime recovery snapshot after restart, got %+v`, recovery)
	}
	resumeResp := performRuntimeWorkflowMessageRequest(t, restarted, `{"actor_id":"user-1","message":"continue"}`)
	if resumeResp.Code != http.StatusOK {
		t.Fatalf(`expected workflow resume 200, got %d: %s`, resumeResp.Code, resumeResp.Body.String())
	}
	if !strings.Contains(resumeResp.Body.String(), `workflow resumed and completed`) {
		t.Fatalf(`expected workflow resume reply, got %s`, resumeResp.Body.String())
	}
	completed, err := restarted.state.LoadWorkflowInstance(t.Context(), `workflow-user-1`)
	if err != nil {
		t.Fatalf(`load completed workflow instance: %v`, err)
	}
	if completed.Status != runtimecore.WorkflowRuntimeStatusCompleted || !completed.Workflow.Completed || !completed.Workflow.Compensated {
		t.Fatalf(`expected completed persisted workflow after restart resume, got %+v`, completed)
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
		if workflow.StatusSource != `sqlite-workflow-instances` || !workflow.StatePersisted || workflow.RuntimeOwner != `runtime-core` {
			t.Fatalf(`expected workflow console provenance metadata, got %+v`, workflow)
		}
		return
	}
	t.Fatalf(`expected workflow-user-1 in console payload, got %+v`, console.Workflows)
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
	if last.Actor != "admin-user" || last.Action != "plugin.enable" || last.Permission != "plugin:enable" || last.Target != "plugin-echo" || last.Allowed || last.Reason != "permission_denied" {
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
	if last.Actor != "config-operator" || last.Action != "config.update" || last.Permission != "plugin:config" || last.Target != "plugin-echo" || !last.Allowed || last.Reason != "plugin_config_updated" {
		t.Fatalf("expected allowed config audit after restart, got %+v", last)
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
	for _, expected := range []string{`"status":"ok"`, `"action":"retry"`, `"target":"job-dead-retry-console"`, `"accepted":true`, `"reason":"job_dead_letter_retried"`, `"job_id":"job-dead-retry-console"`} {
		if !strings.Contains(retryResp.Body.String(), expected) && !strings.Contains(retryResp.Body.String(), strings.ReplaceAll(expected, `:`, `: `)) {
			t.Fatalf("expected retry response to include %s, got %s", expected, retryResp.Body.String())
		}
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
			Actor   string `json:"actor"`
			Action  string `json:"action"`
			Target  string `json:"target"`
			Allowed bool   `json:"allowed"`
			Reason  string `json:"reason"`
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
		if entry.Actor == "job-operator" && entry.Action == "retry" && entry.Target == "job-dead-retry-console" && entry.Allowed && entry.Reason == "job_dead_letter_retried" {
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
	if lastEntry.Actor != "job-operator" || lastEntry.Permission != "job:retry" || lastEntry.Action != "retry" || lastEntry.Target != "job-dead-retry-console" || !lastEntry.Allowed || lastEntry.Reason != "job_dead_letter_retried" {
		t.Fatalf("expected distinct retry audit entry, got %+v", lastEntry)
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
		if entry.Action == "retry" && entry.Target == "job-dead-retry-once" && entry.Allowed {
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
	if entries[0].Actor != "viewer-user" || entries[0].Action != "job.retry" || entries[0].Permission != "job:retry" || entries[0].Target != "job-dead-retry-denied" || entries[0].Allowed || entries[0].Reason != "permission_denied" {
		t.Fatalf("expected denied retry audit entry, got %+v", entries[0])
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
	for _, expected := range []string{`"status":"ok"`, `"action":"config.update"`, `"target":"plugin-echo"`, `"accepted":true`, `"reason":"plugin_config_updated"`, `"plugin_id":"plugin-echo"`} {
		if !strings.Contains(configResp.Body.String(), expected) && !strings.Contains(configResp.Body.String(), strings.ReplaceAll(expected, `:`, `: `)) {
			t.Fatalf("expected config response to include %s, got %s", expected, configResp.Body.String())
		}
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
	if entries[0].Actor != "config-operator" || entries[0].Action != "config.update" || entries[0].Permission != "plugin:config" || entries[0].Target != "plugin-echo" || !entries[0].Allowed || entries[0].Reason != "plugin_config_updated" {
		t.Fatalf("expected config update audit entry, got %+v", entries[0])
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
	if entries[0].Actor != "viewer-user" || entries[0].Action != "plugin.config" || entries[0].Permission != "plugin:config" || entries[0].Target != "plugin-echo" || entries[0].Allowed || entries[0].Reason != "permission_denied" {
		t.Fatalf("expected denied config audit entry, got %+v", entries[0])
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
	for _, expected := range []string{`"status":"ok"`, `"schedule_id":"schedule-cancel-1"`, `"action":"cancel"`} {
		if !strings.Contains(cancelResp.Body.String(), expected) && !strings.Contains(cancelResp.Body.String(), strings.ReplaceAll(expected, `:`, `: `)) {
			t.Fatalf("expected cancel response to include %s, got %s", expected, cancelResp.Body.String())
		}
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
	if lastEntry.Action != "cancel" || lastEntry.Target != "schedule-cancel-1" || !lastEntry.Allowed || lastEntry.Actor != "schedule-admin" || lastEntry.Permission != "schedule:cancel" || lastEntry.Reason != "schedule_cancelled" {
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
	if entries[0].Actor != "viewer-user" || entries[0].Action != "schedule.cancel" || entries[0].Permission != "schedule:cancel" || entries[0].Target != "schedule-cancel-denied" || entries[0].Allowed || entries[0].Reason != "permission_denied" {
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
	if !strings.Contains(cancelResp.Body.String(), "permission denied") {
		t.Fatalf("expected headerless cancel response to mention permission denied, got %s", cancelResp.Body.String())
	}
	entries := app.audits.AuditEntries()
	if len(entries) != 1 || entries[0].Actor != "" || entries[0].Action != "schedule.cancel" || entries[0].Permission != "schedule:cancel" || entries[0].Target != "schedule-cancel-missing-actor" || entries[0].Allowed || entries[0].Reason != "permission_denied" {
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

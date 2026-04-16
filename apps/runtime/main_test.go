package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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
		"      demo_path: /demo/onebot/message\n" +
		"      self_id: 10001\n" +
		"    - id: adapter-onebot-beta\n" +
		"      adapter: onebot\n" +
		"      source: onebot-beta\n" +
		"      platform: onebot/v11\n" +
		"      demo_path: /demo/onebot/message-beta\n"
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
	Schedules []struct {
		ID string `json:"id"`
	} `json:"schedules"`
	Status struct {
		Adapters  int `json:"adapters"`
		Schedules int `json:"schedules"`
	} `json:"status"`
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
	dsn := strings.TrimSpace(os.Getenv(runtimePostgresTestDSNEnv))
	if dsn == "" {
		t.Skipf("set %s to run runtime Postgres smoke test", runtimePostgresTestDSNEnv)
	}

	configPath := writeTestConfigWithPostgresSmokeStoreAt(t, t.TempDir(), dsn)
	app, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("new runtime app with postgres smoke store: %v", err)
	}
	defer func() { _ = app.Close() }()

	beforeCounts, err := app.runtimeStateCounts(t.Context())
	if err != nil {
		t.Fatalf("runtime state counts before request: %v", err)
	}
	messageID := time.Now().UnixNano()
	messageBody := `{"post_type":"message","message_type":"group","time":1712034000,"user_id":10001,"group_id":42,"message_id":` + strconv.FormatInt(messageID, 10) + `,"raw_message":"hello postgres runtime","sender":{"nickname":"alice"}}`

	req := httptest.NewRequest(http.MethodPost, "/demo/onebot/message", strings.NewReader(messageBody))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	app.ServeHTTP(resp, req)

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

	sqliteCounts, err := app.state.Counts(t.Context())
	if err != nil {
		t.Fatalf("sqlite counts: %v", err)
	}
	if sqliteCounts["event_journal"] != 0 || sqliteCounts["idempotency_keys"] != 0 {
		t.Fatalf("expected sqlite to remain out of smoke persistence path when postgres selected, got %+v", sqliteCounts)
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
	if !strings.Contains(resp.Body.String(), `"plugins": 3`) {
		t.Fatalf("expected console payload to report three registered plugins, got %s", resp.Body.String())
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
	for _, expected := range []string{`"plugin_config_state_read_model": "runtime-registry+sqlite-plugin-config"`, `"plugin_config_state_kind": "plugin-owned-persisted-input"`, `"plugin_config_state_persisted": true`, `"plugin_enabled_state_read_model": "runtime-registry+sqlite-plugin-enabled-overlay"`, `"plugin_enabled_state_persisted": true`, `"plugin_operator_scope": "already-registered plugins only"`, `"/demo/plugins/{plugin-id}/enable"`, `"/demo/plugins/{plugin-id}/disable"`, `"/demo/schedules/{schedule-id}/cancel"`, `"console_mode": "read+operator-plugin-enable-disable"`, `"enabled": true`, `"enabledStateSource": "runtime-default-enabled"`, `"enabledStatePersisted": false`, `"configStateKind": "plugin-owned-persisted-input"`, `"configPersisted": false`} {
		if !strings.Contains(resp.Body.String(), expected) {
			t.Fatalf("expected console payload to include %s, got %s", expected, resp.Body.String())
		}
	}
	if !strings.Contains(resp.Body.String(), `"plugin_dispatch_source": "sqlite-plugin-status-snapshot+runtime-dispatch-results"`) {
		t.Fatalf("expected console payload to include plugin_dispatch_source=sqlite-plugin-status-snapshot+runtime-dispatch-results, got %s", resp.Body.String())
	}
	for _, expected := range []string{`"plugin_status_source": "runtime-registry+sqlite-plugin-status-snapshot+runtime-dispatch-results"`, `"plugin_status_evidence_model": "manifest-static-or-last-persisted-plugin-snapshot-with-live-overlay"`, `"plugin_dispatch_kind_visibility": "last-persisted-or-live-dispatch-kind"`, `"plugin_recovery_visibility": "last-dispatch-failed|last-dispatch-succeeded|recovered-after-failure|no-runtime-evidence"`, `"plugin_status_staleness": "static-registration|persisted-snapshot|persisted-snapshot+live-overlay|process-local-volatile"`, `"plugin_status_staleness_reason": "persisted plugin snapshots survive restart while current-process live overlay remains explicitly distinguished from the stored snapshot"`, `"plugin_runtime_state_live": true`, `"rbac_capability_surface": "read-only declaration of current authorization and adjacent dispatch-boundary facts"`, `"rbac_read_model_scope": "current runtime authorizer entrypoints, adjacent dispatch contract/filter boundaries, deny audit taxonomy, and known system gaps"`, `"rbac_current_state": "partial-runtime-local-read-model"`, `"rbac_system_model_state": "not-complete-global-rbac-authn-or-audit-system"`, `"rbac_current_authorization_paths_count": 6`, `"rbac_deny_audit_scope": "authorizer deny paths only"`, `"rbac_manifest_permission_gate_audited": false`, `"rbac_manifest_permission_gate_boundary": "independent dispatch contract check; not part of deny audit taxonomy"`, `"rbac_job_target_plugin_filter_boundary": "dispatch filter only; not an authorizer entrypoint or deny audit taxonomy item"`, `"rbac_console_read_permission": false`, `"rbac_console_read_actor_header": "X-Bot-Platform-Actor"`, `"secrets_provider": "env"`, `"secrets_runtime_owned_ref_prefix": "BOT_PLATFORM_"`, `"rollout_record_store": "in-memory-per-runtime-process"`} {
		if !strings.Contains(resp.Body.String(), expected) {
			t.Fatalf("expected console payload to include %s, got %s", expected, resp.Body.String())
		}
	}
	for _, expected := range []string{`"admin-command-runtime-authorizer"`, `"event-metadata-runtime-authorizer"`, `"job-metadata-runtime-authorizer"`, `"schedule-metadata-runtime-authorizer"`, `"schedule-operator-runtime-authorizer"`, `"dispatch-manifest-permission-gate"`, `"job-target-plugin-filter"`, `"console-read-authorizer"`, `"permission_denied"`, `"plugin_scope_denied"`, `"persistent-policy-store"`, `"policy-hot-reload"`, `"unified-authentication"`, `"unified-resource-model"`, `"independent-authorization-read-model"`, `"actor"`, `"permission"`, `"target_plugin_id"`, `"console read authorization is optional and only enforced when rbac.console_read_permission is configured"`, `"console read authorization currently reads actor only from the X-Bot-Platform-Actor header"`, `"manifest permission gate remains a separate dispatch contract check and does not emit deny audit entries"`, `"target_plugin_id remains a dispatch filter, not a global RBAC resource kind"`, `"Q8 currently remains a partial runtime-local closure rather than a complete global RBAC, authn, or audit system"`} {
		if !strings.Contains(resp.Body.String(), expected) {
			t.Fatalf("expected console payload to include RBAC declaration detail %s, got %s", expected, resp.Body.String())
		}
	}
	for _, expected := range []string{`"secrets.webhook_token_ref"`, `"adapter-webhook.NewWithSecretRef"`, `"secret.read"`, `"secret-write-api"`, `generic secret resolution failures`, `"/admin prepare \u003cplugin-id\u003e"`, `"prepared-record-required"`, `"persisted-rollout-history"`, `"job_status_source": "sqlite-jobs"`, `"schedule_status_source": "sqlite-schedule-plans"`} {
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
	if !strings.Contains(resp.Body.String(), `"snapshot_atomic": false`) {
		t.Fatalf("expected console payload to include snapshot_atomic=false, got %s", resp.Body.String())
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

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	req := httptest.NewRequest(http.MethodPost, "/demo/plugins/plugin-echo/config", strings.NewReader(`{"prefix":true}`))
	req.Header.Set("Content-Type", "application/json")
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
}

func TestRuntimeAppReloadsPersistedPluginEchoConfigAfterRestart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := writeTestConfigAt(t, dir)

	app, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}

	configReq := httptest.NewRequest(http.MethodPost, "/demo/plugins/plugin-echo/config", strings.NewReader(`{"prefix":"persisted: "}`))
	configReq.Header.Set("Content-Type", "application/json")
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

	consoleReq := httptest.NewRequest(http.MethodGet, "/api/console?plugin_id=plugin-echo", nil)
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
	console := readRuntimeConsoleResponse(t, restarted)
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

func TestRuntimeAppSkipsDuplicateIdempotencyKey(t *testing.T) {
	t.Parallel()

	app, err := newRuntimeApp(writeTestConfig(t))
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

	consoleReq := httptest.NewRequest(http.MethodGet, "/api/console", nil)
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

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	enqueueReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/enqueue", strings.NewReader(`{"id":"job-dead-retry-console","type":"ai.chat","prompt":"hello retry","user_id":"user-retry","max_retries":0}`))
	enqueueReq.Header.Set("Content-Type", "application/json")
	enqueueResp := httptest.NewRecorder()
	app.ServeHTTP(enqueueResp, enqueueReq)
	if enqueueResp.Code != http.StatusOK {
		t.Fatalf("expected enqueue 200, got %d: %s", enqueueResp.Code, enqueueResp.Body.String())
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
	for _, expected := range []string{`"status":"ok"`, `"job_id":"job-dead-retry-console"`, `"action":"retry"`, `"accepted":true`} {
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
	if lastEntry.Actor != "job-operator" || lastEntry.Action != "retry" || lastEntry.Target != "job-dead-retry-console" || !lastEntry.Allowed || lastEntry.Reason != "job_dead_letter_retried" {
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

	app, err := newRuntimeApp(writeTestConfig(t))
	if err != nil {
		t.Fatalf("new runtime app: %v", err)
	}
	defer func() { _ = app.Close() }()

	enqueueReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/enqueue", strings.NewReader(`{"id":"job-dead-retry-once","type":"ai.chat","prompt":"retry once","user_id":"user-retry-once","max_retries":0}`))
	enqueueReq.Header.Set("Content-Type", "application/json")
	enqueueResp := httptest.NewRecorder()
	app.ServeHTTP(enqueueResp, enqueueReq)
	if enqueueResp.Code != http.StatusOK {
		t.Fatalf("expected enqueue 200, got %d: %s", enqueueResp.Code, enqueueResp.Body.String())
	}

	timeoutReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/timeout?id=job-dead-retry-once", nil)
	timeoutResp := httptest.NewRecorder()
	app.ServeHTTP(timeoutResp, timeoutReq)
	if timeoutResp.Code != http.StatusOK {
		t.Fatalf("expected timeout 200, got %d: %s", timeoutResp.Code, timeoutResp.Body.String())
	}

	firstRetryReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/job-dead-retry-once/retry", nil)
	firstRetryResp := httptest.NewRecorder()
	app.ServeHTTP(firstRetryResp, firstRetryReq)
	if firstRetryResp.Code != http.StatusOK {
		t.Fatalf("expected first retry 200, got %d: %s", firstRetryResp.Code, firstRetryResp.Body.String())
	}

	secondRetryReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/job-dead-retry-once/retry", nil)
	secondRetryResp := httptest.NewRecorder()
	app.ServeHTTP(secondRetryResp, secondRetryReq)
	if secondRetryResp.Code != http.StatusBadRequest {
		t.Fatalf("expected duplicate retry to be rejected with 400, got %d: %s", secondRetryResp.Code, secondRetryResp.Body.String())
	}
	if !strings.Contains(secondRetryResp.Body.String(), "is not dead-lettered") {
		t.Fatalf("expected duplicate retry rejection to explain state, got %s", secondRetryResp.Body.String())
	}

	missingRetryReq := httptest.NewRequest(http.MethodPost, "/demo/jobs/job-missing-retry/retry", nil)
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
	console := readRuntimeConsoleResponse(t, restarted)
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

	consoleReq := httptest.NewRequest(http.MethodGet, "/api/console", nil)
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

	console := readRuntimeConsoleResponse(t, restarted)
	if console.Status.Adapters != 1 || !hasConsoleAdapter(console, "adapter-onebot-demo") {
		t.Fatalf("expected restarted console to expose persisted adapter instance, got %+v", console)
	}
	adapter := console.Adapters[0]
	if adapter.Adapter != "onebot" || adapter.Source != "onebot" || adapter.Status != "registered" || adapter.Health != "ready" || !adapter.Online || !adapter.StatePersisted {
		t.Fatalf("expected restarted console adapter facts, got %+v", adapter)
	}
	consoleReq := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	consoleResp := httptest.NewRecorder()
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
	if counts["adapter_instances"] != 2 {
		t.Fatalf("expected two persisted adapter instances before restart, got %+v", counts)
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
	if err := app.Close(); err != nil {
		t.Fatalf("close first app: %v", err)
	}

	restarted, err := newRuntimeApp(configPath)
	if err != nil {
		t.Fatalf("restart runtime app: %v", err)
	}
	defer func() { _ = restarted.Close() }()

	console := readRuntimeConsoleResponse(t, restarted)
	if console.Status.Adapters != 2 || !hasConsoleAdapter(console, "adapter-onebot-alpha") || !hasConsoleAdapter(console, "adapter-onebot-beta") {
		t.Fatalf("expected restarted console to expose both configured adapter instances, got %+v", console)
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
	consoleReq := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	consoleResp := httptest.NewRecorder()
	restarted.ServeHTTP(consoleResp, consoleReq)
	for _, expected := range []string{`"id": "adapter-onebot-alpha"`, `"id": "adapter-onebot-beta"`, `"source": "onebot-alpha"`, `"source": "onebot-beta"`, `"demo_path": "/demo/onebot/message"`, `"demo_path": "/demo/onebot/message-beta"`, `"self_id": 10001`, `"adapter_read_model": "sqlite-adapter-instances"`} {
		if !strings.Contains(consoleResp.Body.String(), expected) {
			t.Fatalf("expected restarted console payload to include %q, got %s", expected, consoleResp.Body.String())
		}
	}
	if got := len(restarted.runtime.RegisteredAdapters()); got != 2 {
		t.Fatalf("expected two registered adapters after restart, got %d", got)
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
		app.queue.DispatchReady(t.Context(), time.Now().UTC())
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

	consoleReq := httptest.NewRequest(http.MethodGet, "/api/console", nil)
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
	if !strings.Contains(restored.LastError, "runtime restarted") {
		t.Fatalf("expected restart recovery reason, got %+v", restored)
	}
	consoleReq := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	consoleResp := httptest.NewRecorder()
	restarted.ServeHTTP(consoleResp, consoleReq)
	for _, expected := range []string{
		"job-restart-running",
		`"recoverySummary": "retrying after runtime restart"`,
		`"recoveredJobs": 1`,
		`"recoveredRunning": 1`,
		`"retriedJobs": 1`,
		`"job_recovery_source": "runtime-startup-restore"`,
		`"job_recovery_recovered_jobs": 1`,
	} {
		if !strings.Contains(consoleResp.Body.String(), expected) {
			t.Fatalf("expected console recovery payload %q, got %s", expected, consoleResp.Body.String())
		}
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

func extractAIJobID(raw string) string {
	var payload struct {
		JobID string `json:"job_id"`
	}
	_ = json.Unmarshal([]byte(raw), &payload)
	return payload.JobID
}

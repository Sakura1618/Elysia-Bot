package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runtimecore "github.com/ohmyopencode/bot-platform/packages/runtime-core"
)

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
	if !strings.Contains(resp.Body.String(), `"plugins": 3`) {
		t.Fatalf("expected console payload to report three registered plugins, got %s", resp.Body.String())
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
	for _, expected := range []string{`"plugin_enabled_state_read_model": "runtime-registry+sqlite-plugin-enabled-overlay"`, `"plugin_enabled_state_persisted": true`, `"plugin_operator_scope": "already-registered plugins only"`, `"/demo/plugins/{plugin-id}/enable"`, `"/demo/plugins/{plugin-id}/disable"`, `"console_mode": "read+operator-plugin-enable-disable"`, `"enabled": true`, `"enabledStateSource": "runtime-default-enabled"`, `"enabledStatePersisted": false`} {
		if !strings.Contains(resp.Body.String(), expected) {
			t.Fatalf("expected console payload to include %s, got %s", expected, resp.Body.String())
		}
	}
	if !strings.Contains(resp.Body.String(), `"plugin_dispatch_source": "sqlite-plugin-status-snapshot+runtime-dispatch-results"`) {
		t.Fatalf("expected console payload to include plugin_dispatch_source=sqlite-plugin-status-snapshot+runtime-dispatch-results, got %s", resp.Body.String())
	}
	for _, expected := range []string{`"plugin_status_source": "runtime-registry+sqlite-plugin-status-snapshot+runtime-dispatch-results"`, `"plugin_status_evidence_model": "manifest-static-or-last-persisted-plugin-snapshot-with-live-overlay"`, `"plugin_dispatch_kind_visibility": "last-persisted-or-live-dispatch-kind"`, `"plugin_recovery_visibility": "last-dispatch-failed|last-dispatch-succeeded|recovered-after-failure|no-runtime-evidence"`, `"plugin_status_staleness": "static-registration|persisted-snapshot|persisted-snapshot+live-overlay|process-local-volatile"`, `"plugin_status_staleness_reason": "persisted plugin snapshots survive restart while current-process live overlay remains explicitly distinguished from the stored snapshot"`, `"plugin_runtime_state_live": true`, `"rbac_capability_surface": "read-only declaration of current authorization and adjacent dispatch-boundary facts"`, `"rbac_read_model_scope": "current runtime authorizer entrypoints, adjacent dispatch contract/filter boundaries, deny audit taxonomy, and known system gaps"`, `"rbac_current_state": "partial-runtime-local-read-model"`, `"rbac_system_model_state": "not-complete-global-rbac-authn-or-audit-system"`, `"rbac_current_authorization_paths_count": 5`, `"rbac_deny_audit_scope": "authorizer deny paths only"`, `"rbac_manifest_permission_gate_audited": false`, `"rbac_manifest_permission_gate_boundary": "independent dispatch contract check; not part of deny audit taxonomy"`, `"rbac_job_target_plugin_filter_boundary": "dispatch filter only; not an authorizer entrypoint or deny audit taxonomy item"`, `"rbac_console_read_permission": false`, `"rbac_console_read_actor_header": "X-Bot-Platform-Actor"`, `"secrets_provider": "env"`, `"secrets_runtime_owned_ref_prefix": "BOT_PLATFORM_"`, `"rollout_record_store": "in-memory-per-runtime-process"`} {
		if !strings.Contains(resp.Body.String(), expected) {
			t.Fatalf("expected console payload to include %s, got %s", expected, resp.Body.String())
		}
	}
	for _, expected := range []string{`"admin-command-runtime-authorizer"`, `"event-metadata-runtime-authorizer"`, `"job-metadata-runtime-authorizer"`, `"schedule-metadata-runtime-authorizer"`, `"dispatch-manifest-permission-gate"`, `"job-target-plugin-filter"`, `"console-read-authorizer"`, `"permission_denied"`, `"plugin_scope_denied"`, `"persistent-policy-store"`, `"policy-hot-reload"`, `"unified-authentication"`, `"unified-resource-model"`, `"independent-authorization-read-model"`, `"actor"`, `"permission"`, `"target_plugin_id"`, `"console read authorization is optional and only enforced when rbac.console_read_permission is configured"`, `"console read authorization currently reads actor only from the X-Bot-Platform-Actor header"`, `"manifest permission gate remains a separate dispatch contract check and does not emit deny audit entries"`, `"target_plugin_id remains a dispatch filter, not a global RBAC resource kind"`, `"Q8 currently remains a partial runtime-local closure rather than a complete global RBAC, authn, or audit system"`} {
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

	counts, err := app.state.Counts(t.Context())
	if err != nil {
		t.Fatalf("sqlite counts: %v", err)
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

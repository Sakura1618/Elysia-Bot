package runtimecore

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

type staticRecoverySource struct {
	snapshot RecoverySnapshot
}

func (s staticRecoverySource) LastRecoverySnapshot() RecoverySnapshot {
	return s.snapshot
}

func TestConsoleAPIExposesReadOnlySystemState(t *testing.T) {
	t.Parallel()

	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	queue := NewJobQueue()
	audits := NewInMemoryAuditLog()
	config := Config{Runtime: RuntimeConfig{Environment: "development", LogLevel: "debug", HTTPPort: 8080}}
	logs := []string{"runtime started", "plugin-echo ready"}

	if err := registerPluginWithTestSchema(runtime, pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-echo",
			Name:       "Echo Plugin",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			Publish: &pluginsdk.PluginPublish{
				SourceType:          pluginsdk.PublishSourceTypeGit,
				SourceURI:           "https://github.com/ohmyopencode/bot-platform/tree/main/plugins/plugin-echo",
				RuntimeVersionRange: ">=0.1.0 <1.0.0",
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/plugin-echo", Symbol: "Plugin"},
		},
		Handlers: pluginsdk.Handlers{Event: noopConsoleHandler{}},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	runtime.recordDispatch(DispatchResult{PluginID: "plugin-echo", Kind: "event", Success: false, Error: "subprocess host dispatch failed: timeout"})
	if err := queue.Enqueue(context.TODO(), NewJob("job-console", "ai.call", 1, 30*time.Second)); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	if err := audits.RecordAudit(pluginsdk.AuditEntry{
		Actor:         "admin-user",
		Permission:    "plugin:enable",
		Action:        "enable",
		Target:        "plugin-echo",
		Allowed:       true,
		TraceID:       "trace-console-audit",
		EventID:       "evt-console-audit",
		PluginID:      "plugin-echo",
		RunID:         "run-console-audit",
		CorrelationID: "corr-console-audit",
		ErrorCategory: "operator",
		ErrorCode:     "rollout_prepared",
		OccurredAt:    "2026-04-03T12:00:00Z",
	}); err != nil {
		t.Fatalf("record audit: %v", err)
	}

	api := NewConsoleAPI(runtime, queue, config, logs, audits)
	jobs, err := api.Jobs()
	if err != nil {
		t.Fatalf("console jobs: %v", err)
	}
	plugins := api.Plugins()
	if len(plugins) != 1 || len(jobs) != 1 {
		t.Fatalf("unexpected console lists: plugins=%d jobs=%d", len(api.Plugins()), len(jobs))
	}
	if plugins[0].LastDispatchSuccess == nil || *plugins[0].LastDispatchSuccess || plugins[0].LastDispatchError == "" {
		t.Fatalf("expected plugin runtime state projection, got %+v", plugins)
	}
	if plugins[0].StatusSource != "runtime-registry+runtime-dispatch-results" || plugins[0].StatusEvidence != "runtime-dispatch-result" || !plugins[0].RuntimeStateLive || plugins[0].StatusPersisted {
		t.Fatalf("expected plugin status source/evidence metadata, got %+v", plugins[0])
	}
	if plugins[0].StatusLevel != "error" || plugins[0].StatusRecovery != "last-dispatch-failed" || plugins[0].StatusStaleness != "process-local-volatile" {
		t.Fatalf("expected plugin recovery/staleness metadata, got %+v", plugins[0])
	}
	if plugins[0].CurrentFailureStreak != 1 || plugins[0].LastRecoveredAt != nil || plugins[0].LastRecoveryFailureCount != 0 {
		t.Fatalf("expected failure streak facts without synthetic recovery facts, got %+v", plugins[0])
	}
	if plugins[0].LastDispatchKind != "event" {
		t.Fatalf("expected plugin last dispatch kind, got %+v", plugins[0])
	}
	if plugins[0].LastDispatchAt == nil {
		t.Fatalf("expected plugin last dispatch timestamp, got %+v", plugins[0])
	}
	if plugins[0].Publish == nil || plugins[0].Publish.SourceType != pluginsdk.PublishSourceTypeGit || plugins[0].Publish.SourceURI != "https://github.com/ohmyopencode/bot-platform/tree/main/plugins/plugin-echo" || plugins[0].Publish.RuntimeVersionRange != ">=0.1.0 <1.0.0" {
		t.Fatalf("expected plugin publish metadata in console projection, got %+v", plugins[0].Publish)
	}
	if !strings.Contains(plugins[0].StatusSummary, "last runtime event dispatch failed via runtime-registry+runtime-dispatch-results") || !strings.Contains(plugins[0].StatusSummary, "recovery=last-dispatch-failed") || !strings.Contains(plugins[0].StatusSummary, "evidence=process-local-volatile") || !strings.Contains(plugins[0].StatusSummary, "current_failure_streak=1") {
		t.Fatalf("expected plugin status summary to describe runtime evidence, got %q", plugins[0].StatusSummary)
	}
	if len(api.Audits()) != 1 || api.Audits()[0].Actor != "admin-user" || api.Audits()[0].TraceID != "trace-console-audit" || api.Audits()[0].EventID != "evt-console-audit" || api.Audits()[0].PluginID != "plugin-echo" || api.Audits()[0].RunID != "run-console-audit" || api.Audits()[0].CorrelationID != "corr-console-audit" || api.Audits()[0].ErrorCategory != "operator" || api.Audits()[0].ErrorCode != "rollout_prepared" {
		t.Fatalf("expected console audits to expose audit trail, got %+v", api.Audits())
	}
	if api.Audits()[0].SessionID != "" {
		t.Fatalf("expected explicit empty session_id to remain empty for legacy audit, got %+v", api.Audits()[0])
	}
	if len(api.Logs("plugin-echo")) != 1 {
		t.Fatalf("expected log query to filter logs, got %+v", api.Logs("plugin-echo"))
	}
	filteredJobs, err := api.FilteredJobs("ai.call")
	if err != nil {
		t.Fatalf("filtered jobs: %v", err)
	}
	if len(filteredJobs) != 1 {
		t.Fatalf("expected job query to filter jobs, got %+v", filteredJobs)
	}
	if jobs[0].DispatchRBAC != nil {
		t.Fatalf("expected plain queued job without dispatch metadata to omit RBAC declaration, got %+v", jobs[0].DispatchRBAC)
	}
	status, err := api.Status()
	if err != nil {
		t.Fatalf("console status: %v", err)
	}
	if status.Plugins != 1 || status.Jobs != 1 {
		t.Fatalf("unexpected runtime status %+v", status)
	}

	rendered, err := api.RenderJSON()
	if err != nil {
		t.Fatalf("render json: %v", err)
	}
	for _, expected := range []string{"plugin-echo", "job-console", "runtime started", "development", "admin-user", "enable", `"trace_id": "trace-console-audit"`, `"event_id": "evt-console-audit"`, `"plugin_id": "plugin-echo"`, `"run_id": "run-console-audit"`, `"correlation_id": "corr-console-audit"`, `"error_category": "operator"`, `"error_code": "rollout_prepared"`, `"publish": {`, `"sourceType": "git"`, `"sourceUri": "https://github.com/ohmyopencode/bot-platform/tree/main/plugins/plugin-echo"`, `"runtimeVersionRange": "\u003e=0.1.0 \u003c1.0.0"`, `"statusSource": "runtime-registry+runtime-dispatch-results"`, `"statusEvidence": "runtime-dispatch-result"`, `"runtimeStateLive": true`, `"statusPersisted": false`, `"statusLevel": "error"`, `"statusRecovery": "last-dispatch-failed"`, `"statusStaleness": "process-local-volatile"`, `"lastDispatchKind": "event"`, `"lastDispatchSuccess": false`, `"lastDispatchError": "subprocess host dispatch failed: timeout"`, `"lastDispatchAt":`, `"currentFailureStreak": 1`} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("expected console output to contain %q, got %s", expected, rendered)
		}
	}
}

func TestConsoleAPIReadsPersistedSQLiteAuditsWithObservabilityFields(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()
	entry := pluginsdk.AuditEntry{
		Actor:         "job-operator",
		Permission:    "job:retry",
		Action:        "retry",
		Target:        "job-console-audit-1",
		Allowed:       true,
		Reason:        "job_dead_letter_retried",
		TraceID:       "trace-console-store",
		EventID:       "evt-console-store",
		PluginID:      "plugin-ai-chat",
		RunID:         "run-console-store",
		SessionID:     "session-operator-bearer-job-operator",
		CorrelationID: "corr-console-store",
		ErrorCategory: "operator",
		ErrorCode:     "job_dead_letter_retried",
		OccurredAt:    "2026-04-21T08:10:00Z",
	}
	if err := store.SaveAudit(context.Background(), entry); err != nil {
		t.Fatalf("save audit: %v", err)
	}

	api := NewConsoleAPI(nil, nil, Config{}, nil, store)
	audits := api.Audits()
	if len(audits) != 1 || audits[0] != entry {
		t.Fatalf("expected persisted console audit round-trip, got %+v", audits)
	}
	raw, err := api.RenderJSON()
	if err != nil {
		t.Fatalf("render json: %v", err)
	}
	for _, expected := range []string{`"trace_id": "trace-console-store"`, `"event_id": "evt-console-store"`, `"plugin_id": "plugin-ai-chat"`, `"run_id": "run-console-store"`, `"session_id": "session-operator-bearer-job-operator"`, `"correlation_id": "corr-console-store"`, `"error_category": "operator"`, `"error_code": "job_dead_letter_retried"`} {
		if !strings.Contains(raw, expected) {
			t.Fatalf("expected persisted console audit JSON to include %q, got %s", expected, raw)
		}
	}
}

func TestConsoleJobRBACDeclarationReflectsMetadataPermissionAndTargetFilter(t *testing.T) {
	t.Parallel()

	job := NewJob("job-rbac", "ai.call", 1, 30*time.Second)
	job.Payload = map[string]any{
		"dispatch": map[string]any{
			"actor":            "console-operator",
			"permission":       "plugin:invoke",
			"target_plugin_id": "plugin-ai-chat",
		},
		"reply_handle": map[string]any{
			"capability": "onebot.reply",
			"target_id":  "group:42",
		},
	}

	consoleJob := toConsoleJob(job)
	if consoleJob.DispatchRBAC == nil {
		t.Fatalf("expected queued job RBAC declaration, got nil")
	}
	if consoleJob.DispatchRBAC.Actor != "console-operator" || consoleJob.DispatchRBAC.Permission != "plugin:invoke" || consoleJob.DispatchRBAC.TargetPluginID != "plugin-ai-chat" {
		t.Fatalf("expected actor/permission/target_plugin_id in RBAC declaration, got %+v", consoleJob.DispatchRBAC)
	}
	if !consoleJob.DispatchRBAC.RuntimeAuthorizerEnabled || consoleJob.DispatchRBAC.RuntimeAuthorizerScope != "metadata.permission -> plugin target via shared authorizer" {
		t.Fatalf("expected runtime authorizer declaration for metadata.permission, got %+v", consoleJob.DispatchRBAC)
	}
	if !consoleJob.DispatchRBAC.ManifestGateEnabled || consoleJob.DispatchRBAC.ManifestGateScope != "job handler manifest Permissions must include declared permission" {
		t.Fatalf("expected manifest gate declaration for metadata.permission, got %+v", consoleJob.DispatchRBAC)
	}
	if !consoleJob.DispatchRBAC.JobTargetFilterEnabled {
		t.Fatalf("expected target_plugin_id to be declared as job target filter, got %+v", consoleJob.DispatchRBAC)
	}
	if len(consoleJob.DispatchRBAC.Facts) != 3 {
		t.Fatalf("expected three RBAC facts, got %+v", consoleJob.DispatchRBAC.Facts)
	}
	for _, expected := range []string{
		"runtime authorizer applies only when dispatch metadata.permission is set",
		"manifest permission gate applies only when a required permission is declared",
		"target_plugin_id narrows job dispatch to one plugin and is not a new RBAC target kind",
	} {
		if !containsString(consoleJob.DispatchRBAC.Facts, expected) {
			t.Fatalf("expected RBAC facts to contain %q, got %+v", expected, consoleJob.DispatchRBAC.Facts)
		}
	}
	if !strings.Contains(consoleJob.DispatchRBAC.Summary, "permission=plugin:invoke enables runtime authorizer + manifest permission gate") || !strings.Contains(consoleJob.DispatchRBAC.Summary, "target_plugin_id=plugin-ai-chat limits dispatch routing only") {
		t.Fatalf("expected RBAC summary to explain current boundaries, got %q", consoleJob.DispatchRBAC.Summary)
	}
}

func TestConsoleJobRBACDeclarationMarksActorOnlyMetadataAsNonEnforcing(t *testing.T) {
	t.Parallel()

	job := NewJob("job-rbac-actor-only", "ai.call", 1, 30*time.Second)
	job.Payload = map[string]any{
		"dispatch": map[string]any{
			"actor": "console-operator",
		},
		"reply_handle": map[string]any{
			"capability": "onebot.reply",
			"target_id":  "group:42",
		},
	}

	consoleJob := toConsoleJob(job)
	if consoleJob.DispatchRBAC == nil {
		t.Fatalf("expected actor-only job to still declare visible authorization metadata, got nil")
	}
	if consoleJob.DispatchRBAC.RuntimeAuthorizerEnabled || consoleJob.DispatchRBAC.ManifestGateEnabled || consoleJob.DispatchRBAC.JobTargetFilterEnabled {
		t.Fatalf("expected actor-only metadata not to claim enforcement gates, got %+v", consoleJob.DispatchRBAC)
	}
	if len(consoleJob.DispatchRBAC.Facts) != 1 || consoleJob.DispatchRBAC.Facts[0] != "actor is visible in dispatch metadata but does not trigger runtime authorization without permission" {
		t.Fatalf("expected actor-only fact, got %+v", consoleJob.DispatchRBAC.Facts)
	}
	if !strings.Contains(consoleJob.DispatchRBAC.Summary, "has no permission; runtime authorizer and manifest permission gate are inactive") {
		t.Fatalf("expected actor-only summary to explain inactive gates, got %q", consoleJob.DispatchRBAC.Summary)
	}
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func TestConsoleAPIPluginStatusFallsBackToManifestOnlyEvidence(t *testing.T) {
	t.Parallel()

	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	if err := registerPluginWithTestSchema(runtime, pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-admin",
			Name:       "Admin Plugin",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			Entry:      pluginsdk.PluginEntry{Binary: "plugins/plugin-admin/bin/plugin-admin"},
		},
		Handlers: pluginsdk.Handlers{Event: noopConsoleHandler{}},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	plugins := NewConsoleAPI(runtime, nil, Config{}, nil, nil).Plugins()
	if len(plugins) != 1 {
		t.Fatalf("expected one plugin, got %d", len(plugins))
	}
	plugin := plugins[0]
	if plugin.StatusSource != "runtime-registry" || plugin.StatusEvidence != "manifest-only" || plugin.RuntimeStateLive || plugin.StatusPersisted {
		t.Fatalf("expected manifest-only plugin status evidence, got %+v", plugin)
	}
	if plugin.StatusLevel != "registered" || plugin.StatusRecovery != "no-runtime-evidence" || plugin.StatusStaleness != "static-registration" {
		t.Fatalf("expected manifest-only plugin recovery/staleness, got %+v", plugin)
	}
	if !strings.Contains(plugin.StatusSummary, "manifest registered via runtime-registry; no runtime dispatch evidence yet; status is static registration only") {
		t.Fatalf("expected manifest-only status summary, got %q", plugin.StatusSummary)
	}
}

func TestConsoleAPIExposesPersistedPluginEnabledOverlay(t *testing.T) {
	t.Parallel()

	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()
	manifest := pluginsdk.PluginManifest{
		ID:         "plugin-echo",
		Name:       "Echo Plugin",
		Version:    "0.1.0",
		APIVersion: "v0",
		Mode:       pluginsdk.ModeSubprocess,
		Entry:      pluginsdk.PluginEntry{Module: "plugins/plugin-echo", Symbol: "Plugin"},
	}
	if err := registerPluginWithTestSchema(runtime, pluginsdk.Plugin{Manifest: manifest, Handlers: pluginsdk.Handlers{Event: noopConsoleHandler{}}}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	if err := store.SavePluginManifest(context.Background(), manifest); err != nil {
		t.Fatalf("save plugin manifest: %v", err)
	}
	if err := store.SavePluginEnabledState(context.Background(), manifest.ID, false); err != nil {
		t.Fatalf("save plugin enabled state: %v", err)
	}

	api := NewConsoleAPI(runtime, nil, Config{}, nil, nil)
	api.SetPluginEnabledStateReader(NewSQLiteConsolePluginEnabledStateReader(store))
	plugins := api.Plugins()
	if len(plugins) != 1 {
		t.Fatalf("expected one plugin, got %+v", plugins)
	}
	plugin := plugins[0]
	if plugin.Enabled || !plugin.EnabledStatePersisted || plugin.EnabledStateSource != "sqlite-plugin-enabled-overlay" {
		t.Fatalf("expected persisted disabled overlay in console plugin payload, got %+v", plugin)
	}
	if plugin.EnabledStateUpdatedAt == nil {
		t.Fatalf("expected enabled state timestamp to be present, got %+v", plugin)
	}
	rendered, err := api.RenderJSON()
	if err != nil {
		t.Fatalf("render json: %v", err)
	}
	for _, expected := range []string{`"enabled": false`, `"enabledStateSource": "sqlite-plugin-enabled-overlay"`, `"enabledStatePersisted": true`, `"enabledStateUpdatedAt":`} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("expected rendered console payload to contain %q, got %s", expected, rendered)
		}
	}
}

func TestConsoleAPIExposesPersistedWorkflowInstances(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()
	workflow := NewWorkflow(
		`workflow-user-1`,
		WorkflowStep{Kind: WorkflowStepKindPersist, Name: `greeting`, Value: `hello`},
		WorkflowStep{Kind: WorkflowStepKindWaitEvent, Name: `wait-confirm`, Value: `message.received`},
	)
	workflow.State[`greeting`] = `hello`
	workflow.CurrentIndex = 1
	workflow.WaitingFor = `message.received`
	if err := store.SaveWorkflowInstance(context.Background(), WorkflowInstanceState{
		WorkflowID:    `workflow-user-1`,
		PluginID:      `plugin-workflow-demo`,
		TraceID:       `trace-workflow-console`,
		EventID:       `evt-workflow-console-origin`,
		RunID:         `run-workflow-console`,
		CorrelationID: `corr-workflow-console`,
		Status:        WorkflowRuntimeStatusWaitingEvent,
		Workflow:      workflow,
		LastEventID:   `evt-1`,
		LastEventType: `message.received`,
		CreatedAt:     time.Date(2026, 4, 19, 9, 0, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2026, 4, 19, 9, 1, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf(`save workflow instance: %v`, err)
	}

	api := NewConsoleAPI(NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{}), nil, Config{}, nil, nil)
	api.SetWorkflowReader(NewSQLiteConsoleWorkflowReader(store))
	workflows, err := api.Workflows()
	if err != nil {
		t.Fatalf(`console workflows: %v`, err)
	}
	if len(workflows) != 1 {
		t.Fatalf(`expected one workflow in console payload, got %+v`, workflows)
	}
	workflowItem := workflows[0]
	if workflowItem.ID != `workflow-user-1` || workflowItem.PluginID != `plugin-workflow-demo` || workflowItem.Status != string(WorkflowRuntimeStatusWaitingEvent) {
		t.Fatalf(`expected persisted workflow identity/status in console payload, got %+v`, workflowItem)
	}
	if workflowItem.TraceID != `trace-workflow-console` || workflowItem.EventID != `evt-workflow-console-origin` || workflowItem.RunID != `run-workflow-console` || workflowItem.CorrelationID != `corr-workflow-console` {
		t.Fatalf(`expected workflow observability ids in console payload, got %+v`, workflowItem)
	}
	if !workflowItem.StatePersisted || workflowItem.StatusSource != `sqlite-workflow-instances` || workflowItem.RuntimeOwner != `runtime-core` {
		t.Fatalf(`expected workflow console provenance metadata, got %+v`, workflowItem)
	}
	if workflowItem.WaitingFor != `message.received` || workflowItem.State[`greeting`] != `hello` {
		t.Fatalf(`expected persisted workflow read-side fields, got %+v`, workflowItem)
	}
	rendered, err := api.RenderJSON()
	if err != nil {
		t.Fatalf(`render console json: %v`, err)
	}
	for _, expected := range []string{`"workflows": [`, `"id": "workflow-user-1"`, `"pluginId": "plugin-workflow-demo"`, `"traceId": "trace-workflow-console"`, `"eventId": "evt-workflow-console-origin"`, `"runId": "run-workflow-console"`, `"correlationId": "corr-workflow-console"`, `"status": "waiting_event"`, `"statusSource": "sqlite-workflow-instances"`, `"runtimeOwner": "runtime-core"`} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf(`expected rendered console payload to contain %q, got %s`, expected, rendered)
		}
	}
}

func TestConsoleAPIExposesPersistedPluginConfigStateForPluginEcho(t *testing.T) {
	t.Parallel()

	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()
	manifest := pluginsdk.PluginManifest{
		ID:         "plugin-echo",
		Name:       "Echo Plugin",
		Version:    "0.1.0",
		APIVersion: "v0",
		Mode:       pluginsdk.ModeSubprocess,
		Entry:      pluginsdk.PluginEntry{Module: "plugins/plugin-echo", Symbol: "Plugin"},
	}
	if err := registerPluginWithTestSchema(runtime, pluginsdk.Plugin{Manifest: manifest, Handlers: pluginsdk.Handlers{Event: noopConsoleHandler{}}}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	if err := store.SavePluginConfig(context.Background(), manifest.ID, json.RawMessage(`{"prefix":"persisted: "}`)); err != nil {
		t.Fatalf("save plugin config: %v", err)
	}

	api := NewConsoleAPI(runtime, nil, Config{}, nil, nil)
	api.SetPluginConfigBindings(map[string]ConsolePluginConfigBinding{
		manifest.ID: {StateKind: "plugin-owned-persisted-input"},
	})
	api.SetPluginConfigReader(NewSQLiteConsolePluginConfigReader(store))
	plugins := api.Plugins()
	if len(plugins) != 1 {
		t.Fatalf("expected one plugin, got %+v", plugins)
	}
	plugin := plugins[0]
	if plugin.ConfigStateKind != "plugin-owned-persisted-input" || plugin.ConfigSource != "sqlite-plugin-config" || !plugin.ConfigPersisted {
		t.Fatalf("expected persisted plugin config state metadata in console payload, got %+v", plugin)
	}
	if plugin.Config["prefix"] != "persisted: " {
		t.Fatalf("expected projected persisted plugin config value in console payload, got %+v", plugin.Config)
	}
	if plugin.ConfigUpdatedAt == nil {
		t.Fatalf("expected config updated timestamp to be present, got %+v", plugin)
	}
	rendered, err := api.RenderJSON()
	if err != nil {
		t.Fatalf("render json: %v", err)
	}
	for _, expected := range []string{`"configStateKind": "plugin-owned-persisted-input"`, `"configSource": "sqlite-plugin-config"`, `"configPersisted": true`, `"config": {`, `"prefix": "persisted: "`, `"configUpdatedAt":`} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("expected rendered console payload to contain %q, got %s", expected, rendered)
		}
	}
}

func TestConsoleAPIExposesBoundPluginConfigCapabilityWithoutPersistedState(t *testing.T) {
	t.Parallel()

	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	manifest := pluginsdk.PluginManifest{
		ID:         "plugin-echo",
		Name:       "Echo Plugin",
		Version:    "0.1.0",
		APIVersion: "v0",
		Mode:       pluginsdk.ModeSubprocess,
		Entry:      pluginsdk.PluginEntry{Module: "plugins/plugin-echo", Symbol: "Plugin"},
	}
	if err := registerPluginWithTestSchema(runtime, pluginsdk.Plugin{Manifest: manifest, Handlers: pluginsdk.Handlers{Event: noopConsoleHandler{}}}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	api := NewConsoleAPI(runtime, nil, Config{}, nil, nil)
	api.SetPluginConfigBindings(map[string]ConsolePluginConfigBinding{
		manifest.ID: {StateKind: "plugin-owned-persisted-input"},
	})
	plugins := api.Plugins()
	if len(plugins) != 1 {
		t.Fatalf("expected one plugin, got %+v", plugins)
	}
	plugin := plugins[0]
	if plugin.ConfigStateKind != "plugin-owned-persisted-input" {
		t.Fatalf("expected bound plugin config capability, got %+v", plugin)
	}
	if plugin.ConfigPersisted || plugin.ConfigSource != "" || plugin.ConfigUpdatedAt != nil {
		t.Fatalf("expected bound plugin without persisted row to omit persisted metadata, got %+v", plugin)
	}
	if plugin.EnabledStatePersisted {
		t.Fatalf("expected config capability binding not to affect enabled overlay metadata, got %+v", plugin)
	}
}

func TestConsoleAPIUnboundPluginConfigRowDoesNotExposeConfigCapability(t *testing.T) {
	t.Parallel()

	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()
	manifest := pluginsdk.PluginManifest{
		ID:         "plugin-admin",
		Name:       "Admin Plugin",
		Version:    "0.1.0",
		APIVersion: "v0",
		Mode:       pluginsdk.ModeSubprocess,
		Entry:      pluginsdk.PluginEntry{Module: "plugins/plugin-admin", Symbol: "Plugin"},
	}
	if err := registerPluginWithTestSchema(runtime, pluginsdk.Plugin{Manifest: manifest, Handlers: pluginsdk.Handlers{Event: noopConsoleHandler{}}}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	if err := store.SavePluginConfig(context.Background(), manifest.ID, json.RawMessage(`{"ignored":true}`)); err != nil {
		t.Fatalf("save plugin config: %v", err)
	}

	api := NewConsoleAPI(runtime, nil, Config{}, nil, nil)
	api.SetPluginConfigBindings(map[string]ConsolePluginConfigBinding{
		"plugin-echo": {StateKind: "plugin-owned-persisted-input"},
	})
	api.SetPluginConfigReader(NewSQLiteConsolePluginConfigReader(store))
	plugins := api.Plugins()
	if len(plugins) != 1 {
		t.Fatalf("expected one plugin, got %+v", plugins)
	}
	plugin := plugins[0]
	if plugin.ConfigStateKind != "" || plugin.ConfigPersisted || plugin.ConfigSource != "" || plugin.ConfigUpdatedAt != nil {
		t.Fatalf("expected unbound plugin config row to stay hidden from capability surface, got %+v", plugin)
	}
	if plugin.StatusSource != "runtime-registry" {
		t.Fatalf("expected unbound plugin config row not to affect unrelated plugin status metadata, got %+v", plugin)
	}
}

func TestConsoleAPIPluginStatusClassifiesInstanceConfigRejectBeforeLaunch(t *testing.T) {
	t.Parallel()

	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	if err := registerPluginWithTestSchema(runtime, pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-reject",
			Name:       "Instance Config Reject Plugin",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			Entry:      pluginsdk.PluginEntry{Module: "plugins/plugin-instance-config-reject"},
		},
		Handlers: pluginsdk.Handlers{Event: noopConsoleHandler{}},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	runtime.recordDispatch(DispatchResult{
		PluginID: "plugin-instance-config-reject",
		Kind:     "event",
		Success:  false,
		Error:    `plugin host dispatch "plugin-instance-config-reject": plugin "plugin-instance-config-reject" instance config required property "prefix" must be provided`,
	})

	plugin := NewConsoleAPI(runtime, nil, Config{}, nil, nil).Plugins()[0]
	if plugin.StatusEvidence != "runtime-dispatch-result:instance-config-reject" {
		t.Fatalf("expected instance-config rejection evidence, got %+v", plugin)
	}
	if plugin.StatusLevel != "error" || plugin.StatusRecovery != "last-dispatch-failed" || plugin.StatusStaleness != "process-local-volatile" {
		t.Fatalf("expected rejected plugin failure metadata, got %+v", plugin)
	}
	if !strings.Contains(plugin.StatusSummary, "rejected before subprocess launch") || !strings.Contains(plugin.StatusSummary, "stage=instance-config") {
		t.Fatalf("expected status summary to classify prelaunch instance-config rejection, got %q", plugin.StatusSummary)
	}
}

func TestConsoleAPIPluginStatusMarksRecoveredAfterFailure(t *testing.T) {
	t.Parallel()

	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	if err := registerPluginWithTestSchema(runtime, pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-recover",
			Name:       "Recover Plugin",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			Entry:      pluginsdk.PluginEntry{Module: "plugins/plugin-recover"},
		},
		Handlers: pluginsdk.Handlers{Event: noopConsoleHandler{}},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	failureAt := time.Date(2026, 4, 15, 7, 0, 0, 0, time.UTC)
	recoveredAt := failureAt.Add(3 * time.Minute)
	runtime.recordDispatch(DispatchResult{PluginID: "plugin-recover", Kind: "event", Success: false, Error: "first failure", At: failureAt})
	runtime.recordDispatch(DispatchResult{PluginID: "plugin-recover", Kind: "event", Success: true, At: recoveredAt})

	plugin := NewConsoleAPI(runtime, nil, Config{}, nil, nil).Plugins()[0]
	if plugin.StatusLevel != "ok" || plugin.StatusRecovery != "recovered-after-failure" || plugin.StatusStaleness != "process-local-volatile" {
		t.Fatalf("expected recovered-after-failure plugin metadata, got %+v", plugin)
	}
	if plugin.LastRecoveredAt == nil || !plugin.LastRecoveredAt.Equal(recoveredAt) || plugin.LastRecoveryFailureCount != 1 || plugin.CurrentFailureStreak != 0 {
		t.Fatalf("expected recovered plugin recovery facts, got %+v", plugin)
	}
	if !strings.Contains(plugin.StatusSummary, "recovery=recovered-after-failure") || !strings.Contains(plugin.StatusSummary, "last_recovered_at=2026-04-15T07:03:00Z") || !strings.Contains(plugin.StatusSummary, "last_recovery_failure_count=1") {
		t.Fatalf("expected status summary to mention recovery facts, got %q", plugin.StatusSummary)
	}
}

func TestConsoleAPIPluginStatusDoesNotInventRecoveryFactsForAlwaysSuccessfulHistory(t *testing.T) {
	t.Parallel()

	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	if err := registerPluginWithTestSchema(runtime, pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-stable",
			Name:       "Stable Plugin",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			Entry:      pluginsdk.PluginEntry{Module: "plugins/plugin-stable"},
		},
		Handlers: pluginsdk.Handlers{Event: noopConsoleHandler{}},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	runtime.recordDispatch(DispatchResult{PluginID: "plugin-stable", Kind: "event", Success: true, At: time.Date(2026, 4, 15, 7, 30, 0, 0, time.UTC)})

	plugin := NewConsoleAPI(runtime, nil, Config{}, nil, nil).Plugins()[0]
	if plugin.StatusRecovery != "last-dispatch-succeeded" || plugin.LastRecoveredAt != nil || plugin.LastRecoveryFailureCount != 0 || plugin.CurrentFailureStreak != 0 {
		t.Fatalf("expected stable plugin not to invent recovery facts, got %+v", plugin)
	}
	if strings.Contains(plugin.StatusSummary, "last_recovered_at=") || strings.Contains(plugin.StatusSummary, "last_recovery_failure_count=") || strings.Contains(plugin.StatusSummary, "current_failure_streak=") {
		t.Fatalf("expected stable plugin summary to omit synthetic recovery facts, got %q", plugin.StatusSummary)
	}
}

func TestConsoleAPIPluginStatusUsesPersistedSnapshotWhenNoLiveDispatchExists(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	if err := registerPluginWithTestSchema(runtime, pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-persisted",
			Name:       "Persisted Plugin",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			Entry:      pluginsdk.PluginEntry{Module: "plugins/plugin-persisted"},
		},
		Handlers: pluginsdk.Handlers{Event: noopConsoleHandler{}},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	failureAt := time.Date(2026, 4, 15, 9, 0, 0, 0, time.UTC)
	recoveredAt := failureAt.Add(2 * time.Minute)
	if err := store.SavePluginStatusSnapshot(context.Background(), DispatchResult{PluginID: "plugin-persisted", Kind: "event", Success: false, Error: "timeout", At: failureAt}); err != nil {
		t.Fatalf("save failed plugin snapshot: %v", err)
	}
	if err := store.SavePluginStatusSnapshot(context.Background(), DispatchResult{PluginID: "plugin-persisted", Kind: "event", Success: true, At: recoveredAt}); err != nil {
		t.Fatalf("save recovered plugin snapshot: %v", err)
	}

	api := NewConsoleAPI(runtime, nil, Config{}, nil, nil)
	api.SetPluginSnapshotReader(NewSQLiteConsolePluginSnapshotReader(store))

	plugin := api.Plugins()[0]
	if plugin.StatusSource != "runtime-registry+sqlite-plugin-status-snapshot" || plugin.StatusEvidence != "persisted-plugin-status-snapshot" || plugin.RuntimeStateLive || !plugin.StatusPersisted {
		t.Fatalf("expected persisted-only plugin status evidence, got %+v", plugin)
	}
	if plugin.StatusRecovery != "recovered-after-failure" || plugin.StatusStaleness != "persisted-snapshot" {
		t.Fatalf("expected persisted plugin recovery/staleness metadata, got %+v", plugin)
	}
	if plugin.LastDispatchAt == nil || !plugin.LastDispatchAt.Equal(recoveredAt) || plugin.LastRecoveredAt == nil || !plugin.LastRecoveredAt.Equal(recoveredAt) || plugin.LastRecoveryFailureCount != 1 || plugin.CurrentFailureStreak != 0 {
		t.Fatalf("expected persisted plugin recovery facts, got %+v", plugin)
	}
}

func TestConsoleAPIExposesPersistedAdapterInstances(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()
	if err := store.SaveAdapterInstance(context.Background(), AdapterInstanceState{
		InstanceID: "adapter-onebot-demo",
		Adapter:    "onebot",
		Source:     "onebot",
		RawConfig:  json.RawMessage(`{"mode":"demo-ingress","demo_path":"/demo/onebot/message","platform":"onebot/v11"}`),
		Status:     "registered",
		Health:     "ready",
		Online:     true,
	}); err != nil {
		t.Fatalf("save adapter instance: %v", err)
	}

	api := NewConsoleAPI(nil, nil, Config{}, nil, nil)
	api.SetAdapterInstanceReader(NewSQLiteConsoleAdapterInstanceReader(store))
	instances, err := api.AdapterInstances()
	if err != nil {
		t.Fatalf("adapter instances: %v", err)
	}
	if len(instances) != 1 {
		t.Fatalf("expected one adapter instance, got %+v", instances)
	}
	instance := instances[0]
	if instance.ID != "adapter-onebot-demo" || instance.Adapter != "onebot" || instance.Source != "onebot" {
		t.Fatalf("expected adapter instance identity fields, got %+v", instance)
	}
	if instance.StatusSource != "sqlite-adapter-instances" || instance.ConfigSource != "sqlite-adapter-instances" || !instance.StatePersisted {
		t.Fatalf("expected persisted adapter read model sources, got %+v", instance)
	}
	if instance.Status != "registered" || instance.Health != "ready" || !instance.Online {
		t.Fatalf("expected adapter status facts, got %+v", instance)
	}
	if instance.Config["platform"] != "onebot/v11" || instance.Config["demo_path"] != "/demo/onebot/message" {
		t.Fatalf("expected adapter config payload, got %+v", instance.Config)
	}
	if !strings.Contains(instance.Summary, "persisted state survives restart") {
		t.Fatalf("expected adapter summary to mention restart persistence, got %q", instance.Summary)
	}
	rendered, err := api.RenderJSON()
	if err != nil {
		t.Fatalf("render json: %v", err)
	}
	for _, expected := range []string{`"adapters": [`, `"id": "adapter-onebot-demo"`, `"adapter": "onebot"`, `"status": "registered"`, `"health": "ready"`, `"online": true`, `"statusSource": "sqlite-adapter-instances"`, `"configSource": "sqlite-adapter-instances"`, `"statePersisted": true`, `"demo_path": "/demo/onebot/message"`, `"platform": "onebot/v11"`} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("expected rendered console payload to contain %q, got %s", expected, rendered)
		}
	}
}

func TestConsoleAPIPluginStatusLiveOverlayWinsOverPersistedSnapshot(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	if err := registerPluginWithTestSchema(runtime, pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-overlay",
			Name:       "Overlay Plugin",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			Entry:      pluginsdk.PluginEntry{Module: "plugins/plugin-overlay"},
		},
		Handlers: pluginsdk.Handlers{Event: noopConsoleHandler{}},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	persistedFailureAt := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)
	liveRecoveredAt := persistedFailureAt.Add(5 * time.Minute)
	if err := store.SavePluginStatusSnapshot(context.Background(), DispatchResult{PluginID: "plugin-overlay", Kind: "event", Success: false, Error: "persisted failure", At: persistedFailureAt}); err != nil {
		t.Fatalf("save persisted plugin snapshot: %v", err)
	}
	runtime.recordDispatch(DispatchResult{PluginID: "plugin-overlay", Kind: "event", Success: true, At: liveRecoveredAt})

	api := NewConsoleAPI(runtime, nil, Config{}, nil, nil)
	api.SetPluginSnapshotReader(NewSQLiteConsolePluginSnapshotReader(store))

	plugin := api.Plugins()[0]
	if plugin.StatusSource != "runtime-registry+sqlite-plugin-status-snapshot+runtime-dispatch-results" || plugin.StatusEvidence != "live-dispatch-overlay+persisted-plugin-status-snapshot" || !plugin.RuntimeStateLive || !plugin.StatusPersisted {
		t.Fatalf("expected live overlay to win over persisted snapshot, got %+v", plugin)
	}
	if plugin.StatusRecovery != "recovered-after-failure" || plugin.StatusStaleness != "persisted-snapshot+live-overlay" {
		t.Fatalf("expected overlay recovery/staleness metadata, got %+v", plugin)
	}
	if plugin.LastDispatchAt == nil || !plugin.LastDispatchAt.Equal(liveRecoveredAt) || plugin.LastRecoveredAt == nil || !plugin.LastRecoveredAt.Equal(liveRecoveredAt) || plugin.LastRecoveryFailureCount != 1 || plugin.CurrentFailureStreak != 0 {
		t.Fatalf("expected live overlay recovery facts, got %+v", plugin)
	}
}

func TestConsoleAPIRecoveryIncludesScheduleRestoreFields(t *testing.T) {
	t.Parallel()

	recoveredAt := time.Date(2026, 4, 10, 7, 0, 0, 0, time.UTC)
	api := NewConsoleAPI(nil, nil, Config{}, nil, nil)
	api.SetRecoverySource(staticRecoverySource{snapshot: RecoverySnapshot{
		RecoveredAt:        recoveredAt,
		TotalJobs:          2,
		RecoveredJobs:      1,
		RecoveredRunning:   1,
		RetriedJobs:        1,
		DeadJobs:           0,
		StatusCounts:       map[JobStatus]int{JobStatusRetrying: 1, JobStatusPending: 1},
		TotalSchedules:     3,
		RecoveredSchedules: 1,
		InvalidSchedules:   1,
		ScheduleKinds:      map[ScheduleKind]int{ScheduleKindDelay: 2, ScheduleKindCron: 1},
	}})

	recovery := api.Recovery()
	if recovery.TotalSchedules != 3 || recovery.RecoveredSchedules != 1 || recovery.InvalidSchedules != 1 {
		t.Fatalf("expected schedule recovery fields in console recovery, got %+v", recovery)
	}
	if recovery.ScheduleKinds["delay"] != 2 || recovery.ScheduleKinds["cron"] != 1 {
		t.Fatalf("expected schedule kinds in console recovery, got %+v", recovery.ScheduleKinds)
	}
	if !strings.Contains(recovery.Summary, "jobs_restored=2") || !strings.Contains(recovery.Summary, "schedules_restored=3") || !strings.Contains(recovery.Summary, "schedules_recovered=1") || !strings.Contains(recovery.Summary, "schedules_invalid=1") {
		t.Fatalf("expected recovery summary to include schedule restore details, got %q", recovery.Summary)
	}
}

func TestConsoleAPIObservabilityExposesMinimalAlertabilityBaseline(t *testing.T) {
	t.Parallel()

	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	if err := registerPluginWithTestSchema(runtime, pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-alertability",
			Name:       "Alertability Plugin",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			Entry:      pluginsdk.PluginEntry{Module: "plugins/plugin-alertability", Symbol: "Plugin"},
		},
		Handlers: pluginsdk.Handlers{Event: noopConsoleHandler{}},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	failureAt := time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)
	runtime.recordDispatch(DispatchResult{PluginID: "plugin-alertability", Kind: "event", Success: false, Error: "subprocess host dispatch failed after handshake: upstream status 502", At: failureAt})
	runtime.recordDispatch(DispatchResult{PluginID: "plugin-alertability", Kind: "event", Success: false, Error: "subprocess host dispatch failed after handshake: upstream status 502", At: failureAt.Add(1 * time.Minute)})

	readyJob := NewJob("job-alertability-ready", "ai.chat", 1, 30*time.Second)
	readyJob.Payload = map[string]any{
		"dispatch": map[string]any{
			"target_plugin_id": "plugin-ai-chat",
			"permission":       "job:run",
			"actor":            "runtime-job-runner",
		},
		"reply_handle": map[string]any{
			"capability": "onebot.reply",
			"target_id":  "group-42",
		},
		"reply_target": "group-42",
	}
	jobs := []ConsoleJob{toConsoleJob(readyJob)}

	dueAt := time.Now().UTC().Add(-1 * time.Minute)
	schedules := []ConsoleSchedule{{
		ID:              "schedule-alertability-due",
		Kind:            string(ScheduleKindDelay),
		Source:          "console-test",
		EventType:       "message.received",
		DueAt:           &dueAt,
		DueReady:        true,
		DueStateSummary: "due",
		ScheduleSummary: "message.received | delay due",
	}}
	alerts := []AlertRecord{{
		ID:               "job.dead_letter:job-alertability-dead",
		ObjectType:       alertObjectTypeJob,
		ObjectID:         "job-alertability-dead",
		FailureType:      alertFailureTypeJobDeadLetter,
		FirstOccurredAt:  failureAt,
		LatestOccurredAt: failureAt.Add(2 * time.Minute),
		LatestReason:     "reply upstream status 502",
		Correlation:      "corr-alertability-dead",
		CreatedAt:        failureAt.Add(2 * time.Minute),
	}}

	api := NewConsoleAPI(runtime, nil, Config{}, nil, nil)
	api.SetJobReader(staticConsoleJobReader{jobs: []Job{readyJob}})
	api.SetScheduleReader(staticConsoleScheduleReader{schedules: schedules})
	api.SetAlertReader(staticConsoleAlertReader{alerts: alerts})
	api.SetMeta(map[string]any{
		"job_status_source":      "sqlite-jobs",
		"schedule_status_source": "sqlite-schedule-plans",
		"log_source":             "runtime-log-buffer",
		"trace_source":           "runtime-trace-recorder",
		"metrics_source":         "runtime-metrics-registry",
		"verification_endpoints": []string{"GET /api/console", "GET /metrics", "GET /demo/state/counts", "GET /demo/replies"},
	})

	obs := api.Observability(api.Plugins(), jobs, schedules, alerts)
	if len(obs.Alertability.Baseline) != 4 {
		t.Fatalf("expected four alertability baseline rules, got %+v", obs.Alertability.Baseline)
	}
	if len(obs.Alertability.ActiveFindings) != 4 {
		t.Fatalf("expected four active findings, got %+v", obs.Alertability.ActiveFindings)
	}
	if obs.JobDispatchReady != 1 || obs.ScheduleDueReady != 1 {
		t.Fatalf("expected alertability snapshot to preserve ready counts, got %+v", obs)
	}
	for _, expectedRuleID := range []string{
		consoleAlertabilityRuleRepeatedDispatchFailures,
		consoleAlertabilityRuleReadyBacklog,
		consoleAlertabilityRuleSubprocessFailure,
		consoleAlertabilityRuleDeadLetterFailurePath,
	} {
		if !hasAlertabilityRule(obs.Alertability.Baseline, expectedRuleID) {
			t.Fatalf("expected baseline to include %q, got %+v", expectedRuleID, obs.Alertability.Baseline)
		}
		if !hasAlertabilityFinding(obs.Alertability.ActiveFindings, expectedRuleID) {
			t.Fatalf("expected active findings to include %q, got %+v", expectedRuleID, obs.Alertability.ActiveFindings)
		}
	}
	if !strings.Contains(obs.Alertability.Summary, "4 rules 4 active findings") {
		t.Fatalf("expected alertability summary to describe rule/finding counts, got %q", obs.Alertability.Summary)
	}
	if !strings.Contains(obs.Summary, "alertability=4 rules 4 active findings via repo-local console/metrics surfaces") {
		t.Fatalf("expected observability summary to include alertability baseline, got %q", obs.Summary)
	}

	raw, err := api.RenderJSON()
	if err != nil {
		t.Fatalf("render json: %v", err)
	}
	for _, expected := range []string{
		`"alertability": {`,
		`"id": "repeated_dispatch_failures"`,
		`"id": "ready_backlog"`,
		`"id": "subprocess_failure_classified"`,
		`"id": "dead_letter_failure_paths"`,
		`"ruleId": "repeated_dispatch_failures"`,
		`"ruleId": "ready_backlog"`,
		`"ruleId": "subprocess_failure_classified"`,
		`"ruleId": "dead_letter_failure_paths"`,
		`"summary": "plugin plugin-alertability has repeated dispatch failures; current_failure_streak=2"`,
		`"summary": "ready backlog present; job_dispatch_ready=1 schedule_due_ready=1"`,
		`"summary": "subprocess plugin plugin-alertability currently reports a classified failure path"`,
		`"summary": "dead-letter alert for job job-alertability-dead preserves reply-or-upstream failure reason \"reply upstream status 502\""`,
		`"verificationEndpoints": [`,
		`"GET /metrics"`,
		`"GET /demo/replies"`,
	} {
		if !strings.Contains(raw, expected) {
			t.Fatalf("expected rendered console payload to contain %q, got %s", expected, raw)
		}
	}
}

func hasAlertabilityRule(rules []ConsoleAlertabilityRule, ruleID string) bool {
	for _, rule := range rules {
		if rule.ID == ruleID {
			return true
		}
	}
	return false
}

func hasAlertabilityFinding(findings []ConsoleAlertabilityFinding, ruleID string) bool {
	for _, finding := range findings {
		if finding.RuleID == ruleID {
			return true
		}
	}
	return false
}

func TestConsoleAPIServesReadOnlyJSONOverHTTP(t *testing.T) {
	t.Parallel()

	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	queue := NewJobQueue()
	api := NewConsoleAPI(runtime, queue, Config{Runtime: RuntimeConfig{Environment: "development", HTTPPort: 8080}}, []string{"runtime started"}, nil)
	api.SetMeta(map[string]any{"replay_policy": ReplayPolicy(), "replay_namespace": ReplayPolicy().Namespace, "secrets_policy": SecretPolicy(), "secrets_provider": SecretPolicy().Provider, "rollout_policy": RolloutPolicy(), "rollout_record_store": RolloutPolicy().RecordStore})

	request := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	recorder := httptest.NewRecorder()

	api.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("expected json content type, got %q", contentType)
	}
	for _, expected := range []string{"status", "plugins", "jobs", "logs", "config"} {
		if !strings.Contains(recorder.Body.String(), expected) {
			t.Fatalf("expected console http payload to contain %q, got %s", expected, recorder.Body.String())
		}
	}
	if !strings.Contains(recorder.Body.String(), `"replay_policy"`) || !strings.Contains(recorder.Body.String(), `"runtime.replay.isolated"`) {
		t.Fatalf("expected console http payload to expose replay policy declaration, got %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"secrets_policy"`) || !strings.Contains(recorder.Body.String(), `"secrets_provider": "env"`) || !strings.Contains(recorder.Body.String(), `"BOT_PLATFORM_"`) {
		t.Fatalf("expected console http payload to expose secrets policy declaration, got %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"rollout_policy"`) || !strings.Contains(recorder.Body.String(), `"rollout_record_store": "sqlite-current-runtime-rollout-operations"`) || !strings.Contains(recorder.Body.String(), `"/admin prepare \u003cplugin-id\u003e"`) {
		t.Fatalf("expected console http payload to expose rollout policy declaration, got %s", recorder.Body.String())
	}
}

func TestConsoleAPIServesReadOnlyJSONWhenConsoleReadAuthorizerAllows(t *testing.T) {
	t.Parallel()

	audits := NewInMemoryAuditLog()
	api := NewConsoleAPI(nil, nil, Config{RBAC: &RBACConfig{
		ConsoleReadPermission: "console:read",
		ActorRoles:            map[string][]string{"viewer-user": {"console-viewer"}},
		Policies: map[string]pluginsdk.AuthorizationPolicy{
			"console-viewer": {
				Permissions: []string{"console:read"},
				PluginScope: []string{"console"},
			},
		},
	}}, []string{"runtime started"}, audits)

	request := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	request.Header.Set(ConsoleReadActorHeader, "viewer-user")
	recorder := httptest.NewRecorder()

	api.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "runtime started") {
		t.Fatalf("expected allowed console read response body, got %s", recorder.Body.String())
	}
	if len(audits.AuditEntries()) != 0 {
		t.Fatalf("expected allowed console read not to record deny audit, got %+v", audits.AuditEntries())
	}
}

func TestConsoleAPIExposesPersistedReplayAndRolloutOperations(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()
	replayAt := time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)
	rolloutAt := time.Date(2026, 4, 19, 12, 5, 0, 0, time.UTC)
	if err := store.SaveReplayOperationRecord(context.Background(), ReplayOperationRecord{
		ReplayID:      "replay-op-console-1",
		SourceEventID: "evt-source-console-1",
		ReplayEventID: "replay-evt-source-console-1-1",
		Status:        "succeeded",
		Reason:        "replay_dispatched",
		OccurredAt:    replayAt,
		UpdatedAt:     replayAt,
	}); err != nil {
		t.Fatalf("save replay operation record: %v", err)
	}
	if err := store.SaveRolloutOperationRecord(context.Background(), RolloutOperationRecord{
		OperationID:      "rollout-op-console-1",
		PluginID:         "plugin-echo",
		Action:           "prepare",
		CurrentVersion:   "0.1.0",
		CandidateVersion: "0.2.0-candidate",
		Status:           "prepared",
		OccurredAt:       rolloutAt,
		UpdatedAt:        rolloutAt,
	}); err != nil {
		t.Fatalf("save rollout operation record: %v", err)
	}
	if err := store.SaveRolloutHead(context.Background(), RolloutHeadState{
		PluginID: "plugin-echo",
		Stable: RolloutSnapshotState{
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
		},
		Active: RolloutSnapshotState{
			Version:    "0.2.0-candidate",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
		},
		Candidate: &RolloutSnapshotState{
			Version:    "0.2.0-candidate",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
		},
		Phase:           string(pluginsdk.RolloutPhaseCanary),
		Status:          string(pluginsdk.RolloutStatusCanarying),
		LastOperationID: "rollout-op-console-1",
		UpdatedAt:       rolloutAt,
	}); err != nil {
		t.Fatalf("save rollout head: %v", err)
	}

	api := NewConsoleAPI(nil, nil, Config{}, []string{"runtime started"}, NewInMemoryAuditLog())
	api.SetReplayOperationReader(NewSQLiteConsoleReplayOperationReader(store))
	api.SetRolloutOperationReader(NewSQLiteConsoleRolloutOperationReader(store))
	api.SetRolloutHeadReader(NewSQLiteConsoleRolloutHeadReader(store))
	api.SetMeta(map[string]any{"replay_policy": ReplayPolicy(), "secrets_policy": SecretPolicy(), "rollout_policy": RolloutPolicy(), "replay_record_read_model": "sqlite-replay-operation-records", "rollout_record_read_model": "sqlite-rollout-operation-records", "rollout_head_read_model": "sqlite-rollout-heads"})

	raw, err := api.RenderJSON()
	if err != nil {
		t.Fatalf("render json: %v", err)
	}
	for _, expected := range []string{`"replayOps": [`, `"replayId": "replay-op-console-1"`, `"stateSource": "sqlite-replay-operation-records"`, `"persisted": true`, `"sourceEventId": "evt-source-console-1"`, `"reason": "replay_dispatched"`, `"rolloutOps": [`, `"operationId": "rollout-op-console-1"`, `"pluginId": "plugin-echo"`, `"action": "prepare"`, `"currentVersion": "0.1.0"`, `"candidateVersion": "0.2.0-candidate"`, `"stateSource": "sqlite-rollout-operation-records"`, `"rolloutHeads": [`, `"pluginId": "plugin-echo"`, `"phase": "canary"`, `"status": "canarying"`, `"stable": {`, `"active": {`, `"candidate": {`, `"lastOperationId": "rollout-op-console-1"`, `"stateSource": "sqlite-rollout-heads"`, `"rollout_head_read_model"`, `"replay_policy"`, `"secrets_policy"`, `"rollout_policy"`} {
		if !strings.Contains(raw, expected) {
			t.Fatalf("expected console JSON to include %q, got %s", expected, raw)
		}
	}
}

func TestConsoleAPIRolloutHeadsExposePersistedCurrentTruth(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()
	updatedAt := time.Date(2026, 4, 24, 11, 0, 0, 0, time.UTC)
	if err := store.SaveRolloutHead(context.Background(), RolloutHeadState{
		PluginID:        "plugin-echo",
		Stable:          RolloutSnapshotState{Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess},
		Active:          RolloutSnapshotState{Version: "0.2.0-candidate", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess},
		Candidate:       &RolloutSnapshotState{Version: "0.2.0-candidate", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess},
		Phase:           string(pluginsdk.RolloutPhaseCanary),
		Status:          string(pluginsdk.RolloutStatusCanarying),
		Reason:          "",
		LastOperationID: "rollout-op-canary-1",
		UpdatedAt:       updatedAt,
	}); err != nil {
		t.Fatalf("save rollout head: %v", err)
	}

	api := NewConsoleAPI(nil, nil, Config{}, nil, NewInMemoryAuditLog())
	api.SetRolloutHeadReader(NewSQLiteConsoleRolloutHeadReader(store))

	heads, err := api.RolloutHeads()
	if err != nil {
		t.Fatalf("load rollout heads: %v", err)
	}
	if len(heads) != 1 {
		t.Fatalf("expected one rollout head, got %+v", heads)
	}
	head := heads[0]
	if head.PluginID != "plugin-echo" || head.Phase != "canary" || head.Status != "canarying" || head.Stable.Version != "0.1.0" || head.Active.Version != "0.2.0-candidate" || head.Candidate == nil || head.Candidate.Version != "0.2.0-candidate" || head.LastOperationID != "rollout-op-canary-1" || head.StateSource != "sqlite-rollout-heads" || !head.Persisted {
		t.Fatalf("unexpected rollout head %+v", head)
	}
	if !strings.Contains(head.Summary, "rollout head canary canarying for plugin-echo") || !strings.Contains(head.Summary, "stable=0.1.0 active=0.2.0-candidate") || !strings.Contains(head.Summary, "candidate=0.2.0-candidate") || !strings.Contains(head.Summary, "via sqlite-rollout-heads") {
		t.Fatalf("expected rollout head summary to describe current truth, got %+v", head)
	}
}

func TestConsoleAPIDoesNotRecordDenyAuditWhenConsoleReadPermissionIsUnconfigured(t *testing.T) {
	t.Parallel()

	audits := NewInMemoryAuditLog()
	api := NewConsoleAPI(nil, nil, Config{}, []string{"runtime started"}, audits)

	request := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	recorder := httptest.NewRecorder()

	api.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(audits.AuditEntries()) != 0 {
		t.Fatalf("expected unconfigured console read compatibility path not to record deny audit, got %+v", audits.AuditEntries())
	}
}

func TestConsoleAPIReturnsForbiddenWhenConsoleReadAuthorizerDenies(t *testing.T) {
	t.Parallel()

	audits := NewInMemoryAuditLog()
	api := NewConsoleAPI(nil, nil, Config{RBAC: &RBACConfig{
		ConsoleReadPermission: "console:read",
		ActorRoles:            map[string][]string{"viewer-user": {"console-viewer"}},
		Policies: map[string]pluginsdk.AuthorizationPolicy{
			"console-viewer": {
				Permissions: []string{"console:read"},
				PluginScope: []string{"console"},
			},
		},
	}}, []string{"runtime started"}, audits)

	request := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	recorder := httptest.NewRecorder()

	api.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "permission denied") {
		t.Fatalf("expected forbidden response to mention permission denied, got %s", recorder.Body.String())
	}
	entries := audits.AuditEntries()
	if len(entries) != 1 {
		t.Fatalf("expected one deny audit entry, got %+v", entries)
	}
	if entries[0].Action != "console.read" || entries[0].Permission != "console:read" || entries[0].Target != "console" || entries[0].Allowed || auditEntryReason(entries[0]) != "permission_denied" {
		t.Fatalf("expected denied console read audit entry, got %+v", entries[0])
	}
	if entries[0].Actor != "" {
		t.Fatalf("expected denied audit entry to preserve missing actor header as empty, got %+v", entries[0])
	}
}

func TestConsoleAPIDeniedAuditCopiesRequestIdentitySessionID(t *testing.T) {
	t.Parallel()

	audits := NewInMemoryAuditLog()
	api := NewConsoleAPI(nil, nil, Config{RBAC: &RBACConfig{
		ConsoleReadPermission: "console:read",
		ActorRoles:            map[string][]string{"viewer-user": {"console-viewer"}},
		Policies: map[string]pluginsdk.AuthorizationPolicy{
			"console-viewer": {
				Permissions: []string{"console:read"},
				PluginScope: []string{"console"},
			},
		},
	}}, []string{"runtime started"}, audits)

	request := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	request = request.WithContext(WithRequestIdentityContext(request.Context(), RequestIdentityContext{
		ActorID:    "bearer-admin",
		TokenID:    "console-main",
		AuthMethod: RequestIdentityAuthMethodBearer,
		SessionID:  OperatorBearerSessionID("bearer-admin"),
	}))
	recorder := httptest.NewRecorder()

	api.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", recorder.Code, recorder.Body.String())
	}
	entries := audits.AuditEntries()
	if len(entries) != 1 {
		t.Fatalf("expected one deny audit entry, got %+v", entries)
	}
	if entries[0].Actor != "bearer-admin" || entries[0].SessionID != OperatorBearerSessionID("bearer-admin") {
		t.Fatalf("expected request identity fields in console deny audit, got %+v", entries[0])
	}
}

func TestConsoleAPIReturnsUnauthorizedWhenRequestIdentityAuthorizerRequiresBearerAuth(t *testing.T) {
	t.Parallel()

	audits := NewInMemoryAuditLog()
	api := NewConsoleAPI(nil, nil, Config{RBAC: &RBACConfig{
		ConsoleReadPermission: "console:read",
		ActorRoles:            map[string][]string{"admin-user": {"admin"}},
		Policies: map[string]pluginsdk.AuthorizationPolicy{
			"admin": {
				Permissions: []string{"console:read"},
				PluginScope: []string{"*"},
			},
		},
	}}, []string{"runtime started"}, audits)
	provider := NewCurrentRBACAuthorizerProviderFromConfig(api.config.RBAC)
	api.SetCurrentAuthorizerProvider(provider)
	api.SetReadAuthorizer(NewRequestIdentityConsoleReadAuthorizer(provider, true))

	request := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	request.Header.Set(ConsoleReadActorHeader, "admin-user")
	recorder := httptest.NewRecorder()

	api.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "unauthorized") {
		t.Fatalf("expected unauthorized response body, got %s", recorder.Body.String())
	}
	if len(audits.AuditEntries()) != 0 {
		t.Fatalf("expected unauthorized console read not to record deny audit, got %+v", audits.AuditEntries())
	}
}

func TestConsoleAPIReturnsScopeDeniedAuditReasonWhenConsoleReadScopeRejects(t *testing.T) {
	t.Parallel()

	audits := NewInMemoryAuditLog()
	api := NewConsoleAPI(nil, nil, Config{RBAC: &RBACConfig{
		ConsoleReadPermission: "console:read",
		ActorRoles:            map[string][]string{"scoped-user": {"console-reader"}},
		Policies: map[string]pluginsdk.AuthorizationPolicy{
			"console-reader": {
				Permissions: []string{"console:read"},
				PluginScope: []string{"plugin-echo"},
			},
		},
	}}, []string{"runtime started"}, audits)

	request := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	request.Header.Set(ConsoleReadActorHeader, "scoped-user")
	recorder := httptest.NewRecorder()

	api.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "plugin scope denied") {
		t.Fatalf("expected forbidden response to mention plugin scope denied, got %s", recorder.Body.String())
	}
	entries := audits.AuditEntries()
	if len(entries) != 1 {
		t.Fatalf("expected one deny audit entry, got %+v", entries)
	}
	if entries[0].Actor != "scoped-user" || entries[0].Action != "console.read" || entries[0].Permission != "console:read" || entries[0].Target != "console" || entries[0].Allowed || auditEntryReason(entries[0]) != "plugin_scope_denied" {
		t.Fatalf("expected scope-denied console read audit entry, got %+v", entries[0])
	}
}

func TestConsoleAPIServesFilteredLogsOverHTTP(t *testing.T) {
	t.Parallel()

	api := NewConsoleAPI(nil, nil, Config{}, []string{"runtime started", "plugin-echo ready", "plugin-ai-chat queued"}, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/console?log_query=plugin-echo", nil)
	recorder := httptest.NewRecorder()

	api.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "plugin-ai-chat queued") {
		t.Fatalf("expected filtered logs to exclude unrelated lines, got %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "plugin-echo ready") {
		t.Fatalf("expected filtered logs to include matching line, got %s", recorder.Body.String())
	}
}

func TestConsoleAPITreatsWhitespaceOnlyLogQueryAsUnfiltered(t *testing.T) {
	t.Parallel()

	api := NewConsoleAPI(nil, nil, Config{}, []string{"runtime started", "plugin-echo ready"}, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/console?log_query=%20%20", nil)
	recorder := httptest.NewRecorder()

	api.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	for _, expected := range []string{"runtime started", "plugin-echo ready"} {
		if !strings.Contains(recorder.Body.String(), expected) {
			t.Fatalf("expected whitespace-only log query to keep %q, got %s", expected, recorder.Body.String())
		}
	}
}

func TestConsoleAPIServesFilteredJobsOverHTTP(t *testing.T) {
	t.Parallel()

	queue := NewJobQueue()
	if err := queue.Enqueue(context.Background(), NewJob("job-ai", "ai.call", 1, 30*time.Second)); err != nil {
		t.Fatalf("enqueue first job: %v", err)
	}
	if err := queue.Enqueue(context.Background(), NewJob("job-sync", "adapter.sync", 1, 30*time.Second)); err != nil {
		t.Fatalf("enqueue second job: %v", err)
	}
	api := NewConsoleAPI(nil, queue, Config{}, nil, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/console?job_query=ai.call", nil)
	recorder := httptest.NewRecorder()

	api.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "adapter.sync") {
		t.Fatalf("expected filtered jobs to exclude unrelated entries, got %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "ai.call") {
		t.Fatalf("expected filtered jobs to include matching entry, got %s", recorder.Body.String())
	}
}

func TestConsoleAPIServesFilteredPluginOverHTTP(t *testing.T) {
	t.Parallel()

	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	for _, manifest := range []pluginsdk.PluginManifest{
		{
			ID:         "plugin-echo",
			Name:       "Echo Plugin",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			Publish: &pluginsdk.PluginPublish{
				SourceType:          pluginsdk.PublishSourceTypeGit,
				SourceURI:           "https://github.com/ohmyopencode/bot-platform/tree/main/plugins/plugin-echo",
				RuntimeVersionRange: ">=0.1.0 <1.0.0",
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/plugin-echo", Symbol: "Plugin"},
		},
		{
			ID:         "plugin-admin",
			Name:       "Admin Plugin",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			Entry:      pluginsdk.PluginEntry{Module: "plugins/plugin-admin", Symbol: "Plugin"},
		},
	} {
		if err := registerPluginWithTestSchema(runtime, pluginsdk.Plugin{
			Manifest: manifest,
			Handlers: pluginsdk.Handlers{Event: noopConsoleHandler{}},
		}); err != nil {
			t.Fatalf("register plugin %s: %v", manifest.ID, err)
		}
	}
	failureAt := time.Date(2026, 4, 15, 8, 0, 0, 0, time.UTC)
	runtime.recordDispatch(DispatchResult{PluginID: "plugin-echo", Kind: "event", Success: false, Error: "dispatch timeout", At: failureAt})
	runtime.recordDispatch(DispatchResult{PluginID: "plugin-admin", Kind: "event", Success: true, At: failureAt.Add(2 * time.Minute)})

	api := NewConsoleAPI(runtime, nil, Config{}, nil, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/console?plugin_id=plugin-echo", nil)
	recorder := httptest.NewRecorder()

	api.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "plugin-admin") {
		t.Fatalf("expected filtered plugins to exclude unrelated plugin, got %s", recorder.Body.String())
	}
	for _, expected := range []string{"plugin-echo", `"publish": {`, `"sourceType": "git"`, `"sourceUri": "https://github.com/ohmyopencode/bot-platform/tree/main/plugins/plugin-echo"`, `"runtimeVersionRange": "\u003e=0.1.0 \u003c1.0.0"`, `"lastDispatchSuccess": false`, `"statusRecovery": "last-dispatch-failed"`, `"lastDispatchError": "dispatch timeout"`, `"currentFailureStreak": 1`} {
		if !strings.Contains(recorder.Body.String(), expected) {
			t.Fatalf("expected filtered plugin response to contain %q, got %s", expected, recorder.Body.String())
		}
	}
}

func TestConsoleAPITreatsWhitespaceOnlyPluginQueryAsUnfiltered(t *testing.T) {
	t.Parallel()

	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	for _, manifest := range []pluginsdk.PluginManifest{
		{
			ID:         "plugin-echo",
			Name:       "Echo Plugin",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			Entry:      pluginsdk.PluginEntry{Module: "plugins/plugin-echo", Symbol: "Plugin"},
		},
		{
			ID:         "plugin-admin",
			Name:       "Admin Plugin",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			Entry:      pluginsdk.PluginEntry{Module: "plugins/plugin-admin", Symbol: "Plugin"},
		},
	} {
		if err := registerPluginWithTestSchema(runtime, pluginsdk.Plugin{
			Manifest: manifest,
			Handlers: pluginsdk.Handlers{Event: noopConsoleHandler{}},
		}); err != nil {
			t.Fatalf("register plugin %s: %v", manifest.ID, err)
		}
	}

	api := NewConsoleAPI(runtime, nil, Config{}, nil, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/console?plugin_id=%20%20", nil)
	recorder := httptest.NewRecorder()

	api.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	for _, expected := range []string{"plugin-echo", "plugin-admin"} {
		if !strings.Contains(recorder.Body.String(), expected) {
			t.Fatalf("expected whitespace-only plugin query to keep %q, got %s", expected, recorder.Body.String())
		}
	}
}

func TestConsoleAPIUnknownPluginQueryReturnsEmptyPluginList(t *testing.T) {
	t.Parallel()

	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	if err := registerPluginWithTestSchema(runtime, pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-echo",
			Name:       "Echo Plugin",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			Entry:      pluginsdk.PluginEntry{Module: "plugins/plugin-echo", Symbol: "Plugin"},
		},
		Handlers: pluginsdk.Handlers{Event: noopConsoleHandler{}},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	api := NewConsoleAPI(runtime, nil, Config{}, nil, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/console?plugin_id=plugin-missing", nil)
	recorder := httptest.NewRecorder()

	api.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "plugin-echo") {
		t.Fatalf("expected unknown plugin query to exclude registered plugins, got %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"plugins": []`) {
		t.Fatalf("expected unknown plugin query to return empty plugin list, got %s", recorder.Body.String())
	}
}

func TestConsoleAPITreatsWhitespaceOnlyJobQueryAsUnfiltered(t *testing.T) {
	t.Parallel()

	queue := NewJobQueue()
	if err := queue.Enqueue(context.Background(), NewJob("job-ai", "ai.call", 1, 30*time.Second)); err != nil {
		t.Fatalf("enqueue first job: %v", err)
	}
	if err := queue.Enqueue(context.Background(), NewJob("job-sync", "adapter.sync", 1, 30*time.Second)); err != nil {
		t.Fatalf("enqueue second job: %v", err)
	}
	api := NewConsoleAPI(nil, queue, Config{}, nil, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/console?job_query=%20%20", nil)
	recorder := httptest.NewRecorder()

	api.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	for _, expected := range []string{"ai.call", "adapter.sync"} {
		if !strings.Contains(recorder.Body.String(), expected) {
			t.Fatalf("expected whitespace-only job query to keep %q, got %s", expected, recorder.Body.String())
		}
	}
}

func TestConsoleAPIRejectsNonGetRequests(t *testing.T) {
	t.Parallel()

	api := NewConsoleAPI(nil, nil, Config{}, nil, nil)
	request := httptest.NewRequest(http.MethodPost, "/api/console", nil)
	recorder := httptest.NewRecorder()

	api.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", recorder.Code)
	}
}

func TestConsoleAPIRenderJSONMatchesFrontendJobContract(t *testing.T) {
	t.Parallel()

	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	queue := NewJobQueue()
	job := NewJob("job-console-contract", "ai.call", 2, 30*time.Second)
	job.Correlation = "corr-console"
	job.Status = JobStatusPending
	job.WorkerID = "runtime-local:test-worker"
	now := time.Now().UTC()
	leaseExpiresAt := now.Add(30 * time.Second)
	job.LeaseAcquiredAt = &now
	job.LeaseExpiresAt = &leaseExpiresAt
	job.HeartbeatAt = &now
	job.ReasonCode = JobReasonCodeDispatchRetry
	job.LastError = "reply handle payload is required"
	job.Payload = map[string]any{
		"dispatch": map[string]any{
			"target_plugin_id": "plugin-ai-chat",
			"permission":       "job:run",
			"actor":            "runtime-job-runner",
		},
		"reply_handle": map[string]any{
			"capability": "onebot.reply",
			"target_id":  "group-42",
		},
		"reply_target": "group-42",
		"session_id":   "session-user-42",
	}
	if err := queue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	raw, err := NewConsoleAPI(runtime, queue, Config{}, nil, nil).RenderJSON()
	if err != nil {
		t.Fatalf("render json: %v", err)
	}

	var payload struct {
		Jobs []map[string]any `json:"jobs"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode console payload: %v", err)
	}
	if len(payload.Jobs) != 1 {
		t.Fatalf("expected one job in payload, got %+v", payload.Jobs)
	}
	jobPayload := payload.Jobs[0]
	for _, key := range []string{"id", "type", "status", "retryCount", "maxRetries", "timeout", "createdAt", "workerId", "leaseAcquiredAt", "leaseExpiresAt", "heartbeatAt", "reasonCode", "deadLetter", "correlation", "targetPluginId", "dispatchMetadataPresent", "dispatchContractPresent", "queueContractComplete", "dispatchReady", "queueStateSummary", "dispatchSummary", "queueContractSummary", "leaseSummary", "dispatchPermission", "dispatchActor", "replyHandlePresent", "replyHandleCapability", "replyContractPresent", "replySummary", "sessionIDPresent", "replyTargetPresent", "recoverySummary"} {
		if _, ok := jobPayload[key]; !ok {
			t.Fatalf("expected frontend job key %q in payload, got %+v", key, jobPayload)
		}
	}
	if got, ok := jobPayload["workerId"].(string); !ok || got != "runtime-local:test-worker" {
		t.Fatalf("expected workerId in payload, got %+v", jobPayload)
	}
	if got, ok := jobPayload["reasonCode"].(string); !ok || got != "dispatch_retry" {
		t.Fatalf("expected reasonCode=dispatch_retry, got %+v", jobPayload)
	}
	if got, ok := jobPayload["leaseSummary"].(string); !ok || !strings.Contains(got, "worker=runtime-local:test-worker") {
		t.Fatalf("expected leaseSummary to include worker ownership, got %+v", jobPayload)
	}
	if got, ok := jobPayload["recoverySummary"].(string); !ok || !strings.Contains(got, "reason_code=dispatch_retry") {
		t.Fatalf("expected recoverySummary to include reason code, got %+v", jobPayload)
	}
	if got, ok := jobPayload["dispatchMetadataPresent"].(bool); !ok || !got {
		t.Fatalf("expected dispatchMetadataPresent=true, got %+v", jobPayload)
	}
	if got, ok := jobPayload["dispatchContractPresent"].(bool); !ok || !got {
		t.Fatalf("expected dispatchContractPresent=true, got %+v", jobPayload)
	}
	if got, ok := jobPayload["queueContractComplete"].(bool); !ok || !got {
		t.Fatalf("expected queueContractComplete=true, got %+v", jobPayload)
	}
	if got, ok := jobPayload["dispatchReady"].(bool); !ok || !got {
		t.Fatalf("expected dispatchReady=true, got %+v", jobPayload)
	}
	if got, ok := jobPayload["queueStateSummary"].(string); !ok || got != "ready" {
		t.Fatalf("expected queueStateSummary=ready, got %+v", jobPayload)
	}
	if got, ok := jobPayload["replyHandlePresent"].(bool); !ok || !got {
		t.Fatalf("expected replyHandlePresent=true, got %+v", jobPayload)
	}
	if got, ok := jobPayload["replyHandleCapability"].(string); !ok || got != "onebot.reply" {
		t.Fatalf("expected replyHandleCapability=onebot.reply, got %+v", jobPayload)
	}
	if got, ok := jobPayload["replyContractPresent"].(bool); !ok || !got {
		t.Fatalf("expected replyContractPresent=true, got %+v", jobPayload)
	}
	if got, ok := jobPayload["replySummary"].(string); !ok || got != "onebot.reply -> group-42" {
		t.Fatalf("expected replySummary, got %+v", jobPayload)
	}
	if got, ok := jobPayload["dispatchSummary"].(string); !ok || got != "runtime-job-runner -> plugin-ai-chat [job:run]" {
		t.Fatalf("expected dispatchSummary, got %+v", jobPayload)
	}
	if got, ok := jobPayload["queueContractSummary"].(string); !ok || got != "runtime-job-runner -> plugin-ai-chat [job:run] | onebot.reply -> group-42" {
		t.Fatalf("expected queueContractSummary, got %+v", jobPayload)
	}
	if got, ok := jobPayload["sessionIDPresent"].(bool); !ok || !got {
		t.Fatalf("expected sessionIDPresent=true, got %+v", jobPayload)
	}
	if got, ok := jobPayload["replyTargetPresent"].(bool); !ok || !got {
		t.Fatalf("expected replyTargetPresent=true, got %+v", jobPayload)
	}
	if _, wrong := jobPayload["ID"]; wrong {
		t.Fatalf("expected camelCase job payload keys only, got %+v", jobPayload)
	}
	if _, ok := jobPayload["timeout"].(float64); !ok {
		t.Fatalf("expected numeric timeout for live JSON payload, got %T (%+v)", jobPayload["timeout"], jobPayload["timeout"])
	}
}

func TestConsoleAPIJobRecoverySummaryDistinguishesWorkerAbandonment(t *testing.T) {
	t.Parallel()

	job := NewJob("job-console-recovery", "ai.chat", 1, 30*time.Second)
	job.Status = JobStatusRetrying
	job.ReasonCode = JobReasonCodeWorkerAbandoned
	job.LastError = "worker runtime-local:test-worker lease abandoned during runtime restart"
	job.WorkerID = "runtime-local:test-worker"

	consoleJob := toConsoleJob(job)
	if !strings.Contains(consoleJob.RecoverySummary, "reason_code=worker_abandoned") || !strings.Contains(consoleJob.RecoverySummary, "worker=runtime-local:test-worker") {
		t.Fatalf("expected worker abandonment recovery summary, got %+v", consoleJob)
	}
}

func TestQueuedStateSummary(t *testing.T) {
	t.Parallel()

	future := time.Now().UTC().Add(1 * time.Hour)
	for _, tc := range []struct {
		name                    string
		job                     Job
		dispatchMetadataPresent bool
		dispatchContractPresent bool
		dispatchReady           bool
		want                    string
	}{
		{name: "no dispatch metadata falls back to raw status", job: Job{Status: JobStatusPending}, want: string(JobStatusPending)},
		{name: "ready complete contract", job: Job{Status: JobStatusPending}, dispatchMetadataPresent: true, dispatchContractPresent: true, dispatchReady: true, want: "ready"},
		{name: "ready incomplete contract", job: Job{Status: JobStatusPending}, dispatchMetadataPresent: true, dispatchReady: true, want: "ready-incomplete-contract"},
		{name: "waiting retry", job: Job{Status: JobStatusRetrying, NextRunAt: &future}, dispatchMetadataPresent: true, want: "waiting-retry"},
		{name: "pending with dispatch metadata but not ready", job: Job{Status: JobStatusPending}, dispatchMetadataPresent: true, want: "pending"},
		{name: "done falls back to raw status", job: Job{Status: JobStatusDone}, dispatchMetadataPresent: true, want: string(JobStatusDone)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := queuedStateSummary(tc.job, tc.dispatchMetadataPresent, tc.dispatchContractPresent, tc.dispatchReady); got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

func TestQueuedContractSummary(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		dispatch string
		reply    string
		want     string
	}{
		{name: "both empty", want: ""},
		{name: "dispatch only", dispatch: "runtime-job-runner -> plugin-ai-chat [job:run]", want: "runtime-job-runner -> plugin-ai-chat [job:run]"},
		{name: "reply only", reply: "onebot.reply -> group-42", want: "onebot.reply -> group-42"},
		{name: "both present", dispatch: "runtime-job-runner -> plugin-ai-chat [job:run]", reply: "onebot.reply -> group-42", want: "runtime-job-runner -> plugin-ai-chat [job:run] | onebot.reply -> group-42"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := queuedContractSummary(tc.dispatch, tc.reply); got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

func TestQueuedReplySummary(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		capability string
		target     string
		want       string
	}{
		{name: "both empty", want: ""},
		{name: "capability only", capability: "onebot.reply", want: "onebot.reply"},
		{name: "target only", target: "group-42", want: "reply -> group-42"},
		{name: "both present", capability: "onebot.reply", target: "group-42", want: "onebot.reply -> group-42"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := queuedReplySummary(tc.capability, tc.target); got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

func TestConsoleAPIJobSummariesShowWaitingRetryForFutureRetryingJob(t *testing.T) {
	t.Parallel()

	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	queue := NewJobQueue()
	nextRunAt := time.Now().UTC().Add(1 * time.Hour)
	job := NewJob("job-console-waiting-retry", "ai.chat", 2, 30*time.Second)
	job.Status = JobStatusRetrying
	job.NextRunAt = &nextRunAt
	job.Payload = map[string]any{
		"dispatch": map[string]any{
			"target_plugin_id": "plugin-ai-chat",
			"permission":       "job:run",
			"actor":            "runtime-job-runner",
		},
		"reply_handle": map[string]any{
			"capability": "onebot.reply",
			"target_id":  "group-42",
		},
		"reply_target": "group-42",
	}
	if err := queue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	queue.jobs[job.ID] = job

	jobs, err := NewConsoleAPI(runtime, queue, Config{}, nil, nil).Jobs()
	if err != nil {
		t.Fatalf("console jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected one job, got %+v", jobs)
	}
	if jobs[0].DispatchReady {
		t.Fatalf("expected dispatchReady=false, got %+v", jobs[0])
	}
	if jobs[0].QueueStateSummary != "waiting-retry" {
		t.Fatalf("expected queueStateSummary=waiting-retry, got %+v", jobs[0])
	}
}

func TestConsoleAPIJobSummariesShowReadyIncompleteContractWhenReplyTargetMissing(t *testing.T) {
	t.Parallel()

	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	queue := NewJobQueue()
	job := NewJob("job-console-incomplete-reply-contract", "ai.chat", 2, 30*time.Second)
	job.Status = JobStatusPending
	job.Payload = map[string]any{
		"dispatch": map[string]any{
			"target_plugin_id": "plugin-ai-chat",
			"permission":       "job:run",
			"actor":            "runtime-job-runner",
		},
		"reply_handle": map[string]any{
			"capability": "onebot.reply",
			"target_id":  "group-42",
		},
	}
	if err := queue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	jobs, err := NewConsoleAPI(runtime, queue, Config{}, nil, nil).Jobs()
	if err != nil {
		t.Fatalf("console jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected one job, got %+v", jobs)
	}
	if !jobs[0].DispatchReady {
		t.Fatalf("expected dispatchReady=true, got %+v", jobs[0])
	}
	if !jobs[0].DispatchContractPresent {
		t.Fatalf("expected dispatchContractPresent=true, got %+v", jobs[0])
	}
	if jobs[0].QueueContractComplete {
		t.Fatalf("expected queueContractComplete=false, got %+v", jobs[0])
	}
	if jobs[0].ReplyContractPresent {
		t.Fatalf("expected replyContractPresent=false, got %+v", jobs[0])
	}
	if jobs[0].QueueStateSummary != "ready" {
		t.Fatalf("expected queueStateSummary=ready, got %+v", jobs[0])
	}
	if jobs[0].QueueContractSummary != "runtime-job-runner -> plugin-ai-chat [job:run] | onebot.reply" {
		t.Fatalf("expected partial queueContractSummary for missing reply_target, got %+v", jobs[0])
	}
}

func TestConsoleAPIExposesPersistedSchedulesAndStatusFromStore(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	createdAt := time.Now().UTC()
	dueAt := createdAt.Add(30 * time.Second)
	if err := store.SaveSchedulePlan(context.Background(), storedSchedulePlan{
		Plan: SchedulePlan{
			ID:        "schedule-console-1",
			Kind:      ScheduleKindDelay,
			Delay:     30 * time.Second,
			Source:    "runtime-demo-scheduler",
			EventType: "message.received",
			Metadata:  map[string]any{"message_text": "console schedule"},
		},
		DueAt:     &dueAt,
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}); err != nil {
		t.Fatalf("save schedule plan: %v", err)
	}

	api := NewConsoleAPI(nil, nil, Config{}, nil, nil)
	api.SetScheduleReader(NewSQLiteConsoleScheduleReader(store))

	status, err := api.Status()
	if err != nil {
		t.Fatalf("console status: %v", err)
	}
	if status.Schedules != 1 {
		t.Fatalf("expected schedule status count to come from persisted plans, got %+v", status)
	}

	rendered, err := api.RenderJSON()
	if err != nil {
		t.Fatalf("render json: %v", err)
	}
	for _, expected := range []string{"schedules", "schedule-console-1", "runtime-demo-scheduler", "message.received"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("expected console output to contain %q, got %s", expected, rendered)
		}
	}

	var payload struct {
		Status struct {
			Schedules int `json:"schedules"`
		} `json:"status"`
		Schedules []map[string]any `json:"schedules"`
	}
	if err := json.Unmarshal([]byte(rendered), &payload); err != nil {
		t.Fatalf("decode console payload: %v", err)
	}
	if payload.Status.Schedules != 1 {
		t.Fatalf("expected payload status.schedules=1, got %+v", payload.Status)
	}
	if len(payload.Schedules) != 1 {
		t.Fatalf("expected one schedule in payload, got %+v", payload.Schedules)
	}
	schedulePayload := payload.Schedules[0]
	for _, key := range []string{"id", "kind", "source", "eventType", "delayMs", "executeAt", "dueAt", "dueAtSource", "dueAtEvidence", "dueAtPersisted", "dueReady", "dueStateSummary", "dueSummary", "scheduleSummary", "createdAt", "updatedAt", "claimOwner", "claimedAt", "claimed", "recoveryState"} {
		if _, ok := schedulePayload[key]; !ok {
			t.Fatalf("expected frontend schedule key %q in payload, got %+v", key, schedulePayload)
		}
	}
	if got, ok := schedulePayload["overdue"].(bool); !ok || got {
		t.Fatalf("expected overdue=false for future schedule payload, got %+v", schedulePayload)
	}
	if got, ok := schedulePayload["dueAtSource"].(string); !ok || got != "persisted-state" {
		t.Fatalf("expected dueAtSource=persisted-state, got %+v", schedulePayload)
	}
	if got, ok := schedulePayload["dueAtEvidence"].(string); !ok || got != scheduleDueAtEvidencePersisted {
		t.Fatalf("expected dueAtEvidence=%q, got %+v", scheduleDueAtEvidencePersisted, schedulePayload)
	}
	if got, ok := schedulePayload["dueAtPersisted"].(bool); !ok || !got {
		t.Fatalf("expected dueAtPersisted=true, got %+v", schedulePayload)
	}
	if got, ok := schedulePayload["claimed"].(bool); !ok || got {
		t.Fatalf("expected claimed=false for unclaimed schedule payload, got %+v", schedulePayload)
	}
	if got, ok := schedulePayload["recoveryState"].(string); !ok || got != "" {
		t.Fatalf("expected empty recoveryState for plain persisted schedule payload, got %+v", schedulePayload)
	}
	if got, ok := schedulePayload["dueReady"].(bool); !ok || got {
		t.Fatalf("expected dueReady=false for future schedule, got %+v", schedulePayload)
	}
	if got, ok := schedulePayload["dueStateSummary"].(string); !ok || got != "scheduled" {
		t.Fatalf("expected dueStateSummary=scheduled, got %+v", schedulePayload)
	}
	if got, ok := schedulePayload["dueSummary"].(string); !ok || !strings.Contains(got, "delay scheduled at ") {
		t.Fatalf("expected dueSummary to describe scheduled delay, got %+v", schedulePayload)
	}
	if got, ok := schedulePayload["scheduleSummary"].(string); !ok || !strings.Contains(got, "message.received | delay scheduled at ") {
		t.Fatalf("expected scheduleSummary to combine event type and due summary, got %+v", schedulePayload)
	}
	if _, wrong := schedulePayload["Plan"]; wrong {
		t.Fatalf("expected flattened schedule payload, got %+v", schedulePayload)
	}
}

func TestConsoleAPIExposesComputedDueAtForPersistedDelayScheduleWhenDueAtMissing(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	createdAt := time.Now().UTC()
	plan := SchedulePlan{ID: "schedule-console-computed-dueat", Kind: ScheduleKindDelay, Delay: 45 * time.Second, Source: "runtime-demo-scheduler", EventType: "message.received"}
	if err := store.SaveSchedulePlan(context.Background(), storedSchedulePlan{Plan: plan, DueAt: nil, CreatedAt: createdAt, UpdatedAt: createdAt}); err != nil {
		t.Fatalf("save schedule plan: %v", err)
	}

	api := NewConsoleAPI(nil, nil, Config{}, nil, nil)
	api.SetScheduleReader(NewSQLiteConsoleScheduleReader(store))

	schedules, err := api.Schedules()
	if err != nil {
		t.Fatalf("console schedules: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("expected one schedule, got %+v", schedules)
	}
	if schedules[0].DueAt == nil {
		t.Fatalf("expected computed dueAt, got %+v", schedules[0])
	}
	want := createdAt.Add(plan.Delay)
	if !schedules[0].DueAt.Equal(want) {
		t.Fatalf("expected dueAt=%s, got %+v", want, schedules[0])
	}
	if schedules[0].DueReady {
		t.Fatalf("expected dueReady=false for future computed dueAt, got %+v", schedules[0])
	}
	if schedules[0].Overdue {
		t.Fatalf("expected overdue=false for future computed dueAt, got %+v", schedules[0])
	}
	if schedules[0].DueAtSource != "startup-recovery" || schedules[0].DueAtEvidence != scheduleDueAtEvidenceRecoveredStartup || schedules[0].DueAtPersisted {
		t.Fatalf("expected computed delay dueAt from still-missing persisted row to remain non-persisted startup recovery evidence, got %+v", schedules[0])
	}
	if schedules[0].DueStateSummary != "scheduled" {
		t.Fatalf("expected dueStateSummary=scheduled, got %+v", schedules[0])
	}
	if !strings.Contains(schedules[0].DueSummary, "startup-recovered dueAt") {
		t.Fatalf("expected dueSummary to explain startup-recovered dueAt evidence, got %+v", schedules[0])
	}
}

func TestConsoleAPIExposesComputedDueAtForPersistedOneShotScheduleWhenDueAtMissing(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	executeAt := time.Now().UTC().Add(45 * time.Second)
	plan := SchedulePlan{ID: "schedule-console-computed-oneshot-dueat", Kind: ScheduleKindOneShot, ExecuteAt: executeAt, Source: "runtime-demo-scheduler", EventType: "message.received"}
	if err := store.SaveSchedulePlan(context.Background(), storedSchedulePlan{Plan: plan, DueAt: nil, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("save schedule plan: %v", err)
	}

	api := NewConsoleAPI(nil, nil, Config{}, nil, nil)
	api.SetScheduleReader(NewSQLiteConsoleScheduleReader(store))

	schedules, err := api.Schedules()
	if err != nil {
		t.Fatalf("console schedules: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("expected one schedule, got %+v", schedules)
	}
	if schedules[0].DueAt == nil {
		t.Fatalf("expected computed dueAt, got %+v", schedules[0])
	}
	if !schedules[0].DueAt.Equal(executeAt) {
		t.Fatalf("expected dueAt=%s, got %+v", executeAt, schedules[0])
	}
	if schedules[0].DueReady {
		t.Fatalf("expected dueReady=false for future one-shot dueAt, got %+v", schedules[0])
	}
	if schedules[0].Overdue {
		t.Fatalf("expected overdue=false for future one-shot dueAt, got %+v", schedules[0])
	}
	if schedules[0].DueAtSource != "startup-recovery" || schedules[0].DueAtEvidence != scheduleDueAtEvidenceRecoveredStartup || schedules[0].DueAtPersisted {
		t.Fatalf("expected computed one-shot dueAt from still-missing persisted row to remain non-persisted startup recovery evidence, got %+v", schedules[0])
	}
	if schedules[0].DueStateSummary != "scheduled" {
		t.Fatalf("expected dueStateSummary=scheduled, got %+v", schedules[0])
	}
	if schedules[0].DueSummary == "" || !strings.Contains(schedules[0].DueSummary, "one-shot scheduled at ") || !strings.Contains(schedules[0].DueSummary, "startup-recovered dueAt") {
		t.Fatalf("expected dueSummary to describe scheduled one-shot, got %+v", schedules[0])
	}
	if schedules[0].ScheduleSummary == "" || !strings.Contains(schedules[0].ScheduleSummary, "message.received | one-shot scheduled at ") || !strings.Contains(schedules[0].ScheduleSummary, "startup-recovered dueAt") {
		t.Fatalf("expected scheduleSummary to describe scheduled one-shot, got %+v", schedules[0])
	}
}

func TestConsoleAPIExposesComputedDueAtForPersistedCronScheduleWhenDueAtMissing(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	createdAt := time.Date(2026, 4, 9, 15, 0, 0, 0, time.UTC)
	plan := SchedulePlan{ID: "schedule-console-computed-cron-dueat", Kind: ScheduleKindCron, CronExpr: "* * * * *", Source: "runtime-demo-scheduler", EventType: "message.received"}
	if err := store.SaveSchedulePlan(context.Background(), storedSchedulePlan{Plan: plan, DueAt: nil, CreatedAt: createdAt, UpdatedAt: createdAt}); err != nil {
		t.Fatalf("save schedule plan: %v", err)
	}

	api := NewConsoleAPI(nil, nil, Config{}, nil, nil)
	api.SetScheduleReader(NewSQLiteConsoleScheduleReader(store))

	schedules, err := api.Schedules()
	if err != nil {
		t.Fatalf("console schedules: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("expected one schedule, got %+v", schedules)
	}
	if schedules[0].DueAt == nil {
		t.Fatalf("expected computed dueAt, got %+v", schedules[0])
	}
	want := createdAt.Add(time.Minute)
	if !schedules[0].DueAt.Equal(want) {
		t.Fatalf("expected dueAt=%s, got %+v", want, schedules[0])
	}
	if !schedules[0].DueReady {
		t.Fatalf("expected dueReady=true for restored overdue cron dueAt, got %+v", schedules[0])
	}
	if !schedules[0].Overdue {
		t.Fatalf("expected overdue=true for restored overdue cron dueAt, got %+v", schedules[0])
	}
	if schedules[0].DueAtSource != "startup-recovery" || schedules[0].DueAtEvidence != scheduleDueAtEvidenceRecoveredStartup || schedules[0].DueAtPersisted {
		t.Fatalf("expected computed cron dueAt from still-missing persisted row to remain non-persisted startup recovery evidence, got %+v", schedules[0])
	}
	if schedules[0].DueStateSummary != "due" {
		t.Fatalf("expected dueStateSummary=due, got %+v", schedules[0])
	}
	if schedules[0].DueSummary == "" || !strings.Contains(schedules[0].DueSummary, "cron due at ") || !strings.Contains(schedules[0].DueSummary, "startup-recovered dueAt") {
		t.Fatalf("expected dueSummary to describe overdue cron, got %+v", schedules[0])
	}
	if schedules[0].ScheduleSummary == "" || !strings.Contains(schedules[0].ScheduleSummary, "message.received | cron due at ") || !strings.Contains(schedules[0].ScheduleSummary, "startup-recovered dueAt") {
		t.Fatalf("expected scheduleSummary to describe overdue cron, got %+v", schedules[0])
	}
}

func TestConsoleAPIExposesPersistedDueScheduleAsDue(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	createdAt := time.Now().UTC().Add(-2 * time.Minute)
	dueAt := createdAt.Add(-30 * time.Second)
	if err := store.SaveSchedulePlan(context.Background(), storedSchedulePlan{
		Plan: SchedulePlan{
			ID:        "schedule-console-due-1",
			Kind:      ScheduleKindDelay,
			Delay:     30 * time.Second,
			Source:    "runtime-demo-scheduler",
			EventType: "message.received",
		},
		DueAt:     &dueAt,
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}); err != nil {
		t.Fatalf("save schedule plan: %v", err)
	}

	api := NewConsoleAPI(nil, nil, Config{}, nil, nil)
	api.SetScheduleReader(NewSQLiteConsoleScheduleReader(store))

	schedules, err := api.Schedules()
	if err != nil {
		t.Fatalf("console schedules: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("expected one schedule, got %+v", schedules)
	}
	if !schedules[0].DueReady {
		t.Fatalf("expected dueReady=true, got %+v", schedules[0])
	}
	if !schedules[0].Overdue {
		t.Fatalf("expected overdue=true while keeping dueReady compatibility, got %+v", schedules[0])
	}
	if schedules[0].DueAtSource != "persisted-state" || schedules[0].DueAtEvidence != scheduleDueAtEvidencePersisted || !schedules[0].DueAtPersisted {
		t.Fatalf("expected explicit dueAt to be labeled as persisted-state evidence, got %+v", schedules[0])
	}
	if schedules[0].DueStateSummary != "due" {
		t.Fatalf("expected dueStateSummary=due, got %+v", schedules[0])
	}
	if schedules[0].DueSummary == "" || !strings.Contains(schedules[0].DueSummary, "delay due at ") || !strings.Contains(schedules[0].DueSummary, "persisted dueAt") {
		t.Fatalf("expected dueSummary to describe due delay, got %+v", schedules[0])
	}
}

func TestConsoleAPIExposesClaimedRecoveredScheduleVisibility(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	createdAt := time.Now().UTC().Add(-5 * time.Minute)
	dueAt := createdAt.Add(-30 * time.Second)
	claimedAt := createdAt.Add(-15 * time.Second)
	if err := store.SaveSchedulePlan(context.Background(), storedSchedulePlan{
		Plan: SchedulePlan{
			ID:        "schedule-console-claimed-recovered",
			Kind:      ScheduleKindDelay,
			Delay:     30 * time.Second,
			Source:    "runtime-demo-scheduler",
			EventType: "message.received",
		},
		DueAt:         &dueAt,
		DueAtEvidence: scheduleDueAtEvidenceRecoveredClaim,
		ClaimOwner:    "runtime-local:dead-runtime",
		ClaimedAt:     &claimedAt,
		CreatedAt:     createdAt,
		UpdatedAt:     claimedAt,
	}); err != nil {
		t.Fatalf("save claimed recovered schedule plan: %v", err)
	}

	api := NewConsoleAPI(nil, nil, Config{}, nil, nil)
	api.SetScheduleReader(NewSQLiteConsoleScheduleReader(store))

	schedules, err := api.Schedules()
	if err != nil {
		t.Fatalf("console schedules: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("expected one schedule, got %+v", schedules)
	}
	if !schedules[0].Claimed || schedules[0].ClaimOwner != "runtime-local:dead-runtime" || schedules[0].ClaimedAt == nil || !schedules[0].ClaimedAt.Equal(claimedAt) {
		t.Fatalf("expected claimed ownership visibility, got %+v", schedules[0])
	}
	if schedules[0].RecoveryState != "startup-recovered-abandoned-claim" {
		t.Fatalf("expected abandoned claim recovery state, got %+v", schedules[0])
	}
	if schedules[0].DueAtSource != "startup-claimed-recovery" || schedules[0].DueAtEvidence != scheduleDueAtEvidenceRecoveredClaim {
		t.Fatalf("expected claimed recovery dueAt provenance, got %+v", schedules[0])
	}
	if schedules[0].ScheduleSummary == "" || !strings.Contains(schedules[0].ScheduleSummary, "claimed by runtime-local:dead-runtime") || !strings.Contains(schedules[0].ScheduleSummary, "startup-recovered abandoned claimed dueAt") {
		t.Fatalf("expected schedule summary to include claim and recovery evidence, got %+v", schedules[0])
	}
}

func TestConsoleScheduleOverdue(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	future := now.Add(1 * time.Hour)
	past := now.Add(-1 * time.Hour)
	for _, tc := range []struct {
		name  string
		dueAt *time.Time
		want  bool
	}{
		{name: "missing dueAt", want: false},
		{name: "future dueAt", dueAt: &future, want: false},
		{name: "equal dueAt", dueAt: &now, want: false},
		{name: "past dueAt", dueAt: &past, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := consoleScheduleOverdue(tc.dueAt, now); got != tc.want {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}
		})
	}
}

func TestConsoleScheduleStateSummary(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	future := now.Add(1 * time.Hour)
	past := now.Add(-1 * time.Hour)
	for _, tc := range []struct {
		name  string
		dueAt *time.Time
		ready bool
		want  string
	}{
		{name: "missing dueAt", want: ""},
		{name: "future schedule", dueAt: &future, ready: false, want: "scheduled"},
		{name: "past due schedule", dueAt: &past, ready: true, want: "due"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := consoleScheduleStateSummary(tc.dueAt, tc.ready); got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

func TestConsoleScheduleDueSummary(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		kind     string
		dueAt    *time.Time
		ready    bool
		evidence string
		want     string
	}{
		{name: "missing dueAt", kind: "delay", want: ""},
		{name: "scheduled delay persisted", kind: "delay", dueAt: &now, ready: false, evidence: scheduleDueAtEvidencePersisted, want: "delay scheduled at 2026-04-09T12:00:00Z (persisted dueAt)"},
		{name: "due delay recovered", kind: "delay", dueAt: &now, ready: true, evidence: scheduleDueAtEvidenceRecoveredStartup, want: "delay due at 2026-04-09T12:00:00Z (startup-recovered dueAt)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := consoleScheduleDueSummary(tc.kind, tc.dueAt, tc.ready, tc.evidence); got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

func TestConsoleScheduleSummary(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		eventType string
		kind      string
		dueAt     *time.Time
		ready     bool
		evidence  string
		want      string
	}{
		{name: "missing event and due", want: ""},
		{name: "event only", eventType: "message.received", want: "message.received"},
		{name: "due only", kind: "delay", dueAt: &now, evidence: scheduleDueAtEvidencePersisted, want: "delay scheduled at 2026-04-09T12:00:00Z (persisted dueAt)"},
		{name: "event and due", eventType: "message.received", kind: "delay", dueAt: &now, evidence: scheduleDueAtEvidenceRecoveredStartup, want: "message.received | delay scheduled at 2026-04-09T12:00:00Z (startup-recovered dueAt)"},
		{name: "event due and claim", eventType: "message.received", kind: "delay", dueAt: &now, evidence: scheduleDueAtEvidenceRecoveredClaim, want: "message.received | delay scheduled at 2026-04-09T12:00:00Z (startup-recovered abandoned claimed dueAt)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := consoleScheduleSummary(tc.eventType, tc.kind, tc.dueAt, tc.ready, tc.evidence, "", nil); got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

func TestConsoleScheduleClaimSummary(t *testing.T) {
	t.Parallel()

	claimedAt := time.Date(2026, 4, 9, 12, 30, 0, 0, time.UTC)
	if got := consoleScheduleClaimSummary("runtime-local:test-scheduler", &claimedAt); got != "claimed by runtime-local:test-scheduler at 2026-04-09T12:30:00Z" {
		t.Fatalf("unexpected claim summary %q", got)
	}
	if got := consoleScheduleClaimSummary("", &claimedAt); got != "" {
		t.Fatalf("expected empty summary without owner, got %q", got)
	}
}

func TestConsoleScheduleDueReady(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	future := now.Add(1 * time.Hour)
	past := now.Add(-1 * time.Hour)
	for _, tc := range []struct {
		name  string
		dueAt *time.Time
		want  bool
	}{
		{name: "missing dueAt", want: false},
		{name: "future dueAt", dueAt: &future, want: false},
		{name: "past dueAt", dueAt: &past, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := consoleScheduleDueReady(tc.dueAt, now); got != tc.want {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}
		})
	}
}

func TestConsoleAPIExposesPersistedJobsAndStatusFromStore(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	job := NewJob("job-console-persisted", "ai.chat", 2, 30*time.Second)
	job.TraceID = "trace-console-job"
	job.EventID = "evt-console-job"
	job.Correlation = "corr-console-job"
	job.Status = JobStatusPending
	job.Payload = map[string]any{
		"prompt": "hello persisted",
		"dispatch": map[string]any{
			"target_plugin_id": "plugin-ai-chat",
			"permission":       "job:run",
			"actor":            "runtime-job-runner",
		},
		"reply_handle": map[string]any{
			"capability": "onebot.reply",
			"target_id":  "group-42",
		},
		"reply_target": "group-42",
		"session_id":   "session-user-99",
	}
	if err := store.SaveJob(context.Background(), job); err != nil {
		t.Fatalf("save job: %v", err)
	}

	api := NewConsoleAPI(nil, nil, Config{}, nil, nil)
	api.SetJobReader(NewSQLiteConsoleJobReader(store))

	if got, err := api.Jobs(); err != nil || len(got) != 1 || got[0].ID != job.ID {
		t.Fatalf("expected persisted jobs to be exposed, got %+v", got)
	}
	status, err := api.Status()
	if err != nil {
		t.Fatalf("console status: %v", err)
	}
	if status.Jobs != 1 {
		t.Fatalf("expected persisted job status count, got %+v", status)
	}

	rendered, err := api.RenderJSON()
	if err != nil {
		t.Fatalf("render json: %v", err)
	}
	for _, expected := range []string{"job-console-persisted", `"jobs": 1`, "corr-console-job"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("expected rendered console to contain %q, got %s", expected, rendered)
		}
	}
	if strings.Contains(rendered, "hello persisted") {
		t.Fatalf("expected persisted job payload to be redacted, got %s", rendered)
	}
	if !strings.Contains(rendered, `"redacted": true`) || !strings.Contains(rendered, `"keys": [`) {
		t.Fatalf("expected sanitized job payload metadata, got %s", rendered)
	}
	var payload struct {
		Jobs []map[string]any `json:"jobs"`
	}
	if err := json.Unmarshal([]byte(rendered), &payload); err != nil {
		t.Fatalf("decode persisted console payload: %v", err)
	}
	if len(payload.Jobs) != 1 {
		t.Fatalf("expected one persisted job in payload, got %+v", payload.Jobs)
	}
	jobPayload := payload.Jobs[0]
	for key, want := range map[string]any{
		"targetPluginId":          "plugin-ai-chat",
		"dispatchMetadataPresent": true,
		"dispatchContractPresent": true,
		"queueContractComplete":   true,
		"dispatchReady":           true,
		"queueStateSummary":       "ready",
		"dispatchSummary":         "runtime-job-runner -> plugin-ai-chat [job:run]",
		"queueContractSummary":    "runtime-job-runner -> plugin-ai-chat [job:run] | onebot.reply -> group-42",
		"dispatchPermission":      "job:run",
		"dispatchActor":           "runtime-job-runner",
		"replyHandlePresent":      true,
		"replyHandleCapability":   "onebot.reply",
		"replyContractPresent":    true,
		"replySummary":            "onebot.reply -> group-42",
		"sessionIDPresent":        true,
		"replyTargetPresent":      true,
	} {
		if got, ok := jobPayload[key]; !ok || got != want {
			t.Fatalf("expected persisted job %q=%v, got %+v", key, want, jobPayload)
		}
	}
}

func TestConsoleAPIExposesPersistedRetryingJobAsWaitingRetry(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	nextRunAt := time.Now().UTC().Add(1 * time.Hour)
	job := NewJob("job-console-persisted-waiting-retry", "ai.chat", 2, 30*time.Second)
	job.Status = JobStatusRetrying
	job.NextRunAt = &nextRunAt
	job.Payload = map[string]any{
		"dispatch": map[string]any{
			"target_plugin_id": "plugin-ai-chat",
			"permission":       "job:run",
			"actor":            "runtime-job-runner",
		},
		"reply_handle": map[string]any{
			"capability": "onebot.reply",
			"target_id":  "group-42",
		},
		"reply_target": "group-42",
	}
	if err := store.SaveJob(context.Background(), job); err != nil {
		t.Fatalf("save job: %v", err)
	}

	api := NewConsoleAPI(nil, nil, Config{}, nil, nil)
	api.SetJobReader(NewSQLiteConsoleJobReader(store))

	jobs, err := api.Jobs()
	if err != nil {
		t.Fatalf("console jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected one job, got %+v", jobs)
	}
	if jobs[0].DispatchReady {
		t.Fatalf("expected dispatchReady=false, got %+v", jobs[0])
	}
	if jobs[0].QueueStateSummary != "waiting-retry" {
		t.Fatalf("expected queueStateSummary=waiting-retry, got %+v", jobs[0])
	}
}

func TestConsoleAPIExposesPersistedPartialReplyContractSummary(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	job := NewJob("job-console-persisted-partial-reply", "ai.chat", 2, 30*time.Second)
	job.Status = JobStatusPending
	job.Payload = map[string]any{
		"dispatch": map[string]any{
			"target_plugin_id": "plugin-ai-chat",
			"permission":       "job:run",
			"actor":            "runtime-job-runner",
		},
		"reply_handle": map[string]any{
			"capability": "onebot.reply",
			"target_id":  "group-42",
		},
	}
	if err := store.SaveJob(context.Background(), job); err != nil {
		t.Fatalf("save job: %v", err)
	}

	api := NewConsoleAPI(nil, nil, Config{}, nil, nil)
	api.SetJobReader(NewSQLiteConsoleJobReader(store))

	jobs, err := api.Jobs()
	if err != nil {
		t.Fatalf("console jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected one job, got %+v", jobs)
	}
	if !jobs[0].DispatchReady {
		t.Fatalf("expected dispatchReady=true, got %+v", jobs[0])
	}
	if !jobs[0].DispatchContractPresent {
		t.Fatalf("expected dispatchContractPresent=true, got %+v", jobs[0])
	}
	if jobs[0].QueueContractComplete {
		t.Fatalf("expected queueContractComplete=false, got %+v", jobs[0])
	}
	if jobs[0].ReplyContractPresent {
		t.Fatalf("expected replyContractPresent=false, got %+v", jobs[0])
	}
	if jobs[0].QueueStateSummary != "ready" {
		t.Fatalf("expected queueStateSummary=ready, got %+v", jobs[0])
	}
	if jobs[0].QueueContractSummary != "runtime-job-runner -> plugin-ai-chat [job:run] | onebot.reply" {
		t.Fatalf("expected partial queueContractSummary, got %+v", jobs[0])
	}
}

func TestConsoleAPIExposesPersistedJobWithoutDispatchMetadataAsNonDispatchable(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	job := NewJob("job-console-persisted-no-dispatch", "ai.chat", 2, 30*time.Second)
	job.Status = JobStatusPending
	job.Payload = map[string]any{
		"reply_handle": map[string]any{
			"capability": "onebot.reply",
			"target_id":  "group-42",
		},
		"reply_target": "group-42",
	}
	if err := store.SaveJob(context.Background(), job); err != nil {
		t.Fatalf("save job: %v", err)
	}

	api := NewConsoleAPI(nil, nil, Config{}, nil, nil)
	api.SetJobReader(NewSQLiteConsoleJobReader(store))

	jobs, err := api.Jobs()
	if err != nil {
		t.Fatalf("console jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected one job, got %+v", jobs)
	}
	if jobs[0].DispatchMetadataPresent {
		t.Fatalf("expected dispatchMetadataPresent=false, got %+v", jobs[0])
	}
	if jobs[0].DispatchContractPresent {
		t.Fatalf("expected dispatchContractPresent=false, got %+v", jobs[0])
	}
	if jobs[0].QueueContractComplete {
		t.Fatalf("expected queueContractComplete=false, got %+v", jobs[0])
	}
	if jobs[0].DispatchReady {
		t.Fatalf("expected dispatchReady=false, got %+v", jobs[0])
	}
	if jobs[0].QueueStateSummary != string(JobStatusPending) {
		t.Fatalf("expected queueStateSummary=pending, got %+v", jobs[0])
	}
	if jobs[0].DispatchSummary != "" {
		t.Fatalf("expected empty dispatchSummary, got %+v", jobs[0])
	}
	if jobs[0].QueueContractSummary != "onebot.reply -> group-42" {
		t.Fatalf("expected reply-only queueContractSummary, got %+v", jobs[0])
	}
}

func TestConsoleAPIExposesPersistedDispatchOnlyJobAsReadyIncompleteContract(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	job := NewJob("job-console-persisted-dispatch-only", "ai.chat", 2, 30*time.Second)
	job.Status = JobStatusPending
	job.Payload = map[string]any{
		"dispatch": map[string]any{
			"target_plugin_id": "plugin-ai-chat",
			"permission":       "job:run",
			"actor":            "runtime-job-runner",
		},
	}
	if err := store.SaveJob(context.Background(), job); err != nil {
		t.Fatalf("save job: %v", err)
	}

	api := NewConsoleAPI(nil, nil, Config{}, nil, nil)
	api.SetJobReader(NewSQLiteConsoleJobReader(store))

	jobs, err := api.Jobs()
	if err != nil {
		t.Fatalf("console jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected one job, got %+v", jobs)
	}
	if !jobs[0].DispatchMetadataPresent {
		t.Fatalf("expected dispatchMetadataPresent=true, got %+v", jobs[0])
	}
	if jobs[0].DispatchContractPresent {
		t.Fatalf("expected dispatchContractPresent=false without reply_handle, got %+v", jobs[0])
	}
	if jobs[0].QueueContractComplete {
		t.Fatalf("expected queueContractComplete=false, got %+v", jobs[0])
	}
	if !jobs[0].DispatchReady {
		t.Fatalf("expected dispatchReady=true for pending dispatch-only job, got %+v", jobs[0])
	}
	if jobs[0].ReplyContractPresent {
		t.Fatalf("expected replyContractPresent=false, got %+v", jobs[0])
	}
	if jobs[0].QueueStateSummary != "ready-incomplete-contract" {
		t.Fatalf("expected queueStateSummary=ready-incomplete-contract, got %+v", jobs[0])
	}
	if jobs[0].DispatchSummary != "runtime-job-runner -> plugin-ai-chat [job:run]" {
		t.Fatalf("expected dispatchSummary, got %+v", jobs[0])
	}
	if jobs[0].QueueContractSummary != "runtime-job-runner -> plugin-ai-chat [job:run]" {
		t.Fatalf("expected dispatch-only queueContractSummary, got %+v", jobs[0])
	}
}

func TestConsoleAPIExposesPersistedJobWithoutDispatchOrReplyContractAsBarePending(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	job := NewJob("job-console-persisted-no-contract", "ai.chat", 2, 30*time.Second)
	job.Status = JobStatusPending
	job.Payload = map[string]any{
		"prompt": "hello persisted",
	}
	if err := store.SaveJob(context.Background(), job); err != nil {
		t.Fatalf("save job: %v", err)
	}

	api := NewConsoleAPI(nil, nil, Config{}, nil, nil)
	api.SetJobReader(NewSQLiteConsoleJobReader(store))

	jobs, err := api.Jobs()
	if err != nil {
		t.Fatalf("console jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected one job, got %+v", jobs)
	}
	if jobs[0].DispatchMetadataPresent {
		t.Fatalf("expected dispatchMetadataPresent=false, got %+v", jobs[0])
	}
	if jobs[0].DispatchContractPresent {
		t.Fatalf("expected dispatchContractPresent=false, got %+v", jobs[0])
	}
	if jobs[0].ReplyContractPresent {
		t.Fatalf("expected replyContractPresent=false, got %+v", jobs[0])
	}
	if jobs[0].DispatchReady {
		t.Fatalf("expected dispatchReady=false, got %+v", jobs[0])
	}
	if jobs[0].QueueStateSummary != string(JobStatusPending) {
		t.Fatalf("expected queueStateSummary=pending, got %+v", jobs[0])
	}
	if jobs[0].DispatchSummary != "" || jobs[0].ReplySummary != "" || jobs[0].QueueContractSummary != "" {
		t.Fatalf("expected empty summaries for no-contract job, got %+v", jobs[0])
	}
}

func TestConsoleAPIPropagatesScheduleReadFailuresOverHTTP(t *testing.T) {
	t.Parallel()

	api := NewConsoleAPI(nil, nil, Config{}, nil, nil)
	api.SetScheduleReader(failingConsoleScheduleReader{err: errors.New("schedule store unavailable")})

	req := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	resp := httptest.NewRecorder()
	api.ServeHTTP(resp, req)

	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "schedule store unavailable") {
		t.Fatalf("expected console error response to mention schedule read failure, got %s", resp.Body.String())
	}
}

func TestConsoleAPIPropagatesJobReadFailuresOverHTTP(t *testing.T) {
	t.Parallel()

	api := NewConsoleAPI(nil, nil, Config{}, nil, nil)
	api.SetJobReader(failingConsoleJobReader{err: errors.New("job store unavailable")})

	req := httptest.NewRequest(http.MethodGet, "/api/console", nil)
	resp := httptest.NewRecorder()
	api.ServeHTTP(resp, req)

	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "job store unavailable") {
		t.Fatalf("expected console error response to mention job read failure, got %s", resp.Body.String())
	}
}

type failingConsoleScheduleReader struct{ err error }

func (r failingConsoleScheduleReader) ListSchedulePlans() ([]ConsoleSchedule, error) {
	return nil, r.err
}

type staticConsoleScheduleReader struct{ schedules []ConsoleSchedule }

func (r staticConsoleScheduleReader) ListSchedulePlans() ([]ConsoleSchedule, error) {
	return append([]ConsoleSchedule(nil), r.schedules...), nil
}

type failingConsoleJobReader struct{ err error }

func (r failingConsoleJobReader) ListJobs() ([]Job, error) {
	return nil, r.err
}

type staticConsoleJobReader struct{ jobs []Job }

func (r staticConsoleJobReader) ListJobs() ([]Job, error) {
	return append([]Job(nil), r.jobs...), nil
}

type staticConsoleAlertReader struct{ alerts []AlertRecord }

func (r staticConsoleAlertReader) ListAlerts() ([]AlertRecord, error) {
	return append([]AlertRecord(nil), r.alerts...), nil
}

type noopConsoleHandler struct{}

func (noopConsoleHandler) OnEvent(event eventmodel.Event, ctx eventmodel.ExecutionContext) error {
	return nil
}

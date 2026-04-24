package pluginadmin

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

type lifecycleRecorder struct {
	enabled  []string
	disabled []string
	err      error
}

type rolloutRecorder struct {
	prepared        []string
	canaried        []string
	activated       []string
	rolledBack      []string
	checked         []string
	rollbackChecked []string
	err             error
	records         map[string]pluginsdk.RolloutRecord
	checkErr        error
	rollbackErr     error
}

type auditRecorder struct {
	entries []pluginsdk.AuditEntry
	err     error
}

type replayRecorder struct {
	eventIDs []string
	event    eventmodel.Event
	err      error
}

type providerRecorder struct {
	authorizer *pluginsdk.Authorizer
}

func (p *providerRecorder) CurrentAuthorizer() *pluginsdk.Authorizer {
	if p == nil {
		return nil
	}
	return p.authorizer
}

func (r *auditRecorder) RecordAudit(entry pluginsdk.AuditEntry) error {
	r.entries = append(r.entries, entry)
	return r.err
}

func auditReasonValue(entry pluginsdk.AuditEntry) string {
	value := reflect.ValueOf(entry)
	field := value.FieldByName("Reason")
	if field.IsValid() && field.Kind() == reflect.String {
		return field.String()
	}
	return ""
}

func auditErrorCategoryValue(entry pluginsdk.AuditEntry) string {
	value := reflect.ValueOf(entry)
	field := value.FieldByName("ErrorCategory")
	if field.IsValid() && field.Kind() == reflect.String {
		return field.String()
	}
	return ""
}

func auditErrorCodeValue(entry pluginsdk.AuditEntry) string {
	value := reflect.ValueOf(entry)
	field := value.FieldByName("ErrorCode")
	if field.IsValid() && field.Kind() == reflect.String {
		return field.String()
	}
	return ""
}

func (r *replayRecorder) ReplayEvent(eventID string) (eventmodel.Event, error) {
	r.eventIDs = append(r.eventIDs, eventID)
	return r.event, r.err
}

func (r *rolloutRecorder) Prepare(pluginID string) (pluginsdk.RolloutRecord, error) {
	r.prepared = append(r.prepared, pluginID)
	record := pluginsdk.RolloutRecord{PluginID: pluginID, Status: pluginsdk.RolloutStatusPrepared}
	if r.records == nil {
		r.records = map[string]pluginsdk.RolloutRecord{}
	}
	r.records[pluginID] = record
	return record, r.err
}

func (r *rolloutRecorder) Activate(pluginID string) (pluginsdk.RolloutRecord, error) {
	r.activated = append(r.activated, pluginID)
	record := pluginsdk.RolloutRecord{PluginID: pluginID, Phase: pluginsdk.RolloutPhaseStable, Status: pluginsdk.RolloutStatusActivated}
	if r.records == nil {
		r.records = map[string]pluginsdk.RolloutRecord{}
	}
	r.records[pluginID] = record
	return record, r.err
}

func (r *rolloutRecorder) Canary(pluginID string) (pluginsdk.RolloutRecord, error) {
	r.canaried = append(r.canaried, pluginID)
	record := pluginsdk.RolloutRecord{PluginID: pluginID, Phase: pluginsdk.RolloutPhaseCanary, Status: pluginsdk.RolloutStatusCanarying}
	if r.records == nil {
		r.records = map[string]pluginsdk.RolloutRecord{}
	}
	r.records[pluginID] = record
	return record, r.err
}

func (r *rolloutRecorder) CanActivate(pluginID string) (pluginsdk.RolloutRecord, error) {
	r.checked = append(r.checked, pluginID)
	if r.checkErr != nil {
		return pluginsdk.RolloutRecord{}, r.checkErr
	}
	if r.records == nil {
		return pluginsdk.RolloutRecord{}, errors.New("rollout is not prepared")
	}
	record, ok := r.records[pluginID]
	if !ok || record.Status != pluginsdk.RolloutStatusPrepared {
		return pluginsdk.RolloutRecord{}, errors.New("rollout is not prepared")
	}
	return record, nil
}

func (r *rolloutRecorder) CanRollback(pluginID string) (pluginsdk.RolloutRecord, error) {
	r.rollbackChecked = append(r.rollbackChecked, pluginID)
	if r.rollbackErr != nil {
		return pluginsdk.RolloutRecord{}, r.rollbackErr
	}
	if r.records == nil {
		return pluginsdk.RolloutRecord{}, errors.New("invalid rollout transition")
	}
	record, ok := r.records[pluginID]
	if !ok || (record.Phase != pluginsdk.RolloutPhaseCanary && record.Status != pluginsdk.RolloutStatusActivated) {
		return pluginsdk.RolloutRecord{}, errors.New("invalid rollout transition")
	}
	return record, nil
}

func (r *rolloutRecorder) Rollback(pluginID string) (pluginsdk.RolloutRecord, error) {
	r.rolledBack = append(r.rolledBack, pluginID)
	record := pluginsdk.RolloutRecord{PluginID: pluginID, Phase: pluginsdk.RolloutPhaseRollback, Status: pluginsdk.RolloutStatusRolledBack}
	if r.records == nil {
		r.records = map[string]pluginsdk.RolloutRecord{}
	}
	r.records[pluginID] = record
	return record, r.err
}

func (r *rolloutRecorder) Record(pluginID string) (pluginsdk.RolloutRecord, bool) {
	if r.records == nil {
		return pluginsdk.RolloutRecord{}, false
	}
	record, ok := r.records[pluginID]
	return record, ok
}

func (l *lifecycleRecorder) Enable(pluginID string) error {
	l.enabled = append(l.enabled, pluginID)
	return l.err
}

func (l *lifecycleRecorder) Disable(pluginID string) error {
	l.disabled = append(l.disabled, pluginID)
	return l.err
}

func adminPolicies() map[string]RolePolicy {
	return map[string]RolePolicy{
		"admin":            {Permissions: []string{"plugin:enable", "plugin:disable", "plugin:replay", "plugin:rollout"}, PluginScope: []string{"*"}},
		"echo-operator":    {Permissions: []string{"plugin:enable"}, PluginScope: []string{"plugin-echo"}},
		"enable-any":       {Permissions: []string{"plugin:enable"}, PluginScope: []string{}},
		"scope-echo-only":  {Permissions: []string{}, PluginScope: []string{"plugin-echo"}},
		"rollout-operator": {Permissions: []string{"plugin:rollout"}, PluginScope: []string{"plugin-echo"}},
		"viewer":           {Permissions: []string{"plugin:disable"}, PluginScope: []string{"*"}},
		"replay-operator":  {Permissions: []string{"plugin:replay"}, EventScope: []string{"evt-allowed"}},
	}
}

func TestPluginAdminExecutesEnableDisableCommands(t *testing.T) {
	t.Parallel()

	lifecycle := &lifecycleRecorder{}
	audit := &auditRecorder{}
	plugin := New(lifecycle, nil, nil, nil, map[string][]string{"admin-user": {"admin"}}, adminPolicies(), audit)
	plugin.CurrentTime = func() time.Time { return time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC) }

	if err := plugin.OnCommand(eventmodel.CommandInvocation{
		Name: "admin",
		Raw:  "/admin enable plugin-echo",
		Metadata: map[string]any{
			"actor": "admin-user",
		},
	}, eventmodel.ExecutionContext{TraceID: "trace-1", EventID: "evt-1"}); err != nil {
		t.Fatalf("enable command: %v", err)
	}
	if err := plugin.OnCommand(eventmodel.CommandInvocation{
		Name: "admin",
		Raw:  "/admin disable plugin-echo",
		Metadata: map[string]any{
			"actor": "admin-user",
		},
	}, eventmodel.ExecutionContext{TraceID: "trace-2", EventID: "evt-2"}); err != nil {
		t.Fatalf("disable command: %v", err)
	}

	if len(lifecycle.enabled) != 1 || lifecycle.enabled[0] != "plugin-echo" {
		t.Fatalf("unexpected enabled calls %+v", lifecycle.enabled)
	}
	if len(lifecycle.disabled) != 1 || lifecycle.disabled[0] != "plugin-echo" {
		t.Fatalf("unexpected disabled calls %+v", lifecycle.disabled)
	}
	if len(plugin.AuditLog()) != 2 || !plugin.AuditLog()[0].Allowed || !plugin.AuditLog()[1].Allowed {
		t.Fatalf("unexpected audit trail %+v", plugin.AuditLog())
	}
	if first := plugin.AuditLog()[0]; first.Action != "plugin.enable" || first.Permission != "plugin:enable" || first.Target != "plugin-echo" || first.TraceID != "trace-1" || first.EventID != "evt-1" || first.RunID != "run-evt-1" || first.CorrelationID != "evt-1" || first.Reason != "plugin_enabled" || auditErrorCategoryValue(first) != "operator" || auditErrorCodeValue(first) != "plugin_enabled" {
		t.Fatalf("unexpected normalized enable audit entry %+v", first)
	}
	if second := plugin.AuditLog()[1]; second.Action != "plugin.disable" || second.Permission != "plugin:disable" || second.Target != "plugin-echo" || second.TraceID != "trace-2" || second.EventID != "evt-2" || second.RunID != "run-evt-2" || second.CorrelationID != "evt-2" || second.Reason != "plugin_disabled" || auditErrorCategoryValue(second) != "operator" || auditErrorCodeValue(second) != "plugin_disabled" {
		t.Fatalf("unexpected normalized disable audit entry %+v", second)
	}
	if len(audit.entries) != 2 || audit.entries[0].Target != "plugin-echo" || audit.entries[0].OccurredAt != "2026-04-03T12:00:00Z" {
		t.Fatalf("unexpected external audit entries %+v", audit.entries)
	}
}

func TestPluginAdminCopiesSessionIDFromCommandMetadataIntoAudit(t *testing.T) {
	t.Parallel()

	audit := &auditRecorder{}
	plugin := New(&lifecycleRecorder{}, nil, nil, nil, map[string][]string{"admin-user": {"admin"}}, adminPolicies(), audit)
	if err := plugin.OnCommand(eventmodel.CommandInvocation{
		Name: "admin",
		Raw:  "/admin enable plugin-echo",
		Metadata: map[string]any{
			"actor":      "admin-user",
			"session_id": "session-operator-bearer-admin-user",
		},
	}, eventmodel.ExecutionContext{TraceID: "trace-session", EventID: "evt-session"}); err != nil {
		t.Fatalf("enable command with session metadata: %v", err)
	}
	if len(plugin.AuditLog()) != 1 || plugin.AuditLog()[0].SessionID != "session-operator-bearer-admin-user" {
		t.Fatalf("expected local audit to include session_id, got %+v", plugin.AuditLog())
	}
	if plugin.AuditLog()[0].RunID != "run-evt-session" || plugin.AuditLog()[0].CorrelationID != "evt-session" {
		t.Fatalf("expected local audit to normalize observability ids, got %+v", plugin.AuditLog())
	}
	if len(audit.entries) != 1 || audit.entries[0].SessionID != "session-operator-bearer-admin-user" {
		t.Fatalf("expected external audit to include session_id, got %+v", audit.entries)
	}
}

func TestPluginAdminRejectsUnauthorizedActorAndAudits(t *testing.T) {
	t.Parallel()

	plugin := New(&lifecycleRecorder{}, nil, nil, nil, map[string][]string{"admin-user": {"admin"}}, adminPolicies(), nil)
	err := plugin.OnCommand(eventmodel.CommandInvocation{
		Name: "admin",
		Raw:  "/admin enable plugin-echo",
		Metadata: map[string]any{
			"actor": "guest-user",
		},
	}, eventmodel.ExecutionContext{TraceID: "trace-3", EventID: "evt-3"})
	if err == nil {
		t.Fatal("expected unauthorized actor to fail")
	}
	if len(plugin.AuditLog()) != 1 || plugin.AuditLog()[0].Allowed || plugin.AuditLog()[0].Permission != "plugin:enable" || plugin.AuditLog()[0].Action != "plugin.enable" || auditErrorCategoryValue(plugin.AuditLog()[0]) != "authorization" || auditErrorCodeValue(plugin.AuditLog()[0]) != "permission_denied" {
		t.Fatalf("expected denied audit record, got %+v", plugin.AuditLog())
	}
}

func TestPluginAdminBubblesLifecycleError(t *testing.T) {
	t.Parallel()

	plugin := New(&lifecycleRecorder{err: errors.New("enable failed")}, nil, nil, nil, map[string][]string{"admin-user": {"admin"}}, adminPolicies(), nil)
	err := plugin.OnCommand(eventmodel.CommandInvocation{
		Name: "admin",
		Raw:  "/admin enable plugin-echo",
		Metadata: map[string]any{
			"actor": "admin-user",
		},
	}, eventmodel.ExecutionContext{TraceID: "trace-4", EventID: "evt-4"})
	if err == nil {
		t.Fatal("expected lifecycle error to bubble up")
	}
	if len(plugin.AuditLog()) != 1 || !plugin.AuditLog()[0].Allowed || plugin.AuditLog()[0].Permission != "plugin:enable" || plugin.AuditLog()[0].Action != "plugin.enable" || auditErrorCategoryValue(plugin.AuditLog()[0]) != "operator" || auditErrorCodeValue(plugin.AuditLog()[0]) != "action_failed" {
		t.Fatalf("expected failed audit record, got %+v", plugin.AuditLog())
	}
}

func TestPluginAdminManifestAdoptsV1Contract(t *testing.T) {
	t.Parallel()

	plugin := New(&lifecycleRecorder{}, nil, nil, nil, map[string][]string{"admin-user": {"admin"}}, adminPolicies(), nil)
	manifest := plugin.Manifest

	if manifest.SchemaVersion != pluginsdk.SupportedPluginManifestSchemaVersion {
		t.Fatalf("manifest schemaVersion = %q, want %q", manifest.SchemaVersion, pluginsdk.SupportedPluginManifestSchemaVersion)
	}
	if manifest.Publish == nil {
		t.Fatal("manifest publish metadata is required")
	}
	if manifest.Publish.SourceType != pluginsdk.PublishSourceTypeGit {
		t.Fatalf("manifest publish sourceType = %q, want %q", manifest.Publish.SourceType, pluginsdk.PublishSourceTypeGit)
	}
	if manifest.Publish.SourceURI != pluginAdminPublishSourceURI {
		t.Fatalf("manifest publish sourceUri = %q, want %q", manifest.Publish.SourceURI, pluginAdminPublishSourceURI)
	}
	if manifest.Publish.RuntimeVersionRange != pluginAdminRuntimeVersionRange {
		t.Fatalf("manifest publish runtimeVersionRange = %q, want %q", manifest.Publish.RuntimeVersionRange, pluginAdminRuntimeVersionRange)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("expected plugin-admin manifest to validate, got %v", err)
	}
}

func TestPluginAdminReturnsAuditRecorderFailure(t *testing.T) {
	t.Parallel()

	plugin := New(&lifecycleRecorder{}, nil, nil, nil, map[string][]string{"admin-user": {"admin"}}, adminPolicies(), &auditRecorder{err: errors.New("audit sink unavailable")})
	err := plugin.OnCommand(eventmodel.CommandInvocation{
		Name: "admin",
		Raw:  "/admin enable plugin-echo",
		Metadata: map[string]any{
			"actor": "admin-user",
		},
	}, eventmodel.ExecutionContext{TraceID: "trace-5", EventID: "evt-5"})
	if err == nil || !strings.Contains(err.Error(), "audit sink unavailable") {
		t.Fatalf("expected audit recorder failure, got %v", err)
	}
	if len(plugin.AuditLog()) != 1 || !plugin.AuditLog()[0].Allowed {
		t.Fatalf("expected local audit record to remain available, got %+v", plugin.AuditLog())
	}
}

func TestPluginAdminDoesNotCombinePermissionAndScopeAcrossRoles(t *testing.T) {
	t.Parallel()

	plugin := New(&lifecycleRecorder{}, nil, nil, nil, map[string][]string{"split-user": {"enable-any", "scope-echo-only"}}, adminPolicies(), nil)
	err := plugin.OnCommand(eventmodel.CommandInvocation{
		Name: "admin",
		Raw:  "/admin enable plugin-echo",
		Metadata: map[string]any{
			"actor": "split-user",
		},
	}, eventmodel.ExecutionContext{TraceID: "trace-8", EventID: "evt-8"})
	if err == nil || !strings.Contains(err.Error(), "plugin scope denied") {
		t.Fatalf("expected split-role authorization to be rejected, got %v", err)
	}
	if len(plugin.AuditLog()) != 1 || plugin.AuditLog()[0].Allowed || plugin.AuditLog()[0].Permission != "plugin:enable" || plugin.AuditLog()[0].Action != "plugin.enable" || auditReasonValue(plugin.AuditLog()[0]) != "plugin_scope_denied" || auditErrorCategoryValue(plugin.AuditLog()[0]) != "authorization" || auditErrorCodeValue(plugin.AuditLog()[0]) != "plugin_scope_denied" {
		t.Fatalf("expected denied split-role audit record, got %+v", plugin.AuditLog())
	}
}

func TestPluginAdminRejectsKnownActorWithoutRequiredPermission(t *testing.T) {
	t.Parallel()

	plugin := New(&lifecycleRecorder{}, nil, nil, nil, map[string][]string{"viewer-user": {"viewer"}}, adminPolicies(), nil)
	err := plugin.OnCommand(eventmodel.CommandInvocation{Name: "admin", Raw: "/admin enable plugin-echo", Metadata: map[string]any{"actor": "viewer-user"}}, eventmodel.ExecutionContext{TraceID: "trace-14", EventID: "evt-14"})
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("expected known actor without permission to be denied, got %v", err)
	}
	if len(plugin.AuditLog()) != 1 || plugin.AuditLog()[0].Allowed || plugin.AuditLog()[0].Permission != "plugin:enable" || plugin.AuditLog()[0].Action != "plugin.enable" || auditErrorCategoryValue(plugin.AuditLog()[0]) != "authorization" || auditErrorCodeValue(plugin.AuditLog()[0]) != "permission_denied" {
		t.Fatalf("expected denied wrong-permission audit record, got %+v", plugin.AuditLog())
	}
}

func TestPluginAdminRejectsOutOfScopePluginAndAuditsPermission(t *testing.T) {
	t.Parallel()

	plugin := New(&lifecycleRecorder{}, nil, nil, nil, map[string][]string{"echo-user": {"echo-operator"}}, adminPolicies(), nil)
	err := plugin.OnCommand(eventmodel.CommandInvocation{
		Name: "admin",
		Raw:  "/admin enable plugin-ai-chat",
		Metadata: map[string]any{
			"actor": "echo-user",
		},
	}, eventmodel.ExecutionContext{TraceID: "trace-6", EventID: "evt-6"})
	if err == nil || !strings.Contains(err.Error(), "plugin scope denied") {
		t.Fatalf("expected scope denial, got %v", err)
	}
	if len(plugin.AuditLog()) != 1 || plugin.AuditLog()[0].Allowed || plugin.AuditLog()[0].Permission != "plugin:enable" || plugin.AuditLog()[0].Action != "plugin.enable" || auditReasonValue(plugin.AuditLog()[0]) != "plugin_scope_denied" || auditErrorCategoryValue(plugin.AuditLog()[0]) != "authorization" || auditErrorCodeValue(plugin.AuditLog()[0]) != "plugin_scope_denied" {
		t.Fatalf("expected denied scoped audit record, got %+v", plugin.AuditLog())
	}
}

func TestPluginAdminAllowsScopedRoleForConfiguredPlugin(t *testing.T) {
	t.Parallel()

	lifecycle := &lifecycleRecorder{}
	plugin := New(lifecycle, nil, nil, nil, map[string][]string{"echo-user": {"echo-operator"}}, adminPolicies(), nil)
	err := plugin.OnCommand(eventmodel.CommandInvocation{
		Name: "admin",
		Raw:  "/admin enable plugin-echo",
		Metadata: map[string]any{
			"actor": "echo-user",
		},
	}, eventmodel.ExecutionContext{TraceID: "trace-7", EventID: "evt-7"})
	if err != nil {
		t.Fatalf("expected scoped enable to pass, got %v", err)
	}
	if len(lifecycle.enabled) != 1 || lifecycle.enabled[0] != "plugin-echo" {
		t.Fatalf("unexpected enable calls %+v", lifecycle.enabled)
	}
	if len(plugin.AuditLog()) != 1 || !plugin.AuditLog()[0].Allowed || plugin.AuditLog()[0].Permission != "plugin:enable" || plugin.AuditLog()[0].Action != "plugin.enable" {
		t.Fatalf("expected allowed scoped audit record, got %+v", plugin.AuditLog())
	}
}

func TestPluginAdminPreparesAndActivatesRolloutWithinScope(t *testing.T) {
	t.Parallel()

	lifecycle := &lifecycleRecorder{}
	rollouts := &rolloutRecorder{}
	plugin := New(lifecycle, rollouts, nil, nil, map[string][]string{"rollout-user": {"rollout-operator"}}, adminPolicies(), nil)
	plugin.CurrentTime = func() time.Time { return time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC) }
	if err := plugin.OnCommand(eventmodel.CommandInvocation{Name: "admin", Raw: "/admin prepare plugin-echo", Metadata: map[string]any{"actor": "rollout-user"}}, eventmodel.ExecutionContext{TraceID: "trace-11", EventID: "evt-11"}); err != nil {
		t.Fatalf("prepare rollout: %v", err)
	}
	if err := plugin.OnCommand(eventmodel.CommandInvocation{Name: "admin", Raw: "/admin activate plugin-echo", Metadata: map[string]any{"actor": "rollout-user"}}, eventmodel.ExecutionContext{TraceID: "trace-12", EventID: "evt-12"}); err != nil {
		t.Fatalf("activate rollout: %v", err)
	}
	if len(rollouts.prepared) != 1 || rollouts.prepared[0] != "plugin-echo" || len(rollouts.activated) != 1 || rollouts.activated[0] != "plugin-echo" {
		t.Fatalf("unexpected rollout calls prepared=%+v activated=%+v", rollouts.prepared, rollouts.activated)
	}
	if len(rollouts.checked) != 1 || rollouts.checked[0] != "plugin-echo" {
		t.Fatalf("expected activate to delegate readiness check to rollout manager, got %+v", rollouts.checked)
	}
	if len(lifecycle.enabled) != 1 || lifecycle.enabled[0] != "plugin-echo" {
		t.Fatalf("expected activate to trigger lifecycle enable, got %+v", lifecycle.enabled)
	}
	audit := plugin.AuditLog()
	if len(audit) != 2 {
		t.Fatalf("expected two audit entries, got %+v", audit)
	}
	if reason := auditReasonValue(audit[0]); reason != "rollout_prepared" {
		t.Fatalf("expected prepare success audit reason rollout_prepared, got %+v", audit)
	}
	if reason := auditReasonValue(audit[1]); reason != "rollout_activated" {
		t.Fatalf("expected activate success audit reason rollout_activated, got %+v", audit)
	}
}

func TestPluginAdminCanariesAndRollsBackRolloutWithinScope(t *testing.T) {
	t.Parallel()

	rollouts := &rolloutRecorder{}
	plugin := New(&lifecycleRecorder{}, rollouts, nil, nil, map[string][]string{"rollout-user": {"rollout-operator"}}, adminPolicies(), nil)
	plugin.CurrentTime = func() time.Time { return time.Date(2026, 4, 10, 12, 30, 0, 0, time.UTC) }
	if err := plugin.OnCommand(eventmodel.CommandInvocation{Name: "admin", Raw: "/admin prepare plugin-echo", Metadata: map[string]any{"actor": "rollout-user"}}, eventmodel.ExecutionContext{TraceID: "trace-20", EventID: "evt-20"}); err != nil {
		t.Fatalf("prepare rollout: %v", err)
	}
	if err := plugin.OnCommand(eventmodel.CommandInvocation{Name: "admin", Raw: "/admin canary plugin-echo", Metadata: map[string]any{"actor": "rollout-user"}}, eventmodel.ExecutionContext{TraceID: "trace-21", EventID: "evt-21"}); err != nil {
		t.Fatalf("canary rollout: %v", err)
	}
	if err := plugin.OnCommand(eventmodel.CommandInvocation{Name: "admin", Raw: "/admin rollback plugin-echo", Metadata: map[string]any{"actor": "rollout-user"}}, eventmodel.ExecutionContext{TraceID: "trace-22", EventID: "evt-22"}); err != nil {
		t.Fatalf("rollback rollout: %v", err)
	}
	if len(rollouts.prepared) != 1 || len(rollouts.canaried) != 1 || len(rollouts.rollbackChecked) != 1 || len(rollouts.rolledBack) != 1 {
		t.Fatalf("unexpected rollout integration calls prepared=%+v canaried=%+v rollbackChecked=%+v rolledBack=%+v", rollouts.prepared, rollouts.canaried, rollouts.rollbackChecked, rollouts.rolledBack)
	}
	audit := plugin.AuditLog()
	if len(audit) != 3 {
		t.Fatalf("expected three rollout audit entries, got %+v", audit)
	}
	if reason := auditReasonValue(audit[1]); reason != "rollout_canary_started" {
		t.Fatalf("expected canary success audit reason rollout_canary_started, got %+v", audit)
	}
	if reason := auditReasonValue(audit[2]); reason != "rollout_rolled_back" {
		t.Fatalf("expected rollback success audit reason rollout_rolled_back, got %+v", audit)
	}
}

func TestPluginAdminRejectsActivateWhenRolloutIsNotPrepared(t *testing.T) {
	t.Parallel()

	plugin := New(&lifecycleRecorder{}, &rolloutRecorder{}, nil, nil, map[string][]string{"rollout-user": {"rollout-operator"}}, adminPolicies(), nil)
	err := plugin.OnCommand(eventmodel.CommandInvocation{Name: "admin", Raw: "/admin activate plugin-echo", Metadata: map[string]any{"actor": "rollout-user"}}, eventmodel.ExecutionContext{TraceID: "trace-13", EventID: "evt-13"})
	if err == nil || !strings.Contains(err.Error(), "rollout is not prepared") {
		t.Fatalf("expected activate without prepare to fail, got %v", err)
	}
}

func TestPluginAdminBubblesRolloutManagerActivationCheckError(t *testing.T) {
	t.Parallel()

	plugin := New(&lifecycleRecorder{}, &rolloutRecorder{checkErr: errors.New("rollout record for \"plugin-echo\" drifted after prepare: apiVersion mismatch: current=v0 candidate=v9")}, nil, nil, map[string][]string{"rollout-user": {"rollout-operator"}}, adminPolicies(), nil)
	err := plugin.OnCommand(eventmodel.CommandInvocation{Name: "admin", Raw: "/admin activate plugin-echo", Metadata: map[string]any{"actor": "rollout-user"}}, eventmodel.ExecutionContext{TraceID: "trace-19", EventID: "evt-19"})
	if err == nil || !strings.Contains(err.Error(), "drifted after prepare") {
		t.Fatalf("expected rollout manager activation-check error to bubble, got %v", err)
	}
}

func TestPluginAdminRejectsRollbackWhenRolloutStateCannotRollback(t *testing.T) {
	t.Parallel()

	plugin := New(&lifecycleRecorder{}, &rolloutRecorder{}, nil, nil, map[string][]string{"rollout-user": {"rollout-operator"}}, adminPolicies(), nil)
	err := plugin.OnCommand(eventmodel.CommandInvocation{Name: "admin", Raw: "/admin rollback plugin-echo", Metadata: map[string]any{"actor": "rollout-user"}}, eventmodel.ExecutionContext{TraceID: "trace-23", EventID: "evt-23"})
	if err == nil || !strings.Contains(err.Error(), "invalid rollout transition") {
		t.Fatalf("expected rollback without canary/activation to fail, got %v", err)
	}
	if len(plugin.AuditLog()) != 1 || auditReasonValue(plugin.AuditLog()[0]) != "rollout_invalid_transition" {
		t.Fatalf("expected rollback invalid transition audit reason, got %+v", plugin.AuditLog())
	}
}

func TestPluginAdminReplaysEventWithinScope(t *testing.T) {
	t.Parallel()

	replay := &replayRecorder{event: eventmodel.Event{EventID: "replay-evt-allowed-1"}}
	plugin := New(&lifecycleRecorder{}, nil, replay, nil, map[string][]string{"replay-user": {"replay-operator"}}, adminPolicies(), nil)
	err := plugin.OnCommand(eventmodel.CommandInvocation{Name: "admin", Raw: "/admin replay evt-allowed", Metadata: map[string]any{"actor": "replay-user"}}, eventmodel.ExecutionContext{TraceID: "trace-15", EventID: "evt-15"})
	if err != nil {
		t.Fatalf("expected replay command to pass, got %v", err)
	}
	if len(replay.eventIDs) != 1 || replay.eventIDs[0] != "evt-allowed" {
		t.Fatalf("unexpected replay calls %+v", replay.eventIDs)
	}
	if len(plugin.AuditLog()) != 1 || !plugin.AuditLog()[0].Allowed || plugin.AuditLog()[0].Permission != "plugin:replay" || auditReasonValue(plugin.AuditLog()[0]) != "" {
		t.Fatalf("expected allowed replay audit record, got %+v", plugin.AuditLog())
	}
}

func TestPluginAdminRejectsReplayOutsideScope(t *testing.T) {
	t.Parallel()

	plugin := New(&lifecycleRecorder{}, nil, &replayRecorder{}, nil, map[string][]string{"replay-user": {"replay-operator"}}, adminPolicies(), nil)
	err := plugin.OnCommand(eventmodel.CommandInvocation{Name: "admin", Raw: "/admin replay evt-denied", Metadata: map[string]any{"actor": "replay-user"}}, eventmodel.ExecutionContext{TraceID: "trace-16", EventID: "evt-16"})
	if err == nil || !strings.Contains(err.Error(), "plugin scope denied") {
		t.Fatalf("expected replay scope denial, got %v", err)
	}
	if len(plugin.AuditLog()) != 1 || auditReasonValue(plugin.AuditLog()[0]) != "scope_denied" {
		t.Fatalf("expected replay scope denial audit reason, got %+v", plugin.AuditLog())
	}
}

func TestPluginAdminBubblesReplayErrorAfterAuthorization(t *testing.T) {
	t.Parallel()

	replay := &replayRecorder{err: errors.New("cannot replay a replayed event")}
	plugin := New(&lifecycleRecorder{}, nil, replay, nil, map[string][]string{"admin-user": {"admin"}}, adminPolicies(), nil)
	err := plugin.OnCommand(eventmodel.CommandInvocation{Name: "admin", Raw: "/admin replay evt-replayed", Metadata: map[string]any{"actor": "admin-user"}}, eventmodel.ExecutionContext{TraceID: "trace-17", EventID: "evt-17"})
	if err == nil || !strings.Contains(err.Error(), "cannot replay a replayed event") {
		t.Fatalf("expected replay error to bubble, got %v", err)
	}
	if len(plugin.AuditLog()) != 1 || !plugin.AuditLog()[0].Allowed || plugin.AuditLog()[0].Permission != "plugin:replay" || auditReasonValue(plugin.AuditLog()[0]) != "replay_rejected" {
		t.Fatalf("expected authorized replay audit record, got %+v", plugin.AuditLog())
	}
}

func TestPluginAdminSupportsStructuredArgumentsForReplayCommand(t *testing.T) {
	t.Parallel()

	replay := &replayRecorder{event: eventmodel.Event{EventID: "replay-evt-allowed-2"}}
	plugin := New(&lifecycleRecorder{}, nil, replay, nil, map[string][]string{"replay-user": {"replay-operator"}}, adminPolicies(), nil)
	err := plugin.OnCommand(eventmodel.CommandInvocation{Name: "admin", Arguments: []string{"replay", "evt-allowed"}, Metadata: map[string]any{"actor": "replay-user"}}, eventmodel.ExecutionContext{TraceID: "trace-18", EventID: "evt-18"})
	if err != nil {
		t.Fatalf("expected structured replay command to pass, got %v", err)
	}
	if len(replay.eventIDs) != 1 || replay.eventIDs[0] != "evt-allowed" {
		t.Fatalf("unexpected replay calls %+v", replay.eventIDs)
	}
}

func TestPluginAdminUsesCurrentAuthorizerProviderInsteadOfFrozenAuthorizer(t *testing.T) {
	t.Parallel()

	lifecycle := &lifecycleRecorder{}
	provider := &providerRecorder{authorizer: pluginsdk.NewAuthorizer(
		map[string][]string{"dynamic-user": {"enable-plugin-echo"}},
		map[string]pluginsdk.AuthorizationPolicy{
			"enable-plugin-echo": {Permissions: []string{"plugin:enable"}, PluginScope: []string{"plugin-echo"}},
		},
	)}
	plugin := New(lifecycle, nil, nil, provider, map[string][]string{"dynamic-user": {"viewer"}}, adminPolicies(), nil)

	err := plugin.OnCommand(eventmodel.CommandInvocation{
		Name:     "admin",
		Raw:      "/admin enable plugin-echo",
		Metadata: map[string]any{"actor": "dynamic-user"},
	}, eventmodel.ExecutionContext{TraceID: "trace-provider", EventID: "evt-provider"})
	if err != nil {
		t.Fatalf("expected provider-backed admin command to pass, got %v", err)
	}
	if len(lifecycle.enabled) != 1 || lifecycle.enabled[0] != "plugin-echo" {
		t.Fatalf("expected provider-backed authorizer to enable plugin-echo, got %+v", lifecycle.enabled)
	}
	if len(plugin.AuditLog()) != 1 || !plugin.AuditLog()[0].Allowed || plugin.AuditLog()[0].Permission != "plugin:enable" || plugin.AuditLog()[0].Action != "plugin.enable" {
		t.Fatalf("expected allowed provider-backed audit entry, got %+v", plugin.AuditLog())
	}
}

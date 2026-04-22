package runtimecore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

func TestSubprocessPluginHostHandshakeHealthAndDispatchKinds(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testPluginProcessFactory(t, "echo"))
	plugin := testPluginDefinition()
	event := testPluginEvent()

	if err := host.HealthCheck(context.Background()); err != nil {
		t.Fatalf("health check: %v", err)
	}
	if err := host.DispatchEvent(context.Background(), plugin, event, eventmodel.ExecutionContext{TraceID: event.TraceID, EventID: event.EventID}); err != nil {
		t.Fatalf("dispatch event: %v", err)
	}
	if err := host.DispatchCommand(context.Background(), plugin, eventmodel.CommandInvocation{Name: "admin", Raw: "/admin enable plugin-echo"}, eventmodel.ExecutionContext{TraceID: "trace-command", EventID: "evt-command"}); err != nil {
		t.Fatalf("dispatch command: %v", err)
	}
	if err := host.DispatchJob(context.Background(), plugin, pluginsdk.JobInvocation{ID: "job-1", Type: "ai.chat"}, eventmodel.ExecutionContext{TraceID: "trace-job", EventID: "evt-job"}); err != nil {
		t.Fatalf("dispatch job: %v", err)
	}
	if err := host.DispatchSchedule(context.Background(), plugin, pluginsdk.ScheduleTrigger{ID: "schedule-1", Type: "cron"}, eventmodel.ExecutionContext{TraceID: "trace-schedule", EventID: "evt-schedule"}); err != nil {
		t.Fatalf("dispatch schedule: %v", err)
	}

	stdout := strings.Join(host.StdoutLines(), "\n")
	for _, marker := range []string{"handshake-ready", "event-ok", "command-ok", "job-ok", "schedule-ok"} {
		if !strings.Contains(stdout, marker) {
			t.Fatalf("expected stdout to include %q, got %s", marker, stdout)
		}
	}
	t.Logf("stdout=%s", stdout)
	time.Sleep(50 * time.Millisecond)
	stderr := strings.Join(host.StderrLines(), "\n")
	if !strings.Contains(stderr, "stderr-online") {
		t.Fatalf("expected stderr capture, got %s", stderr)
	}
	t.Logf("stderr=%s", stderr)
}

func TestSubprocessPluginHostAutoRestartsAfterCrash(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testPluginProcessFactory(t, "crash-once"))
	plugin := testPluginDefinition()
	event := testPluginEvent()

	if err := host.DispatchEvent(context.Background(), plugin, event, eventmodel.ExecutionContext{TraceID: event.TraceID, EventID: event.EventID}); err != nil {
		t.Fatalf("expected host to recover and replay after crash, got %v", err)
	}

	if err := host.HealthCheck(context.Background()); err != nil {
		t.Fatalf("health check after restart: %v", err)
	}
	if err := host.DispatchEvent(context.Background(), plugin, event, eventmodel.ExecutionContext{TraceID: event.TraceID, EventID: event.EventID}); err != nil {
		t.Fatalf("dispatch after restart: %v", err)
	}

	stdout := strings.Join(host.StdoutLines(), "\n")
	if strings.Count(stdout, "handshake-ready") < 2 {
		t.Fatalf("expected at least two handshakes across crash recovery, got %s", stdout)
	}
	t.Logf("recovery_stdout=%s", stdout)
	t.Logf("recovery_stderr=%s", strings.Join(host.StderrLines(), "\n"))
}

func TestSubprocessPluginHostSmokeWithBuiltFixtureExecutable(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "crash-once"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	plugin := testPluginDefinition()
	event := testPluginEvent()

	if err := host.DispatchEvent(context.Background(), plugin, event, eventmodel.ExecutionContext{TraceID: event.TraceID, EventID: event.EventID}); err != nil {
		t.Fatalf("expected built fixture to recover and replay after crash, got %v", err)
	}
	if err := host.HealthCheck(context.Background()); err != nil {
		t.Fatalf("health check with built fixture: %v", err)
	}

	stdout := strings.Join(host.StdoutLines(), "\n")
	for _, marker := range []string{"handshake-ready", "event-ok", "healthy"} {
		if !strings.Contains(stdout, marker) {
			t.Fatalf("expected built fixture stdout to include %q, got %s", marker, stdout)
		}
	}
	if strings.Count(stdout, "handshake-ready") < 2 {
		t.Fatalf("expected built fixture crash recovery to trigger a second handshake, got %s", stdout)
	}
	stderr := strings.Join(host.StderrLines(), "\n")
	if !strings.Contains(stderr, "stderr-online") {
		t.Fatalf("expected built fixture stderr capture, got %s", stderr)
	}
	t.Logf("built_fixture_stdout=%s", stdout)
	t.Logf("built_fixture_stderr=%s", stderr)
}

func TestSubprocessPluginHostIsolatesProcessesPerPlugin(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testPluginProcessFactory(t, "echo"))
	pluginA := pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{ID: "plugin-a", Name: "A", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Entry: pluginsdk.PluginEntry{Module: "plugins/a", Symbol: "Plugin"}}}
	pluginB := pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{ID: "plugin-b", Name: "B", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Entry: pluginsdk.PluginEntry{Module: "plugins/b", Symbol: "Plugin"}}}
	event := testPluginEvent()

	if err := host.DispatchEvent(context.Background(), pluginA, event, eventmodel.ExecutionContext{TraceID: "trace-a", EventID: "evt-a"}); err != nil {
		t.Fatalf("dispatch plugin A: %v", err)
	}
	if err := host.DispatchEvent(context.Background(), pluginB, event, eventmodel.ExecutionContext{TraceID: "trace-b", EventID: "evt-b"}); err != nil {
		t.Fatalf("dispatch plugin B: %v", err)
	}

	stdout := strings.Join(host.StdoutLines(), "\n")
	if strings.Count(stdout, "handshake-ready") < 2 {
		t.Fatalf("expected separate plugin processes to handshake independently, got %s", stdout)
	}
}

func TestSubprocessPluginHostHandlesChildReplyTextCallbackBeforeTerminalEventResponse(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testPluginProcessFactory(t, "reply-text-callback"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	var gotHandle eventmodel.ReplyHandle
	var gotText string
	host.SetReplyTextCallback(func(handle eventmodel.ReplyHandle, text string) error {
		gotHandle = handle
		gotText = text
		return nil
	})
	plugin := testPluginDefinition()
	event := eventmodel.Event{
		EventID:        "evt-host-reply",
		TraceID:        "trace-host-reply",
		Source:         "onebot",
		Type:           "message.received",
		Timestamp:      time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC),
		IdempotencyKey: "onebot:reply-text-callback",
		Message:        &eventmodel.Message{ID: "msg-1", Text: "hello from child"},
		Reply: &eventmodel.ReplyHandle{
			Capability: "onebot.reply",
			TargetID:   "group-42",
			MessageID:  "msg-1",
			Metadata: map[string]any{
				"trace_id":  "trace-host-reply",
				"event_id":  "evt-host-reply",
				"plugin_id": plugin.Manifest.ID,
			},
		},
	}

	if err := host.DispatchEvent(context.Background(), plugin, event, eventmodel.ExecutionContext{TraceID: event.TraceID, EventID: event.EventID, Reply: event.Reply}); err != nil {
		t.Fatalf("dispatch event with reply_text callback: %v", err)
	}
	if gotText != "callback: hello from child" {
		t.Fatalf("expected callback text, got %q", gotText)
	}
	if gotHandle.Capability != "onebot.reply" || gotHandle.TargetID != "group-42" || gotHandle.MessageID != "msg-1" {
		t.Fatalf("unexpected reply handle %+v", gotHandle)
	}
	stdout := strings.Join(host.StdoutLines(), "\n")
	for _, marker := range []string{"handshake-ready", `"callback":"reply_text"`, "event-ok"} {
		if !strings.Contains(stdout, marker) {
			t.Fatalf("expected stdout to include %q, got %s", marker, stdout)
		}
	}
}

func TestSubprocessPluginHostHandlesWorkflowStartOrResumeCallbackBeforeTerminalEventResponse(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testPluginProcessFactory(t, "workflow-start-or-resume-callback"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	var got SubprocessWorkflowStartOrResumeRequest
	host.SetWorkflowStartOrResumeCallback(func(ctx context.Context, request SubprocessWorkflowStartOrResumeRequest) (WorkflowTransition, error) {
		got = request
		transition := WorkflowTransition{Started: true}
		transition.Instance = WorkflowInstanceState{
			WorkflowID:    request.WorkflowID,
			PluginID:      request.PluginID,
			Status:        WorkflowRuntimeStatusWaitingEvent,
			Workflow:      request.Initial,
			LastEventID:   request.EventID,
			LastEventType: request.EventType,
		}
		return transition, nil
	})
	plugin := pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{ID: "plugin-routed-parent", Name: "Routed Parent", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Entry: pluginsdk.PluginEntry{Module: "plugins/routed-parent", Symbol: "Plugin"}}}
	event := eventmodel.Event{
		EventID:        "evt-host-workflow",
		TraceID:        "trace-host-workflow",
		Source:         "runtime-workflow-demo",
		Type:           "message.received",
		Timestamp:      time.Date(2026, 4, 22, 12, 10, 0, 0, time.UTC),
		IdempotencyKey: "workflow:callback",
		Actor:          &eventmodel.Actor{ID: "user-1", Type: "user"},
		Message:        &eventmodel.Message{ID: "msg-workflow", Text: "start workflow"},
	}

	if err := host.DispatchEvent(context.Background(), plugin, event, eventmodel.ExecutionContext{TraceID: event.TraceID, EventID: event.EventID}); err != nil {
		t.Fatalf("dispatch event with workflow_start_or_resume callback: %v", err)
	}
	if got.WorkflowID != "workflow-user-1" || got.PluginID != plugin.Manifest.ID || got.EventType != event.Type || got.EventID != event.EventID {
		t.Fatalf("unexpected workflow callback request %+v", got)
	}
	if got.PluginID == "plugin-subprocess-demo" {
		t.Fatalf("expected routed parent plugin identity to override child callback payload, got %+v", got)
	}
	if got.Initial.WaitingFor != "" || len(got.Initial.State) != 0 || len(got.Initial.Steps) != 3 || got.Initial.Steps[0].Name != "greeting" || got.Initial.Steps[0].Value != "start workflow" {
		t.Fatalf("unexpected workflow callback initial state %+v", got.Initial)
	}
	stdout := strings.Join(host.StdoutLines(), "\n")
	for _, marker := range []string{"handshake-ready", `"callback":"workflow_start_or_resume"`, `"workflow_id":"workflow-user-1"`, "event-ok"} {
		if !strings.Contains(stdout, marker) {
			t.Fatalf("expected stdout to include %q, got %s", marker, stdout)
		}
	}
}

func TestSubprocessPluginHostFailsClosedWhenCommandFactoryReturnsLaunchGuardError(t *testing.T) {
	t.Parallel()

	logs := &bytes.Buffer{}
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHostWithErrorFactory(func(context.Context) (*exec.Cmd, error) {
		return nil, errors.New(`subprocess launch guard blocked start: launch guard working directory "D:\\outside" escapes workspace root "D:\\repo"`)
	})
	host.SetObservability(NewLogger(logs), tracer, metrics)
	plugin := testPluginDefinition()
	ctx := eventmodel.ExecutionContext{TraceID: "trace-launch-guard", EventID: "evt-launch-guard", PluginID: plugin.Manifest.ID, CorrelationID: "corr-launch-guard"}

	err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx)
	if err == nil || !strings.Contains(err.Error(), "subprocess start failed") || !strings.Contains(err.Error(), "subprocess launch guard blocked start") || !strings.Contains(err.Error(), `working directory "D:\\outside" escapes workspace root`) {
		t.Fatalf("expected fail-closed launch guard error, got %v", err)
	}
	if stdout := strings.Join(host.StdoutLines(), "\n"); stdout != "" {
		t.Fatalf("expected fail-closed launch guard to skip handshake stdout, got %s", stdout)
	}
	if stderr := strings.Join(host.StderrLines(), "\n"); stderr != "" {
		t.Fatalf("expected fail-closed launch guard to skip subprocess stderr, got %s", stderr)
	}
	for _, expected := range []string{"subprocess host process bootstrap failed", `"failure_stage":"start"`, `"failure_reason":"launcher_guard_blocked"`, `"plugin_id":"plugin-subprocess-demo"`} {
		if !strings.Contains(logs.String(), expected) {
			t.Fatalf("expected fail-closed launch guard log %q, got %s", expected, logs.String())
		}
	}
	renderedTrace := tracer.RenderTrace("trace-launch-guard")
	if !strings.Contains(renderedTrace, "plugin_host.process_bootstrap") {
		t.Fatalf("expected bootstrap trace for fail-closed launch guard, got %s", renderedTrace)
	}
	for _, expected := range []string{
		`bot_platform_subprocess_failures_total{plugin_id="plugin-subprocess-demo",failure_stage="start",failure_reason="launcher_guard_blocked"} 1`,
		`bot_platform_plugin_errors_total{plugin_id="plugin-subprocess-demo"} 1`,
	} {
		if !strings.Contains(metrics.RenderPrometheus(), expected) {
			t.Fatalf("expected fail-closed launch guard metric %q, got %s", expected, metrics.RenderPrometheus())
		}
	}
}

func TestSubprocessPluginHostEnforcesRestartBudgetWithAssertableTelemetry(t *testing.T) {
	t.Parallel()

	logs := &bytes.Buffer{}
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHost(testPluginProcessFactory(t, "crash-after-handshake"))
	host.SetObservability(NewLogger(logs), tracer, metrics)
	host.SetRestartBudget(1, time.Minute)
	plugin := testPluginDefinition()
	ctx := eventmodel.ExecutionContext{TraceID: "trace-restart-budget", EventID: "evt-restart-budget", PluginID: plugin.Manifest.ID}

	err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx)
	if err == nil || !strings.Contains(err.Error(), "subprocess dispatch failed after handshake") {
		t.Fatalf("expected first dispatch to classify crash-after-handshake, got %v", err)
	}
	err = host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx)
	if err == nil || !strings.Contains(err.Error(), "restart budget exhausted") {
		t.Fatalf("expected restart budget guard on second dispatch, got %v", err)
	}
	for _, expected := range []string{"subprocess host process bootstrap failed", `"failure_reason":"launcher_guard_blocked"`, `"failure_stage":"start"`} {
		if !strings.Contains(logs.String(), expected) {
			t.Fatalf("expected restart budget log %q, got %s", expected, logs.String())
		}
	}
	rendered := metrics.RenderPrometheus()
	for _, expected := range []string{
		`bot_platform_subprocess_failures_total{plugin_id="plugin-subprocess-demo",failure_stage="dispatch",failure_reason="crash_after_handshake"} 1`,
		`bot_platform_subprocess_failures_total{plugin_id="plugin-subprocess-demo",failure_stage="start",failure_reason="launcher_guard_blocked"} 2`,
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("expected subprocess failure metric %q, got %s", expected, rendered)
		}
	}
}

func TestSubprocessPluginHostRejectsIncompatibleManifestBeforeProcessLaunch(t *testing.T) {
	t.Parallel()

	var launches atomic.Int32
	logs := &bytes.Buffer{}
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHost(func(ctx context.Context) *exec.Cmd {
		launches.Add(1)
		return testPluginProcessFactory(t, "echo")(ctx)
	})
	host.SetObservability(NewLogger(logs), tracer, metrics)
	event := testPluginEvent()

	for _, tc := range []struct {
		plugin                  pluginsdk.Plugin
		expectedReason          string
		expectedRule            string
		expectedManifestMode    string
		expectedManifestVersion string
		expectedBinaryPresent   bool
		expectedModulePresent   bool
		expectedErrorContains   string
		expectedEnumValue       string
	}{
		{
			plugin:                  pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{ID: "plugin-inproc", Name: "Inproc", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeInProc, Entry: pluginsdk.PluginEntry{Module: "plugins/inproc", Symbol: "Plugin"}}},
			expectedReason:          "manifest_mode_mismatch",
			expectedRule:            "mode",
			expectedManifestMode:    pluginsdk.ModeInProc,
			expectedManifestVersion: "v0",
			expectedBinaryPresent:   false,
			expectedModulePresent:   true,
		},
		{
			plugin:                  pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{ID: "plugin-api", Name: "API", Version: "0.1.0", APIVersion: "v9", Mode: pluginsdk.ModeSubprocess, Entry: pluginsdk.PluginEntry{Module: "plugins/api", Symbol: "Plugin"}}},
			expectedReason:          "manifest_unsupported_api_version",
			expectedRule:            "api_version",
			expectedManifestMode:    pluginsdk.ModeSubprocess,
			expectedManifestVersion: "v9",
			expectedBinaryPresent:   false,
			expectedModulePresent:   true,
		},
		{
			plugin:                  subprocessPluginWithRuntimeVersionRange("plugin-runtime", ">=0.2.0 <1.0.0"),
			expectedReason:          "manifest_unsupported_runtime_version",
			expectedRule:            "runtime_version",
			expectedManifestMode:    pluginsdk.ModeSubprocess,
			expectedManifestVersion: "v0",
			expectedBinaryPresent:   false,
			expectedModulePresent:   true,
			expectedErrorContains:   `current runtime version "0.1.0" does not satisfy required runtimeVersionRange ">=0.2.0 <1.0.0"`,
		},
		{
			plugin:                  pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{ID: "plugin-entry", Name: "Entry", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Entry: pluginsdk.PluginEntry{Symbol: "Plugin"}}},
			expectedReason:          "manifest_missing_entry_target",
			expectedRule:            "entry_target",
			expectedManifestMode:    pluginsdk.ModeSubprocess,
			expectedManifestVersion: "v0",
			expectedBinaryPresent:   false,
			expectedModulePresent:   false,
		},
		{
			plugin:                  pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{ID: "plugin-config", Name: "Config", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, ConfigSchema: map[string]any{"type": "string"}, Entry: pluginsdk.PluginEntry{Module: "plugins/config", Symbol: "Plugin"}}},
			expectedReason:          "manifest_invalid_config_schema",
			expectedRule:            "config_schema",
			expectedManifestMode:    pluginsdk.ModeSubprocess,
			expectedManifestVersion: "v0",
			expectedBinaryPresent:   false,
			expectedModulePresent:   true,
		},
		{
			plugin:                  pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{ID: "plugin-required", Name: "Required", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, ConfigSchema: map[string]any{"type": "object", "required": []any{"token"}, "properties": map[string]any{}}, Entry: pluginsdk.PluginEntry{Module: "plugins/required", Symbol: "Plugin"}}},
			expectedReason:          "manifest_missing_required_config",
			expectedRule:            "config_required",
			expectedManifestMode:    pluginsdk.ModeSubprocess,
			expectedManifestVersion: "v0",
			expectedBinaryPresent:   false,
			expectedModulePresent:   true,
		},
		{
			plugin: pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{
				ID:         "plugin-required-invalid-container",
				Name:       "RequiredInvalidContainer",
				Version:    "0.1.0",
				APIVersion: "v0",
				Mode:       pluginsdk.ModeSubprocess,
				ConfigSchema: map[string]any{
					"type":     "object",
					"required": true,
					"properties": map[string]any{
						"token": map[string]any{"type": "string"},
					},
				},
				Entry: pluginsdk.PluginEntry{Module: "plugins/required-invalid-container", Symbol: "Plugin"},
			}},
			expectedReason:          "manifest_invalid_config_schema",
			expectedRule:            "config_schema",
			expectedManifestMode:    pluginsdk.ModeSubprocess,
			expectedManifestVersion: "v0",
			expectedBinaryPresent:   false,
			expectedModulePresent:   true,
			expectedErrorContains:   `config schema required must be an array of property names`,
		},
		{
			plugin: pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{
				ID:         "plugin-required-invalid-entry",
				Name:       "RequiredInvalidEntry",
				Version:    "0.1.0",
				APIVersion: "v0",
				Mode:       pluginsdk.ModeSubprocess,
				ConfigSchema: map[string]any{
					"type":     "object",
					"required": []any{true},
					"properties": map[string]any{
						"token": map[string]any{"type": "string"},
					},
				},
				Entry: pluginsdk.PluginEntry{Module: "plugins/required-invalid-entry", Symbol: "Plugin"},
			}},
			expectedReason:          "manifest_invalid_config_schema",
			expectedRule:            "config_schema",
			expectedManifestMode:    pluginsdk.ModeSubprocess,
			expectedManifestVersion: "v0",
			expectedBinaryPresent:   false,
			expectedModulePresent:   true,
			expectedErrorContains:   `config schema required entries must be non-empty strings`,
		},
		{
			plugin: pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{
				ID:         "plugin-properties-invalid-container",
				Name:       "PropertiesInvalidContainer",
				Version:    "0.1.0",
				APIVersion: "v0",
				Mode:       pluginsdk.ModeSubprocess,
				ConfigSchema: map[string]any{
					"type":       "object",
					"properties": true,
				},
				Entry: pluginsdk.PluginEntry{Module: "plugins/properties-invalid-container", Symbol: "Plugin"},
			}},
			expectedReason:          "manifest_invalid_config_schema",
			expectedRule:            "config_schema",
			expectedManifestMode:    pluginsdk.ModeSubprocess,
			expectedManifestVersion: "v0",
			expectedBinaryPresent:   false,
			expectedModulePresent:   true,
			expectedErrorContains:   `config schema properties must be an object map, got "boolean"`,
		},
		{
			plugin:                  pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{ID: "plugin-typed-config", Name: "TypedConfig", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, ConfigSchema: map[string]any{"type": "object", "required": []any{"prefix"}, "properties": map[string]any{"prefix": map[string]any{}}}, Entry: pluginsdk.PluginEntry{Module: "plugins/typed-config", Symbol: "Plugin"}}},
			expectedReason:          "manifest_invalid_config_property",
			expectedRule:            "config_property_type",
			expectedManifestMode:    pluginsdk.ModeSubprocess,
			expectedManifestVersion: "v0",
			expectedBinaryPresent:   false,
			expectedModulePresent:   true,
		},
		{
			plugin:                  pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{ID: "plugin-typed-config-default", Name: "TypedConfigDefault", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, ConfigSchema: map[string]any{"type": "object", "required": []any{"prefix"}, "properties": map[string]any{"prefix": map[string]any{"type": "string", "default": true}}}, Entry: pluginsdk.PluginEntry{Module: "plugins/typed-config-default", Symbol: "Plugin"}}},
			expectedReason:          "manifest_invalid_config_property",
			expectedRule:            "config_property_value_type",
			expectedManifestMode:    pluginsdk.ModeSubprocess,
			expectedManifestVersion: "v0",
			expectedBinaryPresent:   false,
			expectedModulePresent:   true,
		},
		{
			plugin:                  pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{ID: "plugin-typed-config-enum-value", Name: "TypedConfigEnumValue", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, ConfigSchema: map[string]any{"type": "object", "required": []any{"prefix"}, "properties": map[string]any{"prefix": map[string]any{"type": "string", "enum": []any{"hello", true}}}}, Entry: pluginsdk.PluginEntry{Module: "plugins/typed-config-enum-value", Symbol: "Plugin"}}},
			expectedReason:          "manifest_invalid_config_property",
			expectedRule:            "config_property_enum_value_type",
			expectedManifestMode:    pluginsdk.ModeSubprocess,
			expectedManifestVersion: "v0",
			expectedBinaryPresent:   false,
			expectedModulePresent:   true,
			expectedErrorContains:   `config schema property "prefix" enum value true must match declared type "string"`,
			expectedEnumValue:       "true",
		},
		{
			plugin:                  pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{ID: "plugin-typed-config-shape", Name: "TypedConfigShape", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, ConfigSchema: map[string]any{"type": "object", "required": []any{"prefix"}, "properties": map[string]any{"prefix": true}}, Entry: pluginsdk.PluginEntry{Module: "plugins/typed-config-shape", Symbol: "Plugin"}}},
			expectedReason:          "manifest_invalid_config_property",
			expectedRule:            "config_property_schema_shape",
			expectedManifestMode:    pluginsdk.ModeSubprocess,
			expectedManifestVersion: "v0",
			expectedBinaryPresent:   false,
			expectedModulePresent:   true,
			expectedErrorContains:   `config schema property "prefix" must be an object schema, got "boolean"`,
		},
		{
			plugin: pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{
				ID:         "plugin-typed-config-shape-missing-object-type",
				Name:       "TypedConfigShapeMissingObjectType",
				Version:    "0.1.0",
				APIVersion: "v0",
				Mode:       pluginsdk.ModeSubprocess,
				ConfigSchema: map[string]any{
					"type":     "object",
					"required": []any{"prefix"},
					"properties": map[string]any{
						"prefix": map[string]any{
							"properties": map[string]any{
								"child": map[string]any{"type": "string"},
							},
						},
					},
				},
				Entry: pluginsdk.PluginEntry{Module: "plugins/typed-config-shape-missing-object-type", Symbol: "Plugin"},
			}},
			expectedReason:          "manifest_invalid_config_property",
			expectedRule:            "config_property_schema_shape",
			expectedManifestMode:    pluginsdk.ModeSubprocess,
			expectedManifestVersion: "v0",
			expectedBinaryPresent:   false,
			expectedModulePresent:   true,
			expectedErrorContains:   `config schema property "prefix" declares properties but is missing object type`,
		},
		{
			plugin: pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{
				ID:         "plugin-typed-config-shape-invalid-properties-container",
				Name:       "TypedConfigShapeInvalidPropertiesContainer",
				Version:    "0.1.0",
				APIVersion: "v0",
				Mode:       pluginsdk.ModeSubprocess,
				ConfigSchema: map[string]any{
					"type":     "object",
					"required": []any{"prefix"},
					"properties": map[string]any{
						"prefix": map[string]any{
							"type":       "object",
							"properties": true,
						},
					},
				},
				Entry: pluginsdk.PluginEntry{Module: "plugins/typed-config-shape-invalid-properties-container", Symbol: "Plugin"},
			}},
			expectedReason:          "manifest_invalid_config_property",
			expectedRule:            "config_property_schema_shape",
			expectedManifestMode:    pluginsdk.ModeSubprocess,
			expectedManifestVersion: "v0",
			expectedBinaryPresent:   false,
			expectedModulePresent:   true,
			expectedErrorContains:   `config schema property "prefix" object properties must be an object map, got "boolean"`,
		},
		{
			plugin:                  pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{ID: "plugin-typed-config-enum-default", Name: "TypedConfigEnumDefault", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, ConfigSchema: map[string]any{"type": "object", "required": []any{"prefix"}, "properties": map[string]any{"prefix": map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "oops"}}}, Entry: pluginsdk.PluginEntry{Module: "plugins/typed-config-enum-default", Symbol: "Plugin"}}},
			expectedReason:          "manifest_invalid_config_property",
			expectedRule:            "config_property_enum_default",
			expectedManifestMode:    pluginsdk.ModeSubprocess,
			expectedManifestVersion: "v0",
			expectedBinaryPresent:   false,
			expectedModulePresent:   true,
			expectedErrorContains:   `default value "oops" must be declared in enum`,
		},
		{
			plugin: pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{
				ID:         "plugin-typed-config-nested-default",
				Name:       "TypedConfigNestedDefault",
				Version:    "0.1.0",
				APIVersion: "v0",
				Mode:       pluginsdk.ModeSubprocess,
				ConfigSchema: map[string]any{
					"type":     "object",
					"required": []any{"settings"},
					"properties": map[string]any{
						"settings": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"labels": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"prefix": map[string]any{
											"type":    "string",
											"default": true,
										},
									},
								},
							},
						},
					},
				},
				Entry: pluginsdk.PluginEntry{Module: "plugins/typed-config-nested-default", Symbol: "Plugin"},
			}},
			expectedReason:          "manifest_invalid_config_property",
			expectedRule:            "config_nested_value_type",
			expectedManifestMode:    pluginsdk.ModeSubprocess,
			expectedManifestVersion: "v0",
			expectedBinaryPresent:   false,
			expectedModulePresent:   true,
			expectedErrorContains:   `nested config schema property "settings.labels.prefix" default value type must match declared type "string", got "boolean"`,
		},
		{
			plugin: pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{
				ID:         "plugin-typed-config-nested-enum-value",
				Name:       "TypedConfigNestedEnumValue",
				Version:    "0.1.0",
				APIVersion: "v0",
				Mode:       pluginsdk.ModeSubprocess,
				ConfigSchema: map[string]any{
					"type":     "object",
					"required": []any{"settings"},
					"properties": map[string]any{
						"settings": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"labels": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"prefix": map[string]any{
											"type": "string",
											"enum": []any{"hello", true},
										},
									},
								},
							},
						},
					},
				},
				Entry: pluginsdk.PluginEntry{Module: "plugins/typed-config-nested-enum-value", Symbol: "Plugin"},
			}},
			expectedReason:          "manifest_invalid_config_property",
			expectedRule:            "config_nested_enum_value_type",
			expectedManifestMode:    pluginsdk.ModeSubprocess,
			expectedManifestVersion: "v0",
			expectedBinaryPresent:   false,
			expectedModulePresent:   true,
			expectedErrorContains:   `nested config schema property "settings.labels.prefix" enum value true must match declared type "string"`,
			expectedEnumValue:       "true",
		},
		{
			plugin: pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{
				ID:         "plugin-typed-config-nested-enum-default",
				Name:       "TypedConfigNestedEnumDefault",
				Version:    "0.1.0",
				APIVersion: "v0",
				Mode:       pluginsdk.ModeSubprocess,
				ConfigSchema: map[string]any{
					"type":     "object",
					"required": []any{"settings"},
					"properties": map[string]any{
						"settings": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"labels": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"prefix": map[string]any{
											"type":    "string",
											"enum":    []any{"hello", "world"},
											"default": "oops",
										},
									},
								},
							},
						},
					},
				},
				Entry: pluginsdk.PluginEntry{Module: "plugins/typed-config-nested-enum-default", Symbol: "Plugin"},
			}},
			expectedReason:          "manifest_invalid_config_property",
			expectedRule:            "config_nested_enum_default",
			expectedManifestMode:    pluginsdk.ModeSubprocess,
			expectedManifestVersion: "v0",
			expectedBinaryPresent:   false,
			expectedModulePresent:   true,
			expectedErrorContains:   `nested config schema property "settings.labels.prefix" default value "oops" must be declared in enum`,
		},
		{
			plugin:                  pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{ID: "plugin-typed-config-nested", Name: "TypedConfigNested", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, ConfigSchema: map[string]any{"type": "object", "required": []any{"settings"}, "properties": map[string]any{"settings": map[string]any{"type": "object", "properties": map[string]any{"prefix": map[string]any{"default": "hello"}}}}}, Entry: pluginsdk.PluginEntry{Module: "plugins/typed-config-nested", Symbol: "Plugin"}}},
			expectedReason:          "manifest_invalid_config_property",
			expectedRule:            "config_nested_property_type",
			expectedManifestMode:    pluginsdk.ModeSubprocess,
			expectedManifestVersion: "v0",
			expectedBinaryPresent:   false,
			expectedModulePresent:   true,
			expectedErrorContains:   `nested config schema property "settings.prefix" must declare a type`,
		},
		{
			plugin:                  pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{ID: "plugin-typed-config-nested-deep", Name: "TypedConfigNestedDeep", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, ConfigSchema: map[string]any{"type": "object", "required": []any{"settings"}, "properties": map[string]any{"settings": map[string]any{"type": "object", "properties": map[string]any{"labels": map[string]any{"type": "object", "properties": map[string]any{"prefix": map[string]any{"default": "hello"}}}}}}}, Entry: pluginsdk.PluginEntry{Module: "plugins/typed-config-nested-deep", Symbol: "Plugin"}}},
			expectedReason:          "manifest_invalid_config_property",
			expectedRule:            "config_nested_property_type",
			expectedManifestMode:    pluginsdk.ModeSubprocess,
			expectedManifestVersion: "v0",
			expectedBinaryPresent:   false,
			expectedModulePresent:   true,
			expectedErrorContains:   `nested config schema property "settings.labels.prefix" must declare a type`,
		},
		{
			plugin: pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{
				ID:         "plugin-typed-config-nested-required",
				Name:       "TypedConfigNestedRequired",
				Version:    "0.1.0",
				APIVersion: "v0",
				Mode:       pluginsdk.ModeSubprocess,
				ConfigSchema: map[string]any{
					"type":     "object",
					"required": []any{"settings"},
					"properties": map[string]any{
						"settings": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"labels": map[string]any{
									"type":       "object",
									"required":   []any{"prefix"},
									"properties": map[string]any{},
								},
							},
						},
					},
				},
				Entry: pluginsdk.PluginEntry{Module: "plugins/typed-config-nested-required", Symbol: "Plugin"},
			}},
			expectedReason:          "manifest_invalid_config_property",
			expectedRule:            "config_nested_required",
			expectedManifestMode:    pluginsdk.ModeSubprocess,
			expectedManifestVersion: "v0",
			expectedBinaryPresent:   false,
			expectedModulePresent:   true,
			expectedErrorContains:   `nested config schema required field "settings.labels.prefix" is missing from properties`,
		},
		{
			plugin: pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{
				ID:         "plugin-typed-config-nested-shape",
				Name:       "TypedConfigNestedShape",
				Version:    "0.1.0",
				APIVersion: "v0",
				Mode:       pluginsdk.ModeSubprocess,
				ConfigSchema: map[string]any{
					"type":     "object",
					"required": []any{"settings"},
					"properties": map[string]any{
						"settings": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"labels": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"prefix": true,
									},
								},
							},
						},
					},
				},
				Entry: pluginsdk.PluginEntry{Module: "plugins/typed-config-nested-shape", Symbol: "Plugin"},
			}},
			expectedReason:          "manifest_invalid_config_property",
			expectedRule:            "config_nested_schema_shape",
			expectedManifestMode:    pluginsdk.ModeSubprocess,
			expectedManifestVersion: "v0",
			expectedBinaryPresent:   false,
			expectedModulePresent:   true,
			expectedErrorContains:   `nested config schema property "settings.labels.prefix" must be an object schema, got "boolean"`,
		},
		{
			plugin: pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{
				ID:         "plugin-typed-config-nested-shape-deep",
				Name:       "TypedConfigNestedShapeDeep",
				Version:    "0.1.0",
				APIVersion: "v0",
				Mode:       pluginsdk.ModeSubprocess,
				ConfigSchema: map[string]any{
					"type":     "object",
					"required": []any{"settings"},
					"properties": map[string]any{
						"settings": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"labels": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"nested": map[string]any{
											"type": "object",
											"properties": map[string]any{
												"prefix": 7,
											},
										},
									},
								},
							},
						},
					},
				},
				Entry: pluginsdk.PluginEntry{Module: "plugins/typed-config-nested-shape-deep", Symbol: "Plugin"},
			}},
			expectedReason:          "manifest_invalid_config_property",
			expectedRule:            "config_nested_schema_shape",
			expectedManifestMode:    pluginsdk.ModeSubprocess,
			expectedManifestVersion: "v0",
			expectedBinaryPresent:   false,
			expectedModulePresent:   true,
			expectedErrorContains:   `nested config schema property "settings.labels.nested.prefix" must be an object schema, got "integer"`,
		},
		{
			plugin: pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{
				ID:         "plugin-typed-config-nested-shape-missing-object-type",
				Name:       "TypedConfigNestedShapeMissingObjectType",
				Version:    "0.1.0",
				APIVersion: "v0",
				Mode:       pluginsdk.ModeSubprocess,
				ConfigSchema: map[string]any{
					"type":     "object",
					"required": []any{"settings"},
					"properties": map[string]any{
						"settings": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"labels": map[string]any{
									"properties": map[string]any{
										"prefix": map[string]any{"type": "string"},
									},
								},
							},
						},
					},
				},
				Entry: pluginsdk.PluginEntry{Module: "plugins/typed-config-nested-shape-missing-object-type", Symbol: "Plugin"},
			}},
			expectedReason:          "manifest_invalid_config_property",
			expectedRule:            "config_nested_schema_shape",
			expectedManifestMode:    pluginsdk.ModeSubprocess,
			expectedManifestVersion: "v0",
			expectedBinaryPresent:   false,
			expectedModulePresent:   true,
			expectedErrorContains:   `nested config schema property "settings.labels" declares properties but is missing object type`,
		},
		{
			plugin: pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{
				ID:         "plugin-typed-config-nested-shape-invalid-properties-container",
				Name:       "TypedConfigNestedShapeInvalidPropertiesContainer",
				Version:    "0.1.0",
				APIVersion: "v0",
				Mode:       pluginsdk.ModeSubprocess,
				ConfigSchema: map[string]any{
					"type":     "object",
					"required": []any{"settings"},
					"properties": map[string]any{
						"settings": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"labels": map[string]any{
									"type":       "object",
									"properties": true,
								},
							},
						},
					},
				},
				Entry: pluginsdk.PluginEntry{Module: "plugins/typed-config-nested-shape-invalid-properties-container", Symbol: "Plugin"},
			}},
			expectedReason:          "manifest_invalid_config_property",
			expectedRule:            "config_nested_schema_shape",
			expectedManifestMode:    pluginsdk.ModeSubprocess,
			expectedManifestVersion: "v0",
			expectedBinaryPresent:   false,
			expectedModulePresent:   true,
			expectedErrorContains:   `nested config schema property "settings.labels" object properties must be an object map, got "boolean"`,
		},
		{
			plugin: pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{
				ID:         "plugin-typed-config-nested-required-invalid-container",
				Name:       "TypedConfigNestedRequiredInvalidContainer",
				Version:    "0.1.0",
				APIVersion: "v0",
				Mode:       pluginsdk.ModeSubprocess,
				ConfigSchema: map[string]any{
					"type":     "object",
					"required": []any{"settings"},
					"properties": map[string]any{
						"settings": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"labels": map[string]any{
									"type":       "object",
									"required":   true,
									"properties": map[string]any{"prefix": map[string]any{"type": "string"}},
								},
							},
						},
					},
				},
				Entry: pluginsdk.PluginEntry{Module: "plugins/typed-config-nested-required-invalid-container", Symbol: "Plugin"},
			}},
			expectedReason:          "manifest_invalid_config_property",
			expectedRule:            "config_nested_required",
			expectedManifestMode:    pluginsdk.ModeSubprocess,
			expectedManifestVersion: "v0",
			expectedBinaryPresent:   false,
			expectedModulePresent:   true,
			expectedErrorContains:   `nested config schema required must be an array of property names`,
		},
		{
			plugin: pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{
				ID:         "plugin-typed-config-nested-required-invalid-entry",
				Name:       "TypedConfigNestedRequiredInvalidEntry",
				Version:    "0.1.0",
				APIVersion: "v0",
				Mode:       pluginsdk.ModeSubprocess,
				ConfigSchema: map[string]any{
					"type":     "object",
					"required": []any{"settings"},
					"properties": map[string]any{
						"settings": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"labels": map[string]any{
									"type":       "object",
									"required":   []any{true},
									"properties": map[string]any{"prefix": map[string]any{"type": "string"}},
								},
							},
						},
					},
				},
				Entry: pluginsdk.PluginEntry{Module: "plugins/typed-config-nested-required-invalid-entry", Symbol: "Plugin"},
			}},
			expectedReason:          "manifest_invalid_config_property",
			expectedRule:            "config_nested_required",
			expectedManifestMode:    pluginsdk.ModeSubprocess,
			expectedManifestVersion: "v0",
			expectedBinaryPresent:   false,
			expectedModulePresent:   true,
			expectedErrorContains:   `nested config schema required entries must be non-empty strings`,
		},
	} {
		err := host.DispatchEvent(context.Background(), tc.plugin, event, eventmodel.ExecutionContext{TraceID: event.TraceID, EventID: event.EventID})
		if err == nil {
			t.Fatalf("expected incompatible plugin %q to be rejected", tc.plugin.Manifest.ID)
		}
		if !strings.Contains(err.Error(), "not compatible with subprocess host") {
			t.Fatalf("expected compatibility error for %q, got %v", tc.plugin.Manifest.ID, err)
		}
		if tc.expectedErrorContains != "" && !strings.Contains(err.Error(), tc.expectedErrorContains) {
			t.Fatalf("expected compatibility error for %q to include %q, got %v", tc.plugin.Manifest.ID, tc.expectedErrorContains, err)
		}
		for _, expected := range []string{
			fmt.Sprintf(`"plugin_id":"%s"`, tc.plugin.Manifest.ID),
			fmt.Sprintf(`"failure_reason":"%s"`, tc.expectedReason),
			fmt.Sprintf(`"compatibility_rule":"%s"`, tc.expectedRule),
			fmt.Sprintf(`"manifest_mode":"%s"`, tc.expectedManifestMode),
			fmt.Sprintf(`"manifest_api_version":"%s"`, tc.expectedManifestVersion),
			fmt.Sprintf(`"entry_binary_present":%t`, tc.expectedBinaryPresent),
			fmt.Sprintf(`"entry_module_present":%t`, tc.expectedModulePresent),
			`"failure_stage":"compatibility"`,
		} {
			if !strings.Contains(logs.String(), expected) {
				t.Fatalf("expected compatibility observability detail %q, got %s", expected, logs.String())
			}
		}
		if tc.expectedEnumValue != "" {
			expectedEnumValueLog := fmt.Sprintf(`"enum_value":"%s"`, tc.expectedEnumValue)
			if !strings.Contains(logs.String(), expectedEnumValueLog) {
				t.Fatalf("expected compatibility enum value log %q, got %s", expectedEnumValueLog, logs.String())
			}
		}
		if tc.plugin.Manifest.ID == "plugin-runtime" {
			for _, expected := range []string{`"current_runtime_version":"0.1.0"`, `"required_runtime_version_range":"\u003e=0.2.0 \u003c1.0.0"`} {
				if !strings.Contains(logs.String(), expected) {
					t.Fatalf("expected runtime version compatibility metadata %q, got %s", expected, logs.String())
				}
			}
		}
	}
	if launches.Load() != 0 {
		t.Fatalf("expected incompatible plugin rejection before process launch, got %d launches", launches.Load())
	}
	for _, expected := range []string{"subprocess host compatibility check failed", "plugin-inproc", "plugin-api", "plugin-runtime", "plugin-entry", "plugin-config", "plugin-required", "plugin-required-invalid-container", "plugin-required-invalid-entry", "plugin-properties-invalid-container", "plugin-typed-config", "plugin-typed-config-default", "plugin-typed-config-enum-value", "plugin-typed-config-shape", "plugin-typed-config-shape-missing-object-type", "plugin-typed-config-shape-invalid-properties-container", "plugin-typed-config-enum-default", "plugin-typed-config-nested-default", "plugin-typed-config-nested-enum-value", "plugin-typed-config-nested-enum-default", "plugin-typed-config-nested", "plugin-typed-config-nested-deep", "plugin-typed-config-nested-required", "plugin-typed-config-nested-shape", "plugin-typed-config-nested-shape-deep", "plugin-typed-config-nested-shape-missing-object-type", "plugin-typed-config-nested-shape-invalid-properties-container", "plugin-typed-config-nested-required-invalid-container", "plugin-typed-config-nested-required-invalid-entry"} {
		if !strings.Contains(logs.String(), expected) {
			t.Fatalf("expected compatibility observability log %q, got %s", expected, logs.String())
		}
	}
	renderedTrace := tracer.RenderTrace(event.TraceID)
	if !strings.Contains(renderedTrace, "plugin_host.compatibility") {
		t.Fatalf("expected compatibility trace span, got %s", renderedTrace)
	}
	spans := tracer.SpansByTrace(event.TraceID)
	if len(spans) != 28 {
		t.Fatalf("expected twenty-eight compatibility spans, got %d", len(spans))
	}
	runtimeVersionTraceMatched := false
	topLevelRequiredInvalidContainerTraceMatched := false
	topLevelRequiredInvalidEntryTraceMatched := false
	topLevelPropertiesInvalidContainerTraceMatched := false
	topLevelShapeTraceMatched := false
	topLevelShapeMissingObjectTypeTraceMatched := false
	topLevelShapeInvalidPropertiesContainerTraceMatched := false
	nestedShapeTraceMatched := false
	nestedShapeInvalidPropertiesContainerTraceMatched := false
	nestedRequiredInvalidContainerTraceMatched := false
	nestedRequiredInvalidEntryTraceMatched := false
	topLevelEnumValueTraceMatched := false
	nestedEnumValueTraceMatched := false
	for _, span := range spans {
		if span.PluginID == "plugin-runtime" && span.Metadata["compatibility_rule"] == "runtime_version" && span.Metadata["failure_reason"] == "manifest_unsupported_runtime_version" && span.Metadata["error"] == `plugin "plugin-runtime" is not compatible with subprocess host: current runtime version "0.1.0" does not satisfy required runtimeVersionRange ">=0.2.0 <1.0.0"` && span.Metadata["current_runtime_version"] == "0.1.0" && span.Metadata["required_runtime_version_range"] == ">=0.2.0 <1.0.0" {
			runtimeVersionTraceMatched = true
		}
		if span.PluginID == "plugin-required-invalid-container" && span.Metadata["compatibility_rule"] == "config_schema" && span.Metadata["failure_reason"] == "manifest_invalid_config_schema" && span.Metadata["error"] == `plugin "plugin-required-invalid-container" is not compatible with subprocess host: config schema required must be an array of property names` {
			topLevelRequiredInvalidContainerTraceMatched = true
		}
		if span.PluginID == "plugin-required-invalid-entry" && span.Metadata["compatibility_rule"] == "config_schema" && span.Metadata["failure_reason"] == "manifest_invalid_config_schema" && span.Metadata["error"] == `plugin "plugin-required-invalid-entry" is not compatible with subprocess host: config schema required entries must be non-empty strings` {
			topLevelRequiredInvalidEntryTraceMatched = true
		}
		if span.PluginID == "plugin-properties-invalid-container" && span.Metadata["compatibility_rule"] == "config_schema" && span.Metadata["failure_reason"] == "manifest_invalid_config_schema" && span.Metadata["error"] == `plugin "plugin-properties-invalid-container" is not compatible with subprocess host: config schema properties must be an object map, got "boolean"` {
			topLevelPropertiesInvalidContainerTraceMatched = true
		}
		if span.PluginID == "plugin-typed-config-shape" && span.Metadata["compatibility_rule"] == "config_property_schema_shape" && span.Metadata["failure_reason"] == "manifest_invalid_config_property" && span.Metadata["error"] == `plugin "plugin-typed-config-shape" is not compatible with subprocess host: config schema property "prefix" must be an object schema, got "boolean"` {
			topLevelShapeTraceMatched = true
		}
		if span.PluginID == "plugin-typed-config-shape-missing-object-type" && span.Metadata["compatibility_rule"] == "config_property_schema_shape" && span.Metadata["failure_reason"] == "manifest_invalid_config_property" && span.Metadata["error"] == `plugin "plugin-typed-config-shape-missing-object-type" is not compatible with subprocess host: config schema property "prefix" declares properties but is missing object type` {
			topLevelShapeMissingObjectTypeTraceMatched = true
		}
		if span.PluginID == "plugin-typed-config-shape-invalid-properties-container" && span.Metadata["compatibility_rule"] == "config_property_schema_shape" && span.Metadata["failure_reason"] == "manifest_invalid_config_property" && span.Metadata["error"] == `plugin "plugin-typed-config-shape-invalid-properties-container" is not compatible with subprocess host: config schema property "prefix" object properties must be an object map, got "boolean"` {
			topLevelShapeInvalidPropertiesContainerTraceMatched = true
		}
		if span.PluginID == "plugin-typed-config-nested-shape" && span.Metadata["compatibility_rule"] == "config_nested_schema_shape" && span.Metadata["failure_reason"] == "manifest_invalid_config_property" && strings.Contains(fmt.Sprint(span.Metadata["error"]), `settings.labels.prefix`) {
			nestedShapeTraceMatched = true
		}
		if span.PluginID == "plugin-typed-config-nested-shape-invalid-properties-container" && span.Metadata["compatibility_rule"] == "config_nested_schema_shape" && span.Metadata["failure_reason"] == "manifest_invalid_config_property" && span.Metadata["error"] == `plugin "plugin-typed-config-nested-shape-invalid-properties-container" is not compatible with subprocess host: nested config schema property "settings.labels" object properties must be an object map, got "boolean"` {
			nestedShapeInvalidPropertiesContainerTraceMatched = true
		}
		if span.PluginID == "plugin-typed-config-nested-required-invalid-container" && span.Metadata["compatibility_rule"] == "config_nested_required" && span.Metadata["failure_reason"] == "manifest_invalid_config_property" && span.Metadata["error"] == `plugin "plugin-typed-config-nested-required-invalid-container" is not compatible with subprocess host: nested config schema required must be an array of property names` {
			nestedRequiredInvalidContainerTraceMatched = true
		}
		if span.PluginID == "plugin-typed-config-nested-required-invalid-entry" && span.Metadata["compatibility_rule"] == "config_nested_required" && span.Metadata["failure_reason"] == "manifest_invalid_config_property" && span.Metadata["error"] == `plugin "plugin-typed-config-nested-required-invalid-entry" is not compatible with subprocess host: nested config schema required entries must be non-empty strings` {
			nestedRequiredInvalidEntryTraceMatched = true
		}
		if span.PluginID == "plugin-typed-config-enum-value" && span.Metadata["compatibility_rule"] == "config_property_enum_value_type" && span.Metadata["failure_reason"] == "manifest_invalid_config_property" && span.Metadata["error"] == `plugin "plugin-typed-config-enum-value" is not compatible with subprocess host: config schema property "prefix" enum value true must match declared type "string"` {
			metadata, enumValue := normalizeSubprocessEnumCompatibilityMetadata(span.Metadata)
			if enumValue == "true" && metadata["enum_value"] == enumValue && strings.Contains(fmt.Sprint(span.Metadata["error"]), enumValue) {
				topLevelEnumValueTraceMatched = true
			}
		}
		if span.PluginID == "plugin-typed-config-nested-enum-value" && span.Metadata["compatibility_rule"] == "config_nested_enum_value_type" && span.Metadata["failure_reason"] == "manifest_invalid_config_property" && span.Metadata["error"] == `plugin "plugin-typed-config-nested-enum-value" is not compatible with subprocess host: nested config schema property "settings.labels.prefix" enum value true must match declared type "string"` {
			metadata, enumValue := normalizeSubprocessEnumCompatibilityMetadata(span.Metadata)
			nestedMetadata, nestedEnumValue := normalizeSubprocessEnumCompatibilityMetadata(map[string]any{"enum_value": span.Metadata})
			if enumValue == "true" && metadata["enum_value"] == enumValue && nestedEnumValue == enumValue && nestedMetadata["enum_value"] == enumValue && strings.Contains(fmt.Sprint(span.Metadata["error"]), enumValue) {
				nestedEnumValueTraceMatched = true
			}
		}
	}
	if !strings.Contains(logs.String(), `"enum_value":"true"`) {
		t.Fatalf("expected nested enum value compatibility log metadata, got %s", logs.String())
	}
	if !runtimeVersionTraceMatched {
		t.Fatalf("expected runtime version compatibility trace metadata, got %+v", spans)
	}
	if !topLevelRequiredInvalidContainerTraceMatched {
		t.Fatalf("expected top-level required invalid container compatibility trace metadata, got %+v", spans)
	}
	if !topLevelRequiredInvalidEntryTraceMatched {
		t.Fatalf("expected top-level required invalid entry compatibility trace metadata, got %+v", spans)
	}
	if !topLevelPropertiesInvalidContainerTraceMatched {
		t.Fatalf("expected top-level properties invalid container compatibility trace metadata, got %+v", spans)
	}
	if !topLevelShapeTraceMatched {
		t.Fatalf("expected top-level shape compatibility trace metadata, got %+v", spans)
	}
	if !topLevelShapeMissingObjectTypeTraceMatched {
		t.Fatalf("expected top-level missing object type compatibility trace metadata, got %+v", spans)
	}
	if !topLevelShapeInvalidPropertiesContainerTraceMatched {
		t.Fatalf("expected top-level invalid properties container compatibility trace metadata, got %+v", spans)
	}
	if !nestedShapeTraceMatched {
		t.Fatalf("expected nested shape compatibility trace metadata, got %+v", spans)
	}
	if !nestedShapeInvalidPropertiesContainerTraceMatched {
		t.Fatalf("expected nested invalid properties container compatibility trace metadata, got %+v", spans)
	}
	if !nestedRequiredInvalidContainerTraceMatched {
		t.Fatalf("expected nested required invalid container compatibility trace metadata, got %+v", spans)
	}
	if !nestedRequiredInvalidEntryTraceMatched {
		t.Fatalf("expected nested required invalid entry compatibility trace metadata, got %+v", spans)
	}
	if !topLevelEnumValueTraceMatched {
		t.Fatalf("expected top-level enum value compatibility trace metadata, got %+v", spans)
	}
	if !nestedEnumValueTraceMatched {
		t.Fatalf("expected nested enum value compatibility trace metadata, got %+v", spans)
	}
	for _, tc := range []struct {
		pluginID              string
		expectedReason        string
		expectedRule          string
		expectedManifestMode  string
		expectedManifestAPI   string
		expectedBinaryPresent bool
		expectedModulePresent bool
	}{
		{pluginID: "plugin-inproc", expectedReason: "manifest_mode_mismatch", expectedRule: "mode", expectedManifestMode: pluginsdk.ModeInProc, expectedManifestAPI: "v0", expectedBinaryPresent: false, expectedModulePresent: true},
		{pluginID: "plugin-api", expectedReason: "manifest_unsupported_api_version", expectedRule: "api_version", expectedManifestMode: pluginsdk.ModeSubprocess, expectedManifestAPI: "v9", expectedBinaryPresent: false, expectedModulePresent: true},
		{pluginID: "plugin-runtime", expectedReason: "manifest_unsupported_runtime_version", expectedRule: "runtime_version", expectedManifestMode: pluginsdk.ModeSubprocess, expectedManifestAPI: "v0", expectedBinaryPresent: false, expectedModulePresent: true},
		{pluginID: "plugin-entry", expectedReason: "manifest_missing_entry_target", expectedRule: "entry_target", expectedManifestMode: pluginsdk.ModeSubprocess, expectedManifestAPI: "v0", expectedBinaryPresent: false, expectedModulePresent: false},
		{pluginID: "plugin-config", expectedReason: "manifest_invalid_config_schema", expectedRule: "config_schema", expectedManifestMode: pluginsdk.ModeSubprocess, expectedManifestAPI: "v0", expectedBinaryPresent: false, expectedModulePresent: true},
		{pluginID: "plugin-required", expectedReason: "manifest_missing_required_config", expectedRule: "config_required", expectedManifestMode: pluginsdk.ModeSubprocess, expectedManifestAPI: "v0", expectedBinaryPresent: false, expectedModulePresent: true},
		{pluginID: "plugin-required-invalid-container", expectedReason: "manifest_invalid_config_schema", expectedRule: "config_schema", expectedManifestMode: pluginsdk.ModeSubprocess, expectedManifestAPI: "v0", expectedBinaryPresent: false, expectedModulePresent: true},
		{pluginID: "plugin-properties-invalid-container", expectedReason: "manifest_invalid_config_schema", expectedRule: "config_schema", expectedManifestMode: pluginsdk.ModeSubprocess, expectedManifestAPI: "v0", expectedBinaryPresent: false, expectedModulePresent: true},
		{pluginID: "plugin-required-invalid-entry", expectedReason: "manifest_invalid_config_schema", expectedRule: "config_schema", expectedManifestMode: pluginsdk.ModeSubprocess, expectedManifestAPI: "v0", expectedBinaryPresent: false, expectedModulePresent: true},
		{pluginID: "plugin-typed-config", expectedReason: "manifest_invalid_config_property", expectedRule: "config_property_type", expectedManifestMode: pluginsdk.ModeSubprocess, expectedManifestAPI: "v0", expectedBinaryPresent: false, expectedModulePresent: true},
		{pluginID: "plugin-typed-config-default", expectedReason: "manifest_invalid_config_property", expectedRule: "config_property_value_type", expectedManifestMode: pluginsdk.ModeSubprocess, expectedManifestAPI: "v0", expectedBinaryPresent: false, expectedModulePresent: true},
		{pluginID: "plugin-typed-config-enum-value", expectedReason: "manifest_invalid_config_property", expectedRule: "config_property_enum_value_type", expectedManifestMode: pluginsdk.ModeSubprocess, expectedManifestAPI: "v0", expectedBinaryPresent: false, expectedModulePresent: true},
		{pluginID: "plugin-typed-config-shape", expectedReason: "manifest_invalid_config_property", expectedRule: "config_property_schema_shape", expectedManifestMode: pluginsdk.ModeSubprocess, expectedManifestAPI: "v0", expectedBinaryPresent: false, expectedModulePresent: true},
		{pluginID: "plugin-typed-config-shape-missing-object-type", expectedReason: "manifest_invalid_config_property", expectedRule: "config_property_schema_shape", expectedManifestMode: pluginsdk.ModeSubprocess, expectedManifestAPI: "v0", expectedBinaryPresent: false, expectedModulePresent: true},
		{pluginID: "plugin-typed-config-shape-invalid-properties-container", expectedReason: "manifest_invalid_config_property", expectedRule: "config_property_schema_shape", expectedManifestMode: pluginsdk.ModeSubprocess, expectedManifestAPI: "v0", expectedBinaryPresent: false, expectedModulePresent: true},
		{pluginID: "plugin-typed-config-enum-default", expectedReason: "manifest_invalid_config_property", expectedRule: "config_property_enum_default", expectedManifestMode: pluginsdk.ModeSubprocess, expectedManifestAPI: "v0", expectedBinaryPresent: false, expectedModulePresent: true},
		{pluginID: "plugin-typed-config-nested-default", expectedReason: "manifest_invalid_config_property", expectedRule: "config_nested_value_type", expectedManifestMode: pluginsdk.ModeSubprocess, expectedManifestAPI: "v0", expectedBinaryPresent: false, expectedModulePresent: true},
		{pluginID: "plugin-typed-config-nested-enum-value", expectedReason: "manifest_invalid_config_property", expectedRule: "config_nested_enum_value_type", expectedManifestMode: pluginsdk.ModeSubprocess, expectedManifestAPI: "v0", expectedBinaryPresent: false, expectedModulePresent: true},
		{pluginID: "plugin-typed-config-nested-enum-default", expectedReason: "manifest_invalid_config_property", expectedRule: "config_nested_enum_default", expectedManifestMode: pluginsdk.ModeSubprocess, expectedManifestAPI: "v0", expectedBinaryPresent: false, expectedModulePresent: true},
		{pluginID: "plugin-typed-config-nested", expectedReason: "manifest_invalid_config_property", expectedRule: "config_nested_property_type", expectedManifestMode: pluginsdk.ModeSubprocess, expectedManifestAPI: "v0", expectedBinaryPresent: false, expectedModulePresent: true},
		{pluginID: "plugin-typed-config-nested-deep", expectedReason: "manifest_invalid_config_property", expectedRule: "config_nested_property_type", expectedManifestMode: pluginsdk.ModeSubprocess, expectedManifestAPI: "v0", expectedBinaryPresent: false, expectedModulePresent: true},
		{pluginID: "plugin-typed-config-nested-required", expectedReason: "manifest_invalid_config_property", expectedRule: "config_nested_required", expectedManifestMode: pluginsdk.ModeSubprocess, expectedManifestAPI: "v0", expectedBinaryPresent: false, expectedModulePresent: true},
		{pluginID: "plugin-typed-config-nested-required-invalid-container", expectedReason: "manifest_invalid_config_property", expectedRule: "config_nested_required", expectedManifestMode: pluginsdk.ModeSubprocess, expectedManifestAPI: "v0", expectedBinaryPresent: false, expectedModulePresent: true},
		{pluginID: "plugin-typed-config-nested-required-invalid-entry", expectedReason: "manifest_invalid_config_property", expectedRule: "config_nested_required", expectedManifestMode: pluginsdk.ModeSubprocess, expectedManifestAPI: "v0", expectedBinaryPresent: false, expectedModulePresent: true},
		{pluginID: "plugin-typed-config-nested-shape", expectedReason: "manifest_invalid_config_property", expectedRule: "config_nested_schema_shape", expectedManifestMode: pluginsdk.ModeSubprocess, expectedManifestAPI: "v0", expectedBinaryPresent: false, expectedModulePresent: true},
		{pluginID: "plugin-typed-config-nested-shape-deep", expectedReason: "manifest_invalid_config_property", expectedRule: "config_nested_schema_shape", expectedManifestMode: pluginsdk.ModeSubprocess, expectedManifestAPI: "v0", expectedBinaryPresent: false, expectedModulePresent: true},
		{pluginID: "plugin-typed-config-nested-shape-missing-object-type", expectedReason: "manifest_invalid_config_property", expectedRule: "config_nested_schema_shape", expectedManifestMode: pluginsdk.ModeSubprocess, expectedManifestAPI: "v0", expectedBinaryPresent: false, expectedModulePresent: true},
		{pluginID: "plugin-typed-config-nested-shape-invalid-properties-container", expectedReason: "manifest_invalid_config_property", expectedRule: "config_nested_schema_shape", expectedManifestMode: pluginsdk.ModeSubprocess, expectedManifestAPI: "v0", expectedBinaryPresent: false, expectedModulePresent: true},
	} {
		matched := false
		for _, span := range spans {
			if span.PluginID != tc.pluginID {
				continue
			}
			matched = true
			if span.Metadata["failure_reason"] != tc.expectedReason || span.Metadata["compatibility_rule"] != tc.expectedRule || span.Metadata["manifest_mode"] != tc.expectedManifestMode || span.Metadata["manifest_api_version"] != tc.expectedManifestAPI || span.Metadata["entry_binary_present"] != tc.expectedBinaryPresent || span.Metadata["entry_module_present"] != tc.expectedModulePresent {
				t.Fatalf("expected compatibility trace metadata for %s, got %+v", tc.pluginID, span.Metadata)
			}
		}
		if !matched {
			t.Fatalf("expected compatibility span for %s, got %+v", tc.pluginID, spans)
		}
	}
	metricsOutput := metrics.RenderPrometheus()
	for _, expected := range []string{
		`bot_platform_plugin_errors_total{plugin_id="plugin-inproc"} 1`,
		`bot_platform_plugin_errors_total{plugin_id="plugin-api"} 1`,
		`bot_platform_plugin_errors_total{plugin_id="plugin-runtime"} 1`,
		`bot_platform_plugin_errors_total{plugin_id="plugin-entry"} 1`,
		`bot_platform_plugin_errors_total{plugin_id="plugin-config"} 1`,
		`bot_platform_plugin_errors_total{plugin_id="plugin-required"} 1`,
		`bot_platform_plugin_errors_total{plugin_id="plugin-required-invalid-container"} 1`,
		`bot_platform_plugin_errors_total{plugin_id="plugin-properties-invalid-container"} 1`,
		`bot_platform_plugin_errors_total{plugin_id="plugin-required-invalid-entry"} 1`,
		`bot_platform_plugin_errors_total{plugin_id="plugin-typed-config"} 1`,
		`bot_platform_plugin_errors_total{plugin_id="plugin-typed-config-default"} 1`,
		`bot_platform_plugin_errors_total{plugin_id="plugin-typed-config-enum-value"} 1`,
		`bot_platform_plugin_errors_total{plugin_id="plugin-typed-config-shape"} 1`,
		`bot_platform_plugin_errors_total{plugin_id="plugin-typed-config-shape-missing-object-type"} 1`,
		`bot_platform_plugin_errors_total{plugin_id="plugin-typed-config-shape-invalid-properties-container"} 1`,
		`bot_platform_plugin_errors_total{plugin_id="plugin-typed-config-enum-default"} 1`,
		`bot_platform_plugin_errors_total{plugin_id="plugin-typed-config-nested-default"} 1`,
		`bot_platform_plugin_errors_total{plugin_id="plugin-typed-config-nested-enum-value"} 1`,
		`bot_platform_plugin_errors_total{plugin_id="plugin-typed-config-nested-enum-default"} 1`,
		`bot_platform_plugin_errors_total{plugin_id="plugin-typed-config-nested"} 1`,
		`bot_platform_plugin_errors_total{plugin_id="plugin-typed-config-nested-deep"} 1`,
		`bot_platform_plugin_errors_total{plugin_id="plugin-typed-config-nested-required"} 1`,
		`bot_platform_plugin_errors_total{plugin_id="plugin-typed-config-nested-required-invalid-container"} 1`,
		`bot_platform_plugin_errors_total{plugin_id="plugin-typed-config-nested-required-invalid-entry"} 1`,
		`bot_platform_plugin_errors_total{plugin_id="plugin-typed-config-nested-shape"} 1`,
		`bot_platform_plugin_errors_total{plugin_id="plugin-typed-config-nested-shape-deep"} 1`,
		`bot_platform_plugin_errors_total{plugin_id="plugin-typed-config-nested-shape-missing-object-type"} 1`,
		`bot_platform_plugin_errors_total{plugin_id="plugin-typed-config-nested-shape-invalid-properties-container"} 1`,
	} {
		if !strings.Contains(metricsOutput, expected) {
			t.Fatalf("expected compatibility metric %q, got %s", expected, metricsOutput)
		}
	}
}

func TestSubprocessPluginHostAcceptsSupportedSubprocessManifest(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testPluginProcessFactory(t, "echo"))
	plugin := pluginsdk.Plugin{Manifest: pluginsdk.PluginManifest{ID: "plugin-ok", Name: "OK", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Entry: pluginsdk.PluginEntry{Module: "plugins/ok", Symbol: "Plugin"}}}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), eventmodel.ExecutionContext{TraceID: "trace-ok", EventID: "evt-ok"}); err != nil {
		t.Fatalf("expected compatible subprocess plugin to dispatch, got %v", err)
	}
}

func TestSubprocessPluginHostAcceptsSupportedRuntimeVersionRange(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testPluginProcessFactory(t, "echo"))
	plugin := subprocessPluginWithRuntimeVersionRange("plugin-runtime-ok", ">=0.1.0 <1.0.0")

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), eventmodel.ExecutionContext{TraceID: "trace-runtime-ok", EventID: "evt-runtime-ok"}); err != nil {
		t.Fatalf("expected compatible runtimeVersionRange to dispatch, got %v", err)
	}
}

func subprocessPluginWithRuntimeVersionRange(pluginID, runtimeVersionRange string) pluginsdk.Plugin {
	manifestJSON := fmt.Sprintf(`{
		"id":%q,
		"name":"Runtime Range",
		"version":"0.1.0",
		"apiVersion":"v0",
		"mode":"subprocess",
		"publish":{
			"sourceType":"git",
			"sourceUri":"https://example.com/plugins/%s",
			"runtimeVersionRange":%q
		},
		"entry":{
			"module":"plugins/%s",
			"symbol":"Plugin"
		}
	}`, pluginID, pluginID, runtimeVersionRange, pluginID)
	var manifest pluginsdk.PluginManifest
	if err := json.Unmarshal([]byte(manifestJSON), &manifest); err != nil {
		panic(fmt.Sprintf("unmarshal subprocess runtime version range manifest: %v", err))
	}
	return pluginsdk.Plugin{Manifest: manifest}
}

func TestSubprocessPluginHostSendsInstanceConfigWhenConfigured(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testPluginProcessFactory(t, "assert-instance-config"))
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-typed-config-instance-config",
			Name:       "TypedConfigInstanceConfig",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"prefix": map[string]any{"type": "string"},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/typed-config-instance-config", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"prefix": "!"},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-transport", EventID: "evt-instance-config-transport"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected configured instance config to reach subprocess host request, got %v", err)
	}

	stdout := strings.Join(host.StdoutLines(), "\n")
	for _, expected := range []string{"handshake-ready", "event-ok"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("expected subprocess stdout to include %q, got %s", expected, stdout)
		}
	}
}

func TestBuildSubprocessHostRequestDoesNotInjectTopLevelManifestDefaultIntoInstanceConfig(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-default-omitted-request",
			Name:       "InstanceConfigDefaultOmittedRequest",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"prefix": map[string]any{"type": "string", "default": "hello"},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-default-omitted-request", Symbol: "Plugin"},
		},
	}

	request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err != nil {
		t.Fatalf("expected request build to accept manifest default without injecting it, got %v", err)
	}
	if request.InstanceConfig != nil {
		t.Fatalf("expected top-level manifest default to stay omitted from host request, got %s", string(request.InstanceConfig))
	}
}

func TestBuiltSubprocessFixtureDoesNotReceiveInjectedTopLevelManifestDefault(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-default-not-injected"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-default-omitted-fixture",
			Name:       "InstanceConfigDefaultOmittedFixture",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"prefix": map[string]any{"type": "string", "default": "hello"},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-default-omitted-fixture", Symbol: "Plugin"},
		},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-default-omitted", EventID: "evt-instance-config-default-omitted"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected built fixture request to omit top-level manifest default injection, got %v", err)
	}

	stdout := strings.Join(host.StdoutLines(), "\n")
	for _, expected := range []string{"handshake-ready", "event-ok"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("expected built fixture stdout to include %q, got %s", expected, stdout)
		}
	}
	stderr := strings.Join(host.StderrLines(), "\n")
	if !strings.Contains(stderr, "stderr-online") {
		t.Fatalf("expected built fixture stderr capture, got %s", stderr)
	}
}

func TestSubprocessPluginHostRejectsTopLevelInstanceConfigValueTypeMismatchBeforeProcessLaunch(t *testing.T) {
	t.Parallel()

	launches := 0
	logs := &bytes.Buffer{}
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHost(func(ctx context.Context) *exec.Cmd {
		launches++
		return testPluginProcessFactory(t, "echo")(ctx)
	})
	host.SetObservability(NewLogger(logs), tracer, metrics)
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-type-mismatch",
			Name:       "InstanceConfigTypeMismatch",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"prefix": map[string]any{"type": "string"},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-type-mismatch", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"prefix": true},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-type-mismatch", EventID: "evt-instance-config-type-mismatch", PluginID: plugin.Manifest.ID, CorrelationID: "corr-instance-config-type-mismatch"}
	err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx)
	if err == nil || !strings.Contains(err.Error(), `instance config property "prefix" value type must match declared type "string", got "boolean"`) {
		t.Fatalf("expected top-level instance config type mismatch, got %v", err)
	}
	if launches != 0 {
		t.Fatalf("expected instance config rejection before subprocess launch, launches=%d", launches)
	}
	for _, expected := range []string{"subprocess host instance config rejected", `"failure_stage":"instance_config"`, `"failure_reason":"instance_config_value_type_mismatch"`, `"compatibility_rule":"instance_config_top_level_value_type"`, `"property_name":"prefix"`, `"actual_type":"boolean"`} {
		if !strings.Contains(logs.String(), expected) {
			t.Fatalf("expected instance config rejection log %q, got %s", expected, logs.String())
		}
	}
	rendered := tracer.RenderTrace("trace-instance-config-type-mismatch")
	if !strings.Contains(rendered, "plugin_host.instance_config") {
		t.Fatalf("expected instance config trace span, got %s", rendered)
	}
	if strings.Contains(rendered, "plugin_host.dispatch") {
		t.Fatalf("expected mismatch to stop before dispatch span, got %s", rendered)
	}
	if !strings.Contains(metrics.RenderPrometheus(), `bot_platform_plugin_errors_total{plugin_id="plugin-instance-config-type-mismatch"} 1`) {
		t.Fatalf("expected plugin error metric, got %s", metrics.RenderPrometheus())
	}
}

func TestSubprocessPluginHostRejectsMissingRequiredTopLevelInstanceConfigBeforeProcessLaunch(t *testing.T) {
	t.Parallel()

	launches := 0
	logs := &bytes.Buffer{}
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHost(func(ctx context.Context) *exec.Cmd {
		launches++
		return testPluginProcessFactory(t, "echo")(ctx)
	})
	host.SetObservability(NewLogger(logs), tracer, metrics)
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-required-missing",
			Name:       "InstanceConfigRequiredMissing",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type":     "object",
				"required": []any{"prefix"},
				"properties": map[string]any{
					"prefix": map[string]any{"type": "string"},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-required-missing", Symbol: "Plugin"},
		},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-required-missing", EventID: "evt-instance-config-required-missing", PluginID: plugin.Manifest.ID, CorrelationID: "corr-instance-config-required-missing"}
	err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx)
	if err == nil || !strings.Contains(err.Error(), `instance config required property "prefix" must be provided`) {
		t.Fatalf("expected missing required top-level instance config rejection, got %v", err)
	}
	if launches != 0 {
		t.Fatalf("expected missing required instance config rejection before subprocess launch, launches=%d", launches)
	}
	for _, expected := range []string{"subprocess host instance config rejected", `"failure_stage":"instance_config"`, `"failure_reason":"instance_config_missing_required_value"`, `"compatibility_rule":"instance_config_top_level_required"`, `"property_name":"prefix"`, `"declared_type":"string"`} {
		if !strings.Contains(logs.String(), expected) {
			t.Fatalf("expected missing required instance config rejection log %q, got %s", expected, logs.String())
		}
	}
	rendered := tracer.RenderTrace("trace-instance-config-required-missing")
	if !strings.Contains(rendered, "plugin_host.instance_config") {
		t.Fatalf("expected instance config trace span, got %s", rendered)
	}
	if strings.Contains(rendered, "plugin_host.dispatch") {
		t.Fatalf("expected missing required rejection to stop before dispatch span, got %s", rendered)
	}
	if !strings.Contains(metrics.RenderPrometheus(), `bot_platform_plugin_errors_total{plugin_id="plugin-instance-config-required-missing"} 1`) {
		t.Fatalf("expected plugin error metric, got %s", metrics.RenderPrometheus())
	}
}

func TestBuildSubprocessHostRequestPreservesTopLevelInstanceConfigValueOutsideManifestEnum(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-enum-member-request",
			Name:       "InstanceConfigEnumMemberRequest",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"prefix": map[string]any{"type": "string", "enum": []any{"hello", "world"}},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-enum-member-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"prefix": "oops"},
	}

	request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err != nil {
		t.Fatalf("expected request build to preserve out-of-enum top-level instance value without rejection, got %v", err)
	}
	if request.InstanceConfig == nil {
		t.Fatal("expected host request to keep explicit out-of-enum top-level instance value")
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.InstanceConfig, &decoded); err != nil {
		t.Fatalf("expected instance config payload to remain decodable, got %v", err)
	}
	prefix, ok := decoded["prefix"].(string)
	if !ok || prefix != "oops" {
		t.Fatalf("expected out-of-enum top-level instance value to pass through unchanged, got %+v", decoded)
	}
}

func TestBuiltSubprocessFixtureReceivesTopLevelInstanceConfigValueOutsideManifestEnum(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-enum-not-enforced"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-enum-member-fixture",
			Name:       "InstanceConfigEnumMemberFixture",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"prefix": map[string]any{"type": "string", "enum": []any{"hello", "world"}},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-enum-member-fixture", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"prefix": "oops"},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-enum-member", EventID: "evt-instance-config-enum-member"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected built fixture request to receive out-of-enum top-level instance value without rejection, got %v", err)
	}

	stdout := strings.Join(host.StdoutLines(), "\n")
	for _, expected := range []string{"handshake-ready", "event-ok"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("expected built fixture stdout to include %q, got %s", expected, stdout)
		}
	}
	stderr := strings.Join(host.StderrLines(), "\n")
	if !strings.Contains(stderr, "stderr-online") {
		t.Fatalf("expected built fixture stderr capture, got %s", stderr)
	}
}

func TestBuildSubprocessHostRequestDoesNotInjectNestedManifestDefaultIntoInstanceConfig(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-nested-default-request",
			Name:       "InstanceConfigNestedDefaultRequest",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"prefix": map[string]any{"type": "string", "default": "hello"},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-nested-default-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{}},
	}

	request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err != nil {
		t.Fatalf("expected request build to preserve nested object without injecting manifest default, got %v", err)
	}
	if request.InstanceConfig == nil {
		t.Fatal("expected host request to keep explicit nested settings object")
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.InstanceConfig, &decoded); err != nil {
		t.Fatalf("expected nested instance config payload to remain decodable, got %v", err)
	}
	settings, ok := decoded["settings"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested settings object to remain present, got %+v", decoded)
	}
	if len(settings) != 0 {
		t.Fatalf("expected nested settings object to remain empty when manifest default is omitted, got %+v", decoded)
	}
	if _, exists := settings["prefix"]; exists {
		t.Fatalf("expected nested manifest default to stay absent from request payload, got %+v", decoded)
	}
	if string(request.InstanceConfig) != `{"settings":{}}` {
		t.Fatalf("expected nested manifest default to stay uninjected in raw payload, got %s", string(request.InstanceConfig))
	}
}

func TestBuiltSubprocessFixtureDoesNotReceiveInjectedNestedManifestDefault(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-nested-default-not-injected"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-nested-default-fixture",
			Name:       "InstanceConfigNestedDefaultFixture",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"prefix": map[string]any{"type": "string", "default": "hello"},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-nested-default-fixture", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-nested-default", EventID: "evt-instance-config-nested-default"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected built fixture request to omit nested manifest default injection, got %v", err)
	}

	stdout := strings.Join(host.StdoutLines(), "\n")
	for _, expected := range []string{"handshake-ready", "event-ok"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("expected built fixture stdout to include %q, got %s", expected, stdout)
		}
	}
	stderr := strings.Join(host.StderrLines(), "\n")
	if !strings.Contains(stderr, "stderr-online") {
		t.Fatalf("expected built fixture stderr capture, got %s", stderr)
	}
}

func TestBuildSubprocessHostRequestPreservesNestedInstanceConfigValueOutsideManifestEnum(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-nested-enum-member-request",
			Name:       "InstanceConfigNestedEnumMemberRequest",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"prefix": map[string]any{"type": "string", "enum": []any{"hello", "world"}},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-nested-enum-member-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"prefix": "oops"}},
	}

	request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err != nil {
		t.Fatalf("expected request build to preserve out-of-enum nested instance value without rejection, got %v", err)
	}
	if request.InstanceConfig == nil {
		t.Fatal("expected host request to keep explicit out-of-enum nested instance value")
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.InstanceConfig, &decoded); err != nil {
		t.Fatalf("expected nested instance config payload to remain decodable, got %v", err)
	}
	settings, ok := decoded["settings"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested settings object to remain present, got %+v", decoded)
	}
	prefix, ok := settings["prefix"].(string)
	if !ok || prefix != "oops" {
		t.Fatalf("expected out-of-enum nested instance value to pass through unchanged, got %+v", decoded)
	}
}

func TestBuiltSubprocessFixtureReceivesNestedInstanceConfigValueOutsideManifestEnum(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-nested-enum-not-enforced"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-nested-enum-member-fixture",
			Name:       "InstanceConfigNestedEnumMemberFixture",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"prefix": map[string]any{"type": "string", "enum": []any{"hello", "world"}},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-nested-enum-member-fixture", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"prefix": "oops"}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-nested-enum-member", EventID: "evt-instance-config-nested-enum-member"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected built fixture request to receive out-of-enum nested instance value without rejection, got %v", err)
	}

	stdout := strings.Join(host.StdoutLines(), "\n")
	for _, expected := range []string{"handshake-ready", "event-ok"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("expected built fixture stdout to include %q, got %s", expected, stdout)
		}
	}
	stderr := strings.Join(host.StderrLines(), "\n")
	if !strings.Contains(stderr, "stderr-online") {
		t.Fatalf("expected built fixture stderr capture, got %s", stderr)
	}
}

func TestSubprocessPluginHostRejectsNestedInstanceConfigValueTypeMismatchBeforeProcessLaunch(t *testing.T) {
	t.Parallel()

	launches := 0
	logs := &bytes.Buffer{}
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHost(func(ctx context.Context) *exec.Cmd {
		launches++
		return testPluginProcessFactory(t, "echo")(ctx)
	})
	host.SetObservability(NewLogger(logs), tracer, metrics)
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-nested-type-mismatch",
			Name:       "InstanceConfigNestedTypeMismatch",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"prefix": map[string]any{"type": "string"},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-nested-type-mismatch", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"prefix": true}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-nested-type-mismatch", EventID: "evt-instance-config-nested-type-mismatch", PluginID: plugin.Manifest.ID, CorrelationID: "corr-instance-config-nested-type-mismatch"}
	err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx)
	if err == nil || !strings.Contains(err.Error(), `nested instance config property "settings.prefix" value type must match declared type "string", got "boolean"`) {
		t.Fatalf("expected nested instance config type mismatch rejection, got %v", err)
	}
	if launches != 0 {
		t.Fatalf("expected nested instance config rejection before subprocess launch, launches=%d", launches)
	}
	if len(host.StdoutLines()) != 0 || len(host.StderrLines()) != 0 {
		t.Fatalf("expected nested instance config rejection to avoid subprocess side effects, stdout=%v stderr=%v", host.StdoutLines(), host.StderrLines())
	}
	for _, expected := range []string{"subprocess host instance config rejected", `"failure_stage":"instance_config"`, `"failure_reason":"instance_config_value_type_mismatch"`, `"compatibility_rule":"instance_config_nested_value_type"`, `"property_name":"settings.prefix"`, `"parent_property_name":"settings"`, `"nested_property_name":"prefix"`, `"actual_type":"boolean"`} {
		if !strings.Contains(logs.String(), expected) {
			t.Fatalf("expected nested instance config rejection log %q, got %s", expected, logs.String())
		}
	}
	rendered := tracer.RenderTrace("trace-instance-config-nested-type-mismatch")
	if !strings.Contains(rendered, "plugin_host.instance_config") {
		t.Fatalf("expected nested instance config trace span, got %s", rendered)
	}
	if strings.Contains(rendered, "plugin_host.dispatch") {
		t.Fatalf("expected nested mismatch to stop before dispatch span, got %s", rendered)
	}
	if !strings.Contains(metrics.RenderPrometheus(), `bot_platform_plugin_errors_total{plugin_id="plugin-instance-config-nested-type-mismatch"} 1`) {
		t.Fatalf("expected plugin error metric, got %s", metrics.RenderPrometheus())
	}
}

func TestBuildSubprocessHostRequestRejectsDeeperNestedInstanceConfigValueTypeMismatch(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-deeper-nested-type-mismatch-request",
			Name:       "InstanceConfigDeeperNestedTypeMismatchRequest",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"labels": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"prefix": map[string]any{"type": "string"},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-deeper-nested-type-mismatch-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"prefix": true}}},
	}

	_, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err == nil || !strings.Contains(err.Error(), `nested instance config property "settings.labels.prefix" value type must match declared type "string", got "boolean"`) {
		t.Fatalf("expected request build to reject deeper nested instance value type mismatch, got %v", err)
	}
	var configErr *subprocessInstanceConfigFailure
	if !errors.As(err, &configErr) {
		t.Fatalf("expected deeper nested request build error classification, got %T %v", err, err)
	}
	if configErr.reason != subprocessFailureReasonInstanceConfigValueTypeMismatch {
		t.Fatalf("expected deeper nested value type mismatch reason, got %q", configErr.reason)
	}
	if configErr.compatibilityRule != "instance_config_deeper_nested_value_type" {
		t.Fatalf("expected deeper nested compatibility rule, got %q", configErr.compatibilityRule)
	}
	for key, expected := range map[string]any{
		"property_name":              "settings.labels.prefix",
		"parent_property_name":       "settings.labels",
		"nested_property_name":       "prefix",
		"root_property_name":         "settings",
		"intermediate_property_name": "labels",
		"declared_type":              "string",
		"actual_type":                "boolean",
	} {
		if actual := configErr.metadata[key]; actual != expected {
			t.Fatalf("expected deeper nested request build metadata %q=%v, got %+v", key, expected, configErr.metadata)
		}
	}
}

func TestSubprocessPluginHostRejectsDeeperNestedInstanceConfigValueTypeMismatchBeforeProcessLaunch(t *testing.T) {
	t.Parallel()

	launches := 0
	logs := &bytes.Buffer{}
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHost(func(ctx context.Context) *exec.Cmd {
		launches++
		return testPluginProcessFactory(t, "echo")(ctx)
	})
	host.SetObservability(NewLogger(logs), tracer, metrics)
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-deeper-nested-type-mismatch",
			Name:       "InstanceConfigDeeperNestedTypeMismatch",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"labels": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"prefix": map[string]any{"type": "string"},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-deeper-nested-type-mismatch", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"prefix": true}}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-deeper-nested-type-mismatch", EventID: "evt-instance-config-deeper-nested-type-mismatch", PluginID: plugin.Manifest.ID, CorrelationID: "corr-instance-config-deeper-nested-type-mismatch"}
	err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx)
	if err == nil || !strings.Contains(err.Error(), `nested instance config property "settings.labels.prefix" value type must match declared type "string", got "boolean"`) {
		t.Fatalf("expected deeper nested instance config type mismatch rejection, got %v", err)
	}
	if launches != 0 {
		t.Fatalf("expected deeper nested instance config rejection before subprocess launch, launches=%d", launches)
	}
	if len(host.StdoutLines()) != 0 || len(host.StderrLines()) != 0 {
		t.Fatalf("expected deeper nested instance config rejection to avoid subprocess side effects, stdout=%v stderr=%v", host.StdoutLines(), host.StderrLines())
	}
	for _, expected := range []string{"subprocess host instance config rejected", `"failure_stage":"instance_config"`, `"failure_reason":"instance_config_value_type_mismatch"`, `"compatibility_rule":"instance_config_deeper_nested_value_type"`, `"property_name":"settings.labels.prefix"`, `"parent_property_name":"settings.labels"`, `"nested_property_name":"prefix"`, `"root_property_name":"settings"`, `"intermediate_property_name":"labels"`, `"actual_type":"boolean"`} {
		if !strings.Contains(logs.String(), expected) {
			t.Fatalf("expected deeper nested instance config rejection log %q, got %s", expected, logs.String())
		}
	}
	rendered := tracer.RenderTrace("trace-instance-config-deeper-nested-type-mismatch")
	if !strings.Contains(rendered, "plugin_host.instance_config") {
		t.Fatalf("expected deeper nested instance config trace span, got %s", rendered)
	}
	if strings.Contains(rendered, "plugin_host.dispatch") {
		t.Fatalf("expected deeper nested mismatch to stop before dispatch span, got %s", rendered)
	}
	if !strings.Contains(metrics.RenderPrometheus(), `bot_platform_plugin_errors_total{plugin_id="plugin-instance-config-deeper-nested-type-mismatch"} 1`) {
		t.Fatalf("expected plugin error metric, got %s", metrics.RenderPrometheus())
	}
}
func TestBuildSubprocessHostRequestRejectsDeeperNestedInstanceConfigValueOutsideManifestEnum(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-deeper-nested-enum-member-request",
			Name:       "InstanceConfigDeeperNestedEnumMemberRequest",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"labels": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"prefix": map[string]any{"type": "string", "enum": []any{"hello", "world"}},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-deeper-nested-enum-member-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"prefix": "oops"}}},
	}

	_, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err == nil || !strings.Contains(err.Error(), `nested instance config property "settings.labels.prefix" value "oops" must be declared in enum ["hello","world"] for declared type "string"`) {
		t.Fatalf("expected request build to reject deeper nested out-of-enum instance value, got %v", err)
	}
	var configErr *subprocessInstanceConfigFailure
	if !errors.As(err, &configErr) {
		t.Fatalf("expected deeper nested enum request build error classification, got %T %v", err, err)
	}
	if configErr.reason != subprocessFailureReasonInstanceConfigEnumValueOutOfSet {
		t.Fatalf("expected deeper nested enum reason, got %q", configErr.reason)
	}
	if configErr.compatibilityRule != "instance_config_deeper_nested_enum" {
		t.Fatalf("expected deeper nested enum compatibility rule, got %q", configErr.compatibilityRule)
	}
	for key, expected := range map[string]any{
		"property_name":              "settings.labels.prefix",
		"parent_property_name":       "settings.labels",
		"nested_property_name":       "prefix",
		"root_property_name":         "settings",
		"intermediate_property_name": "labels",
		"declared_type":              "string",
		"actual_value":               "oops",
	} {
		if actual := configErr.metadata[key]; actual != expected {
			t.Fatalf("expected deeper nested enum request build metadata %q=%v, got %+v", key, expected, configErr.metadata)
		}
	}
	enumValues, ok := configErr.metadata["enum_values"].([]any)
	if !ok || len(enumValues) != 2 || enumValues[0] != "hello" || enumValues[1] != "world" {
		t.Fatalf("expected deeper nested enum request build enum_values metadata, got %+v", configErr.metadata)
	}
}

func TestSubprocessPluginHostRejectsDeeperNestedInstanceConfigValueOutsideManifestEnumBeforeProcessLaunch(t *testing.T) {
	t.Parallel()

	launches := 0
	logs := &bytes.Buffer{}
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHost(func(ctx context.Context) *exec.Cmd {
		launches++
		return testBuiltPluginProcessFactory(t, "assert-instance-config-deeper-nested-enum-not-enforced")(ctx)
	})
	host.SetObservability(NewLogger(logs), tracer, metrics)
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-deeper-nested-enum-member-fixture",
			Name:       "InstanceConfigDeeperNestedEnumMemberFixture",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"labels": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"prefix": map[string]any{"type": "string", "enum": []any{"hello", "world"}},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-deeper-nested-enum-member-fixture", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"prefix": "oops"}}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-deeper-nested-enum-member", EventID: "evt-instance-config-deeper-nested-enum-member", PluginID: plugin.Manifest.ID, CorrelationID: "corr-instance-config-deeper-nested-enum-member"}

	err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx)
	if err == nil || !strings.Contains(err.Error(), `nested instance config property "settings.labels.prefix" value "oops" must be declared in enum ["hello","world"] for declared type "string"`) {
		t.Fatalf("expected deeper nested enum instance config rejection, got %v", err)
	}
	if launches != 0 {
		t.Fatalf("expected deeper nested enum instance config rejection before subprocess launch, launches=%d", launches)
	}
	if len(host.StdoutLines()) != 0 || len(host.StderrLines()) != 0 {
		t.Fatalf("expected deeper nested enum instance config rejection to avoid subprocess side effects, stdout=%v stderr=%v", host.StdoutLines(), host.StderrLines())
	}
	for _, expected := range []string{"subprocess host instance config rejected", `"failure_stage":"instance_config"`, `"failure_reason":"instance_config_enum_value_out_of_set"`, `"compatibility_rule":"instance_config_deeper_nested_enum"`, `"property_name":"settings.labels.prefix"`, `"parent_property_name":"settings.labels"`, `"nested_property_name":"prefix"`, `"root_property_name":"settings"`, `"intermediate_property_name":"labels"`, `"actual_value":"oops"`, `"enum_values":["hello","world"]`} {
		if !strings.Contains(logs.String(), expected) {
			t.Fatalf("expected deeper nested enum instance config rejection log %q, got %s", expected, logs.String())
		}
	}
	rendered := tracer.RenderTrace("trace-instance-config-deeper-nested-enum-member")
	if !strings.Contains(rendered, "plugin_host.instance_config") {
		t.Fatalf("expected deeper nested enum instance config trace span, got %s", rendered)
	}
	if strings.Contains(rendered, "plugin_host.dispatch") {
		t.Fatalf("expected deeper nested enum rejection to stop before dispatch span, got %s", rendered)
	}
	spans := tracer.SpansByTrace("trace-instance-config-deeper-nested-enum-member")
	matched := false
	for _, span := range spans {
		if span.SpanName != "plugin_host.instance_config" || span.PluginID != plugin.Manifest.ID {
			continue
		}
		if span.Metadata["failure_reason"] != "instance_config_enum_value_out_of_set" || span.Metadata["compatibility_rule"] != "instance_config_deeper_nested_enum" || span.Metadata["actual_value"] != "oops" {
			continue
		}
		enumValues, ok := span.Metadata["enum_values"].([]any)
		if !ok || len(enumValues) != 2 || enumValues[0] != "hello" || enumValues[1] != "world" {
			t.Fatalf("expected deeper nested enum trace metadata, got %+v", span.Metadata)
		}
		matched = true
	}
	if !matched {
		t.Fatalf("expected deeper nested enum trace metadata, got %+v", spans)
	}
	if !strings.Contains(metrics.RenderPrometheus(), `bot_platform_plugin_errors_total{plugin_id="plugin-instance-config-deeper-nested-enum-member-fixture"} 1`) {
		t.Fatalf("expected plugin error metric, got %s", metrics.RenderPrometheus())
	}
}

func TestBuildSubprocessHostRequestRejectsDeeperNestedArrayValueTypeMismatch(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-deeper-nested-array-value-request",
			Name:       "InstanceConfigDeeperNestedArrayValueRequest",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"labels": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"prefix": map[string]any{"type": "string"},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-deeper-nested-array-value-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"prefix": []any{"oops"}}}},
	}

	_, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err == nil || !strings.Contains(err.Error(), `nested instance config property "settings.labels.prefix" value type must match declared type "string", got "array"`) {
		t.Fatalf("expected request build to reject deeper nested array value type mismatch, got %v", err)
	}
	var configErr *subprocessInstanceConfigFailure
	if !errors.As(err, &configErr) {
		t.Fatalf("expected deeper nested array request build error classification, got %T %v", err, err)
	}
	if configErr.reason != subprocessFailureReasonInstanceConfigValueTypeMismatch {
		t.Fatalf("expected deeper nested array value type mismatch reason, got %q", configErr.reason)
	}
	if configErr.compatibilityRule != "instance_config_deeper_nested_value_type" {
		t.Fatalf("expected deeper nested array compatibility rule, got %q", configErr.compatibilityRule)
	}
	for key, expected := range map[string]any{
		"property_name":              "settings.labels.prefix",
		"parent_property_name":       "settings.labels",
		"nested_property_name":       "prefix",
		"root_property_name":         "settings",
		"intermediate_property_name": "labels",
		"declared_type":              "string",
		"actual_type":                "array",
	} {
		if actual := configErr.metadata[key]; actual != expected {
			t.Fatalf("expected deeper nested array request build metadata %q=%v, got %+v", key, expected, configErr.metadata)
		}
	}
}

func TestSubprocessPluginHostRejectsDeeperNestedArrayValueTypeMismatchBeforeProcessLaunch(t *testing.T) {
	t.Parallel()

	launches := 0
	logs := &bytes.Buffer{}
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHost(func(ctx context.Context) *exec.Cmd {
		launches++
		return testPluginProcessFactory(t, "echo")(ctx)
	})
	host.SetObservability(NewLogger(logs), tracer, metrics)
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-deeper-nested-array-value",
			Name:       "InstanceConfigDeeperNestedArrayValue",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"labels": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"prefix": map[string]any{"type": "string"},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-deeper-nested-array-value", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"prefix": []any{"oops"}}}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-deeper-nested-array-value", EventID: "evt-instance-config-deeper-nested-array-value", PluginID: plugin.Manifest.ID, CorrelationID: "corr-instance-config-deeper-nested-array-value"}
	err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx)
	if err == nil || !strings.Contains(err.Error(), `nested instance config property "settings.labels.prefix" value type must match declared type "string", got "array"`) {
		t.Fatalf("expected deeper nested array instance config type mismatch rejection, got %v", err)
	}
	if launches != 0 {
		t.Fatalf("expected deeper nested array instance config rejection before subprocess launch, launches=%d", launches)
	}
	if len(host.StdoutLines()) != 0 || len(host.StderrLines()) != 0 {
		t.Fatalf("expected deeper nested array instance config rejection to avoid subprocess side effects, stdout=%v stderr=%v", host.StdoutLines(), host.StderrLines())
	}
	for _, expected := range []string{"subprocess host instance config rejected", `"failure_stage":"instance_config"`, `"failure_reason":"instance_config_value_type_mismatch"`, `"compatibility_rule":"instance_config_deeper_nested_value_type"`, `"property_name":"settings.labels.prefix"`, `"parent_property_name":"settings.labels"`, `"nested_property_name":"prefix"`, `"root_property_name":"settings"`, `"intermediate_property_name":"labels"`, `"actual_type":"array"`} {
		if !strings.Contains(logs.String(), expected) {
			t.Fatalf("expected deeper nested array instance config rejection log %q, got %s", expected, logs.String())
		}
	}
	rendered := tracer.RenderTrace("trace-instance-config-deeper-nested-array-value")
	if !strings.Contains(rendered, "plugin_host.instance_config") {
		t.Fatalf("expected deeper nested array instance config trace span, got %s", rendered)
	}
	if strings.Contains(rendered, "plugin_host.dispatch") {
		t.Fatalf("expected deeper nested array mismatch to stop before dispatch span, got %s", rendered)
	}
	if !strings.Contains(metrics.RenderPrometheus(), `bot_platform_plugin_errors_total{plugin_id="plugin-instance-config-deeper-nested-array-value"} 1`) {
		t.Fatalf("expected plugin error metric, got %s", metrics.RenderPrometheus())
	}
}

func TestBuildSubprocessHostRequestRejectsDeeperNestedObjectValueTypeMismatch(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-deeper-nested-object-value-request",
			Name:       "InstanceConfigDeeperNestedObjectValueRequest",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"labels": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"prefix": map[string]any{"type": "string"},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-deeper-nested-object-value-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"prefix": map[string]any{"bad": true}}}},
	}

	_, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err == nil || !strings.Contains(err.Error(), `nested instance config property "settings.labels.prefix" value type must match declared type "string", got "object"`) {
		t.Fatalf("expected request build to reject deeper nested object value type mismatch, got %v", err)
	}
	var configErr *subprocessInstanceConfigFailure
	if !errors.As(err, &configErr) {
		t.Fatalf("expected deeper nested object request build error classification, got %T %v", err, err)
	}
	if configErr.reason != subprocessFailureReasonInstanceConfigValueTypeMismatch {
		t.Fatalf("expected deeper nested object value type mismatch reason, got %q", configErr.reason)
	}
	if configErr.compatibilityRule != "instance_config_deeper_nested_value_type" {
		t.Fatalf("expected deeper nested object compatibility rule, got %q", configErr.compatibilityRule)
	}
	for key, expected := range map[string]any{
		"property_name":              "settings.labels.prefix",
		"parent_property_name":       "settings.labels",
		"nested_property_name":       "prefix",
		"root_property_name":         "settings",
		"intermediate_property_name": "labels",
		"declared_type":              "string",
		"actual_type":                "object",
	} {
		if actual := configErr.metadata[key]; actual != expected {
			t.Fatalf("expected deeper nested object request build metadata %q=%v, got %+v", key, expected, configErr.metadata)
		}
	}
}

func TestSubprocessPluginHostRejectsDeeperNestedObjectValueTypeMismatchBeforeProcessLaunch(t *testing.T) {
	t.Parallel()

	launches := 0
	logs := &bytes.Buffer{}
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHost(func(ctx context.Context) *exec.Cmd {
		launches++
		return testPluginProcessFactory(t, "echo")(ctx)
	})
	host.SetObservability(NewLogger(logs), tracer, metrics)
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-deeper-nested-object-value",
			Name:       "InstanceConfigDeeperNestedObjectValue",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"labels": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"prefix": map[string]any{"type": "string"},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-deeper-nested-object-value", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"prefix": map[string]any{"bad": true}}}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-deeper-nested-object-value", EventID: "evt-instance-config-deeper-nested-object-value", PluginID: plugin.Manifest.ID, CorrelationID: "corr-instance-config-deeper-nested-object-value"}
	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err == nil || !strings.Contains(err.Error(), `nested instance config property "settings.labels.prefix" value type must match declared type "string", got "object"`) {
		t.Fatalf("expected deeper nested object instance config type mismatch rejection, got %v", err)
	}
	if launches != 0 {
		t.Fatalf("expected deeper nested object instance config rejection before subprocess launch, launches=%d", launches)
	}
	if len(host.StdoutLines()) != 0 || len(host.StderrLines()) != 0 {
		t.Fatalf("expected deeper nested object instance config rejection to avoid subprocess side effects, stdout=%v stderr=%v", host.StdoutLines(), host.StderrLines())
	}
	for _, expected := range []string{"subprocess host instance config rejected", `"failure_stage":"instance_config"`, `"failure_reason":"instance_config_value_type_mismatch"`, `"compatibility_rule":"instance_config_deeper_nested_value_type"`, `"property_name":"settings.labels.prefix"`, `"parent_property_name":"settings.labels"`, `"nested_property_name":"prefix"`, `"root_property_name":"settings"`, `"intermediate_property_name":"labels"`, `"actual_type":"object"`} {
		if !strings.Contains(logs.String(), expected) {
			t.Fatalf("expected deeper nested object instance config rejection log %q, got %s", expected, logs.String())
		}
	}
	rendered := tracer.RenderTrace("trace-instance-config-deeper-nested-object-value")
	if !strings.Contains(rendered, "plugin_host.instance_config") {
		t.Fatalf("expected deeper nested object instance config trace span, got %s", rendered)
	}
	if strings.Contains(rendered, "plugin_host.dispatch") {
		t.Fatalf("expected deeper nested object mismatch to stop before dispatch span, got %s", rendered)
	}
	if !strings.Contains(metrics.RenderPrometheus(), `bot_platform_plugin_errors_total{plugin_id="plugin-instance-config-deeper-nested-object-value"} 1`) {
		t.Fatalf("expected plugin error metric, got %s", metrics.RenderPrometheus())
	}
}

func TestBuildSubprocessHostRequestDoesNotInjectDeeperNestedManifestDefaultIntoInstanceConfig(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-deeper-nested-default-request",
			Name:       "InstanceConfigDeeperNestedDefaultRequest",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"labels": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"prefix": map[string]any{"type": "string", "default": "hello"},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-deeper-nested-default-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{}}},
	}

	request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err != nil {
		t.Fatalf("expected request build to preserve deeper nested object without injecting manifest default, got %v", err)
	}
	if request.InstanceConfig == nil {
		t.Fatal("expected host request to keep explicit deeper nested labels object")
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.InstanceConfig, &decoded); err != nil {
		t.Fatalf("expected deeper nested instance config payload to remain decodable, got %v", err)
	}
	settings, ok := decoded["settings"].(map[string]any)
	if !ok {
		t.Fatalf("expected settings object to remain present, got %+v", decoded)
	}
	labels, ok := settings["labels"].(map[string]any)
	if !ok {
		t.Fatalf("expected deeper nested labels object to remain present, got %+v", decoded)
	}
	if len(labels) != 0 {
		t.Fatalf("expected deeper nested labels object to remain empty when manifest default is omitted, got %+v", decoded)
	}
	if _, exists := labels["prefix"]; exists {
		t.Fatalf("expected deeper nested manifest default to stay absent from request payload, got %+v", decoded)
	}
	if string(request.InstanceConfig) != `{"settings":{"labels":{}}}` {
		t.Fatalf("expected deeper nested manifest default to stay uninjected in raw payload, got %s", string(request.InstanceConfig))
	}
}

func TestBuiltSubprocessFixtureDoesNotReceiveInjectedDeeperNestedManifestDefault(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-deeper-nested-default-not-injected"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-deeper-nested-default-fixture",
			Name:       "InstanceConfigDeeperNestedDefaultFixture",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"labels": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"prefix": map[string]any{"type": "string", "default": "hello"},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-deeper-nested-default-fixture", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{}}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-deeper-nested-default", EventID: "evt-instance-config-deeper-nested-default"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected built fixture request to omit deeper nested manifest default injection, got %v", err)
	}

	stdout := strings.Join(host.StdoutLines(), "\n")
	for _, expected := range []string{"handshake-ready", "event-ok"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("expected built fixture stdout to include %q, got %s", expected, stdout)
		}
	}
	stderr := strings.Join(host.StderrLines(), "\n")
	if !strings.Contains(stderr, "stderr-online") {
		t.Fatalf("expected built fixture stderr capture, got %s", stderr)
	}
}

func TestBuildSubprocessHostRequestRejectsBeyondSupportedDeeperNestedTypeMismatch(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-type-mismatch-request",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedTypeMismatchRequest",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"labels": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"naming": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"prefix": map[string]any{"type": "string"},
										},
									},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-type-mismatch-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{"prefix": true}}}},
	}

	_, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err == nil || !strings.Contains(err.Error(), `nested instance config property "settings.labels.naming.prefix" value type must match declared type "string", got "boolean"`) {
		t.Fatalf("expected request build to reject beyond-supported deeper nested type mismatch, got %v", err)
	}
	var configErr *subprocessInstanceConfigFailure
	if !errors.As(err, &configErr) {
		t.Fatalf("expected beyond-supported deeper nested request build error classification, got %T %v", err, err)
	}
	if configErr.reason != subprocessFailureReasonInstanceConfigValueTypeMismatch || configErr.compatibilityRule != "instance_config_deeper_nested_value_type" {
		t.Fatalf("expected deeper nested beyond-supported mismatch classification, got reason=%q rule=%q", configErr.reason, configErr.compatibilityRule)
	}
	for key, expected := range map[string]any{
		"property_name":        "settings.labels.naming.prefix",
		"parent_property_name": "settings.labels.naming",
		"nested_property_name": "prefix",
		"root_property_name":   "settings",
		"declared_type":        "string",
		"actual_type":          "boolean",
	} {
		if actual := configErr.metadata[key]; actual != expected {
			t.Fatalf("expected beyond-supported deeper nested mismatch metadata %q=%v, got %+v", key, expected, configErr.metadata)
		}
	}
}

func TestBuildSubprocessHostRequestRejectsBeyondSupportedDeeperNestedObjectNodeMismatch(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-object-node-mismatch-request",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedObjectNodeMismatchRequest",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"labels": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"naming": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"prefix": map[string]any{"type": "string"},
										},
									},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-object-node-mismatch-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": true}}},
	}

	_, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err == nil || !strings.Contains(err.Error(), `nested instance config property "settings.labels.naming" value type must match declared type "object", got "boolean"`) {
		t.Fatalf("expected request build to reject beyond-supported deeper nested object-node mismatch, got %v", err)
	}
	var configErr *subprocessInstanceConfigFailure
	if !errors.As(err, &configErr) {
		t.Fatalf("expected beyond-supported deeper nested object-node request build error classification, got %T %v", err, err)
	}
	if configErr.reason != subprocessFailureReasonInstanceConfigValueTypeMismatch || configErr.compatibilityRule != "instance_config_deeper_nested_value_type" {
		t.Fatalf("expected deeper nested object-node mismatch classification, got reason=%q rule=%q", configErr.reason, configErr.compatibilityRule)
	}
	for key, expected := range map[string]any{
		"property_name":              "settings.labels.naming",
		"parent_property_name":       "settings.labels",
		"nested_property_name":       "naming",
		"root_property_name":         "settings",
		"intermediate_property_name": "labels",
		"declared_type":              "object",
		"actual_type":                "boolean",
	} {
		if actual := configErr.metadata[key]; actual != expected {
			t.Fatalf("expected beyond-supported deeper nested object-node mismatch metadata %q=%v, got %+v", key, expected, configErr.metadata)
		}
	}
}

func TestBuildSubprocessHostRequestMergesBeyondSupportedDeeperNestedDefaultIntoExistingNamingBranch(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-default-request",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedDefaultRequest",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"labels": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"naming": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"prefix": map[string]any{"type": "string", "default": "hello"},
										},
									},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-default-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{}}}},
	}

	request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err != nil {
		t.Fatalf("expected request build to merge beyond-supported deeper nested default into existing naming branch, got %v", err)
	}
	if request.InstanceConfig == nil {
		t.Fatal("expected host request to keep beyond-supported deeper nested payload")
	}
	if string(request.InstanceConfig) != `{"settings":{"labels":{"naming":{"prefix":"hello"}}}}` {
		t.Fatalf("expected beyond-supported deeper nested default merge in raw payload, got %s", string(request.InstanceConfig))
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.InstanceConfig, &decoded); err != nil {
		t.Fatalf("expected merged beyond-supported deeper nested payload to remain decodable, got %v", err)
	}
	settings, ok := decoded["settings"].(map[string]any)
	if !ok {
		t.Fatalf("expected settings object to remain present, got %+v", decoded)
	}
	labels, ok := settings["labels"].(map[string]any)
	if !ok {
		t.Fatalf("expected labels object to remain present, got %+v", decoded)
	}
	naming, ok := labels["naming"].(map[string]any)
	if !ok {
		t.Fatalf("expected naming object to remain present, got %+v", decoded)
	}
	prefix, ok := naming["prefix"].(string)
	if !ok || prefix != "hello" {
		t.Fatalf("expected beyond-supported deeper nested merged default leaf, got %+v", decoded)
	}
	if originalNaming := plugin.InstanceConfig["settings"].(map[string]any)["labels"].(map[string]any)["naming"].(map[string]any); len(originalNaming) != 0 {
		t.Fatalf("expected original plugin instance config branch to remain unmodified, got %+v", plugin.InstanceConfig)
	}
}

func TestBuiltSubprocessFixtureReceivesBeyondSupportedDeeperNestedMergedDefault(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-default-merged"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-default-fixture",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedDefaultFixture",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"labels": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"naming": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"prefix": map[string]any{"type": "string", "default": "hello"},
										},
									},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-default-fixture", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{}}}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-beyond-supported-deeper-nested-default-merged", EventID: "evt-instance-config-beyond-supported-deeper-nested-default-merged"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected built fixture request to receive merged beyond-supported deeper nested default, got %v", err)
	}

	stdout := strings.Join(host.StdoutLines(), "\n")
	for _, expected := range []string{"handshake-ready", "event-ok"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("expected built fixture stdout to include %q, got %s", expected, stdout)
		}
	}
	stderr := strings.Join(host.StderrLines(), "\n")
	if !strings.Contains(stderr, "stderr-online") {
		t.Fatalf("expected built fixture stderr capture, got %s", stderr)
	}
}

func TestSubprocessPluginHostRejectsBeyondSupportedDeeperNestedObjectNodeMismatchBeforeProcessLaunch(t *testing.T) {
	t.Parallel()

	launches := 0
	logs := &bytes.Buffer{}
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHost(func(ctx context.Context) *exec.Cmd {
		launches++
		return testPluginProcessFactory(t, "echo")(ctx)
	})
	host.SetObservability(NewLogger(logs), tracer, metrics)
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-object-node-mismatch-fixture",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedObjectNodeMismatchFixture",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"labels": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"naming": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"prefix": map[string]any{"type": "string"},
										},
									},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-object-node-mismatch-fixture", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": true}}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-beyond-supported-deeper-nested-object-node-mismatch", EventID: "evt-instance-config-beyond-supported-deeper-nested-object-node-mismatch", PluginID: plugin.Manifest.ID, CorrelationID: "corr-instance-config-beyond-supported-deeper-nested-object-node-mismatch"}

	err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx)
	if err == nil || !strings.Contains(err.Error(), `nested instance config property "settings.labels.naming" value type must match declared type "object", got "boolean"`) {
		t.Fatalf("expected deeper nested object-node mismatch rejection, got %v", err)
	}
	if launches != 0 {
		t.Fatalf("expected deeper nested object-node mismatch rejection before subprocess launch, launches=%d", launches)
	}
	if len(host.StdoutLines()) != 0 || len(host.StderrLines()) != 0 {
		t.Fatalf("expected deeper nested object-node mismatch rejection to avoid subprocess side effects, stdout=%v stderr=%v", host.StdoutLines(), host.StderrLines())
	}
	for _, expected := range []string{"subprocess host instance config rejected", `"failure_stage":"instance_config"`, `"failure_reason":"instance_config_value_type_mismatch"`, `"compatibility_rule":"instance_config_deeper_nested_value_type"`, `"property_name":"settings.labels.naming"`, `"parent_property_name":"settings.labels"`, `"nested_property_name":"naming"`, `"root_property_name":"settings"`, `"intermediate_property_name":"labels"`, `"declared_type":"object"`, `"actual_type":"boolean"`} {
		if !strings.Contains(logs.String(), expected) {
			t.Fatalf("expected deeper nested object-node mismatch rejection log %q, got %s", expected, logs.String())
		}
	}
	rendered := tracer.RenderTrace("trace-instance-config-beyond-supported-deeper-nested-object-node-mismatch")
	if !strings.Contains(rendered, "plugin_host.instance_config") {
		t.Fatalf("expected deeper nested object-node mismatch trace span, got %s", rendered)
	}
	if strings.Contains(rendered, "plugin_host.dispatch") {
		t.Fatalf("expected deeper nested object-node mismatch rejection to stop before dispatch span, got %s", rendered)
	}
	if !strings.Contains(metrics.RenderPrometheus(), `bot_platform_plugin_errors_total{plugin_id="plugin-instance-config-beyond-supported-deeper-nested-object-node-mismatch-fixture"} 1`) {
		t.Fatalf("expected plugin error metric, got %s", metrics.RenderPrometheus())
	}
}

func testBeyondSupportedDeeperNestedNamingManifest(id string, name string, module string, prefixSchema map[string]any, namingRequired bool) pluginsdk.PluginManifest {
	namingSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prefix": prefixSchema,
		},
	}
	if namingRequired {
		namingSchema["required"] = []any{"prefix"}
	}
	return pluginsdk.PluginManifest{
		ID:         id,
		Name:       name,
		Version:    "0.1.0",
		APIVersion: "v0",
		Mode:       pluginsdk.ModeSubprocess,
		ConfigSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"settings": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"labels": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"naming": namingSchema,
							},
						},
					},
				},
			},
		},
		Entry: pluginsdk.PluginEntry{Module: module, Symbol: "Plugin"},
	}
}

func testBeyondSupportedDeeperNestedNamingInstanceConfig(prefixValue any) map[string]any {
	return map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{"prefix": prefixValue}}}}
}

func testBeyondSupportedDeeperNestedEmptyNamingInstanceConfig() map[string]any {
	return map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{}}}}
}

func TestBuildSubprocessHostRequestRejectsBeyondSupportedDeeperNestedValidationOnExistingNamingBranch(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name           string
		pluginID       string
		prefixSchema   map[string]any
		namingRequired bool
		instanceConfig map[string]any
		wantErr        string
		wantReason     string
		wantRule       string
		wantMetadata   map[string]any
		wantEnumValues []any
	}

	cases := []testCase{
		{
			name:           "required_missing",
			pluginID:       "plugin-instance-config-beyond-supported-deeper-nested-required-request",
			prefixSchema:   map[string]any{"type": "string"},
			namingRequired: true,
			instanceConfig: testBeyondSupportedDeeperNestedEmptyNamingInstanceConfig(),
			wantErr:        `nested instance config required property "settings.labels.naming.prefix" must be provided`,
			wantReason:     string(subprocessFailureReasonInstanceConfigMissingRequired),
			wantRule:       "instance_config_deeper_nested_required",
			wantMetadata: map[string]any{
				"property_name":        "settings.labels.naming.prefix",
				"parent_property_name": "settings.labels.naming",
				"nested_property_name": "prefix",
				"root_property_name":   "settings",
				"declared_type":        "string",
			},
		},
		{
			name:           "required_enum_missing",
			pluginID:       "plugin-instance-config-beyond-supported-deeper-nested-required-enum-request",
			prefixSchema:   map[string]any{"type": "string", "enum": []any{"hello", "world"}},
			namingRequired: true,
			instanceConfig: testBeyondSupportedDeeperNestedEmptyNamingInstanceConfig(),
			wantErr:        `nested instance config required property "settings.labels.naming.prefix" must be provided`,
			wantReason:     string(subprocessFailureReasonInstanceConfigMissingRequired),
			wantRule:       "instance_config_deeper_nested_required",
			wantMetadata: map[string]any{
				"property_name":        "settings.labels.naming.prefix",
				"parent_property_name": "settings.labels.naming",
				"nested_property_name": "prefix",
				"root_property_name":   "settings",
				"declared_type":        "string",
			},
		},
		{
			name:           "enum_out_of_set",
			pluginID:       "plugin-instance-config-beyond-supported-deeper-nested-enum-request",
			prefixSchema:   map[string]any{"type": "string", "enum": []any{"hello", "world"}},
			instanceConfig: testBeyondSupportedDeeperNestedNamingInstanceConfig("oops"),
			wantErr:        `nested instance config property "settings.labels.naming.prefix" value "oops" must be declared in enum ["hello","world"] for declared type "string"`,
			wantReason:     string(subprocessFailureReasonInstanceConfigEnumValueOutOfSet),
			wantRule:       "instance_config_deeper_nested_enum",
			wantMetadata: map[string]any{
				"property_name":        "settings.labels.naming.prefix",
				"parent_property_name": "settings.labels.naming",
				"nested_property_name": "prefix",
				"root_property_name":   "settings",
				"declared_type":        "string",
				"actual_value":         "oops",
			},
			wantEnumValues: []any{"hello", "world"},
		},
		{
			name:           "required_enum_default_explicit_bad_value",
			pluginID:       "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-bad-value-request",
			prefixSchema:   map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"},
			namingRequired: true,
			instanceConfig: testBeyondSupportedDeeperNestedNamingInstanceConfig("oops"),
			wantErr:        `nested instance config property "settings.labels.naming.prefix" value "oops" must be declared in enum ["hello","world"] for declared type "string"`,
			wantReason:     string(subprocessFailureReasonInstanceConfigEnumValueOutOfSet),
			wantRule:       "instance_config_deeper_nested_enum",
			wantMetadata: map[string]any{
				"property_name":        "settings.labels.naming.prefix",
				"parent_property_name": "settings.labels.naming",
				"nested_property_name": "prefix",
				"root_property_name":   "settings",
				"declared_type":        "string",
				"actual_value":         "oops",
			},
			wantEnumValues: []any{"hello", "world"},
		},
		{
			name:           "required_enum_default_explicit_array_bad_value",
			pluginID:       "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-array-valued-bad-value-request",
			prefixSchema:   map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"},
			namingRequired: true,
			instanceConfig: testBeyondSupportedDeeperNestedNamingInstanceConfig([]any{"oops"}),
			wantErr:        `nested instance config property "settings.labels.naming.prefix" value type must match declared type "string", got "array"`,
			wantReason:     string(subprocessFailureReasonInstanceConfigValueTypeMismatch),
			wantRule:       "instance_config_deeper_nested_value_type",
			wantMetadata: map[string]any{
				"property_name":        "settings.labels.naming.prefix",
				"parent_property_name": "settings.labels.naming",
				"nested_property_name": "prefix",
				"root_property_name":   "settings",
				"declared_type":        "string",
				"actual_type":          "array",
			},
		},
		{
			name:           "required_enum_default_explicit_wrong_type_bad_value",
			pluginID:       "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-wrong-type-bad-value-request",
			prefixSchema:   map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"},
			namingRequired: true,
			instanceConfig: testBeyondSupportedDeeperNestedNamingInstanceConfig(true),
			wantErr:        `nested instance config property "settings.labels.naming.prefix" value type must match declared type "string", got "boolean"`,
			wantReason:     string(subprocessFailureReasonInstanceConfigValueTypeMismatch),
			wantRule:       "instance_config_deeper_nested_value_type",
			wantMetadata: map[string]any{
				"property_name":        "settings.labels.naming.prefix",
				"parent_property_name": "settings.labels.naming",
				"nested_property_name": "prefix",
				"root_property_name":   "settings",
				"declared_type":        "string",
				"actual_type":          "boolean",
			},
		},
		{
			name:           "required_enum_default_explicit_object_bad_value",
			pluginID:       "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-object-valued-bad-value-request",
			prefixSchema:   map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"},
			namingRequired: true,
			instanceConfig: testBeyondSupportedDeeperNestedNamingInstanceConfig(map[string]any{"bad": true}),
			wantErr:        `nested instance config property "settings.labels.naming.prefix" value type must match declared type "string", got "object"`,
			wantReason:     string(subprocessFailureReasonInstanceConfigValueTypeMismatch),
			wantRule:       "instance_config_deeper_nested_value_type",
			wantMetadata: map[string]any{
				"property_name":        "settings.labels.naming.prefix",
				"parent_property_name": "settings.labels.naming",
				"nested_property_name": "prefix",
				"root_property_name":   "settings",
				"declared_type":        "string",
				"actual_type":          "object",
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			plugin := pluginsdk.Plugin{
				Manifest:       testBeyondSupportedDeeperNestedNamingManifest(tc.pluginID, tc.pluginID, "plugins/"+tc.pluginID, tc.prefixSchema, tc.namingRequired),
				InstanceConfig: tc.instanceConfig,
			}

			_, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected request build rejection containing %q, got %v", tc.wantErr, err)
			}
			var configErr *subprocessInstanceConfigFailure
			if !errors.As(err, &configErr) {
				t.Fatalf("expected request build error classification, got %T %v", err, err)
			}
			if string(configErr.reason) != tc.wantReason {
				t.Fatalf("expected request build failure reason %q, got %q", tc.wantReason, configErr.reason)
			}
			if configErr.compatibilityRule != tc.wantRule {
				t.Fatalf("expected request build compatibility rule %q, got %q", tc.wantRule, configErr.compatibilityRule)
			}
			for key, expected := range tc.wantMetadata {
				if actual := configErr.metadata[key]; actual != expected {
					t.Fatalf("expected request build metadata %q=%v, got %+v", key, expected, configErr.metadata)
				}
			}
			if tc.wantEnumValues != nil {
				enumValues, ok := configErr.metadata["enum_values"].([]any)
				if !ok || len(enumValues) != len(tc.wantEnumValues) {
					t.Fatalf("expected enum_values metadata, got %+v", configErr.metadata)
				}
				for index, expected := range tc.wantEnumValues {
					if enumValues[index] != expected {
						t.Fatalf("expected enum_values[%d]=%v, got %+v", index, expected, enumValues)
					}
				}
			}
		})
	}
}

func TestSubprocessPluginHostRejectsBeyondSupportedDeeperNestedValidationOnExistingNamingBranchBeforeProcessLaunch(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name            string
		pluginID        string
		prefixSchema    map[string]any
		namingRequired  bool
		instanceConfig  map[string]any
		wantErr         string
		wantLogSnippets []string
	}

	cases := []testCase{
		{
			name:            "required_missing",
			pluginID:        "plugin-instance-config-beyond-supported-deeper-nested-required-fixture",
			prefixSchema:    map[string]any{"type": "string"},
			namingRequired:  true,
			instanceConfig:  testBeyondSupportedDeeperNestedEmptyNamingInstanceConfig(),
			wantErr:         `nested instance config required property "settings.labels.naming.prefix" must be provided`,
			wantLogSnippets: []string{`"failure_reason":"instance_config_missing_required_value"`, `"compatibility_rule":"instance_config_deeper_nested_required"`, `"property_name":"settings.labels.naming.prefix"`, `"declared_type":"string"`},
		},
		{
			name:            "enum_out_of_set",
			pluginID:        "plugin-instance-config-beyond-supported-deeper-nested-enum-fixture",
			prefixSchema:    map[string]any{"type": "string", "enum": []any{"hello", "world"}},
			instanceConfig:  testBeyondSupportedDeeperNestedNamingInstanceConfig("oops"),
			wantErr:         `nested instance config property "settings.labels.naming.prefix" value "oops" must be declared in enum ["hello","world"] for declared type "string"`,
			wantLogSnippets: []string{`"failure_reason":"instance_config_enum_value_out_of_set"`, `"compatibility_rule":"instance_config_deeper_nested_enum"`, `"property_name":"settings.labels.naming.prefix"`, `"actual_value":"oops"`, `"enum_values":["hello","world"]`},
		},
		{
			name:            "required_enum_default_explicit_wrong_type_bad_value",
			pluginID:        "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-wrong-type-bad-value-fixture",
			prefixSchema:    map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"},
			namingRequired:  true,
			instanceConfig:  testBeyondSupportedDeeperNestedNamingInstanceConfig(true),
			wantErr:         `nested instance config property "settings.labels.naming.prefix" value type must match declared type "string", got "boolean"`,
			wantLogSnippets: []string{`"failure_reason":"instance_config_value_type_mismatch"`, `"compatibility_rule":"instance_config_deeper_nested_value_type"`, `"property_name":"settings.labels.naming.prefix"`, `"actual_type":"boolean"`},
		},
		{
			name:            "required_enum_default_explicit_array_bad_value",
			pluginID:        "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-array-valued-bad-value-fixture",
			prefixSchema:    map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"},
			namingRequired:  true,
			instanceConfig:  testBeyondSupportedDeeperNestedNamingInstanceConfig([]any{"oops"}),
			wantErr:         `nested instance config property "settings.labels.naming.prefix" value type must match declared type "string", got "array"`,
			wantLogSnippets: []string{`"failure_reason":"instance_config_value_type_mismatch"`, `"compatibility_rule":"instance_config_deeper_nested_value_type"`, `"property_name":"settings.labels.naming.prefix"`, `"actual_type":"array"`},
		},
		{
			name:            "required_enum_default_explicit_object_bad_value",
			pluginID:        "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-object-valued-bad-value-fixture",
			prefixSchema:    map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"},
			namingRequired:  true,
			instanceConfig:  testBeyondSupportedDeeperNestedNamingInstanceConfig(map[string]any{"bad": true}),
			wantErr:         `nested instance config property "settings.labels.naming.prefix" value type must match declared type "string", got "object"`,
			wantLogSnippets: []string{`"failure_reason":"instance_config_value_type_mismatch"`, `"compatibility_rule":"instance_config_deeper_nested_value_type"`, `"property_name":"settings.labels.naming.prefix"`, `"actual_type":"object"`},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			launches := 0
			logs := &bytes.Buffer{}
			tracer := NewTraceRecorder()
			metrics := NewMetricsRegistry()
			host := NewSubprocessPluginHost(func(ctx context.Context) *exec.Cmd {
				launches++
				return testPluginProcessFactory(t, "echo")(ctx)
			})
			host.SetObservability(NewLogger(logs), tracer, metrics)
			plugin := pluginsdk.Plugin{
				Manifest:       testBeyondSupportedDeeperNestedNamingManifest(tc.pluginID, tc.pluginID, "plugins/"+tc.pluginID, tc.prefixSchema, tc.namingRequired),
				InstanceConfig: tc.instanceConfig,
			}
			ctx := eventmodel.ExecutionContext{TraceID: "trace-" + tc.pluginID, EventID: "evt-" + tc.pluginID, PluginID: tc.pluginID, CorrelationID: "corr-" + tc.pluginID}

			err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected host rejection containing %q, got %v", tc.wantErr, err)
			}
			if launches != 0 {
				t.Fatalf("expected host rejection before subprocess launch, launches=%d", launches)
			}
			if len(host.StdoutLines()) != 0 || len(host.StderrLines()) != 0 {
				t.Fatalf("expected host rejection to avoid subprocess side effects, stdout=%v stderr=%v", host.StdoutLines(), host.StderrLines())
			}
			for _, expected := range append([]string{"subprocess host instance config rejected", `"failure_stage":"instance_config"`}, tc.wantLogSnippets...) {
				if !strings.Contains(logs.String(), expected) {
					t.Fatalf("expected host rejection log %q, got %s", expected, logs.String())
				}
			}
			rendered := tracer.RenderTrace("trace-" + tc.pluginID)
			if !strings.Contains(rendered, "plugin_host.instance_config") {
				t.Fatalf("expected instance config trace span, got %s", rendered)
			}
			if strings.Contains(rendered, "plugin_host.dispatch") {
				t.Fatalf("expected rejection to stop before dispatch span, got %s", rendered)
			}
			if !strings.Contains(metrics.RenderPrometheus(), fmt.Sprintf(`bot_platform_plugin_errors_total{plugin_id=%q} 1`, tc.pluginID)) {
				t.Fatalf("expected plugin error metric, got %s", metrics.RenderPrometheus())
			}
		})
	}
}

func TestBuildSubprocessHostRequestMergesBeyondSupportedDeeperNestedDefaultVariantsOnExistingNamingBranch(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		pluginID       string
		prefixSchema   map[string]any
		namingRequired bool
	}{
		{name: "default_only", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-default-request-variant", prefixSchema: map[string]any{"type": "string", "default": "hello"}},
		{name: "required_default", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-default-request", prefixSchema: map[string]any{"type": "string", "default": "hello"}, namingRequired: true},
		{name: "enum_default", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-enum-default-request", prefixSchema: map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"}},
		{name: "required_enum_default", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-request", prefixSchema: map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"}, namingRequired: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			instanceConfig := testBeyondSupportedDeeperNestedEmptyNamingInstanceConfig()
			plugin := pluginsdk.Plugin{
				Manifest:       testBeyondSupportedDeeperNestedNamingManifest(tc.pluginID, tc.pluginID, "plugins/"+tc.pluginID, tc.prefixSchema, tc.namingRequired),
				InstanceConfig: instanceConfig,
			}

			request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
			if err != nil {
				t.Fatalf("expected request build to merge deeper nested default, got %v", err)
			}
			if request.InstanceConfig == nil {
				t.Fatal("expected host request to include merged deeper nested payload")
			}
			if string(request.InstanceConfig) != `{"settings":{"labels":{"naming":{"prefix":"hello"}}}}` {
				t.Fatalf("expected merged deeper nested default payload, got %s", string(request.InstanceConfig))
			}
			var decoded map[string]any
			if err := json.Unmarshal(request.InstanceConfig, &decoded); err != nil {
				t.Fatalf("expected merged payload to remain decodable, got %v", err)
			}
			prefix := decoded["settings"].(map[string]any)["labels"].(map[string]any)["naming"].(map[string]any)["prefix"]
			if prefix != "hello" {
				t.Fatalf("expected merged default leaf, got %+v", decoded)
			}
			originalNaming := instanceConfig["settings"].(map[string]any)["labels"].(map[string]any)["naming"].(map[string]any)
			if len(originalNaming) != 0 {
				t.Fatalf("expected original instance config branch to remain unmodified, got %+v", instanceConfig)
			}
		})
	}
}

func TestBuildSubprocessHostRequestMaterializesBeyondSupportedDeeperNestedRequiredEnumDefaultChildOmissionBoundary(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-child-omission-request",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultChildOmissionRequest",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"labels": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"naming": map[string]any{
										"type":     "object",
										"required": []any{"prefix"},
										"properties": map[string]any{
											"prefix": map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"},
										},
									},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-child-omission-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{}}},
	}

	request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err != nil {
		t.Fatalf("expected request build to synthesize beyond-supported deeper nested child omission branch without rejection, got %v", err)
	}
	if request.InstanceConfig == nil {
		t.Fatal("expected host request to include synthesized beyond-supported deeper nested child omission payload")
	}
	if string(request.InstanceConfig) != `{"settings":{"labels":{"naming":{"prefix":"hello"}}}}` {
		t.Fatalf("expected beyond-supported deeper nested child omission payload to synthesize naming default in raw payload, got %s", string(request.InstanceConfig))
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.InstanceConfig, &decoded); err != nil {
		t.Fatalf("expected beyond-supported deeper nested child omission payload to remain decodable, got %v", err)
	}
	settings, ok := decoded["settings"].(map[string]any)
	if !ok {
		t.Fatalf("expected settings object to remain present, got %+v", decoded)
	}
	labels, ok := settings["labels"].(map[string]any)
	if !ok {
		t.Fatalf("expected labels object to remain present, got %+v", decoded)
	}
	naming, ok := labels["naming"].(map[string]any)
	if !ok {
		t.Fatalf("expected synthesized naming object to be present, got %+v", decoded)
	}
	prefix, ok := naming["prefix"].(string)
	if !ok || prefix != "hello" {
		t.Fatalf("expected synthesized naming prefix default, got %+v", decoded)
	}
}

func TestBuildSubprocessHostRequestPreservesBeyondSupportedDeeperNestedRequiredEnumDefaultParentOmissionBoundary(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-parent-omission-request",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultParentOmissionRequest",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"labels": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"naming": map[string]any{
										"type":     "object",
										"required": []any{"prefix"},
										"properties": map[string]any{
											"prefix": map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"},
										},
									},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-parent-omission-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{}},
	}

	request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err != nil {
		t.Fatalf("expected request build to preserve beyond-supported deeper nested required+enum+default parent omission boundary without rejection, got %v", err)
	}
	if request.InstanceConfig == nil {
		t.Fatal("expected host request to preserve beyond-supported deeper nested required+enum+default parent omission payload")
	}
	if string(request.InstanceConfig) != `{"settings":{}}` {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default parent omission payload to remain unmodified in raw payload, got %s", string(request.InstanceConfig))
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.InstanceConfig, &decoded); err != nil {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default parent omission payload to remain decodable, got %v", err)
	}
	settings, ok := decoded["settings"].(map[string]any)
	if !ok {
		t.Fatalf("expected settings object to remain present, got %+v", decoded)
	}
	if len(settings) != 0 {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default parent omission boundary to keep settings object empty, got %+v", decoded)
	}
	if _, exists := settings["labels"]; exists {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default parent omission to keep labels key absent in request payload, got %+v", decoded)
	}
}

func TestBuildSubprocessHostRequestPreservesBeyondSupportedDeeperNestedRequiredEnumDefaultRootOmissionBoundary(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-request",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultRootOmissionRequest",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"labels": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"naming": map[string]any{
										"type":     "object",
										"required": []any{"prefix"},
										"properties": map[string]any{
											"prefix": map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"},
										},
									},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{},
	}

	request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err != nil {
		t.Fatalf("expected request build to preserve beyond-supported deeper nested required+enum+default root omission boundary without rejection, got %v", err)
	}
	if request.InstanceConfig != nil {
		t.Fatalf("expected host request to omit beyond-supported deeper nested required+enum+default root omission payload, got %s", string(request.InstanceConfig))
	}
	encodedRequest, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("expected root omission host request to remain encodable, got %v", err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(encodedRequest, &envelope); err != nil {
		t.Fatalf("expected root omission host request to remain decodable, got %v", err)
	}
	if _, exists := envelope["instance_config"]; exists {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default root omission boundary to omit instance_config entirely, got %+v", envelope)
	}
}

func TestBuildSubprocessHostRequestTreatsNilAndEmptyInstanceConfigAsEquivalentBeyondSupportedDeeperNestedRequiredEnumDefaultRootOmissionBoundary(t *testing.T) {
	t.Parallel()

	buildEnvelope := func(instanceConfig map[string]any) ([]byte, hostRequest) {
		t.Helper()
		plugin := pluginsdk.Plugin{
			Manifest: pluginsdk.PluginManifest{
				ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-equivalence-request",
				Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultRootOmissionEquivalenceRequest",
				Version:    "0.1.0",
				APIVersion: "v0",
				Mode:       pluginsdk.ModeSubprocess,
				ConfigSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"settings": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"labels": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"naming": map[string]any{
											"type":     "object",
											"required": []any{"prefix"},
											"properties": map[string]any{
												"prefix": map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"},
											},
										},
									},
								},
							},
						},
					},
				},
				Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-equivalence-request", Symbol: "Plugin"},
			},
			InstanceConfig: instanceConfig,
		}

		request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
		if err != nil {
			t.Fatalf("expected request build to accept equivalent root omission instance config, got %v", err)
		}
		if request.InstanceConfig != nil {
			t.Fatalf("expected equivalent root omission payload to omit instance_config entirely, got %s", string(request.InstanceConfig))
		}
		encodedRequest, err := json.Marshal(request)
		if err != nil {
			t.Fatalf("expected equivalent root omission request to remain encodable, got %v", err)
		}
		var envelope map[string]any
		if err := json.Unmarshal(encodedRequest, &envelope); err != nil {
			t.Fatalf("expected equivalent root omission request to remain decodable, got %v", err)
		}
		if _, exists := envelope["instance_config"]; exists {
			t.Fatalf("expected equivalent root omission request to omit instance_config, got %+v", envelope)
		}
		return encodedRequest, request
	}

	emptyEncoded, emptyRequest := buildEnvelope(map[string]any{})
	nilEncoded, nilRequest := buildEnvelope(nil)
	if emptyRequest.InstanceConfig != nil || nilRequest.InstanceConfig != nil {
		t.Fatalf("expected nil-map and empty-map root omission requests to both omit instance_config, got empty=%v nil=%v", emptyRequest.InstanceConfig, nilRequest.InstanceConfig)
	}
	if string(emptyEncoded) != string(nilEncoded) {
		t.Fatalf("expected nil-map and empty-map root omission requests to encode identically, got empty=%s nil=%s", string(emptyEncoded), string(nilEncoded))
	}
}

func TestBuildSubprocessHostRequestTreatsNilAndEmptyInstanceConfigAsEquivalentBeyondSupportedDeeperNestedRequiredEnumDefaultRootOmissionBoundaryForCommand(t *testing.T) {
	t.Parallel()

	payload := json.RawMessage(`{"command":{"name":"admin","raw":"/admin enable plugin-command-root-omission"},"ctx":{"trace_id":"trace-command-root-omission","event_id":"evt-command-root-omission"}}`)
	buildEnvelope := func(instanceConfig map[string]any) ([]byte, hostRequest) {
		t.Helper()
		plugin := pluginsdk.Plugin{
			Manifest: pluginsdk.PluginManifest{
				ID:         "plugin-command-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-equivalence-request",
				Name:       "CommandInstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultRootOmissionEquivalenceRequest",
				Version:    "0.1.0",
				APIVersion: "v0",
				Mode:       pluginsdk.ModeSubprocess,
				ConfigSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"settings": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"labels": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"naming": map[string]any{
											"type":     "object",
											"required": []any{"prefix"},
											"properties": map[string]any{
												"prefix": map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"},
											},
										},
									},
								},
							},
						},
					},
				},
				Entry: pluginsdk.PluginEntry{Module: "plugins/command-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-equivalence-request", Symbol: "Plugin"},
			},
			InstanceConfig: instanceConfig,
		}

		request, err := buildSubprocessHostRequest("command", payload, plugin)
		if err != nil {
			t.Fatalf("expected command request build to accept equivalent root omission instance config, got %v", err)
		}
		if request.InstanceConfig != nil {
			t.Fatalf("expected equivalent command root omission payload to omit instance_config entirely, got %s", string(request.InstanceConfig))
		}
		if string(request.Command) != string(payload) {
			t.Fatalf("expected command payload to remain unchanged, got %s", string(request.Command))
		}
		encodedRequest, err := json.Marshal(request)
		if err != nil {
			t.Fatalf("expected equivalent command root omission request to remain encodable, got %v", err)
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(encodedRequest, &envelope); err != nil {
			t.Fatalf("expected equivalent command root omission request to remain decodable, got %v", err)
		}
		if _, exists := envelope["instance_config"]; exists {
			t.Fatalf("expected equivalent command root omission request to omit instance_config, got %s", string(encodedRequest))
		}
		rawCommand, exists := envelope["command"]
		if !exists {
			t.Fatalf("expected command root omission request to preserve command payload, got %s", string(encodedRequest))
		}
		if string(rawCommand) != string(payload) {
			t.Fatalf("expected encoded command payload to remain unchanged, got %s", string(rawCommand))
		}
		return encodedRequest, request
	}

	emptyEncoded, emptyRequest := buildEnvelope(map[string]any{})
	nilEncoded, nilRequest := buildEnvelope(nil)
	if emptyRequest.InstanceConfig != nil || nilRequest.InstanceConfig != nil {
		t.Fatalf("expected nil-map and empty-map command root omission requests to both omit instance_config, got empty=%v nil=%v", emptyRequest.InstanceConfig, nilRequest.InstanceConfig)
	}
	if string(emptyEncoded) != string(nilEncoded) {
		t.Fatalf("expected nil-map and empty-map command root omission requests to encode identically, got empty=%s nil=%s", string(emptyEncoded), string(nilEncoded))
	}
}

func TestBuildSubprocessHostRequestTreatsNilAndEmptyInstanceConfigAsEquivalentBeyondSupportedDeeperNestedRequiredEnumDefaultRootOmissionBoundaryForJob(t *testing.T) {
	t.Parallel()

	payload := json.RawMessage(`{"job":{"id":"job-root-omission","type":"ai.chat","metadata":{"source":"job-root-omission"}},"ctx":{"trace_id":"trace-job-root-omission","event_id":"evt-job-root-omission"}}`)
	buildEnvelope := func(instanceConfig map[string]any) ([]byte, hostRequest) {
		t.Helper()
		plugin := pluginsdk.Plugin{
			Manifest: pluginsdk.PluginManifest{
				ID:         "plugin-job-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-equivalence-request",
				Name:       "JobInstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultRootOmissionEquivalenceRequest",
				Version:    "0.1.0",
				APIVersion: "v0",
				Mode:       pluginsdk.ModeSubprocess,
				ConfigSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"settings": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"labels": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"naming": map[string]any{
											"type":     "object",
											"required": []any{"prefix"},
											"properties": map[string]any{
												"prefix": map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"},
											},
										},
									},
								},
							},
						},
					},
				},
				Entry: pluginsdk.PluginEntry{Module: "plugins/job-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-equivalence-request", Symbol: "Plugin"},
			},
			InstanceConfig: instanceConfig,
		}

		request, err := buildSubprocessHostRequest("job", payload, plugin)
		if err != nil {
			t.Fatalf("expected job request build to accept equivalent root omission instance config, got %v", err)
		}
		if request.InstanceConfig != nil {
			t.Fatalf("expected equivalent job root omission payload to omit instance_config entirely, got %s", string(request.InstanceConfig))
		}
		if string(request.Job) != string(payload) {
			t.Fatalf("expected job payload to remain unchanged, got %s", string(request.Job))
		}
		encodedRequest, err := json.Marshal(request)
		if err != nil {
			t.Fatalf("expected equivalent job root omission request to remain encodable, got %v", err)
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(encodedRequest, &envelope); err != nil {
			t.Fatalf("expected equivalent job root omission request to remain decodable, got %v", err)
		}
		if _, exists := envelope["instance_config"]; exists {
			t.Fatalf("expected equivalent job root omission request to omit instance_config, got %s", string(encodedRequest))
		}
		rawJob, exists := envelope["job"]
		if !exists {
			t.Fatalf("expected job root omission request to preserve job payload, got %s", string(encodedRequest))
		}
		if string(rawJob) != string(payload) {
			t.Fatalf("expected encoded job payload to remain unchanged, got %s", string(rawJob))
		}
		return encodedRequest, request
	}

	emptyEncoded, emptyRequest := buildEnvelope(map[string]any{})
	nilEncoded, nilRequest := buildEnvelope(nil)
	if emptyRequest.InstanceConfig != nil || nilRequest.InstanceConfig != nil {
		t.Fatalf("expected nil-map and empty-map job root omission requests to both omit instance_config, got empty=%v nil=%v", emptyRequest.InstanceConfig, nilRequest.InstanceConfig)
	}
	if string(emptyEncoded) != string(nilEncoded) {
		t.Fatalf("expected nil-map and empty-map job root omission requests to encode identically, got empty=%s nil=%s", string(emptyEncoded), string(nilEncoded))
	}
}

func TestBuildSubprocessHostRequestTreatsNilAndEmptyInstanceConfigAsEquivalentBeyondSupportedDeeperNestedRequiredEnumDefaultRootOmissionBoundaryForSchedule(t *testing.T) {
	t.Parallel()

	payload := json.RawMessage(`{"schedule_trigger":{"id":"schedule-root-omission","type":"cron","metadata":{"source":"schedule-root-omission"}},"ctx":{"trace_id":"trace-schedule-root-omission","event_id":"evt-schedule-root-omission"}}`)
	buildEnvelope := func(instanceConfig map[string]any) ([]byte, hostRequest) {
		t.Helper()
		plugin := pluginsdk.Plugin{
			Manifest: pluginsdk.PluginManifest{
				ID:         "plugin-schedule-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-equivalence-request",
				Name:       "ScheduleInstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultRootOmissionEquivalenceRequest",
				Version:    "0.1.0",
				APIVersion: "v0",
				Mode:       pluginsdk.ModeSubprocess,
				ConfigSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"settings": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"labels": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"naming": map[string]any{
											"type":     "object",
											"required": []any{"prefix"},
											"properties": map[string]any{
												"prefix": map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"},
											},
										},
									},
								},
							},
						},
					},
				},
				Entry: pluginsdk.PluginEntry{Module: "plugins/schedule-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-equivalence-request", Symbol: "Plugin"},
			},
			InstanceConfig: instanceConfig,
		}

		request, err := buildSubprocessHostRequest("schedule", payload, plugin)
		if err != nil {
			t.Fatalf("expected schedule request build to accept equivalent root omission instance config, got %v", err)
		}
		if request.InstanceConfig != nil {
			t.Fatalf("expected equivalent schedule root omission payload to omit instance_config entirely, got %s", string(request.InstanceConfig))
		}
		if string(request.ScheduleTrigger) != string(payload) {
			t.Fatalf("expected schedule payload to remain unchanged, got %s", string(request.ScheduleTrigger))
		}
		encodedRequest, err := json.Marshal(request)
		if err != nil {
			t.Fatalf("expected equivalent schedule root omission request to remain encodable, got %v", err)
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(encodedRequest, &envelope); err != nil {
			t.Fatalf("expected equivalent schedule root omission request to remain decodable, got %v", err)
		}
		if _, exists := envelope["instance_config"]; exists {
			t.Fatalf("expected equivalent schedule root omission request to omit instance_config, got %s", string(encodedRequest))
		}
		rawSchedule, exists := envelope["schedule_trigger"]
		if !exists {
			t.Fatalf("expected schedule root omission request to preserve schedule payload, got %s", string(encodedRequest))
		}
		if string(rawSchedule) != string(payload) {
			t.Fatalf("expected encoded schedule payload to remain unchanged, got %s", string(rawSchedule))
		}
		return encodedRequest, request
	}

	emptyEncoded, emptyRequest := buildEnvelope(map[string]any{})
	nilEncoded, nilRequest := buildEnvelope(nil)
	if emptyRequest.InstanceConfig != nil || nilRequest.InstanceConfig != nil {
		t.Fatalf("expected nil-map and empty-map schedule root omission requests to both omit instance_config, got empty=%v nil=%v", emptyRequest.InstanceConfig, nilRequest.InstanceConfig)
	}
	if string(emptyEncoded) != string(nilEncoded) {
		t.Fatalf("expected nil-map and empty-map schedule root omission requests to encode identically, got empty=%s nil=%s", string(emptyEncoded), string(nilEncoded))
	}
}

func TestBuiltSubprocessFixtureTreatsNilAndEmptyInstanceConfigAsEquivalentBeyondSupportedDeeperNestedRequiredEnumDefaultRootOmissionBoundaryForCommand(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name           string
		instanceConfig map[string]any
	}{
		{name: "empty_map", instanceConfig: map[string]any{}},
		{name: "nil_map", instanceConfig: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-command-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-not-enforced"))
			t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
			plugin := pluginsdk.Plugin{
				Manifest: pluginsdk.PluginManifest{
					ID:         "plugin-command-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-fixture",
					Name:       "CommandInstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultRootOmissionFixture",
					Version:    "0.1.0",
					APIVersion: "v0",
					Mode:       pluginsdk.ModeSubprocess,
					ConfigSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"settings": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"labels": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"naming": map[string]any{
												"type":     "object",
												"required": []any{"prefix"},
												"properties": map[string]any{
													"prefix": map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"},
												},
											},
										},
									},
								},
							},
						},
					},
					Entry: pluginsdk.PluginEntry{Module: "plugins/command-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-fixture", Symbol: "Plugin"},
				},
				InstanceConfig: tc.instanceConfig,
			}
			ctx := eventmodel.ExecutionContext{TraceID: "trace-command-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-" + tc.name, EventID: "evt-command-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-" + tc.name}

			if err := host.DispatchCommand(context.Background(), plugin, eventmodel.CommandInvocation{Name: "admin", Raw: "/admin enable plugin-command-root-omission"}, ctx); err != nil {
				t.Fatalf("expected built fixture command request to preserve equivalent root omission boundary without rejection, got %v", err)
			}

			stdout := strings.Join(host.StdoutLines(), "\n")
			for _, expected := range []string{"handshake-ready", "command-ok"} {
				if !strings.Contains(stdout, expected) {
					t.Fatalf("expected built fixture command stdout to include %q, got %s", expected, stdout)
				}
			}
			stderr := strings.Join(host.StderrLines(), "\n")
			if !strings.Contains(stderr, "stderr-online") {
				t.Fatalf("expected built fixture command stderr capture, got %s", stderr)
			}
		})
	}
}

func TestBuiltSubprocessFixtureTreatsNilAndEmptyInstanceConfigAsEquivalentBeyondSupportedDeeperNestedRequiredEnumDefaultRootOmissionBoundaryForJob(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name           string
		instanceConfig map[string]any
	}{
		{name: "empty_map", instanceConfig: map[string]any{}},
		{name: "nil_map", instanceConfig: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-job-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-not-enforced"))
			t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
			plugin := pluginsdk.Plugin{
				Manifest: pluginsdk.PluginManifest{
					ID:         "plugin-job-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-fixture",
					Name:       "JobInstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultRootOmissionFixture",
					Version:    "0.1.0",
					APIVersion: "v0",
					Mode:       pluginsdk.ModeSubprocess,
					ConfigSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"settings": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"labels": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"naming": map[string]any{
												"type":     "object",
												"required": []any{"prefix"},
												"properties": map[string]any{
													"prefix": map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"},
												},
											},
										},
									},
								},
							},
						},
					},
					Entry: pluginsdk.PluginEntry{Module: "plugins/job-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-fixture", Symbol: "Plugin"},
				},
				InstanceConfig: tc.instanceConfig,
			}
			ctx := eventmodel.ExecutionContext{TraceID: "trace-job-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-" + tc.name, EventID: "evt-job-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-" + tc.name}

			if err := host.DispatchJob(context.Background(), plugin, pluginsdk.JobInvocation{ID: "job-root-omission", Type: "ai.chat", Metadata: map[string]any{"source": "job-root-omission"}}, ctx); err != nil {
				t.Fatalf("expected built fixture job request to preserve equivalent root omission boundary without rejection, got %v", err)
			}

			stdout := strings.Join(host.StdoutLines(), "\n")
			for _, expected := range []string{"handshake-ready", "job-ok"} {
				if !strings.Contains(stdout, expected) {
					t.Fatalf("expected built fixture job stdout to include %q, got %s", expected, stdout)
				}
			}
			stderr := strings.Join(host.StderrLines(), "\n")
			if !strings.Contains(stderr, "stderr-online") {
				t.Fatalf("expected built fixture job stderr capture, got %s", stderr)
			}
		})
	}
}

func TestBuiltSubprocessFixtureTreatsNilAndEmptyInstanceConfigAsEquivalentBeyondSupportedDeeperNestedRequiredEnumDefaultRootOmissionBoundaryForSchedule(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name           string
		instanceConfig map[string]any
	}{
		{name: "empty_map", instanceConfig: map[string]any{}},
		{name: "nil_map", instanceConfig: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-schedule-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-not-enforced"))
			t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
			plugin := pluginsdk.Plugin{
				Manifest: pluginsdk.PluginManifest{
					ID:         "plugin-schedule-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-fixture",
					Name:       "ScheduleInstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultRootOmissionFixture",
					Version:    "0.1.0",
					APIVersion: "v0",
					Mode:       pluginsdk.ModeSubprocess,
					ConfigSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"settings": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"labels": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"naming": map[string]any{
												"type":     "object",
												"required": []any{"prefix"},
												"properties": map[string]any{
													"prefix": map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"},
												},
											},
										},
									},
								},
							},
						},
					},
					Entry: pluginsdk.PluginEntry{Module: "plugins/schedule-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-fixture", Symbol: "Plugin"},
				},
				InstanceConfig: tc.instanceConfig,
			}
			ctx := eventmodel.ExecutionContext{TraceID: "trace-schedule-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-" + tc.name, EventID: "evt-schedule-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-" + tc.name}

			if err := host.DispatchSchedule(context.Background(), plugin, pluginsdk.ScheduleTrigger{ID: "schedule-root-omission", Type: "cron", Metadata: map[string]any{"source": "schedule-root-omission"}}, ctx); err != nil {
				t.Fatalf("expected built fixture schedule request to preserve equivalent root omission boundary without rejection, got %v", err)
			}

			stdout := strings.Join(host.StdoutLines(), "\n")
			for _, expected := range []string{"handshake-ready", "schedule-ok"} {
				if !strings.Contains(stdout, expected) {
					t.Fatalf("expected built fixture schedule stdout to include %q, got %s", expected, stdout)
				}
			}
			stderr := strings.Join(host.StderrLines(), "\n")
			if !strings.Contains(stderr, "stderr-online") {
				t.Fatalf("expected built fixture schedule stderr capture, got %s", stderr)
			}
		})
	}
}

func TestBuiltSubprocessFixtureRejectsBeyondSupportedDeeperNestedValidationOnExistingNamingBranch(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		pluginID       string
		prefixSchema   map[string]any
		namingRequired bool
		instanceConfig map[string]any
		wantErr        string
	}{
		{name: "required_enum_default_explicit_bad_value", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-bad-value-fixture", prefixSchema: map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"}, namingRequired: true, instanceConfig: testBeyondSupportedDeeperNestedNamingInstanceConfig("oops"), wantErr: `nested instance config property "settings.labels.naming.prefix" value "oops" must be declared in enum ["hello","world"] for declared type "string"`},
		{name: "required_enum_default_explicit_array_bad_value", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-array-valued-bad-value-fixture", prefixSchema: map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"}, namingRequired: true, instanceConfig: testBeyondSupportedDeeperNestedNamingInstanceConfig([]any{"oops"}), wantErr: `nested instance config property "settings.labels.naming.prefix" value type must match declared type "string", got "array"`},
		{name: "required_enum_default_explicit_wrong_type_bad_value", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-wrong-type-bad-value-fixture", prefixSchema: map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"}, namingRequired: true, instanceConfig: testBeyondSupportedDeeperNestedNamingInstanceConfig(true), wantErr: `nested instance config property "settings.labels.naming.prefix" value type must match declared type "string", got "boolean"`},
		{name: "required_enum_default_explicit_object_bad_value", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-object-valued-bad-value-fixture", prefixSchema: map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"}, namingRequired: true, instanceConfig: testBeyondSupportedDeeperNestedNamingInstanceConfig(map[string]any{"bad": true}), wantErr: `nested instance config property "settings.labels.naming.prefix" value type must match declared type "string", got "object"`},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			host := NewSubprocessPluginHost(testPluginProcessFactory(t, "echo"))
			plugin := pluginsdk.Plugin{
				Manifest:       testBeyondSupportedDeeperNestedNamingManifest(tc.pluginID, tc.pluginID, "plugins/"+tc.pluginID, tc.prefixSchema, tc.namingRequired),
				InstanceConfig: tc.instanceConfig,
			}
			ctx := eventmodel.ExecutionContext{TraceID: "trace-" + tc.pluginID, EventID: "evt-" + tc.pluginID}

			if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected built fixture request rejection containing %q, got %v", tc.wantErr, err)
			}
			if len(host.StdoutLines()) != 0 || len(host.StderrLines()) != 0 {
				t.Fatalf("expected rejection before subprocess side effects, stdout=%v stderr=%v", host.StdoutLines(), host.StderrLines())
			}
		})
	}
}

func TestBuiltSubprocessFixtureMergesBeyondSupportedDeeperNestedDefaultVariantsOnExistingNamingBranch(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		pluginID       string
		prefixSchema   map[string]any
		namingRequired bool
	}{
		{name: "required_enum_default", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-fixture", prefixSchema: map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"}, namingRequired: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-default-merged"))
			t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
			plugin := pluginsdk.Plugin{
				Manifest:       testBeyondSupportedDeeperNestedNamingManifest(tc.pluginID, tc.pluginID, "plugins/"+tc.pluginID, tc.prefixSchema, tc.namingRequired),
				InstanceConfig: testBeyondSupportedDeeperNestedEmptyNamingInstanceConfig(),
			}
			ctx := eventmodel.ExecutionContext{TraceID: "trace-" + tc.pluginID, EventID: "evt-" + tc.pluginID}

			if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
				t.Fatalf("expected built fixture request to receive merged deeper nested default, got %v", err)
			}

			stdout := strings.Join(host.StdoutLines(), "\n")
			for _, expected := range []string{"handshake-ready", "event-ok"} {
				if !strings.Contains(stdout, expected) {
					t.Fatalf("expected built fixture stdout to include %q, got %s", expected, stdout)
				}
			}
			stderr := strings.Join(host.StderrLines(), "\n")
			if !strings.Contains(stderr, "stderr-online") {
				t.Fatalf("expected built fixture stderr capture, got %s", stderr)
			}
		})
	}
}

func TestBuiltSubprocessFixtureReceivesBeyondSupportedDeeperNestedRequiredEnumDefaultChildOmissionBoundary(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-required-enum-default-child-omission-not-enforced"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-child-omission-fixture",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultChildOmissionFixture",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"labels": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"naming": map[string]any{
										"type":     "object",
										"required": []any{"prefix"},
										"properties": map[string]any{
											"prefix": map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"},
										},
									},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-child-omission-fixture", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{}}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-beyond-supported-deeper-nested-required-enum-default-child-omission", EventID: "evt-instance-config-beyond-supported-deeper-nested-required-enum-default-child-omission"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected built fixture request to preserve beyond-supported deeper nested required+enum+default child omission boundary without rejection, got %v", err)
	}

	stdout := strings.Join(host.StdoutLines(), "\n")
	for _, expected := range []string{"handshake-ready", "event-ok"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("expected built fixture stdout to include %q, got %s", expected, stdout)
		}
	}
	stderr := strings.Join(host.StderrLines(), "\n")
	if !strings.Contains(stderr, "stderr-online") {
		t.Fatalf("expected built fixture stderr capture, got %s", stderr)
	}
}

func TestBuiltSubprocessFixtureReceivesBeyondSupportedDeeperNestedRequiredEnumDefaultParentOmissionBoundary(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-required-enum-default-parent-omission-not-enforced"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-parent-omission-fixture",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultParentOmissionFixture",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"labels": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"naming": map[string]any{
										"type":     "object",
										"required": []any{"prefix"},
										"properties": map[string]any{
											"prefix": map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"},
										},
									},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-parent-omission-fixture", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-beyond-supported-deeper-nested-required-enum-default-parent-omission", EventID: "evt-instance-config-beyond-supported-deeper-nested-required-enum-default-parent-omission"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected built fixture request to preserve beyond-supported deeper nested required+enum+default parent omission boundary without rejection, got %v", err)
	}

	stdout := strings.Join(host.StdoutLines(), "\n")
	for _, expected := range []string{"handshake-ready", "event-ok"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("expected built fixture stdout to include %q, got %s", expected, stdout)
		}
	}
	stderr := strings.Join(host.StderrLines(), "\n")
	if !strings.Contains(stderr, "stderr-online") {
		t.Fatalf("expected built fixture stderr capture, got %s", stderr)
	}
}

func TestBuiltSubprocessFixtureReceivesBeyondSupportedDeeperNestedRequiredEnumDefaultRootOmissionBoundary(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-not-enforced"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-fixture",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultRootOmissionFixture",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"labels": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"naming": map[string]any{
										"type":     "object",
										"required": []any{"prefix"},
										"properties": map[string]any{
											"prefix": map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"},
										},
									},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-fixture", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission", EventID: "evt-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected built fixture request to preserve beyond-supported deeper nested required+enum+default root omission boundary without rejection, got %v", err)
	}

	stdout := strings.Join(host.StdoutLines(), "\n")
	for _, expected := range []string{"handshake-ready", "event-ok"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("expected built fixture stdout to include %q, got %s", expected, stdout)
		}
	}
	stderr := strings.Join(host.StderrLines(), "\n")
	if !strings.Contains(stderr, "stderr-online") {
		t.Fatalf("expected built fixture stderr capture, got %s", stderr)
	}
}

func TestBuiltSubprocessFixtureTreatsNilAndEmptyInstanceConfigAsEquivalentBeyondSupportedDeeperNestedRequiredEnumDefaultRootOmissionBoundary(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name           string
		instanceConfig map[string]any
	}{
		{name: "empty_map", instanceConfig: map[string]any{}},
		{name: "nil_map", instanceConfig: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-not-enforced"))
			t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
			plugin := pluginsdk.Plugin{
				Manifest: pluginsdk.PluginManifest{
					ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-equivalence-fixture",
					Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultRootOmissionEquivalenceFixture",
					Version:    "0.1.0",
					APIVersion: "v0",
					Mode:       pluginsdk.ModeSubprocess,
					ConfigSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"settings": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"labels": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"naming": map[string]any{
												"type":     "object",
												"required": []any{"prefix"},
												"properties": map[string]any{
													"prefix": map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"},
												},
											},
										},
									},
								},
							},
						},
					},
					Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-equivalence-fixture", Symbol: "Plugin"},
				},
				InstanceConfig: tc.instanceConfig,
			}
			ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-" + tc.name, EventID: "evt-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-" + tc.name}

			if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
				t.Fatalf("expected built fixture request to preserve equivalent root omission boundary without rejection, got %v", err)
			}

			stdout := strings.Join(host.StdoutLines(), "\n")
			for _, expected := range []string{"handshake-ready", "event-ok"} {
				if !strings.Contains(stdout, expected) {
					t.Fatalf("expected built fixture stdout to include %q, got %s", expected, stdout)
				}
			}
			stderr := strings.Join(host.StderrLines(), "\n")
			if !strings.Contains(stderr, "stderr-online") {
				t.Fatalf("expected built fixture stderr capture, got %s", stderr)
			}
		})
	}
}

func TestBuildSubprocessHostRequestRejectsBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitLeafVariants(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		pluginID       string
		instanceConfig map[string]any
		wantErr        string
	}{
		{name: "explicit_bad_value", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-bad-value-request", instanceConfig: testBeyondSupportedDeeperNestedNamingInstanceConfig("oops"), wantErr: `nested instance config property "settings.labels.naming.prefix" value "oops" must be declared in enum ["hello","world"] for declared type "string"`},
		{name: "explicit_array_bad_value", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-array-valued-bad-value-request", instanceConfig: testBeyondSupportedDeeperNestedNamingInstanceConfig([]any{"oops"}), wantErr: `nested instance config property "settings.labels.naming.prefix" value type must match declared type "string", got "array"`},
		{name: "explicit_wrong_type_bad_value", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-wrong-type-bad-value-request", instanceConfig: testBeyondSupportedDeeperNestedNamingInstanceConfig(true), wantErr: `nested instance config property "settings.labels.naming.prefix" value type must match declared type "string", got "boolean"`},
		{name: "explicit_object_bad_value", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-object-valued-bad-value-request", instanceConfig: testBeyondSupportedDeeperNestedNamingInstanceConfig(map[string]any{"bad": true}), wantErr: `nested instance config property "settings.labels.naming.prefix" value type must match declared type "string", got "object"`},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			plugin := pluginsdk.Plugin{
				Manifest:       testBeyondSupportedDeeperNestedNamingManifest(tc.pluginID, tc.pluginID, "plugins/"+tc.pluginID, map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"}, true),
				InstanceConfig: tc.instanceConfig,
			}
			_, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected request build rejection containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestBuildSubprocessHostRequestRejectsBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitObjectNodeBadValues(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		pluginID       string
		instanceConfig map[string]any
		wantErr        string
		wantActualType string
	}{
		{name: "boolean", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-object-node-bad-value-request", instanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": true}}}, wantErr: `nested instance config property "settings.labels.naming" value type must match declared type "object", got "boolean"`, wantActualType: "boolean"},
		{name: "string", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-string-valued-object-node-bad-value-request", instanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": "oops"}}}, wantErr: `nested instance config property "settings.labels.naming" value type must match declared type "object", got "string"`, wantActualType: "string"},
		{name: "number", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-number-valued-object-node-bad-value-request", instanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": 7.0}}}, wantErr: `nested instance config property "settings.labels.naming" value type must match declared type "object", got "number"`, wantActualType: "number"},
		{name: "null", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-null-valued-object-node-bad-value-request", instanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": nil}}}, wantErr: `nested instance config property "settings.labels.naming" value type must match declared type "object", got "null"`, wantActualType: "null"},
		{name: "array", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-array-valued-object-node-bad-value-request", instanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": []any{"oops"}}}}, wantErr: `nested instance config property "settings.labels.naming" value type must match declared type "object", got "array"`, wantActualType: "array"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			plugin := pluginsdk.Plugin{
				Manifest:       testBeyondSupportedDeeperNestedNamingManifest(tc.pluginID, tc.pluginID, "plugins/"+tc.pluginID, map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"}, true),
				InstanceConfig: tc.instanceConfig,
			}
			_, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected request build rejection containing %q, got %v", tc.wantErr, err)
			}
			var configErr *subprocessInstanceConfigFailure
			if !errors.As(err, &configErr) {
				t.Fatalf("expected request build error classification, got %T %v", err, err)
			}
			if configErr.reason != subprocessFailureReasonInstanceConfigValueTypeMismatch || configErr.compatibilityRule != "instance_config_deeper_nested_value_type" {
				t.Fatalf("expected object-node mismatch classification, got reason=%q rule=%q", configErr.reason, configErr.compatibilityRule)
			}
			for key, expected := range map[string]any{
				"property_name":              "settings.labels.naming",
				"parent_property_name":       "settings.labels",
				"nested_property_name":       "naming",
				"root_property_name":         "settings",
				"intermediate_property_name": "labels",
				"declared_type":              "object",
				"actual_type":                tc.wantActualType,
			} {
				if actual := configErr.metadata[key]; actual != expected {
					t.Fatalf("expected request build metadata %q=%v, got %+v", key, expected, configErr.metadata)
				}
			}
		})
	}
}

func TestSubprocessPluginHostRejectsBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitObjectNodeBadValuesBeforeProcessLaunch(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		pluginID       string
		instanceConfig map[string]any
		wantErr        string
		wantActualType string
	}{
		{name: "boolean", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-object-node-bad-value-fixture", instanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": true}}}, wantErr: `nested instance config property "settings.labels.naming" value type must match declared type "object", got "boolean"`, wantActualType: "boolean"},
		{name: "string", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-string-valued-object-node-bad-value-fixture", instanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": "oops"}}}, wantErr: `nested instance config property "settings.labels.naming" value type must match declared type "object", got "string"`, wantActualType: "string"},
		{name: "number", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-number-valued-object-node-bad-value-fixture", instanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": 7.0}}}, wantErr: `nested instance config property "settings.labels.naming" value type must match declared type "object", got "number"`, wantActualType: "number"},
		{name: "null", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-null-valued-object-node-bad-value-fixture", instanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": nil}}}, wantErr: `nested instance config property "settings.labels.naming" value type must match declared type "object", got "null"`, wantActualType: "null"},
		{name: "array", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-array-valued-object-node-bad-value-fixture", instanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": []any{"oops"}}}}, wantErr: `nested instance config property "settings.labels.naming" value type must match declared type "object", got "array"`, wantActualType: "array"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			launches := 0
			logs := &bytes.Buffer{}
			tracer := NewTraceRecorder()
			metrics := NewMetricsRegistry()
			host := NewSubprocessPluginHost(func(ctx context.Context) *exec.Cmd {
				launches++
				return testPluginProcessFactory(t, "echo")(ctx)
			})
			host.SetObservability(NewLogger(logs), tracer, metrics)
			plugin := pluginsdk.Plugin{
				Manifest:       testBeyondSupportedDeeperNestedNamingManifest(tc.pluginID, tc.pluginID, "plugins/"+tc.pluginID, map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"}, true),
				InstanceConfig: tc.instanceConfig,
			}
			ctx := eventmodel.ExecutionContext{TraceID: "trace-" + tc.pluginID, EventID: "evt-" + tc.pluginID, PluginID: tc.pluginID, CorrelationID: "corr-" + tc.pluginID}
			err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected host rejection containing %q, got %v", tc.wantErr, err)
			}
			if launches != 0 {
				t.Fatalf("expected host rejection before subprocess launch, launches=%d", launches)
			}
			if len(host.StdoutLines()) != 0 || len(host.StderrLines()) != 0 {
				t.Fatalf("expected host rejection to avoid subprocess side effects, stdout=%v stderr=%v", host.StdoutLines(), host.StderrLines())
			}
			for _, expected := range []string{"subprocess host instance config rejected", `"failure_stage":"instance_config"`, `"failure_reason":"instance_config_value_type_mismatch"`, `"compatibility_rule":"instance_config_deeper_nested_value_type"`, `"property_name":"settings.labels.naming"`, `"parent_property_name":"settings.labels"`, `"nested_property_name":"naming"`, `"root_property_name":"settings"`, `"intermediate_property_name":"labels"`, `"declared_type":"object"`, `"actual_type":"` + tc.wantActualType + `"`} {
				if !strings.Contains(logs.String(), expected) {
					t.Fatalf("expected host rejection log %q, got %s", expected, logs.String())
				}
			}
			rendered := tracer.RenderTrace("trace-" + tc.pluginID)
			if !strings.Contains(rendered, "plugin_host.instance_config") {
				t.Fatalf("expected instance config trace span, got %s", rendered)
			}
			if strings.Contains(rendered, "plugin_host.dispatch") {
				t.Fatalf("expected rejection to stop before dispatch span, got %s", rendered)
			}
			if !strings.Contains(metrics.RenderPrometheus(), fmt.Sprintf(`bot_platform_plugin_errors_total{plugin_id=%q} 1`, tc.pluginID)) {
				t.Fatalf("expected plugin error metric, got %s", metrics.RenderPrometheus())
			}
		})
	}
}

func TestBuildSubprocessHostRequestRejectsDeeperNestedInstanceConfigObjectMissingManifestRequiredMember(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-deeper-nested-required-member-request",
			Name:       "InstanceConfigDeeperNestedRequiredMemberRequest",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"labels": map[string]any{
								"type":     "object",
								"required": []any{"prefix"},
								"properties": map[string]any{
									"prefix": map[string]any{"type": "string"},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-deeper-nested-required-member-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{}}},
	}

	_, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err == nil || !strings.Contains(err.Error(), `nested instance config required property "settings.labels.prefix" must be provided`) {
		t.Fatalf("expected request build to reject deeper nested instance object missing required member, got %v", err)
	}
	var configErr *subprocessInstanceConfigFailure
	if !errors.As(err, &configErr) {
		t.Fatalf("expected deeper nested required request build error classification, got %T %v", err, err)
	}
	if configErr.reason != subprocessFailureReasonInstanceConfigMissingRequired {
		t.Fatalf("expected deeper nested required missing reason, got %q", configErr.reason)
	}
	if configErr.compatibilityRule != "instance_config_deeper_nested_required" {
		t.Fatalf("expected deeper nested required compatibility rule, got %q", configErr.compatibilityRule)
	}
	for key, expected := range map[string]any{
		"property_name":              "settings.labels.prefix",
		"parent_property_name":       "settings.labels",
		"nested_property_name":       "prefix",
		"root_property_name":         "settings",
		"intermediate_property_name": "labels",
		"declared_type":              "string",
	} {
		if actual := configErr.metadata[key]; actual != expected {
			t.Fatalf("expected deeper nested required request build metadata %q=%v, got %+v", key, expected, configErr.metadata)
		}
	}
}

func TestSubprocessPluginHostRejectsDeeperNestedInstanceConfigObjectMissingManifestRequiredMemberBeforeProcessLaunch(t *testing.T) {
	t.Parallel()

	launches := 0
	logs := &bytes.Buffer{}
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHost(func(ctx context.Context) *exec.Cmd {
		launches++
		return testBuiltPluginProcessFactory(t, "assert-instance-config-deeper-nested-required-not-enforced")(ctx)
	})
	host.SetObservability(NewLogger(logs), tracer, metrics)
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-deeper-nested-required-member-fixture",
			Name:       "InstanceConfigDeeperNestedRequiredMemberFixture",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"labels": map[string]any{
								"type":     "object",
								"required": []any{"prefix"},
								"properties": map[string]any{
									"prefix": map[string]any{"type": "string"},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-deeper-nested-required-member-fixture", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{}}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-deeper-nested-required-member", EventID: "evt-instance-config-deeper-nested-required-member", PluginID: plugin.Manifest.ID, CorrelationID: "corr-instance-config-deeper-nested-required-member"}

	err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx)
	if err == nil || !strings.Contains(err.Error(), `nested instance config required property "settings.labels.prefix" must be provided`) {
		t.Fatalf("expected deeper nested missing required instance config rejection, got %v", err)
	}
	if launches != 0 {
		t.Fatalf("expected deeper nested missing required instance config rejection before subprocess launch, launches=%d", launches)
	}
	if len(host.StdoutLines()) != 0 || len(host.StderrLines()) != 0 {
		t.Fatalf("expected deeper nested missing required instance config rejection to avoid subprocess side effects, stdout=%v stderr=%v", host.StdoutLines(), host.StderrLines())
	}
	for _, expected := range []string{"subprocess host instance config rejected", `"failure_stage":"instance_config"`, `"failure_reason":"instance_config_missing_required_value"`, `"compatibility_rule":"instance_config_deeper_nested_required"`, `"property_name":"settings.labels.prefix"`, `"parent_property_name":"settings.labels"`, `"nested_property_name":"prefix"`, `"root_property_name":"settings"`, `"intermediate_property_name":"labels"`, `"declared_type":"string"`} {
		if !strings.Contains(logs.String(), expected) {
			t.Fatalf("expected deeper nested missing required instance config rejection log %q, got %s", expected, logs.String())
		}
	}
	rendered := tracer.RenderTrace("trace-instance-config-deeper-nested-required-member")
	if !strings.Contains(rendered, "plugin_host.instance_config") {
		t.Fatalf("expected deeper nested missing required instance config trace span, got %s", rendered)
	}
	if strings.Contains(rendered, "plugin_host.dispatch") {
		t.Fatalf("expected deeper nested missing required instance config rejection to stop before dispatch span, got %s", rendered)
	}
	if !strings.Contains(metrics.RenderPrometheus(), `bot_platform_plugin_errors_total{plugin_id="plugin-instance-config-deeper-nested-required-member-fixture"} 1`) {
		t.Fatalf("expected plugin error metric, got %s", metrics.RenderPrometheus())
	}
}

func TestBuildSubprocessHostRequestPreservesNestedInstanceConfigObjectMissingManifestRequiredMember(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-nested-required-member-request",
			Name:       "InstanceConfigNestedRequiredMemberRequest",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type":     "object",
						"required": []any{"prefix"},
						"properties": map[string]any{
							"prefix": map[string]any{"type": "string"},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-nested-required-member-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{}},
	}

	request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err != nil {
		t.Fatalf("expected request build to preserve nested instance object missing required member without rejection, got %v", err)
	}
	if request.InstanceConfig == nil {
		t.Fatal("expected host request to keep nested instance object even when required member is missing")
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.InstanceConfig, &decoded); err != nil {
		t.Fatalf("expected nested instance config payload to remain decodable, got %v", err)
	}
	settings, ok := decoded["settings"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested settings object to remain present, got %+v", decoded)
	}
	if len(settings) != 0 {
		t.Fatalf("expected nested settings object to remain empty when required member is missing, got %+v", decoded)
	}
	if _, exists := settings["prefix"]; exists {
		t.Fatalf("expected nested required member to stay absent in request payload, got %+v", decoded)
	}
}

func TestBuiltSubprocessFixtureReceivesNestedInstanceConfigObjectMissingManifestRequiredMember(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-nested-required-not-enforced"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-nested-required-member-fixture",
			Name:       "InstanceConfigNestedRequiredMemberFixture",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type":     "object",
						"required": []any{"prefix"},
						"properties": map[string]any{
							"prefix": map[string]any{"type": "string"},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-nested-required-member-fixture", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-nested-required-member", EventID: "evt-instance-config-nested-required-member"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected built fixture request to receive nested instance object missing required member without rejection, got %v", err)
	}

	stdout := strings.Join(host.StdoutLines(), "\n")
	for _, expected := range []string{"handshake-ready", "event-ok"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("expected built fixture stdout to include %q, got %s", expected, stdout)
		}
	}
	stderr := strings.Join(host.StderrLines(), "\n")
	if !strings.Contains(stderr, "stderr-online") {
		t.Fatalf("expected built fixture stderr capture, got %s", stderr)
	}
}

func TestSubprocessPluginHostRejectsInvalidCaptureLimits(t *testing.T) {
	t.Parallel()

	if _, err := NewSubprocessPluginHostWithCaptureLimit(testPluginProcessFactory(t, "echo"), 0); err == nil {
		t.Fatal("expected zero capture limit to fail")
	}
}

func TestSubprocessPluginHostBoundsCapturedOutputInMemory(t *testing.T) {
	t.Parallel()

	host, err := NewSubprocessPluginHostWithCaptureLimit(testPluginProcessFactory(t, "flood-stderr"), 3)
	if err != nil {
		t.Fatalf("new host with capture limit: %v", err)
	}
	plugin := testPluginDefinition()
	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), eventmodel.ExecutionContext{TraceID: "trace-limits", EventID: "evt-limits"}); err != nil {
		t.Fatalf("dispatch with bounded capture: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	stderr := strings.Join(host.StderrLines(), "\n")
	if strings.Contains(stderr, "stderr-online") || strings.Contains(stderr, "flood-0") || strings.Contains(stderr, "flood-1") {
		t.Fatalf("expected oldest stderr lines to be dropped, got %s", stderr)
	}
	for _, line := range []string{"flood-2", "flood-3", "flood-4"} {
		if !strings.Contains(stderr, line) {
			t.Fatalf("expected bounded stderr to retain %q, got %s", line, stderr)
		}
	}
}

func TestSubprocessPluginHostTimesOutHungHandshake(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testPluginProcessFactory(t, "hang-handshake"))
	host.handshakeTimeout = 50 * time.Millisecond
	err := host.DispatchEvent(context.Background(), testPluginDefinition(), testPluginEvent(), eventmodel.ExecutionContext{TraceID: "trace-timeout", EventID: "evt-timeout"})
	if err == nil || !strings.Contains(err.Error(), "subprocess response timeout") {
		t.Fatalf("expected handshake timeout, got %v", err)
	}
}

func TestSubprocessPluginHostClassifiesHandshakeBootstrapFailureWithObservability(t *testing.T) {
	t.Parallel()

	logs := &bytes.Buffer{}
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHost(testPluginProcessFactory(t, "hang-handshake"))
	host.handshakeTimeout = 50 * time.Millisecond
	host.SetObservability(NewLogger(logs), tracer, metrics)
	ctx := eventmodel.ExecutionContext{TraceID: "trace-handshake-bootstrap", EventID: "evt-handshake-bootstrap", PluginID: "plugin-subprocess-demo", CorrelationID: "corr-handshake-bootstrap"}
	err := host.DispatchEvent(context.Background(), testPluginDefinition(), testPluginEvent(), ctx)
	if err == nil || !strings.Contains(err.Error(), "subprocess handshake failed") {
		t.Fatalf("expected handshake bootstrap classification, got %v", err)
	}
	for _, expected := range []string{"subprocess host process bootstrap failed", `"failure_stage":"handshake"`, `"plugin_id":"plugin-subprocess-demo"`, "subprocess host ensure process failed"} {
		if !strings.Contains(logs.String(), expected) {
			t.Fatalf("expected handshake bootstrap log %q, got %s", expected, logs.String())
		}
	}
	rendered := tracer.RenderTrace("trace-handshake-bootstrap")
	if !strings.Contains(rendered, "plugin_host.process_bootstrap") {
		t.Fatalf("expected bootstrap trace span, got %s", rendered)
	}
	if !strings.Contains(metrics.RenderPrometheus(), `bot_platform_plugin_errors_total{plugin_id="plugin-subprocess-demo"} 1`) {
		t.Fatalf("expected plugin error metric, got %s", metrics.RenderPrometheus())
	}
}

func TestSubprocessPluginHostClassifiesInvalidHandshakeResponseWithObservability(t *testing.T) {
	t.Parallel()

	logs := &bytes.Buffer{}
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHost(testPluginProcessFactory(t, "bad-handshake"))
	host.SetObservability(NewLogger(logs), tracer, metrics)
	ctx := eventmodel.ExecutionContext{TraceID: "trace-bad-handshake", EventID: "evt-bad-handshake", PluginID: "plugin-subprocess-demo", CorrelationID: "corr-bad-handshake"}
	err := host.DispatchEvent(context.Background(), testPluginDefinition(), testPluginEvent(), ctx)
	if err == nil || !strings.Contains(err.Error(), "subprocess handshake failed") || !strings.Contains(err.Error(), "invalid handshake response") {
		t.Fatalf("expected invalid handshake bootstrap classification, got %v", err)
	}
	for _, expected := range []string{"subprocess host process bootstrap failed", `"failure_stage":"handshake"`, `"error":"invalid handshake response:`} {
		if !strings.Contains(logs.String(), expected) {
			t.Fatalf("expected invalid handshake log %q, got %s", expected, logs.String())
		}
	}
	rendered := tracer.RenderTrace("trace-bad-handshake")
	if !strings.Contains(rendered, "plugin_host.process_bootstrap") || !strings.Contains(rendered, "plugin-subprocess-demo") {
		t.Fatalf("expected bootstrap trace span for invalid handshake, got %s", rendered)
	}
	if !strings.Contains(metrics.RenderPrometheus(), `bot_platform_plugin_errors_total{plugin_id="plugin-subprocess-demo"} 1`) {
		t.Fatalf("expected plugin error metric, got %s", metrics.RenderPrometheus())
	}
}

func TestSubprocessPluginHostTimesOutHungEventResponseAndRecovers(t *testing.T) {
	t.Parallel()

	var launches atomic.Int32
	host := NewSubprocessPluginHost(func(ctx context.Context) *exec.Cmd {
		count := launches.Add(1)
		mode := "echo"
		if count == 1 {
			mode = "hang-event"
		}
		return testPluginProcessFactory(t, mode)(ctx)
	})
	host.responseTimeout = 50 * time.Millisecond
	plugin := testPluginDefinition()
	event := testPluginEvent()

	err := host.DispatchEvent(context.Background(), plugin, event, eventmodel.ExecutionContext{TraceID: event.TraceID, EventID: event.EventID})
	if err == nil || !strings.Contains(err.Error(), "subprocess response timeout") {
		t.Fatalf("expected hung event response to timeout, got %v", err)
	}
	if err := host.DispatchEvent(context.Background(), plugin, event, eventmodel.ExecutionContext{TraceID: event.TraceID, EventID: event.EventID}); err != nil {
		t.Fatalf("expected host to recover on next dispatch, got %v", err)
	}
	if launches.Load() < 2 {
		t.Fatalf("expected timeout to recycle subprocess, launches=%d", launches.Load())
	}
}

func TestSubprocessPluginHostTimesOutHungHealthCheckAndRecovers(t *testing.T) {
	t.Parallel()

	var launches atomic.Int32
	host := NewSubprocessPluginHost(func(ctx context.Context) *exec.Cmd {
		count := launches.Add(1)
		mode := "echo"
		if count == 1 {
			mode = "hang-health"
		}
		return testPluginProcessFactory(t, mode)(ctx)
	})
	host.responseTimeout = 50 * time.Millisecond

	err := host.HealthCheck(context.Background())
	if err == nil || !strings.Contains(err.Error(), "subprocess response timeout") {
		t.Fatalf("expected hung health check to timeout, got %v", err)
	}
	if err := host.HealthCheck(context.Background()); err != nil {
		t.Fatalf("expected host to recover on next health check, got %v", err)
	}
	if launches.Load() < 2 {
		t.Fatalf("expected timeout to recycle health subprocess, launches=%d", launches.Load())
	}
}

func TestSubprocessPluginHostRecordsObservabilityForSuccessfulDispatch(t *testing.T) {
	t.Parallel()

	logs := &bytes.Buffer{}
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHost(testPluginProcessFactory(t, "echo"))
	host.SetObservability(NewLogger(logs), tracer, metrics)
	plugin := testPluginDefinition()
	ctx := eventmodel.ExecutionContext{TraceID: "trace-observe", EventID: "evt-observe", PluginID: plugin.Manifest.ID}
	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("dispatch event: %v", err)
	}
	if !strings.Contains(logs.String(), "subprocess host dispatch started") || !strings.Contains(logs.String(), "subprocess host dispatch completed") || !strings.Contains(logs.String(), "subprocess host handshake completed") {
		t.Fatalf("expected host observability logs, got %s", logs.String())
	}
	if !strings.Contains(tracer.RenderTrace("trace-observe"), "plugin_host.dispatch") {
		t.Fatalf("expected host dispatch span, got %s", tracer.RenderTrace("trace-observe"))
	}
	if !strings.Contains(metrics.RenderPrometheus(), "bot_platform_handler_latency_ms{plugin_id=\"plugin-subprocess-demo\"}") {
		t.Fatalf("expected handler latency metric, got %s", metrics.RenderPrometheus())
	}
}

func TestSubprocessPluginHostRecordsObservabilityForTimeout(t *testing.T) {
	t.Parallel()

	logs := &bytes.Buffer{}
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHost(testPluginProcessFactory(t, "hang-event"))
	host.responseTimeout = 50 * time.Millisecond
	host.SetObservability(NewLogger(logs), tracer, metrics)
	plugin := testPluginDefinition()
	err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), eventmodel.ExecutionContext{TraceID: "trace-timeout-observe", EventID: "evt-timeout-observe", PluginID: plugin.Manifest.ID})
	if err == nil || !strings.Contains(err.Error(), "subprocess response timeout") {
		t.Fatalf("expected timeout, got %v", err)
	}
	if !strings.Contains(logs.String(), "subprocess host dispatch timed out") {
		t.Fatalf("expected timeout observability log, got %s", logs.String())
	}
	if !strings.Contains(tracer.RenderTrace("trace-timeout-observe"), "plugin_host.dispatch") {
		t.Fatalf("expected timeout trace span, got %s", tracer.RenderTrace("trace-timeout-observe"))
	}
	if !strings.Contains(metrics.RenderPrometheus(), "bot_platform_plugin_errors_total{plugin_id=\"plugin-subprocess-demo\"} 1") {
		t.Fatalf("expected plugin error metric, got %s", metrics.RenderPrometheus())
	}
}

func TestSubprocessPluginHostRecordsObservabilityForSecondReadFailureAfterRestart(t *testing.T) {
	t.Parallel()

	var launches atomic.Int32
	logs := &bytes.Buffer{}
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHost(func(ctx context.Context) *exec.Cmd {
		launches.Add(1)
		return testPluginProcessFactory(t, "exit-after-handshake")(ctx)
	})
	host.SetObservability(NewLogger(logs), tracer, metrics)
	plugin := testPluginDefinition()
	err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), eventmodel.ExecutionContext{TraceID: "trace-restart-fail", EventID: "evt-restart-fail", PluginID: plugin.Manifest.ID})
	if err == nil || !strings.Contains(err.Error(), "EOF") {
		t.Fatalf("expected second read failure after restart, got %v", err)
	}
	if launches.Load() != 2 {
		t.Fatalf("expected classified crash-after-handshake path to attempt a single replay restart, launches=%d", launches.Load())
	}
	if strings.Contains(logs.String(), "subprocess host restarting after read failure") || !strings.Contains(logs.String(), "subprocess host dispatch failed after handshake") || !strings.Contains(logs.String(), "subprocess host dispatch failed") {
		t.Fatalf("expected restart failure observability logs, got %s", logs.String())
	}
	if !strings.Contains(metrics.RenderPrometheus(), "bot_platform_plugin_errors_total{plugin_id=\"plugin-subprocess-demo\"} 1") {
		t.Fatalf("expected plugin error metric, got %s", metrics.RenderPrometheus())
	}
	if !strings.Contains(tracer.RenderTrace("trace-restart-fail"), "plugin_host.dispatch") {
		t.Fatalf("expected trace span, got %s", tracer.RenderTrace("trace-restart-fail"))
	}
}

func TestSubprocessPluginHostClassifiesCrashAfterHandshakeWithObservability(t *testing.T) {
	t.Parallel()

	logs := &bytes.Buffer{}
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHost(testPluginProcessFactory(t, "exit-after-handshake"))
	host.SetObservability(NewLogger(logs), tracer, metrics)
	plugin := testPluginDefinition()
	ctx := eventmodel.ExecutionContext{TraceID: "trace-crash-after-handshake", EventID: "evt-crash-after-handshake", PluginID: plugin.Manifest.ID, CorrelationID: "corr-crash-after-handshake"}
	err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx)
	if err == nil || !strings.Contains(err.Error(), "subprocess dispatch failed after handshake") || !strings.Contains(err.Error(), "EOF") {
		t.Fatalf("expected crash-after-handshake classification, got %v", err)
	}
	for _, expected := range []string{"subprocess host dispatch failed after handshake", `"failure_stage":"dispatch"`, `"failure_reason":"crash_after_handshake"`, `"request_type":"event"`, `"correlation_id":"corr-crash-after-handshake"`} {
		if !strings.Contains(logs.String(), expected) {
			t.Fatalf("expected crash-after-handshake log %q, got %s", expected, logs.String())
		}
	}
	rendered := tracer.RenderTrace("trace-crash-after-handshake")
	if !strings.Contains(rendered, "plugin_host.dispatch") || !strings.Contains(rendered, "plugin_host.dispatch_failure") {
		t.Fatalf("expected dispatch and dispatch failure spans, got %s", rendered)
	}
	if !strings.Contains(metrics.RenderPrometheus(), `bot_platform_plugin_errors_total{plugin_id="plugin-subprocess-demo"} 1`) {
		t.Fatalf("expected plugin error metric, got %s", metrics.RenderPrometheus())
	}
	if strings.Contains(logs.String(), "subprocess host restarting after read failure") {
		t.Fatalf("expected classified crash-after-handshake path to skip generic restart log, got %s", logs.String())
	}
}

func testPluginProcessFactory(t *testing.T, mode string) func(context.Context) *exec.Cmd {
	t.Helper()
	markerPath := ""
	if mode == "crash-once" {
		markerPath = filepath.Join(t.TempDir(), "crash-once.marker")
	}

	return func(ctx context.Context) *exec.Cmd {
		modeArg := mode
		if mode == "crash-once" {
			modeArg = "crash-once:" + markerPath
		} else if mode == "crash-after-handshake-once" {
			if markerPath == "" {
				markerPath = filepath.Join(t.TempDir(), "crash-after-handshake.marker")
			}
			modeArg = "crash-after-handshake-once:" + markerPath
		}
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestHelperPluginProcess", "--", modeArg)
		cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
		return cmd
	}
}

func testBuiltPluginProcessFactory(t *testing.T, mode string) func(context.Context) *exec.Cmd {
	t.Helper()
	binaryPath := buildSubprocessFixtureBinary(t)
	markerPath := ""
	if mode == "crash-once" {
		markerPath = filepath.Join(t.TempDir(), "crash-once-built.marker")
	}

	return func(ctx context.Context) *exec.Cmd {
		modeArg := mode
		if mode == "crash-once" {
			modeArg = "crash-once:" + markerPath
		} else if mode == "crash-after-handshake-once" {
			if markerPath == "" {
				markerPath = filepath.Join(t.TempDir(), "crash-after-handshake-built.marker")
			}
			modeArg = "crash-after-handshake-once:" + markerPath
		}
		return exec.CommandContext(ctx, binaryPath, modeArg)
	}
}

func buildSubprocessFixtureBinary(t *testing.T) string {
	t.Helper()
	outputPath := filepath.Join(t.TempDir(), "subprocess-fixture.exe")
	cmd := exec.Command("go", "build", "-o", outputPath, "./testdata/subprocess-fixture")
	cmd.Dir = "."
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build subprocess fixture: %v\n%s", err, string(output))
	}
	return outputPath
}

func shutdownSubprocessHostForTest(host *SubprocessPluginHost) {
	host.mu.Lock()
	defer host.mu.Unlock()
	for pluginID, process := range host.processes {
		if process == nil || process.cmd == nil || process.cmd.Process == nil {
			delete(host.processes, pluginID)
			continue
		}
		_ = process.cmd.Process.Kill()
		_, _ = process.cmd.Process.Wait()
		delete(host.processes, pluginID)
	}
}

func testPluginDefinition() pluginsdk.Plugin {
	return pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-subprocess-demo",
			Name:       "Subprocess Demo",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			Entry:      pluginsdk.PluginEntry{Module: "plugins/subprocess-demo", Symbol: "Plugin"},
		},
	}
}

func testPluginEvent() eventmodel.Event {
	return eventmodel.Event{
		EventID:        "evt-host",
		TraceID:        "trace-host",
		Source:         "scheduler",
		Type:           "schedule.triggered",
		Timestamp:      time.Date(2026, 4, 2, 22, 0, 0, 0, time.UTC),
		IdempotencyKey: "schedule:host",
	}
}

func TestHelperPluginProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	mode := os.Args[len(os.Args)-1]
	_, _ = os.Stderr.WriteString("stderr-online\n")
	if mode == "hang-handshake" {
		time.Sleep(5 * time.Second)
		os.Exit(0)
	}
	if mode == "bad-handshake" {
		_, _ = os.Stdout.WriteString("{\"type\":\"event\",\"status\":\"ok\",\"message\":\"wrong-handshake\"}\n")
		os.Exit(0)
	}
	_, _ = os.Stdout.WriteString("{\"type\":\"handshake\",\"status\":\"ok\",\"message\":\"handshake-ready\"}\n")
	if mode == "crash-after-handshake" {
		os.Exit(2)
	}
	if strings.HasPrefix(mode, "crash-after-handshake-once:") {
		marker := strings.TrimPrefix(mode, "crash-after-handshake-once:")
		if _, err := os.Stat(marker); errors.Is(err, os.ErrNotExist) {
			_ = os.WriteFile(marker, []byte("crashed"), 0o644)
			os.Exit(2)
		}
	}
	if mode == "crash-after-handshake-once" {
		os.Exit(2)
	}
	if mode == "exit-after-handshake" {
		os.Exit(0)
	}

	reader := bufio.NewReader(os.Stdin)
	crashed := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimSpace(line)
		if strings.Contains(line, `"type":"health"`) {
			if mode == "hang-health" {
				time.Sleep(5 * time.Second)
				os.Exit(0)
			}
			_, _ = os.Stdout.WriteString("{\"type\":\"health\",\"status\":\"ok\",\"message\":\"healthy\"}\n")
			continue
		}
		if strings.Contains(line, `"type":"event"`) {
			if mode == "reply-text-callback" {
				var requestEnvelope map[string]json.RawMessage
				if err := json.Unmarshal([]byte(line), &requestEnvelope); err != nil {
					_, _ = os.Stdout.WriteString("{\"type\":\"event\",\"status\":\"error\",\"error\":\"failed to decode host request envelope\"}\n")
					continue
				}
				rawEvent, ok := requestEnvelope["event"]
				if !ok {
					_, _ = os.Stdout.WriteString("{\"type\":\"event\",\"status\":\"error\",\"error\":\"missing event payload\"}\n")
					continue
				}
				var payload struct {
					Event eventmodel.Event            `json:"event"`
					Ctx   eventmodel.ExecutionContext `json:"ctx"`
				}
				if err := json.Unmarshal(rawEvent, &payload); err != nil {
					_, _ = os.Stdout.WriteString("{\"type\":\"event\",\"status\":\"error\",\"error\":\"failed to decode event payload\"}\n")
					continue
				}
				if payload.Ctx.Reply == nil {
					_, _ = os.Stdout.WriteString("{\"type\":\"event\",\"status\":\"error\",\"error\":\"missing reply handle\"}\n")
					continue
				}
				callbackEnvelope := map[string]any{
					"type":     "callback",
					"callback": "reply_text",
					"reply_text": map[string]any{
						"handle": payload.Ctx.Reply,
						"text":   "callback: " + payload.Event.Message.Text,
					},
				}
				encoded, err := json.Marshal(callbackEnvelope)
				if err != nil {
					_, _ = os.Stdout.WriteString("{\"type\":\"event\",\"status\":\"error\",\"error\":\"failed to encode callback envelope\"}\n")
					continue
				}
				_, _ = os.Stdout.WriteString(string(encoded) + "\n")
				ackLine, err := reader.ReadString('\n')
				if err != nil {
					_, _ = os.Stdout.WriteString("{\"type\":\"event\",\"status\":\"error\",\"error\":\"failed to read callback ack\"}\n")
					continue
				}
				var ack hostCallbackResult
				if err := json.Unmarshal([]byte(strings.TrimSpace(ackLine)), &ack); err != nil {
					_, _ = os.Stdout.WriteString("{\"type\":\"event\",\"status\":\"error\",\"error\":\"failed to decode callback ack\"}\n")
					continue
				}
				if ack.Type != "callback_result" || ack.Status != "ok" {
					_, _ = os.Stdout.WriteString("{\"type\":\"event\",\"status\":\"error\",\"error\":\"unexpected callback ack\"}\n")
					continue
				}
				_, _ = os.Stdout.WriteString("{\"type\":\"event\",\"status\":\"ok\",\"message\":\"event-ok\"}\n")
				continue
			}
			if mode == "workflow-start-or-resume-callback" {
				var requestEnvelope map[string]json.RawMessage
				if err := json.Unmarshal([]byte(line), &requestEnvelope); err != nil {
					_, _ = os.Stdout.WriteString("{\"type\":\"event\",\"status\":\"error\",\"error\":\"failed to decode host request envelope\"}\n")
					continue
				}
				rawEvent, ok := requestEnvelope["event"]
				if !ok {
					_, _ = os.Stdout.WriteString("{\"type\":\"event\",\"status\":\"error\",\"error\":\"missing event payload\"}\n")
					continue
				}
				var payload struct {
					Event eventmodel.Event            `json:"event"`
					Ctx   eventmodel.ExecutionContext `json:"ctx"`
				}
				if err := json.Unmarshal(rawEvent, &payload); err != nil {
					_, _ = os.Stdout.WriteString("{\"type\":\"event\",\"status\":\"error\",\"error\":\"failed to decode event payload\"}\n")
					continue
				}
				greeting := ""
				if payload.Event.Message != nil {
					greeting = payload.Event.Message.Text
				}
				workflowID := "workflow-anon"
				if payload.Event.Actor != nil && strings.TrimSpace(payload.Event.Actor.ID) != "" {
					workflowID = "workflow-" + payload.Event.Actor.ID
				}
				callbackEnvelope := map[string]any{
					"type":     "callback",
					"callback": "workflow_start_or_resume",
					"workflow_start_or_resume": map[string]any{
						"workflow_id": workflowID,
						"plugin_id":   "plugin-subprocess-demo",
						"event_type":  payload.Event.Type,
						"event_id":    payload.Event.EventID,
						"initial": map[string]any{
							"id":           workflowID,
							"steps":        []map[string]any{{"kind": "persist_state", "name": "greeting", "value": greeting}, {"kind": "wait_event", "name": "wait-confirm", "value": "message.received"}, {"kind": "compensate", "name": "complete"}},
							"currentIndex": 0,
							"state":        map[string]any{},
							"waitingFor":   "",
							"completed":    false,
							"compensated":  false,
						},
					},
				}
				encoded, err := json.Marshal(callbackEnvelope)
				if err != nil {
					_, _ = os.Stdout.WriteString("{\"type\":\"event\",\"status\":\"error\",\"error\":\"failed to encode workflow callback envelope\"}\n")
					continue
				}
				_, _ = os.Stdout.WriteString(string(encoded) + "\n")
				ackLine, err := reader.ReadString('\n')
				if err != nil {
					_, _ = os.Stdout.WriteString("{\"type\":\"event\",\"status\":\"error\",\"error\":\"failed to read workflow callback ack\"}\n")
					continue
				}
				var ack hostCallbackResult
				if err := json.Unmarshal([]byte(strings.TrimSpace(ackLine)), &ack); err != nil {
					_, _ = os.Stdout.WriteString("{\"type\":\"event\",\"status\":\"error\",\"error\":\"failed to decode workflow callback ack\"}\n")
					continue
				}
				if ack.Type != "callback_result" || ack.Status != "ok" || ack.WorkflowStartOrResume == nil || !ack.WorkflowStartOrResume.Started {
					_, _ = os.Stdout.WriteString("{\"type\":\"event\",\"status\":\"error\",\"error\":\"unexpected workflow callback ack\"}\n")
					continue
				}
				_, _ = os.Stdout.WriteString("{\"type\":\"event\",\"status\":\"ok\",\"message\":\"event-ok\"}\n")
				continue
			}
			if mode == "assert-instance-config" {
				var requestEnvelope map[string]json.RawMessage
				if err := json.Unmarshal([]byte(line), &requestEnvelope); err != nil {
					_, _ = os.Stdout.WriteString("{\"type\":\"event\",\"status\":\"error\",\"error\":\"failed to decode host request envelope\"}\n")
					continue
				}
				if _, ok := requestEnvelope["config"]; ok {
					_, _ = os.Stdout.WriteString("{\"type\":\"event\",\"status\":\"error\",\"error\":\"unexpected config field in host request\"}\n")
					continue
				}
				rawInstanceConfig, ok := requestEnvelope["instance_config"]
				if !ok {
					_, _ = os.Stdout.WriteString("{\"type\":\"event\",\"status\":\"error\",\"error\":\"missing instance_config field in host request\"}\n")
					continue
				}
				var instanceConfig map[string]any
				if err := json.Unmarshal(rawInstanceConfig, &instanceConfig); err != nil {
					_, _ = os.Stdout.WriteString("{\"type\":\"event\",\"status\":\"error\",\"error\":\"failed to decode instance_config payload\"}\n")
					continue
				}
				prefix, ok := instanceConfig["prefix"].(string)
				if !ok || prefix != "!" {
					_, _ = os.Stdout.WriteString("{\"type\":\"event\",\"status\":\"error\",\"error\":\"unexpected instance_config prefix value\"}\n")
					continue
				}
			}
			if mode == "hang-event" {
				time.Sleep(5 * time.Second)
				os.Exit(0)
			}
			if mode == "flood-stderr" {
				for i := 0; i < 5; i++ {
					_, _ = os.Stderr.WriteString(fmt.Sprintf("flood-%d\n", i))
				}
			}
			if strings.HasPrefix(mode, "crash-once:") && !crashed {
				marker := strings.TrimPrefix(mode, "crash-once:")
				if _, err := os.Stat(marker); errors.Is(err, os.ErrNotExist) {
					_ = os.WriteFile(marker, []byte("crashed"), 0o644)
					crashed = true
					os.Exit(2)
				}
			}
			if mode == "crash-once" && !crashed {
				crashed = true
				os.Exit(2)
			}
			_, _ = os.Stdout.WriteString("{\"type\":\"event\",\"status\":\"ok\",\"message\":\"event-ok\"}\n")
			continue
		}
		if strings.Contains(line, `"type":"command"`) {
			_, _ = os.Stdout.WriteString("{\"type\":\"command\",\"status\":\"ok\",\"message\":\"command-ok\"}\n")
			continue
		}
		if strings.Contains(line, `"type":"job"`) {
			_, _ = os.Stdout.WriteString("{\"type\":\"job\",\"status\":\"ok\",\"message\":\"job-ok\"}\n")
			continue
		}
		if strings.Contains(line, `"type":"schedule"`) {
			_, _ = os.Stdout.WriteString("{\"type\":\"schedule\",\"status\":\"ok\",\"message\":\"schedule-ok\"}\n")
		}
	}
	os.Exit(0)
}

package runtimecore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

type stubAdapter struct {
	id     string
	source string
}

func (a stubAdapter) ID() string     { return a.id }
func (a stubAdapter) Source() string { return a.source }

type recordingEventHandler struct {
	called bool
	ctx    eventmodel.ExecutionContext
}

func (h *recordingEventHandler) OnEvent(event eventmodel.Event, ctx eventmodel.ExecutionContext) error {
	h.called = true
	h.ctx = ctx
	return nil
}

type failingEventHandler struct{}

func (failingEventHandler) OnEvent(event eventmodel.Event, ctx eventmodel.ExecutionContext) error {
	return errors.New("boom")
}

type recordingSupervisor struct {
	ensured []string
}

func (s *recordingSupervisor) EnsurePlugin(_ context.Context, pluginID string) error {
	s.ensured = append(s.ensured, pluginID)
	return nil
}

type recordingCommandHandler struct {
	called  bool
	command eventmodel.CommandInvocation
	ctx     eventmodel.ExecutionContext
}

func (h *recordingCommandHandler) OnCommand(command eventmodel.CommandInvocation, ctx eventmodel.ExecutionContext) error {
	h.called = true
	h.command = command
	h.ctx = ctx
	return nil
}

type recordingJobHandler struct {
	called bool
	job    pluginsdk.JobInvocation
	ctx    eventmodel.ExecutionContext
}

func (h *recordingJobHandler) OnJob(job pluginsdk.JobInvocation, ctx eventmodel.ExecutionContext) error {
	h.called = true
	h.job = job
	h.ctx = ctx
	return nil
}

type failingInspectJobQueue struct {
	*JobQueue
	err error
}

func (q failingInspectJobQueue) Inspect(_ context.Context, _ string) (Job, error) {
	return Job{}, q.err
}

type heartbeatCountingQueue struct {
	*JobQueue
	heartbeats int32
	lastErr    atomic.Value
}

func (q *heartbeatCountingQueue) Heartbeat(ctx context.Context, id string) (Job, error) {
	updated, err := q.JobQueue.Heartbeat(ctx, id)
	if err != nil {
		q.lastErr.Store(err.Error())
		return updated, err
	}
	atomic.AddInt32(&q.heartbeats, 1)
	return updated, nil
}

func (q *heartbeatCountingQueue) HeartbeatCount() int32 {
	return atomic.LoadInt32(&q.heartbeats)
}

func (q *heartbeatCountingQueue) LastHeartbeatError() string {
	if value := q.lastErr.Load(); value != nil {
		if errText, ok := value.(string); ok {
			return errText
		}
	}
	return ""
}

type recordingScheduleHandler struct {
	called  bool
	trigger pluginsdk.ScheduleTrigger
	ctx     eventmodel.ExecutionContext
}

func (h *recordingScheduleHandler) OnSchedule(trigger pluginsdk.ScheduleTrigger, ctx eventmodel.ExecutionContext) error {
	h.called = true
	h.trigger = trigger
	h.ctx = ctx
	return nil
}

type recordingCommandAuthorizer struct {
	called  bool
	err     error
	command eventmodel.CommandInvocation
	ctx     eventmodel.ExecutionContext
}

func (a *recordingCommandAuthorizer) AuthorizeCommand(_ context.Context, command eventmodel.CommandInvocation, ctx eventmodel.ExecutionContext) error {
	a.called = true
	a.command = command
	a.ctx = ctx
	return a.err
}

type recordingEventAuthorizer struct {
	called bool
	err    error
	event  eventmodel.Event
	ctx    eventmodel.ExecutionContext
	plugin pluginsdk.Plugin
}

func (a *recordingEventAuthorizer) AuthorizeEvent(_ context.Context, event eventmodel.Event, ctx eventmodel.ExecutionContext, plugin pluginsdk.Plugin) error {
	a.called = true
	a.event = event
	a.ctx = ctx
	a.plugin = plugin
	return a.err
}

type recordingJobAuthorizer struct {
	called bool
	err    error
	job    pluginsdk.JobInvocation
	ctx    eventmodel.ExecutionContext
	plugin pluginsdk.Plugin
}

func (a *recordingJobAuthorizer) AuthorizeJob(_ context.Context, job pluginsdk.JobInvocation, ctx eventmodel.ExecutionContext, plugin pluginsdk.Plugin) error {
	a.called = true
	a.job = job
	a.ctx = ctx
	a.plugin = plugin
	return a.err
}

type recordingScheduleAuthorizer struct {
	called  bool
	err     error
	trigger pluginsdk.ScheduleTrigger
	ctx     eventmodel.ExecutionContext
	plugin  pluginsdk.Plugin
}

func (a *recordingScheduleAuthorizer) AuthorizeSchedule(_ context.Context, trigger pluginsdk.ScheduleTrigger, ctx eventmodel.ExecutionContext, plugin pluginsdk.Plugin) error {
	a.called = true
	a.trigger = trigger
	a.ctx = ctx
	a.plugin = plugin
	return a.err
}

func TestRuntimeRegistersAdapterAndPluginThenDispatchesViaHost(t *testing.T) {
	t.Parallel()

	handler := &recordingEventHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})

	if err := runtime.RegisterAdapter(AdapterRegistration{
		ID:      "adapter-onebot",
		Source:  "onebot",
		Adapter: stubAdapter{id: "adapter-onebot", source: "onebot"},
	}); err != nil {
		t.Fatalf("register adapter: %v", err)
	}

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-echo",
			Name:       "Echo Plugin",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			Entry:      pluginsdk.PluginEntry{Module: "plugins/echo", Symbol: "Plugin"},
		},
		Handlers: pluginsdk.Handlers{Event: handler},
	}

	if err := runtime.RegisterPlugin(plugin); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	event := eventmodel.Event{
		EventID:        "evt-1",
		TraceID:        "trace-1",
		Source:         "onebot",
		Type:           "message.received",
		Timestamp:      time.Date(2026, 4, 2, 10, 0, 0, 0, time.UTC),
		IdempotencyKey: "onebot:msg:1",
	}

	if err := runtime.DispatchEvent(context.Background(), event); err != nil {
		t.Fatalf("dispatch event: %v", err)
	}

	if !handler.called {
		t.Fatal("expected runtime to dispatch event through plugin host")
	}
	if handler.ctx.PluginID != "plugin-echo" {
		t.Fatalf("expected plugin context to include plugin id, got %q", handler.ctx.PluginID)
	}
	if handler.ctx.Reply != nil {
		t.Fatalf("did not expect reply handle when event has none, got %+v", handler.ctx.Reply)
	}
	if len(runtime.RegisteredAdapters()) != 1 {
		t.Fatalf("expected 1 registered adapter, got %d", len(runtime.RegisteredAdapters()))
	}
	results := runtime.DispatchResults()
	if len(results) != 1 || !results[0].Success {
		t.Fatalf("expected one successful dispatch result, got %+v", results)
	}
}

func TestRuntimeDispatchPassesReplyHandleIntoExecutionContext(t *testing.T) {
	t.Parallel()

	handler := &recordingEventHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-echo",
			Name:       "Echo Plugin",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			Entry:      pluginsdk.PluginEntry{Module: "plugins/echo", Symbol: "Plugin"},
		},
		Handlers: pluginsdk.Handlers{Event: handler},
	}

	if err := runtime.RegisterPlugin(plugin); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	event := eventmodel.Event{
		EventID:        "evt-reply",
		TraceID:        "trace-reply",
		Source:         "onebot",
		Type:           "message.received",
		Timestamp:      time.Date(2026, 4, 2, 10, 2, 0, 0, time.UTC),
		IdempotencyKey: "onebot:msg:reply",
		Reply: &eventmodel.ReplyHandle{
			Capability: "onebot.reply",
			TargetID:   "group-42",
		},
	}

	if err := runtime.DispatchEvent(context.Background(), event); err != nil {
		t.Fatalf("dispatch event: %v", err)
	}
	if handler.ctx.Reply == nil || handler.ctx.Reply.TargetID != "group-42" {
		t.Fatalf("expected reply handle in execution context, got %+v", handler.ctx.Reply)
	}
}

func TestRuntimeDispatchRecordsObservability(t *testing.T) {
	t.Parallel()

	buffer := &bytes.Buffer{}
	logger := NewLogger(buffer)
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	handler := &recordingEventHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	runtime.SetObservability(logger, tracer, metrics)

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-echo",
			Name:       "Echo Plugin",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			Entry:      pluginsdk.PluginEntry{Module: "plugins/echo", Symbol: "Plugin"},
		},
		Handlers: pluginsdk.Handlers{Event: handler},
	}
	if err := runtime.RegisterPlugin(plugin); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	event := eventmodel.Event{
		EventID:        "evt-observe",
		TraceID:        "trace-observe",
		Source:         "onebot",
		Type:           "message.received",
		Timestamp:      time.Date(2026, 4, 3, 20, 0, 0, 0, time.UTC),
		IdempotencyKey: "onebot:observe",
	}
	if err := runtime.DispatchEvent(context.Background(), event); err != nil {
		t.Fatalf("dispatch event: %v", err)
	}

	if handler.ctx.RunID == "" || handler.ctx.CorrelationID != "onebot:observe" {
		t.Fatalf("expected run/correlation propagation, got %+v", handler.ctx)
	}
	if !strings.Contains(buffer.String(), "runtime dispatch started") || !strings.Contains(buffer.String(), "plugin dispatch completed") || !strings.Contains(buffer.String(), "trace-observe") {
		t.Fatalf("expected runtime observability logs, got %s", buffer.String())
	}
	if rendered := tracer.RenderTrace("trace-observe"); !strings.Contains(rendered, "runtime.dispatch") || !strings.Contains(rendered, "plugin.invoke") {
		t.Fatalf("expected runtime and plugin spans, got %s", rendered)
	}
	output := metrics.RenderPrometheus()
	if !strings.Contains(output, `bot_platform_runtime_dispatch_total{plugin_id="plugin-echo",operation="event",outcome="success"} 1`) || !strings.Contains(output, `bot_platform_runtime_dispatch_last_duration_ms{plugin_id="plugin-echo",operation="event"}`) {
		t.Fatalf("expected dispatch metrics, got %s", output)
	}
}

func TestRuntimeDispatchPassesInstanceConfigIntoSubprocessHostRequest(t *testing.T) {
	t.Parallel()

	buffer := &bytes.Buffer{}
	logger := NewLogger(buffer)
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHost(testPluginProcessFactory(t, "assert-instance-config"))
	host.SetObservability(logger, tracer, metrics)
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	handler := &recordingEventHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, host)
	runtime.SetObservability(logger, tracer, metrics)

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-runtime",
			Name:       "Instance Config Runtime",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"prefix": map[string]any{"type": "string"},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-runtime", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"prefix": "!"},
		Handlers:       pluginsdk.Handlers{Event: handler},
	}
	if err := runtime.RegisterPlugin(plugin); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	event := eventmodel.Event{
		EventID:        "evt-instance-config-runtime",
		TraceID:        "trace-instance-config-runtime",
		Source:         "scheduler",
		Type:           "schedule.triggered",
		Timestamp:      time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC),
		IdempotencyKey: "schedule:instance-config-runtime",
	}
	if err := runtime.DispatchEvent(context.Background(), event); err != nil {
		t.Fatalf("dispatch event with subprocess instance config: %v", err)
	}
	if handler.called {
		t.Fatal("expected subprocess host dispatch to avoid in-process handler execution")
	}
	stdout := strings.Join(host.StdoutLines(), "\n")
	for _, expected := range []string{"handshake-ready", "event-ok"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("expected subprocess stdout to include %q, got %s", expected, stdout)
		}
	}
	if !strings.Contains(buffer.String(), "subprocess host dispatch completed") || !strings.Contains(buffer.String(), "runtime dispatch completed") {
		t.Fatalf("expected runtime+host logs, got %s", buffer.String())
	}
	if rendered := tracer.RenderTrace("trace-instance-config-runtime"); !strings.Contains(rendered, "runtime.dispatch") || !strings.Contains(rendered, "plugin_host.dispatch") {
		t.Fatalf("expected runtime and subprocess host spans, got %s", rendered)
	}
}

func TestRuntimeDispatchDoesNotInjectDeeperNestedManifestDefaultIntoSubprocessHostRequest(t *testing.T) {
	t.Parallel()

	buffer := &bytes.Buffer{}
	logger := NewLogger(buffer)
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-deeper-nested-default-not-injected"))
	host.SetObservability(logger, tracer, metrics)
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	handler := &recordingEventHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, host)
	runtime.SetObservability(logger, tracer, metrics)

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-deeper-nested-default-runtime",
			Name:       "Instance Config Deeper Nested Default Runtime",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-deeper-nested-default-runtime", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{}}},
		Handlers:       pluginsdk.Handlers{Event: handler},
	}
	if err := runtime.RegisterPlugin(plugin); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	event := eventmodel.Event{
		EventID:        "evt-instance-config-deeper-nested-default-runtime",
		TraceID:        "trace-instance-config-deeper-nested-default-runtime",
		Source:         "scheduler",
		Type:           "schedule.triggered",
		Timestamp:      time.Date(2026, 4, 14, 10, 13, 0, 0, time.UTC),
		IdempotencyKey: "schedule:instance-config-deeper-nested-default-runtime",
	}
	if err := runtime.DispatchEvent(context.Background(), event); err != nil {
		t.Fatalf("expected runtime dispatch to preserve deeper nested manifest default omission without rejection, got %v", err)
	}
	if handler.called {
		t.Fatal("expected subprocess host dispatch to avoid in-process handler execution")
	}
	stdout := strings.Join(host.StdoutLines(), "\n")
	for _, expected := range []string{"handshake-ready", "event-ok"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("expected subprocess stdout to include %q, got %s", expected, stdout)
		}
	}
	if strings.Contains(buffer.String(), "subprocess host instance config rejected") {
		t.Fatalf("expected deeper nested default-only runtime path to avoid instance config rejection logs, got %s", buffer.String())
	}
	if !strings.Contains(buffer.String(), "subprocess host dispatch completed") || !strings.Contains(buffer.String(), "runtime dispatch completed") {
		t.Fatalf("expected runtime+host logs for deeper nested default-only dispatch, got %s", buffer.String())
	}
	rendered := tracer.RenderTrace("trace-instance-config-deeper-nested-default-runtime")
	if !strings.Contains(rendered, "runtime.dispatch") || !strings.Contains(rendered, "plugin_host.dispatch") {
		t.Fatalf("expected runtime and subprocess host dispatch spans, got %s", rendered)
	}
	if strings.Contains(metrics.RenderPrometheus(), `bot_platform_plugin_errors_total{plugin_id="plugin-instance-config-deeper-nested-default-runtime"}`) {
		t.Fatalf("expected deeper nested default-only runtime path to avoid plugin error metrics, got %s", metrics.RenderPrometheus())
	}
}

func TestRuntimeDispatchRejectsBeyondSupportedDeeperNestedObjectNodeMismatchIntoSubprocessHostRequest(t *testing.T) {
	t.Parallel()

	buffer := &bytes.Buffer{}
	logger := NewLogger(buffer)
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHost(testPluginProcessFactory(t, "echo"))
	host.SetObservability(logger, tracer, metrics)
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	handler := &recordingEventHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, host)
	runtime.SetObservability(logger, tracer, metrics)

	plugin := pluginsdk.Plugin{
		Manifest:       testBeyondSupportedDeeperNestedNamingManifest("plugin-instance-config-beyond-supported-deeper-nested-object-node-mismatch-runtime", "Instance Config Beyond Supported Deeper Nested Object Node Mismatch Runtime", "plugins/instance-config-beyond-supported-deeper-nested-object-node-mismatch-runtime", map[string]any{"type": "string"}, false),
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": true}}},
		Handlers:       pluginsdk.Handlers{Event: handler},
	}
	if err := runtime.RegisterPlugin(plugin); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	event := eventmodel.Event{EventID: "evt-instance-config-beyond-supported-deeper-nested-object-node-mismatch-runtime", TraceID: "trace-instance-config-beyond-supported-deeper-nested-object-node-mismatch-runtime", Source: "scheduler", Type: "schedule.triggered", Timestamp: time.Date(2026, 4, 14, 10, 15, 0, 0, time.UTC), IdempotencyKey: "schedule:instance-config-beyond-supported-deeper-nested-object-node-mismatch-runtime"}
	err := runtime.DispatchEvent(context.Background(), event)
	if err == nil || !strings.Contains(err.Error(), `dispatch completed with no successful plugin deliveries`) {
		t.Fatalf("expected runtime to stop after deeper nested object-node mismatch rejection, got %v", err)
	}
	if handler.called {
		t.Fatal("expected subprocess host dispatch to stop before in-process handler execution")
	}
	if len(runtime.DispatchResults()) != 1 || runtime.DispatchResults()[0].Success {
		t.Fatalf("expected failed dispatch result after deeper nested object-node mismatch rejection, got %+v", runtime.DispatchResults())
	}
	if !strings.Contains(runtime.DispatchResults()[0].Error, `nested instance config property "settings.labels.naming" value type must match declared type "object", got "boolean"`) {
		t.Fatalf("expected dispatch result to capture deeper nested object-node mismatch rejection, got %+v", runtime.DispatchResults())
	}
	if len(host.StdoutLines()) != 0 || len(host.StderrLines()) != 0 {
		t.Fatalf("expected runtime deeper nested object-node mismatch rejection to avoid subprocess side effects, stdout=%v stderr=%v", host.StdoutLines(), host.StderrLines())
	}
	for _, expected := range []string{"subprocess host instance config rejected", `"failure_stage":"instance_config"`, `"failure_reason":"instance_config_value_type_mismatch"`, `"compatibility_rule":"instance_config_deeper_nested_value_type"`, `"property_name":"settings.labels.naming"`, `"parent_property_name":"settings.labels"`, `"nested_property_name":"naming"`, `"root_property_name":"settings"`, `"intermediate_property_name":"labels"`, `"declared_type":"object"`, `"actual_type":"boolean"`, "runtime dispatch failed"} {
		if !strings.Contains(buffer.String(), expected) {
			t.Fatalf("expected runtime observability to include %q, got %s", expected, buffer.String())
		}
	}
	rendered := tracer.RenderTrace(event.TraceID)
	if !strings.Contains(rendered, "runtime.dispatch") || !strings.Contains(rendered, "plugin_host.instance_config") {
		t.Fatalf("expected runtime and object-node instance config spans, got %s", rendered)
	}
	if strings.Contains(rendered, "plugin_host.dispatch") {
		t.Fatalf("expected object-node mismatch rejection to stop before plugin_host.dispatch, got %s", rendered)
	}
	if !strings.Contains(metrics.RenderPrometheus(), `bot_platform_plugin_errors_total{plugin_id="plugin-instance-config-beyond-supported-deeper-nested-object-node-mismatch-runtime"} 2`) {
		t.Fatalf("expected plugin error metric to reflect host rejection plus runtime dispatch failure accounting, got %s", metrics.RenderPrometheus())
	}
}

func TestRuntimeDispatchRejectsBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitObjectNodeBadValuesIntoSubprocessHostRequest(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		pluginID       string
		instanceConfig map[string]any
		wantErr        string
		wantActualType string
	}{
		{name: "boolean", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-object-node-bad-value-runtime", instanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": true}}}, wantErr: `nested instance config property "settings.labels.naming" value type must match declared type "object", got "boolean"`, wantActualType: "boolean"},
		{name: "string", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-string-valued-object-node-bad-value-runtime", instanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": "oops"}}}, wantErr: `nested instance config property "settings.labels.naming" value type must match declared type "object", got "string"`, wantActualType: "string"},
		{name: "number", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-number-valued-object-node-bad-value-runtime", instanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": 7.0}}}, wantErr: `nested instance config property "settings.labels.naming" value type must match declared type "object", got "number"`, wantActualType: "number"},
		{name: "null", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-null-valued-object-node-bad-value-runtime", instanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": nil}}}, wantErr: `nested instance config property "settings.labels.naming" value type must match declared type "object", got "null"`, wantActualType: "null"},
		{name: "array", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-array-valued-object-node-bad-value-runtime", instanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": []any{"oops"}}}}, wantErr: `nested instance config property "settings.labels.naming" value type must match declared type "object", got "array"`, wantActualType: "array"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			buffer := &bytes.Buffer{}
			logger := NewLogger(buffer)
			tracer := NewTraceRecorder()
			metrics := NewMetricsRegistry()
			host := NewSubprocessPluginHost(testPluginProcessFactory(t, "echo"))
			host.SetObservability(logger, tracer, metrics)
			t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
			handler := &recordingEventHandler{}
			runtime := NewInMemoryRuntime(NoopSupervisor{}, host)
			runtime.SetObservability(logger, tracer, metrics)

			plugin := pluginsdk.Plugin{
				Manifest:       testBeyondSupportedDeeperNestedNamingManifest(tc.pluginID, tc.pluginID, "plugins/"+tc.pluginID, map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"}, true),
				InstanceConfig: tc.instanceConfig,
				Handlers:       pluginsdk.Handlers{Event: handler},
			}
			if err := runtime.RegisterPlugin(plugin); err != nil {
				t.Fatalf("register plugin: %v", err)
			}

			event := eventmodel.Event{EventID: "evt-" + tc.pluginID, TraceID: "trace-" + tc.pluginID, Source: "scheduler", Type: "schedule.triggered", Timestamp: time.Date(2026, 4, 14, 10, 41, 0, 0, time.UTC), IdempotencyKey: "schedule:" + tc.pluginID}
			err := runtime.DispatchEvent(context.Background(), event)
			if err == nil || !strings.Contains(err.Error(), `dispatch completed with no successful plugin deliveries`) {
				t.Fatalf("expected runtime rejection wrapper error, got %v", err)
			}
			if handler.called {
				t.Fatal("expected subprocess host rejection to stop before in-process handler execution")
			}
			if len(runtime.DispatchResults()) != 1 || runtime.DispatchResults()[0].Success {
				t.Fatalf("expected failed dispatch result, got %+v", runtime.DispatchResults())
			}
			if !strings.Contains(runtime.DispatchResults()[0].Error, tc.wantErr) {
				t.Fatalf("expected dispatch result to capture %q, got %+v", tc.wantErr, runtime.DispatchResults())
			}
			if len(host.StdoutLines()) != 0 || len(host.StderrLines()) != 0 {
				t.Fatalf("expected runtime rejection to avoid subprocess side effects, stdout=%v stderr=%v", host.StdoutLines(), host.StderrLines())
			}
			for _, expected := range []string{"subprocess host instance config rejected", `"failure_stage":"instance_config"`, `"failure_reason":"instance_config_value_type_mismatch"`, `"compatibility_rule":"instance_config_deeper_nested_value_type"`, `"property_name":"settings.labels.naming"`, `"parent_property_name":"settings.labels"`, `"nested_property_name":"naming"`, `"root_property_name":"settings"`, `"intermediate_property_name":"labels"`, `"declared_type":"object"`, `"actual_type":"` + tc.wantActualType + `"`, "runtime dispatch failed"} {
				if !strings.Contains(buffer.String(), expected) {
					t.Fatalf("expected runtime observability to include %q, got %s", expected, buffer.String())
				}
			}
			rendered := tracer.RenderTrace(event.TraceID)
			if !strings.Contains(rendered, "runtime.dispatch") || !strings.Contains(rendered, "plugin_host.instance_config") {
				t.Fatalf("expected runtime and object-node instance config spans, got %s", rendered)
			}
			if strings.Contains(rendered, "plugin_host.dispatch") {
				t.Fatalf("expected object-node rejection to stop before plugin_host.dispatch, got %s", rendered)
			}
			if !strings.Contains(metrics.RenderPrometheus(), fmt.Sprintf(`bot_platform_plugin_errors_total{plugin_id=%q} 2`, tc.pluginID)) {
				t.Fatalf("expected plugin error metric to reflect host rejection plus runtime dispatch failure accounting, got %s", metrics.RenderPrometheus())
			}
		})
	}
}

func TestRuntimeDispatchRejectsBeyondSupportedDeeperNestedValidationOnExistingNamingBranchBeforePluginDelivery(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name            string
		pluginID        string
		prefixSchema    map[string]any
		namingRequired  bool
		instanceConfig  map[string]any
		wantDispatchErr string
		wantLogParts    []string
	}{
		{name: "type_mismatch", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-runtime", prefixSchema: map[string]any{"type": "string"}, instanceConfig: testBeyondSupportedDeeperNestedNamingInstanceConfig(true), wantDispatchErr: `nested instance config property "settings.labels.naming.prefix" value type must match declared type "string", got "boolean"`, wantLogParts: []string{`"failure_reason":"instance_config_value_type_mismatch"`, `"compatibility_rule":"instance_config_deeper_nested_value_type"`, `"property_name":"settings.labels.naming.prefix"`, `"actual_type":"boolean"`, "runtime dispatch failed"}},
		{name: "array_value_mismatch", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-array-value-mismatch-runtime", prefixSchema: map[string]any{"type": "string"}, instanceConfig: testBeyondSupportedDeeperNestedNamingInstanceConfig([]any{"oops"}), wantDispatchErr: `nested instance config property "settings.labels.naming.prefix" value type must match declared type "string", got "array"`, wantLogParts: []string{`"failure_reason":"instance_config_value_type_mismatch"`, `"compatibility_rule":"instance_config_deeper_nested_value_type"`, `"property_name":"settings.labels.naming.prefix"`, `"actual_type":"array"`, "runtime dispatch failed"}},
		{name: "object_value_mismatch", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-object-value-mismatch-runtime", prefixSchema: map[string]any{"type": "string"}, instanceConfig: testBeyondSupportedDeeperNestedNamingInstanceConfig(map[string]any{"bad": true}), wantDispatchErr: `nested instance config property "settings.labels.naming.prefix" value type must match declared type "string", got "object"`, wantLogParts: []string{`"failure_reason":"instance_config_value_type_mismatch"`, `"compatibility_rule":"instance_config_deeper_nested_value_type"`, `"property_name":"settings.labels.naming.prefix"`, `"actual_type":"object"`, "runtime dispatch failed"}},
		{name: "required_missing", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-runtime", prefixSchema: map[string]any{"type": "string"}, namingRequired: true, instanceConfig: testBeyondSupportedDeeperNestedEmptyNamingInstanceConfig(), wantDispatchErr: `nested instance config required property "settings.labels.naming.prefix" must be provided`, wantLogParts: []string{`"failure_reason":"instance_config_missing_required_value"`, `"compatibility_rule":"instance_config_deeper_nested_required"`, `"property_name":"settings.labels.naming.prefix"`, "runtime dispatch failed"}},
		{name: "enum_out_of_set", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-enum-runtime", prefixSchema: map[string]any{"type": "string", "enum": []any{"hello", "world"}}, instanceConfig: testBeyondSupportedDeeperNestedNamingInstanceConfig("oops"), wantDispatchErr: `nested instance config property "settings.labels.naming.prefix" value "oops" must be declared in enum ["hello","world"] for declared type "string"`, wantLogParts: []string{`"failure_reason":"instance_config_enum_value_out_of_set"`, `"compatibility_rule":"instance_config_deeper_nested_enum"`, `"property_name":"settings.labels.naming.prefix"`, `"actual_value":"oops"`, `"enum_values":["hello","world"]`, "runtime dispatch failed"}},
		{name: "required_enum_boundary", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-runtime", prefixSchema: map[string]any{"type": "string", "enum": []any{"hello", "world"}}, namingRequired: true, instanceConfig: testBeyondSupportedDeeperNestedEmptyNamingInstanceConfig(), wantDispatchErr: `nested instance config required property "settings.labels.naming.prefix" must be provided`, wantLogParts: []string{`"failure_reason":"instance_config_missing_required_value"`, `"property_name":"settings.labels.naming.prefix"`, "runtime dispatch failed"}},
		{name: "required_enum_default_explicit_bad_value", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-bad-value-runtime", prefixSchema: map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"}, namingRequired: true, instanceConfig: testBeyondSupportedDeeperNestedNamingInstanceConfig("oops"), wantDispatchErr: `nested instance config property "settings.labels.naming.prefix" value "oops" must be declared in enum ["hello","world"] for declared type "string"`, wantLogParts: []string{`"failure_reason":"instance_config_enum_value_out_of_set"`, `"property_name":"settings.labels.naming.prefix"`, `"actual_value":"oops"`, "runtime dispatch failed"}},
		{name: "required_enum_default_explicit_array_bad_value", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-array-valued-bad-value-runtime", prefixSchema: map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"}, namingRequired: true, instanceConfig: testBeyondSupportedDeeperNestedNamingInstanceConfig([]any{"oops"}), wantDispatchErr: `nested instance config property "settings.labels.naming.prefix" value type must match declared type "string", got "array"`, wantLogParts: []string{`"failure_reason":"instance_config_value_type_mismatch"`, `"property_name":"settings.labels.naming.prefix"`, `"actual_type":"array"`, "runtime dispatch failed"}},
		{name: "required_enum_default_explicit_wrong_type_bad_value", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-wrong-type-bad-value-runtime", prefixSchema: map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"}, namingRequired: true, instanceConfig: testBeyondSupportedDeeperNestedNamingInstanceConfig(true), wantDispatchErr: `nested instance config property "settings.labels.naming.prefix" value type must match declared type "string", got "boolean"`, wantLogParts: []string{`"failure_reason":"instance_config_value_type_mismatch"`, `"property_name":"settings.labels.naming.prefix"`, `"actual_type":"boolean"`, "runtime dispatch failed"}},
		{name: "required_enum_default_explicit_object_bad_value", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-object-valued-bad-value-runtime", prefixSchema: map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"}, namingRequired: true, instanceConfig: testBeyondSupportedDeeperNestedNamingInstanceConfig(map[string]any{"bad": true}), wantDispatchErr: `nested instance config property "settings.labels.naming.prefix" value type must match declared type "string", got "object"`, wantLogParts: []string{`"failure_reason":"instance_config_value_type_mismatch"`, `"property_name":"settings.labels.naming.prefix"`, `"actual_type":"object"`, "runtime dispatch failed"}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			buffer := &bytes.Buffer{}
			logger := NewLogger(buffer)
			tracer := NewTraceRecorder()
			metrics := NewMetricsRegistry()
			host := NewSubprocessPluginHost(testPluginProcessFactory(t, "echo"))
			host.SetObservability(logger, tracer, metrics)
			t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
			handler := &recordingEventHandler{}
			runtime := NewInMemoryRuntime(NoopSupervisor{}, host)
			runtime.SetObservability(logger, tracer, metrics)

			plugin := pluginsdk.Plugin{
				Manifest:       testBeyondSupportedDeeperNestedNamingManifest(tc.pluginID, tc.pluginID, "plugins/"+tc.pluginID, tc.prefixSchema, tc.namingRequired),
				InstanceConfig: tc.instanceConfig,
				Handlers:       pluginsdk.Handlers{Event: handler},
			}
			if err := runtime.RegisterPlugin(plugin); err != nil {
				t.Fatalf("register plugin: %v", err)
			}

			event := eventmodel.Event{EventID: "evt-" + tc.pluginID, TraceID: "trace-" + tc.pluginID, Source: "scheduler", Type: "schedule.triggered", Timestamp: time.Date(2026, 4, 14, 10, 30, 0, 0, time.UTC), IdempotencyKey: "schedule:" + tc.pluginID}
			err := runtime.DispatchEvent(context.Background(), event)
			if err == nil || !strings.Contains(err.Error(), "dispatch completed with no successful plugin deliveries") {
				t.Fatalf("expected runtime rejection wrapper error, got %v", err)
			}
			if handler.called {
				t.Fatal("expected subprocess host rejection to stop before in-process handler execution")
			}
			if len(runtime.DispatchResults()) != 1 || runtime.DispatchResults()[0].Success {
				t.Fatalf("expected failed dispatch result, got %+v", runtime.DispatchResults())
			}
			if !strings.Contains(runtime.DispatchResults()[0].Error, tc.wantDispatchErr) {
				t.Fatalf("expected dispatch result to capture %q, got %+v", tc.wantDispatchErr, runtime.DispatchResults())
			}
			if len(host.StdoutLines()) != 0 || len(host.StderrLines()) != 0 {
				t.Fatalf("expected runtime rejection to avoid subprocess side effects, stdout=%v stderr=%v", host.StdoutLines(), host.StderrLines())
			}
			for _, expected := range append([]string{"subprocess host instance config rejected", `"failure_stage":"instance_config"`}, tc.wantLogParts...) {
				if !strings.Contains(buffer.String(), expected) {
					t.Fatalf("expected runtime observability to include %q, got %s", expected, buffer.String())
				}
			}
			rendered := tracer.RenderTrace(event.TraceID)
			if !strings.Contains(rendered, "runtime.dispatch") || !strings.Contains(rendered, "plugin_host.instance_config") {
				t.Fatalf("expected runtime and instance_config spans, got %s", rendered)
			}
			if strings.Contains(rendered, "plugin_host.dispatch") {
				t.Fatalf("expected runtime rejection to stop before plugin_host.dispatch, got %s", rendered)
			}
			if !strings.Contains(metrics.RenderPrometheus(), fmt.Sprintf(`bot_platform_plugin_errors_total{plugin_id=%q} 2`, tc.pluginID)) {
				t.Fatalf("expected plugin error metric to reflect host rejection plus runtime dispatch failure accounting, got %s", metrics.RenderPrometheus())
			}
		})
	}
}

func TestRuntimeDispatchMergesBeyondSupportedDeeperNestedDefaultVariantsOnExistingNamingBranch(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		pluginID       string
		prefixSchema   map[string]any
		namingRequired bool
	}{
		{name: "required_default", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-default-runtime", prefixSchema: map[string]any{"type": "string", "default": "hello"}, namingRequired: true},
		{name: "enum_default", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-enum-default-runtime", prefixSchema: map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"}},
		{name: "required_enum_default", pluginID: "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-runtime", prefixSchema: map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"}, namingRequired: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			buffer := &bytes.Buffer{}
			logger := NewLogger(buffer)
			tracer := NewTraceRecorder()
			metrics := NewMetricsRegistry()
			host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-default-merged"))
			host.SetObservability(logger, tracer, metrics)
			t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
			handler := &recordingEventHandler{}
			runtime := NewInMemoryRuntime(NoopSupervisor{}, host)
			runtime.SetObservability(logger, tracer, metrics)

			plugin := pluginsdk.Plugin{
				Manifest:       testBeyondSupportedDeeperNestedNamingManifest(tc.pluginID, tc.pluginID, "plugins/"+tc.pluginID, tc.prefixSchema, tc.namingRequired),
				InstanceConfig: testBeyondSupportedDeeperNestedEmptyNamingInstanceConfig(),
				Handlers:       pluginsdk.Handlers{Event: handler},
			}
			if err := runtime.RegisterPlugin(plugin); err != nil {
				t.Fatalf("register plugin: %v", err)
			}

			event := eventmodel.Event{EventID: "evt-" + tc.pluginID, TraceID: "trace-" + tc.pluginID, Source: "scheduler", Type: "schedule.triggered", Timestamp: time.Date(2026, 4, 14, 10, 35, 0, 0, time.UTC), IdempotencyKey: "schedule:" + tc.pluginID}
			if err := runtime.DispatchEvent(context.Background(), event); err != nil {
				t.Fatalf("expected runtime dispatch to merge deeper nested default, got %v", err)
			}
			if handler.called {
				t.Fatal("expected subprocess host dispatch to avoid in-process handler execution")
			}
			stdout := strings.Join(host.StdoutLines(), "\n")
			for _, expected := range []string{"handshake-ready", "event-ok"} {
				if !strings.Contains(stdout, expected) {
					t.Fatalf("expected subprocess stdout to include %q, got %s", expected, stdout)
				}
			}
			if strings.Contains(buffer.String(), "subprocess host instance config rejected") {
				t.Fatalf("expected deeper nested default merge runtime path to avoid rejection logs, got %s", buffer.String())
			}
			if !strings.Contains(buffer.String(), "subprocess host dispatch completed") || !strings.Contains(buffer.String(), "runtime dispatch completed") {
				t.Fatalf("expected runtime+host logs for deeper nested default merge dispatch, got %s", buffer.String())
			}
			rendered := tracer.RenderTrace(event.TraceID)
			if !strings.Contains(rendered, "runtime.dispatch") || !strings.Contains(rendered, "plugin_host.dispatch") {
				t.Fatalf("expected runtime and subprocess host dispatch spans, got %s", rendered)
			}
			if strings.Contains(metrics.RenderPrometheus(), fmt.Sprintf(`bot_platform_plugin_errors_total{plugin_id=%q}`, tc.pluginID)) {
				t.Fatalf("expected no plugin error metric for merged default path, got %s", metrics.RenderPrometheus())
			}
		})
	}
}

func TestRuntimeDispatchMaterializesBeyondSupportedDeeperNestedRequiredEnumDefaultChildOmissionBoundaryIntoSubprocessHostRequest(t *testing.T) {
	t.Parallel()

	buffer := &bytes.Buffer{}
	logger := NewLogger(buffer)
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-required-enum-default-child-omission-not-enforced"))
	host.SetObservability(logger, tracer, metrics)
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	handler := &recordingEventHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, host)
	runtime.SetObservability(logger, tracer, metrics)

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-child-omission-runtime",
			Name:       "Instance Config Beyond Supported Deeper Nested Required Enum Default Child Omission Runtime",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-child-omission-runtime", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{}}},
		Handlers:       pluginsdk.Handlers{Event: handler},
	}
	if err := runtime.RegisterPlugin(plugin); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	event := eventmodel.Event{
		EventID:        "evt-instance-config-beyond-supported-deeper-nested-required-enum-default-child-omission-runtime",
		TraceID:        "trace-instance-config-beyond-supported-deeper-nested-required-enum-default-child-omission-runtime",
		Source:         "scheduler",
		Type:           "schedule.triggered",
		Timestamp:      time.Date(2026, 4, 14, 10, 20, 6, 0, time.UTC),
		IdempotencyKey: "schedule:instance-config-beyond-supported-deeper-nested-required-enum-default-child-omission-runtime",
	}
	if err := runtime.DispatchEvent(context.Background(), event); err != nil {
		t.Fatalf("expected runtime dispatch to synthesize beyond-supported deeper nested child omission branch without rejection, got %v", err)
	}
	if handler.called {
		t.Fatal("expected subprocess host dispatch to avoid in-process handler execution")
	}
	stdout := strings.Join(host.StdoutLines(), "\n")
	for _, expected := range []string{"handshake-ready", "event-ok"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("expected subprocess stdout to include %q, got %s", expected, stdout)
		}
	}
	if strings.Contains(buffer.String(), "subprocess host instance config rejected") {
		t.Fatalf("expected beyond-supported deeper nested child omission runtime path to avoid instance config rejection logs, got %s", buffer.String())
	}
	if !strings.Contains(buffer.String(), "subprocess host dispatch completed") || !strings.Contains(buffer.String(), "runtime dispatch completed") {
		t.Fatalf("expected runtime+host logs for beyond-supported deeper nested child omission dispatch, got %s", buffer.String())
	}
	rendered := tracer.RenderTrace("trace-instance-config-beyond-supported-deeper-nested-required-enum-default-child-omission-runtime")
	if !strings.Contains(rendered, "runtime.dispatch") || !strings.Contains(rendered, "plugin_host.dispatch") {
		t.Fatalf("expected runtime and subprocess host dispatch spans, got %s", rendered)
	}
	if strings.Contains(metrics.RenderPrometheus(), `bot_platform_plugin_errors_total{plugin_id="plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-child-omission-runtime"}`) {
		t.Fatalf("expected beyond-supported deeper nested child omission runtime path to avoid plugin error metrics, got %s", metrics.RenderPrometheus())
	}
}

func TestRuntimeDispatchMergesBeyondSupportedDeeperNestedDefaultIntoExistingNamingBranch(t *testing.T) {
	t.Parallel()

	buffer := &bytes.Buffer{}
	logger := NewLogger(buffer)
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-default-merged"))
	host.SetObservability(logger, tracer, metrics)
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	handler := &recordingEventHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, host)
	runtime.SetObservability(logger, tracer, metrics)

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-default-runtime",
			Name:       "Instance Config Beyond Supported Deeper Nested Default Runtime",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-default-runtime", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{}}}},
		Handlers:       pluginsdk.Handlers{Event: handler},
	}
	if err := runtime.RegisterPlugin(plugin); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	event := eventmodel.Event{
		EventID:        "evt-instance-config-beyond-supported-deeper-nested-default-runtime",
		TraceID:        "trace-instance-config-beyond-supported-deeper-nested-default-runtime",
		Source:         "scheduler",
		Type:           "schedule.triggered",
		Timestamp:      time.Date(2026, 4, 14, 10, 18, 0, 0, time.UTC),
		IdempotencyKey: "schedule:instance-config-beyond-supported-deeper-nested-default-runtime",
	}
	if err := runtime.DispatchEvent(context.Background(), event); err != nil {
		t.Fatalf("expected runtime dispatch to merge beyond-supported deeper nested default on existing naming branch, got %v", err)
	}
	if handler.called {
		t.Fatal("expected subprocess host dispatch to avoid in-process handler execution")
	}
	stdout := strings.Join(host.StdoutLines(), "\n")
	for _, expected := range []string{"handshake-ready", "event-ok"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("expected subprocess stdout to include %q, got %s", expected, stdout)
		}
	}
	if strings.Contains(buffer.String(), "subprocess host instance config rejected") {
		t.Fatalf("expected beyond-supported deeper nested default merge runtime path to avoid instance config rejection logs, got %s", buffer.String())
	}
	if !strings.Contains(buffer.String(), "subprocess host dispatch completed") || !strings.Contains(buffer.String(), "runtime dispatch completed") {
		t.Fatalf("expected runtime+host logs for beyond-supported deeper nested default merge dispatch, got %s", buffer.String())
	}
	rendered := tracer.RenderTrace("trace-instance-config-beyond-supported-deeper-nested-default-runtime")
	if !strings.Contains(rendered, "runtime.dispatch") || !strings.Contains(rendered, "plugin_host.dispatch") {
		t.Fatalf("expected runtime and subprocess host dispatch spans, got %s", rendered)
	}
	if strings.Contains(metrics.RenderPrometheus(), `bot_platform_plugin_errors_total{plugin_id="plugin-instance-config-beyond-supported-deeper-nested-default-runtime"}`) {
		t.Fatalf("expected beyond-supported deeper nested default merge runtime path to avoid plugin error metrics, got %s", metrics.RenderPrometheus())
	}
}

func TestRuntimeDispatchPassesBeyondSupportedDeeperNestedRequiredEnumDefaultParentOmissionBoundaryIntoSubprocessHostRequest(t *testing.T) {
	t.Parallel()

	buffer := &bytes.Buffer{}
	logger := NewLogger(buffer)
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-required-enum-default-parent-omission-not-enforced"))
	host.SetObservability(logger, tracer, metrics)
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	handler := &recordingEventHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, host)
	runtime.SetObservability(logger, tracer, metrics)

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-parent-omission-runtime",
			Name:       "Instance Config Beyond Supported Deeper Nested Required Enum Default Parent Omission Runtime",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-parent-omission-runtime", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{}},
		Handlers:       pluginsdk.Handlers{Event: handler},
	}
	if err := runtime.RegisterPlugin(plugin); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	event := eventmodel.Event{
		EventID:        "evt-instance-config-beyond-supported-deeper-nested-required-enum-default-parent-omission-runtime",
		TraceID:        "trace-instance-config-beyond-supported-deeper-nested-required-enum-default-parent-omission-runtime",
		Source:         "scheduler",
		Type:           "schedule.triggered",
		Timestamp:      time.Date(2026, 4, 14, 10, 22, 0, 0, time.UTC),
		IdempotencyKey: "schedule:instance-config-beyond-supported-deeper-nested-required-enum-default-parent-omission-runtime",
	}
	if err := runtime.DispatchEvent(context.Background(), event); err != nil {
		t.Fatalf("expected runtime dispatch to preserve beyond-supported deeper nested required+enum+default parent omission boundary without rejection, got %v", err)
	}
	if handler.called {
		t.Fatal("expected subprocess host dispatch to avoid in-process handler execution")
	}
	stdout := strings.Join(host.StdoutLines(), "\n")
	for _, expected := range []string{"handshake-ready", "event-ok"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("expected subprocess stdout to include %q, got %s", expected, stdout)
		}
	}
	if strings.Contains(buffer.String(), "subprocess host instance config rejected") {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default parent omission runtime path to avoid instance config rejection logs, got %s", buffer.String())
	}
	if !strings.Contains(buffer.String(), "subprocess host dispatch completed") || !strings.Contains(buffer.String(), "runtime dispatch completed") {
		t.Fatalf("expected runtime+host logs for beyond-supported deeper nested required+enum+default parent omission dispatch, got %s", buffer.String())
	}
	rendered := tracer.RenderTrace("trace-instance-config-beyond-supported-deeper-nested-required-enum-default-parent-omission-runtime")
	if !strings.Contains(rendered, "runtime.dispatch") || !strings.Contains(rendered, "plugin_host.dispatch") {
		t.Fatalf("expected runtime and subprocess host dispatch spans, got %s", rendered)
	}
	if strings.Contains(metrics.RenderPrometheus(), `bot_platform_plugin_errors_total{plugin_id="plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-parent-omission-runtime"}`) {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default parent omission runtime path to avoid plugin error metrics, got %s", metrics.RenderPrometheus())
	}
}

func TestRuntimeDispatchPassesBeyondSupportedDeeperNestedRequiredEnumDefaultRootOmissionBoundaryIntoSubprocessHostRequest(t *testing.T) {
	t.Parallel()

	buffer := &bytes.Buffer{}
	logger := NewLogger(buffer)
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-not-enforced"))
	host.SetObservability(logger, tracer, metrics)
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	handler := &recordingEventHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, host)
	runtime.SetObservability(logger, tracer, metrics)

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-runtime",
			Name:       "Instance Config Beyond Supported Deeper Nested Required Enum Default Root Omission Runtime",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-runtime", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{},
		Handlers:       pluginsdk.Handlers{Event: handler},
	}
	if err := runtime.RegisterPlugin(plugin); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	event := eventmodel.Event{
		EventID:        "evt-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-runtime",
		TraceID:        "trace-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-runtime",
		Source:         "scheduler",
		Type:           "schedule.triggered",
		Timestamp:      time.Date(2026, 4, 14, 10, 22, 30, 0, time.UTC),
		IdempotencyKey: "schedule:instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-runtime",
	}
	if err := runtime.DispatchEvent(context.Background(), event); err != nil {
		t.Fatalf("expected runtime dispatch to preserve beyond-supported deeper nested required+enum+default root omission boundary without rejection, got %v", err)
	}
	if handler.called {
		t.Fatal("expected subprocess host dispatch to avoid in-process handler execution")
	}
	stdout := strings.Join(host.StdoutLines(), "\n")
	for _, expected := range []string{"handshake-ready", "event-ok"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("expected subprocess stdout to include %q, got %s", expected, stdout)
		}
	}
	if strings.Contains(buffer.String(), "subprocess host instance config rejected") {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default root omission runtime path to avoid instance config rejection logs, got %s", buffer.String())
	}
	if !strings.Contains(buffer.String(), "subprocess host dispatch completed") || !strings.Contains(buffer.String(), "runtime dispatch completed") {
		t.Fatalf("expected runtime+host logs for beyond-supported deeper nested required+enum+default root omission dispatch, got %s", buffer.String())
	}
	rendered := tracer.RenderTrace("trace-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-runtime")
	if !strings.Contains(rendered, "runtime.dispatch") || !strings.Contains(rendered, "plugin_host.dispatch") {
		t.Fatalf("expected runtime and subprocess host dispatch spans, got %s", rendered)
	}
	if strings.Contains(metrics.RenderPrometheus(), `bot_platform_plugin_errors_total{plugin_id="plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-runtime"}`) {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default root omission runtime path to avoid plugin error metrics, got %s", metrics.RenderPrometheus())
	}
}

func TestRuntimeDispatchTreatsNilAndEmptyInstanceConfigAsEquivalentBeyondSupportedDeeperNestedRequiredEnumDefaultRootOmissionBoundary(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name           string
		instanceConfig map[string]any
	}{
		{name: "empty_map", instanceConfig: map[string]any{}},
		{name: "nil_map", instanceConfig: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buffer := &bytes.Buffer{}
			logger := NewLogger(buffer)
			tracer := NewTraceRecorder()
			metrics := NewMetricsRegistry()
			host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-not-enforced"))
			host.SetObservability(logger, tracer, metrics)
			t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
			handler := &recordingEventHandler{}
			runtime := NewInMemoryRuntime(NoopSupervisor{}, host)
			runtime.SetObservability(logger, tracer, metrics)

			plugin := pluginsdk.Plugin{
				Manifest: pluginsdk.PluginManifest{
					ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-equivalence-runtime",
					Name:       "Instance Config Beyond Supported Deeper Nested Required Enum Default Root Omission Equivalence Runtime",
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
					Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-equivalence-runtime", Symbol: "Plugin"},
				},
				InstanceConfig: tc.instanceConfig,
				Handlers:       pluginsdk.Handlers{Event: handler},
			}
			if err := runtime.RegisterPlugin(plugin); err != nil {
				t.Fatalf("register plugin: %v", err)
			}

			event := eventmodel.Event{
				EventID:        "evt-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-" + tc.name,
				TraceID:        "trace-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-" + tc.name,
				Source:         "scheduler",
				Type:           "schedule.triggered",
				Timestamp:      time.Date(2026, 4, 15, 6, 0, 0, 0, time.UTC),
				IdempotencyKey: "schedule:instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-" + tc.name,
			}
			if err := runtime.DispatchEvent(context.Background(), event); err != nil {
				t.Fatalf("expected runtime dispatch to preserve equivalent root omission boundary without rejection, got %v", err)
			}
			if handler.called {
				t.Fatal("expected subprocess host dispatch to avoid in-process handler execution")
			}
			stdout := strings.Join(host.StdoutLines(), "\n")
			for _, expected := range []string{"handshake-ready", "event-ok"} {
				if !strings.Contains(stdout, expected) {
					t.Fatalf("expected subprocess stdout to include %q, got %s", expected, stdout)
				}
			}
			if strings.Contains(buffer.String(), "subprocess host instance config rejected") {
				t.Fatalf("expected equivalent root omission runtime path to avoid instance config rejection logs, got %s", buffer.String())
			}
			if !strings.Contains(buffer.String(), "subprocess host dispatch completed") || !strings.Contains(buffer.String(), "runtime dispatch completed") {
				t.Fatalf("expected runtime+host logs for equivalent root omission dispatch, got %s", buffer.String())
			}
			rendered := tracer.RenderTrace(event.TraceID)
			if !strings.Contains(rendered, "runtime.dispatch") || !strings.Contains(rendered, "plugin_host.dispatch") {
				t.Fatalf("expected runtime and subprocess host dispatch spans, got %s", rendered)
			}
			if strings.Contains(metrics.RenderPrometheus(), `bot_platform_plugin_errors_total{plugin_id="plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-root-omission-equivalence-runtime"}`) {
				t.Fatalf("expected equivalent root omission runtime path to avoid plugin error metrics, got %s", metrics.RenderPrometheus())
			}
		})
	}
}


func TestRuntimeDispatchRejectsNestedInstanceConfigValueTypeMismatchBeforePluginDelivery(t *testing.T) {
	t.Parallel()

	buffer := &bytes.Buffer{}
	logger := NewLogger(buffer)
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHost(testPluginProcessFactory(t, "assert-instance-config"))
	host.SetObservability(logger, tracer, metrics)
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	handler := &recordingEventHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, host)
	runtime.SetObservability(logger, tracer, metrics)

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-nested-runtime",
			Name:       "Instance Config Nested Runtime",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-nested-runtime", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"prefix": true}},
		Handlers:       pluginsdk.Handlers{Event: handler},
	}
	if err := runtime.RegisterPlugin(plugin); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	event := eventmodel.Event{
		EventID:        "evt-instance-config-nested-runtime",
		TraceID:        "trace-instance-config-nested-runtime",
		Source:         "scheduler",
		Type:           "schedule.triggered",
		Timestamp:      time.Date(2026, 4, 14, 10, 5, 0, 0, time.UTC),
		IdempotencyKey: "schedule:instance-config-nested-runtime",
	}
	err := runtime.DispatchEvent(context.Background(), event)
	if err == nil || !strings.Contains(err.Error(), `dispatch completed with no successful plugin deliveries`) {
		t.Fatalf("expected runtime to stop after nested instance config rejection, got %v", err)
	}
	if handler.called {
		t.Fatal("expected nested instance config rejection to stop before in-process handler execution")
	}
	if len(runtime.DispatchResults()) != 1 || runtime.DispatchResults()[0].Success {
		t.Fatalf("expected failed dispatch result after nested instance config rejection, got %+v", runtime.DispatchResults())
	}
	if !strings.Contains(runtime.DispatchResults()[0].Error, `nested instance config property "settings.prefix" value type must match declared type "string", got "boolean"`) {
		t.Fatalf("expected dispatch result to capture nested instance config rejection, got %+v", runtime.DispatchResults())
	}
	if len(host.StdoutLines()) != 0 || len(host.StderrLines()) != 0 {
		t.Fatalf("expected runtime nested instance config rejection to avoid subprocess side effects, stdout=%v stderr=%v", host.StdoutLines(), host.StderrLines())
	}
	for _, expected := range []string{"subprocess host instance config rejected", `"failure_stage":"instance_config"`, `"failure_reason":"instance_config_value_type_mismatch"`, `"compatibility_rule":"instance_config_nested_value_type"`, `"property_name":"settings.prefix"`, "runtime dispatch failed"} {
		if !strings.Contains(buffer.String(), expected) {
			t.Fatalf("expected runtime observability to include %q, got %s", expected, buffer.String())
		}
	}
	rendered := tracer.RenderTrace("trace-instance-config-nested-runtime")
	if !strings.Contains(rendered, "runtime.dispatch") || !strings.Contains(rendered, "plugin_host.instance_config") {
		t.Fatalf("expected runtime and nested instance config spans, got %s", rendered)
	}
	if strings.Contains(rendered, "plugin_host.dispatch") {
		t.Fatalf("expected nested instance config rejection to stop before plugin_host.dispatch, got %s", rendered)
	}
	if !strings.Contains(metrics.RenderPrometheus(), `bot_platform_plugin_errors_total{plugin_id="plugin-instance-config-nested-runtime"} 2`) {
		t.Fatalf("expected plugin error metric to reflect host rejection plus runtime dispatch failure accounting, got %s", metrics.RenderPrometheus())
	}
}

func TestRuntimeDispatchRejectsDeeperNestedInstanceConfigValueTypeMismatchBeforePluginDelivery(t *testing.T) {
	t.Parallel()

	buffer := &bytes.Buffer{}
	logger := NewLogger(buffer)
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHost(testPluginProcessFactory(t, "assert-instance-config"))
	host.SetObservability(logger, tracer, metrics)
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	handler := &recordingEventHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, host)
	runtime.SetObservability(logger, tracer, metrics)

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-deeper-nested-runtime",
			Name:       "Instance Config Deeper Nested Runtime",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-deeper-nested-runtime", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"prefix": true}}},
		Handlers:       pluginsdk.Handlers{Event: handler},
	}
	if err := runtime.RegisterPlugin(plugin); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	event := eventmodel.Event{
		EventID:        "evt-instance-config-deeper-nested-runtime",
		TraceID:        "trace-instance-config-deeper-nested-runtime",
		Source:         "scheduler",
		Type:           "schedule.triggered",
		Timestamp:      time.Date(2026, 4, 14, 10, 10, 0, 0, time.UTC),
		IdempotencyKey: "schedule:instance-config-deeper-nested-runtime",
	}
	err := runtime.DispatchEvent(context.Background(), event)
	if err == nil || !strings.Contains(err.Error(), `dispatch completed with no successful plugin deliveries`) {
		t.Fatalf("expected runtime to stop after deeper nested instance config rejection, got %v", err)
	}
	if handler.called {
		t.Fatal("expected deeper nested instance config rejection to stop before in-process handler execution")
	}
	if len(runtime.DispatchResults()) != 1 || runtime.DispatchResults()[0].Success {
		t.Fatalf("expected failed dispatch result after deeper nested instance config rejection, got %+v", runtime.DispatchResults())
	}
	if !strings.Contains(runtime.DispatchResults()[0].Error, `nested instance config property "settings.labels.prefix" value type must match declared type "string", got "boolean"`) {
		t.Fatalf("expected dispatch result to capture deeper nested instance config rejection, got %+v", runtime.DispatchResults())
	}
	if len(host.StdoutLines()) != 0 || len(host.StderrLines()) != 0 {
		t.Fatalf("expected runtime deeper nested instance config rejection to avoid subprocess side effects, stdout=%v stderr=%v", host.StdoutLines(), host.StderrLines())
	}
	for _, expected := range []string{"subprocess host instance config rejected", `"failure_stage":"instance_config"`, `"failure_reason":"instance_config_value_type_mismatch"`, `"compatibility_rule":"instance_config_deeper_nested_value_type"`, `"property_name":"settings.labels.prefix"`, `"parent_property_name":"settings.labels"`, `"root_property_name":"settings"`, `"intermediate_property_name":"labels"`, "runtime dispatch failed"} {
		if !strings.Contains(buffer.String(), expected) {
			t.Fatalf("expected runtime observability to include %q, got %s", expected, buffer.String())
		}
	}
	rendered := tracer.RenderTrace("trace-instance-config-deeper-nested-runtime")
	if !strings.Contains(rendered, "runtime.dispatch") || !strings.Contains(rendered, "plugin_host.instance_config") {
		t.Fatalf("expected runtime and deeper nested instance config spans, got %s", rendered)
	}
	if strings.Contains(rendered, "plugin_host.dispatch") {
		t.Fatalf("expected deeper nested instance config rejection to stop before plugin_host.dispatch, got %s", rendered)
	}
	if !strings.Contains(metrics.RenderPrometheus(), `bot_platform_plugin_errors_total{plugin_id="plugin-instance-config-deeper-nested-runtime"} 2`) {
		t.Fatalf("expected plugin error metric to reflect host rejection plus runtime dispatch failure accounting, got %s", metrics.RenderPrometheus())
	}
}

func TestRuntimeDispatchRejectsDeeperNestedInstanceConfigValueOutsideManifestEnumBeforePluginDelivery(t *testing.T) {
	t.Parallel()

	buffer := &bytes.Buffer{}
	logger := NewLogger(buffer)
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-deeper-nested-enum-not-enforced"))
	host.SetObservability(logger, tracer, metrics)
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	handler := &recordingEventHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, host)
	runtime.SetObservability(logger, tracer, metrics)

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-deeper-nested-enum-runtime",
			Name:       "Instance Config Deeper Nested Enum Runtime",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-deeper-nested-enum-runtime", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"prefix": "oops"}}},
		Handlers:       pluginsdk.Handlers{Event: handler},
	}
	if err := runtime.RegisterPlugin(plugin); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	event := eventmodel.Event{
		EventID:        "evt-instance-config-deeper-nested-enum-runtime",
		TraceID:        "trace-instance-config-deeper-nested-enum-runtime",
		Source:         "scheduler",
		Type:           "schedule.triggered",
		Timestamp:      time.Date(2026, 4, 14, 10, 12, 0, 0, time.UTC),
		IdempotencyKey: "schedule:instance-config-deeper-nested-enum-runtime",
	}
	err := runtime.DispatchEvent(context.Background(), event)
	if err == nil || !strings.Contains(err.Error(), `dispatch completed with no successful plugin deliveries`) {
		t.Fatalf("expected runtime to stop after deeper nested enum instance config rejection, got %v", err)
	}
	if handler.called {
		t.Fatal("expected deeper nested enum instance config rejection to stop before in-process handler execution")
	}
	if len(runtime.DispatchResults()) != 1 || runtime.DispatchResults()[0].Success {
		t.Fatalf("expected failed dispatch result after deeper nested enum instance config rejection, got %+v", runtime.DispatchResults())
	}
	if !strings.Contains(runtime.DispatchResults()[0].Error, `nested instance config property "settings.labels.prefix" value "oops" must be declared in enum ["hello","world"] for declared type "string"`) {
		t.Fatalf("expected dispatch result to capture deeper nested enum instance config rejection, got %+v", runtime.DispatchResults())
	}
	if len(host.StdoutLines()) != 0 || len(host.StderrLines()) != 0 {
		t.Fatalf("expected runtime deeper nested enum instance config rejection to avoid subprocess side effects, stdout=%v stderr=%v", host.StdoutLines(), host.StderrLines())
	}
	for _, expected := range []string{"subprocess host instance config rejected", `"failure_stage":"instance_config"`, `"failure_reason":"instance_config_enum_value_out_of_set"`, `"compatibility_rule":"instance_config_deeper_nested_enum"`, `"property_name":"settings.labels.prefix"`, `"parent_property_name":"settings.labels"`, `"root_property_name":"settings"`, `"intermediate_property_name":"labels"`, `"actual_value":"oops"`, `"enum_values":["hello","world"]`, "runtime dispatch failed"} {
		if !strings.Contains(buffer.String(), expected) {
			t.Fatalf("expected runtime observability to include %q, got %s", expected, buffer.String())
		}
	}
	rendered := tracer.RenderTrace("trace-instance-config-deeper-nested-enum-runtime")
	if !strings.Contains(rendered, "runtime.dispatch") || !strings.Contains(rendered, "plugin_host.instance_config") {
		t.Fatalf("expected runtime and deeper nested enum instance config spans, got %s", rendered)
	}
	if strings.Contains(rendered, "plugin_host.dispatch") {
		t.Fatalf("expected deeper nested enum instance config rejection to stop before plugin_host.dispatch, got %s", rendered)
	}
	spans := tracer.SpansByTrace("trace-instance-config-deeper-nested-enum-runtime")
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
			t.Fatalf("expected deeper nested enum runtime trace metadata, got %+v", span.Metadata)
		}
		matched = true
	}
	if !matched {
		t.Fatalf("expected deeper nested enum runtime trace metadata, got %+v", spans)
	}
	if !strings.Contains(metrics.RenderPrometheus(), `bot_platform_plugin_errors_total{plugin_id="plugin-instance-config-deeper-nested-enum-runtime"} 2`) {
		t.Fatalf("expected plugin error metric to reflect host rejection plus runtime dispatch failure accounting, got %s", metrics.RenderPrometheus())
	}
}

func TestRuntimeDispatchRejectsDeeperNestedArrayValueTypeMismatchBeforePluginDelivery(t *testing.T) {
	t.Parallel()

	buffer := &bytes.Buffer{}
	logger := NewLogger(buffer)
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHost(testPluginProcessFactory(t, "assert-instance-config"))
	host.SetObservability(logger, tracer, metrics)
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	handler := &recordingEventHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, host)
	runtime.SetObservability(logger, tracer, metrics)

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-deeper-nested-array-runtime",
			Name:       "Instance Config Deeper Nested Array Runtime",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-deeper-nested-array-runtime", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"prefix": []any{"oops"}}}},
		Handlers:       pluginsdk.Handlers{Event: handler},
	}
	if err := runtime.RegisterPlugin(plugin); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	event := eventmodel.Event{
		EventID:        "evt-instance-config-deeper-nested-array-runtime",
		TraceID:        "trace-instance-config-deeper-nested-array-runtime",
		Source:         "scheduler",
		Type:           "schedule.triggered",
		Timestamp:      time.Date(2026, 4, 14, 10, 15, 0, 0, time.UTC),
		IdempotencyKey: "schedule:instance-config-deeper-nested-array-runtime",
	}
	err := runtime.DispatchEvent(context.Background(), event)
	if err == nil || !strings.Contains(err.Error(), `dispatch completed with no successful plugin deliveries`) {
		t.Fatalf("expected runtime to stop after deeper nested array instance config rejection, got %v", err)
	}
	if handler.called {
		t.Fatal("expected deeper nested array instance config rejection to stop before in-process handler execution")
	}
	if len(runtime.DispatchResults()) != 1 || runtime.DispatchResults()[0].Success {
		t.Fatalf("expected failed dispatch result after deeper nested array instance config rejection, got %+v", runtime.DispatchResults())
	}
	if !strings.Contains(runtime.DispatchResults()[0].Error, `nested instance config property "settings.labels.prefix" value type must match declared type "string", got "array"`) {
		t.Fatalf("expected dispatch result to capture deeper nested array instance config rejection, got %+v", runtime.DispatchResults())
	}
	if len(host.StdoutLines()) != 0 || len(host.StderrLines()) != 0 {
		t.Fatalf("expected runtime deeper nested array instance config rejection to avoid subprocess side effects, stdout=%v stderr=%v", host.StdoutLines(), host.StderrLines())
	}
	for _, expected := range []string{"subprocess host instance config rejected", `"failure_stage":"instance_config"`, `"failure_reason":"instance_config_value_type_mismatch"`, `"compatibility_rule":"instance_config_deeper_nested_value_type"`, `"property_name":"settings.labels.prefix"`, `"parent_property_name":"settings.labels"`, `"root_property_name":"settings"`, `"intermediate_property_name":"labels"`, `"actual_type":"array"`, "runtime dispatch failed"} {
		if !strings.Contains(buffer.String(), expected) {
			t.Fatalf("expected runtime observability to include %q, got %s", expected, buffer.String())
		}
	}
	rendered := tracer.RenderTrace("trace-instance-config-deeper-nested-array-runtime")
	if !strings.Contains(rendered, "runtime.dispatch") || !strings.Contains(rendered, "plugin_host.instance_config") {
		t.Fatalf("expected runtime and deeper nested array instance config spans, got %s", rendered)
	}
	if strings.Contains(rendered, "plugin_host.dispatch") {
		t.Fatalf("expected deeper nested array instance config rejection to stop before plugin_host.dispatch, got %s", rendered)
	}
	if !strings.Contains(metrics.RenderPrometheus(), `bot_platform_plugin_errors_total{plugin_id="plugin-instance-config-deeper-nested-array-runtime"} 2`) {
		t.Fatalf("expected plugin error metric to reflect host rejection plus runtime dispatch failure accounting, got %s", metrics.RenderPrometheus())
	}
}

func TestRuntimeDispatchRejectsDeeperNestedObjectValueTypeMismatchBeforePluginDelivery(t *testing.T) {
	t.Parallel()

	buffer := &bytes.Buffer{}
	logger := NewLogger(buffer)
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHost(testPluginProcessFactory(t, "assert-instance-config"))
	host.SetObservability(logger, tracer, metrics)
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	handler := &recordingEventHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, host)
	runtime.SetObservability(logger, tracer, metrics)

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-deeper-nested-object-runtime",
			Name:       "Instance Config Deeper Nested Object Runtime",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-deeper-nested-object-runtime", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"prefix": map[string]any{"bad": true}}}},
		Handlers:       pluginsdk.Handlers{Event: handler},
	}
	if err := runtime.RegisterPlugin(plugin); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	event := eventmodel.Event{
		EventID:        "evt-instance-config-deeper-nested-object-runtime",
		TraceID:        "trace-instance-config-deeper-nested-object-runtime",
		Source:         "scheduler",
		Type:           "schedule.triggered",
		Timestamp:      time.Date(2026, 4, 14, 10, 20, 0, 0, time.UTC),
		IdempotencyKey: "schedule:instance-config-deeper-nested-object-runtime",
	}
	err := runtime.DispatchEvent(context.Background(), event)
	if err == nil || !strings.Contains(err.Error(), `dispatch completed with no successful plugin deliveries`) {
		t.Fatalf("expected runtime to stop after deeper nested object instance config rejection, got %v", err)
	}
	if handler.called {
		t.Fatal("expected deeper nested object instance config rejection to stop before in-process handler execution")
	}
	if len(runtime.DispatchResults()) != 1 || runtime.DispatchResults()[0].Success {
		t.Fatalf("expected failed dispatch result after deeper nested object instance config rejection, got %+v", runtime.DispatchResults())
	}
	if !strings.Contains(runtime.DispatchResults()[0].Error, `nested instance config property "settings.labels.prefix" value type must match declared type "string", got "object"`) {
		t.Fatalf("expected dispatch result to capture deeper nested object instance config rejection, got %+v", runtime.DispatchResults())
	}
	if len(host.StdoutLines()) != 0 || len(host.StderrLines()) != 0 {
		t.Fatalf("expected runtime deeper nested object instance config rejection to avoid subprocess side effects, stdout=%v stderr=%v", host.StdoutLines(), host.StderrLines())
	}
	for _, expected := range []string{"subprocess host instance config rejected", `"failure_stage":"instance_config"`, `"failure_reason":"instance_config_value_type_mismatch"`, `"compatibility_rule":"instance_config_deeper_nested_value_type"`, `"property_name":"settings.labels.prefix"`, `"parent_property_name":"settings.labels"`, `"root_property_name":"settings"`, `"intermediate_property_name":"labels"`, `"actual_type":"object"`, "runtime dispatch failed"} {
		if !strings.Contains(buffer.String(), expected) {
			t.Fatalf("expected runtime observability to include %q, got %s", expected, buffer.String())
		}
	}
	rendered := tracer.RenderTrace("trace-instance-config-deeper-nested-object-runtime")
	if !strings.Contains(rendered, "runtime.dispatch") || !strings.Contains(rendered, "plugin_host.instance_config") {
		t.Fatalf("expected runtime and deeper nested object instance config spans, got %s", rendered)
	}
	if strings.Contains(rendered, "plugin_host.dispatch") {
		t.Fatalf("expected deeper nested object instance config rejection to stop before plugin_host.dispatch, got %s", rendered)
	}
	if !strings.Contains(metrics.RenderPrometheus(), `bot_platform_plugin_errors_total{plugin_id="plugin-instance-config-deeper-nested-object-runtime"} 2`) {
		t.Fatalf("expected plugin error metric to reflect host rejection plus runtime dispatch failure accounting, got %s", metrics.RenderPrometheus())
	}
}

func TestRuntimeDispatchRejectsDeeperNestedMissingRequiredInstanceConfigBeforePluginDelivery(t *testing.T) {
	t.Parallel()

	buffer := &bytes.Buffer{}
	logger := NewLogger(buffer)
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-deeper-nested-required-not-enforced"))
	host.SetObservability(logger, tracer, metrics)
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	handler := &recordingEventHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, host)
	runtime.SetObservability(logger, tracer, metrics)

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-deeper-nested-required-runtime",
			Name:       "Instance Config Deeper Nested Required Runtime",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-deeper-nested-required-runtime", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{}}},
		Handlers:       pluginsdk.Handlers{Event: handler},
	}
	if err := runtime.RegisterPlugin(plugin); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	event := eventmodel.Event{
		EventID:        "evt-instance-config-deeper-nested-required-runtime",
		TraceID:        "trace-instance-config-deeper-nested-required-runtime",
		Source:         "scheduler",
		Type:           "schedule.triggered",
		Timestamp:      time.Date(2026, 4, 14, 10, 25, 0, 0, time.UTC),
		IdempotencyKey: "schedule:instance-config-deeper-nested-required-runtime",
	}
	err := runtime.DispatchEvent(context.Background(), event)
	if err == nil || !strings.Contains(err.Error(), `dispatch completed with no successful plugin deliveries`) {
		t.Fatalf("expected runtime to stop after deeper nested missing required instance config rejection, got %v", err)
	}
	if handler.called {
		t.Fatal("expected deeper nested missing required instance config rejection to stop before in-process handler execution")
	}
	if len(runtime.DispatchResults()) != 1 || runtime.DispatchResults()[0].Success {
		t.Fatalf("expected failed dispatch result after deeper nested missing required instance config rejection, got %+v", runtime.DispatchResults())
	}
	if !strings.Contains(runtime.DispatchResults()[0].Error, `nested instance config required property "settings.labels.prefix" must be provided`) {
		t.Fatalf("expected dispatch result to capture deeper nested missing required instance config rejection, got %+v", runtime.DispatchResults())
	}
	if len(host.StdoutLines()) != 0 || len(host.StderrLines()) != 0 {
		t.Fatalf("expected runtime deeper nested missing required instance config rejection to avoid subprocess side effects, stdout=%v stderr=%v", host.StdoutLines(), host.StderrLines())
	}
	for _, expected := range []string{"subprocess host instance config rejected", `"failure_stage":"instance_config"`, `"failure_reason":"instance_config_missing_required_value"`, `"compatibility_rule":"instance_config_deeper_nested_required"`, `"property_name":"settings.labels.prefix"`, `"parent_property_name":"settings.labels"`, `"nested_property_name":"prefix"`, `"root_property_name":"settings"`, `"intermediate_property_name":"labels"`, `"declared_type":"string"`, "runtime dispatch failed"} {
		if !strings.Contains(buffer.String(), expected) {
			t.Fatalf("expected runtime observability to include %q, got %s", expected, buffer.String())
		}
	}
	rendered := tracer.RenderTrace("trace-instance-config-deeper-nested-required-runtime")
	if !strings.Contains(rendered, "runtime.dispatch") || !strings.Contains(rendered, "plugin_host.instance_config") {
		t.Fatalf("expected runtime and deeper nested missing required instance config spans, got %s", rendered)
	}
	if strings.Contains(rendered, "plugin_host.dispatch") {
		t.Fatalf("expected deeper nested missing required instance config rejection to stop before plugin_host.dispatch, got %s", rendered)
	}
	if !strings.Contains(metrics.RenderPrometheus(), `bot_platform_plugin_errors_total{plugin_id="plugin-instance-config-deeper-nested-required-runtime"} 2`) {
		t.Fatalf("expected plugin error metric to reflect host rejection plus runtime dispatch failure accounting, got %s", metrics.RenderPrometheus())
	}
}

func TestRuntimeDispatchFailureRecordsPluginErrorMetric(t *testing.T) {
	t.Parallel()

	buffer := &bytes.Buffer{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	runtime.SetObservability(NewLogger(buffer), NewTraceRecorder(), NewMetricsRegistry())
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-fail", Name: "Fail", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Entry: pluginsdk.PluginEntry{Module: "plugins/fail", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Event: failingEventHandler{}},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	err := runtime.DispatchEvent(context.Background(), eventmodel.Event{EventID: "evt-fail", TraceID: "trace-fail", Source: "onebot", Type: "message.received", Timestamp: time.Date(2026, 4, 3, 20, 1, 0, 0, time.UTC), IdempotencyKey: "onebot:fail"})
	if err == nil {
		t.Fatal("expected dispatch failure")
	}
	if !strings.Contains(runtime.metrics.RenderPrometheus(), "bot_platform_plugin_errors_total{plugin_id=\"plugin-fail\"} 1") {
		t.Fatalf("expected plugin error metric, got %s", runtime.metrics.RenderPrometheus())
	}
}

func TestRuntimeDispatchEventStopsBeforePluginWhenEventAuthorizerDenies(t *testing.T) {
	t.Parallel()

	buffer := &bytes.Buffer{}
	handler := &recordingEventHandler{}
	authorizer := &recordingEventAuthorizer{err: errors.New("event authorization denied")}
	audits := NewInMemoryAuditLog()
	supervisor := &recordingSupervisor{}
	runtime := NewInMemoryRuntime(supervisor, DirectPluginHost{})
	runtime.SetAuditRecorder(audits)
	runtime.SetObservability(NewLogger(buffer), NewTraceRecorder(), NewMetricsRegistry())
	runtime.SetEventAuthorizer(authorizer)
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-echo", Name: "Echo Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Permissions: []string{"message:read"}, Entry: pluginsdk.PluginEntry{Module: "plugins/echo", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Event: handler},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	err := runtime.DispatchEvent(context.Background(), eventmodel.Event{EventID: "evt-denied", TraceID: "trace-denied", Source: "onebot", Type: "message.received", Timestamp: time.Date(2026, 4, 5, 20, 0, 0, 0, time.UTC), Actor: &eventmodel.Actor{ID: "viewer-user", Type: "user"}, Metadata: map[string]any{"permission": "message:read"}, IdempotencyKey: "onebot:evt-denied"})
	if err == nil || !strings.Contains(err.Error(), "dispatch completed with no successful plugin deliveries") {
		t.Fatalf("expected event authorization denial to stop event dispatch, got %v", err)
	}
	if !authorizer.called {
		t.Fatal("expected runtime event authorizer to be invoked")
	}
	if handler.called {
		t.Fatal("expected plugin event handler not to be called after authorization denial")
	}
	if len(supervisor.ensured) != 0 {
		t.Fatalf("expected denied event not to reach supervisor, got %+v", supervisor.ensured)
	}
	if len(runtime.DispatchResults()) != 1 || runtime.DispatchResults()[0].Success {
		t.Fatalf("expected denied event dispatch result, got %+v", runtime.DispatchResults())
	}
	entries := audits.AuditEntries()
	if len(entries) != 1 {
		t.Fatalf("expected one denied event audit entry, got %+v", entries)
	}
	if entries[0].Actor != "viewer-user" || entries[0].Action != "message.read" || entries[0].Permission != "message:read" || entries[0].Target != "plugin-echo" || entries[0].Allowed || auditEntryReason(entries[0]) != "permission_denied" {
		t.Fatalf("expected denied event audit entry, got %+v", entries[0])
	}
	logs := decodeRuntimeLogEntries(t, buffer)
	matched := false
	for _, entry := range logs {
		if entry.Message != "runtime dispatch authorization failed" {
			continue
		}
		matched = true
		if entry.Fields["component"] != "runtime" || entry.Fields["operation"] != "dispatch.event.authorize" {
			t.Fatalf("expected runtime authorization log baseline fields, got %+v", entry)
		}
		if entry.Fields["error_category"] != "authorization" || entry.Fields["error_code"] != "permission_denied" {
			t.Fatalf("expected runtime authorization log taxonomy, got %+v", entry)
		}
	}
	if !matched {
		t.Fatalf("expected runtime authorization failure log, got %+v", logs)
	}
}

func TestRuntimeDispatchEventWithConfiguredMetadataAuthorizerAllowsScopedPluginAndSkipsOthers(t *testing.T) {
	t.Parallel()

	allowed := &recordingEventHandler{}
	blocked := &recordingEventHandler{}
	audits := NewInMemoryAuditLog()
	supervisor := &recordingSupervisor{}
	runtime := NewInMemoryRuntime(supervisor, DirectPluginHost{})
	runtime.SetAuditRecorder(audits)
	runtime.SetEventAuthorizer(NewMetadataEventAuthorizer(&RBACConfig{
		ActorRoles: map[string][]string{"reader-user": {"echo-reader"}},
		Policies: map[string]pluginsdk.AuthorizationPolicy{
			"echo-reader": {Permissions: []string{"message:read"}, PluginScope: []string{"plugin-echo"}},
		},
	}))
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-echo", Name: "Echo Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Permissions: []string{"message:read"}, Entry: pluginsdk.PluginEntry{Module: "plugins/echo", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Event: allowed},
	}); err != nil {
		t.Fatalf("register allowed plugin: %v", err)
	}
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-ai-chat", Name: "AI Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Permissions: []string{"message:read"}, Entry: pluginsdk.PluginEntry{Module: "plugins/ai-chat", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Event: blocked},
	}); err != nil {
		t.Fatalf("register blocked plugin: %v", err)
	}
	if err := runtime.DispatchEvent(context.Background(), eventmodel.Event{EventID: "evt-allowed", TraceID: "trace-allowed", Source: "onebot", Type: "message.received", Timestamp: time.Date(2026, 4, 5, 20, 1, 0, 0, time.UTC), Actor: &eventmodel.Actor{ID: "reader-user", Type: "user"}, Metadata: map[string]any{"permission": "message:read"}, IdempotencyKey: "onebot:evt-allowed"}); err != nil {
		t.Fatalf("expected scoped event authorization to allow at least one plugin, got %v", err)
	}
	if !allowed.called {
		t.Fatal("expected scoped event authorization to reach allowed plugin")
	}
	if blocked.called {
		t.Fatal("expected out-of-scope plugin not to receive event")
	}
	if len(supervisor.ensured) != 1 || supervisor.ensured[0] != "plugin-echo" {
		t.Fatalf("expected only allowed plugin to reach supervisor, got %+v", supervisor.ensured)
	}
	results := runtime.DispatchResults()
	if len(results) != 2 {
		t.Fatalf("expected one denied and one allowed dispatch result, got %+v", results)
	}
	entries := audits.AuditEntries()
	if len(entries) != 1 {
		t.Fatalf("expected one scope-denied event audit entry, got %+v", entries)
	}
	if entries[0].Actor != "reader-user" || entries[0].Action != "message.read" || entries[0].Permission != "message:read" || entries[0].Target != "plugin-ai-chat" || entries[0].Allowed || auditEntryReason(entries[0]) != "plugin_scope_denied" {
		t.Fatalf("expected scope-denied event audit entry, got %+v", entries[0])
	}
}

func TestRuntimeDispatchEventWithoutPermissionMetadataDoesNotRecordDenyAudit(t *testing.T) {
	t.Parallel()

	handler := &recordingEventHandler{}
	audits := NewInMemoryAuditLog()
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	runtime.SetAuditRecorder(audits)
	runtime.SetEventAuthorizer(NewMetadataEventAuthorizer(&RBACConfig{
		ActorRoles: map[string][]string{"reader-user": {"echo-reader"}},
		Policies: map[string]pluginsdk.AuthorizationPolicy{
			"echo-reader": {Permissions: []string{"message:read"}, PluginScope: []string{"plugin-echo"}},
		},
	}))
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-echo", Name: "Echo Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Entry: pluginsdk.PluginEntry{Module: "plugins/echo", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Event: handler},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	if err := runtime.DispatchEvent(context.Background(), eventmodel.Event{EventID: "evt-no-permission", TraceID: "trace-no-permission", Source: "onebot", Type: "message.received", Timestamp: time.Date(2026, 4, 5, 20, 1, 30, 0, time.UTC), Metadata: map[string]any{"actor": "reader-user"}, IdempotencyKey: "onebot:evt-no-permission"}); err != nil {
		t.Fatalf("expected event without permission metadata to preserve compatibility, got %v", err)
	}
	if !handler.called {
		t.Fatal("expected event handler to be called when permission metadata is absent")
	}
	if entries := audits.AuditEntries(); len(entries) != 0 {
		t.Fatalf("expected compatibility path not to record deny audit, got %+v", entries)
	}
}

func TestRuntimeDispatchEventRequiresDeclaredManifestPermissionWhenPermissionMetadataPresent(t *testing.T) {
	t.Parallel()

	buffer := &bytes.Buffer{}
	handler := &recordingEventHandler{}
	audits := NewInMemoryAuditLog()
	supervisor := &recordingSupervisor{}
	runtime := NewInMemoryRuntime(supervisor, DirectPluginHost{})
	runtime.SetAuditRecorder(audits)
	runtime.SetObservability(NewLogger(buffer), NewTraceRecorder(), NewMetricsRegistry())
	runtime.SetEventAuthorizer(NewMetadataEventAuthorizer(&RBACConfig{
		ActorRoles: map[string][]string{"reader-user": {"reader"}},
		Policies: map[string]pluginsdk.AuthorizationPolicy{
			"reader": {Permissions: []string{"message:read"}, PluginScope: []string{"*"}},
		},
	}))
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-echo", Name: "Echo Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Entry: pluginsdk.PluginEntry{Module: "plugins/echo", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Event: handler},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	err := runtime.DispatchEvent(context.Background(), eventmodel.Event{EventID: "evt-manifest", TraceID: "trace-manifest", Source: "onebot", Type: "message.received", Timestamp: time.Date(2026, 4, 5, 20, 2, 0, 0, time.UTC), Actor: &eventmodel.Actor{ID: "reader-user", Type: "user"}, Metadata: map[string]any{"permission": "message:read"}, IdempotencyKey: "onebot:evt-manifest"})
	if err == nil || !strings.Contains(err.Error(), `registered event handler plugins: plugin-echo`) || !strings.Contains(err.Error(), `add "message:read" to the plugin manifest Permissions`) {
		t.Fatalf("expected runtime event manifest permission gate denial, got %v", err)
	}
	if handler.called {
		t.Fatal("expected event handler not to run when manifest permission is missing")
	}
	if len(supervisor.ensured) != 0 {
		t.Fatalf("expected manifest permission gate to stop before supervisor, got %+v", supervisor.ensured)
	}
	if len(runtime.DispatchResults()) != 0 {
		t.Fatalf("expected no dispatch results when event manifest permission gate blocks early, got %+v", runtime.DispatchResults())
	}
	if strings.Contains(runtime.metrics.RenderPrometheus(), `bot_platform_plugin_errors_total{plugin_id="plugin-echo"}`) {
		t.Fatalf("expected manifest permission gate denial not to increment plugin error metric, got %s", runtime.metrics.RenderPrometheus())
	}
	logOutput := buffer.String()
	if !strings.Contains(logOutput, `runtime event manifest permission gate failed`) ||
		!strings.Contains(logOutput, `"failure_stage":"manifest_permission_gate"`) ||
		!strings.Contains(logOutput, `"failure_reason":"missing_manifest_permission"`) ||
		!strings.Contains(logOutput, `"dispatch_kind":"event"`) ||
		!strings.Contains(logOutput, `"registered_handler_plugins":"plugin-echo"`) ||
		!strings.Contains(logOutput, `"registered_handler_plugins_count":1`) ||
		!strings.Contains(logOutput, `"trace_id":"trace-manifest"`) {
		t.Fatalf("expected manifest permission gate observability log, got %s", logOutput)
	}
	if entries := audits.AuditEntries(); len(entries) != 0 {
		t.Fatalf("expected manifest permission gate event denial not to record deny audit, got %+v", entries)
	}
}

func TestRuntimeDispatchesCommandViaHost(t *testing.T) {
	t.Parallel()

	handler := &recordingCommandHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:          "plugin-admin",
			Name:        "Admin Plugin",
			Version:     "0.1.0",
			APIVersion:  "v0",
			Mode:        pluginsdk.ModeSubprocess,
			Permissions: []string{"plugin:enable", "plugin:disable", "plugin:rollout"},
			Entry:       pluginsdk.PluginEntry{Module: "plugins/admin", Symbol: "Plugin"},
		},
		Handlers: pluginsdk.Handlers{Command: handler},
	}

	if err := runtime.RegisterPlugin(plugin); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	command := eventmodel.CommandInvocation{Name: "admin", Raw: "/admin enable plugin-echo"}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-command", EventID: "evt-command"}

	if err := runtime.DispatchCommand(context.Background(), command, ctx); err != nil {
		t.Fatalf("dispatch command: %v", err)
	}
	if !handler.called {
		t.Fatal("expected command handler to be called")
	}
	if handler.command.Name != "admin" || handler.ctx.PluginID != "plugin-admin" {
		t.Fatalf("unexpected command dispatch state command=%+v ctx=%+v", handler.command, handler.ctx)
	}
	results := runtime.DispatchResults()
	if len(results) != 1 || !results[0].Success {
		t.Fatalf("expected one successful dispatch result, got %+v", results)
	}
}

func TestRuntimeSkipsDisabledPluginFromEnabledOverlay(t *testing.T) {
	t.Parallel()

	allowed := &recordingEventHandler{}
	blocked := &recordingEventHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()
	lifecycle := NewPluginLifecycleService(store)
	runtime.SetPluginEnabledStateSource(lifecycle)

	for _, plugin := range []pluginsdk.Plugin{
		{
			Manifest: pluginsdk.PluginManifest{ID: "plugin-allowed", Name: "Allowed Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Entry: pluginsdk.PluginEntry{Module: "plugins/allowed", Symbol: "Plugin"}},
			Handlers: pluginsdk.Handlers{Event: allowed},
		},
		{
			Manifest: pluginsdk.PluginManifest{ID: "plugin-disabled", Name: "Disabled Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Entry: pluginsdk.PluginEntry{Module: "plugins/disabled", Symbol: "Plugin"}},
			Handlers: pluginsdk.Handlers{Event: blocked},
		},
	} {
		if err := runtime.RegisterPlugin(plugin); err != nil {
			t.Fatalf("register plugin %s: %v", plugin.Manifest.ID, err)
		}
		if err := store.SavePluginManifest(context.Background(), plugin.Manifest); err != nil {
			t.Fatalf("save plugin manifest %s: %v", plugin.Manifest.ID, err)
		}
	}
	if err := lifecycle.Disable("plugin-disabled"); err != nil {
		t.Fatalf("disable plugin via lifecycle: %v", err)
	}

	event := eventmodel.Event{EventID: "evt-disabled", TraceID: "trace-disabled", Source: "onebot", Type: "message.received", Timestamp: time.Now().UTC(), IdempotencyKey: "onebot:disabled:1"}
	if err := runtime.DispatchEvent(context.Background(), event); err != nil {
		t.Fatalf("dispatch event: %v", err)
	}
	if !allowed.called {
		t.Fatal("expected enabled plugin to receive dispatch")
	}
	if blocked.called {
		t.Fatal("expected disabled plugin to be skipped")
	}
	results := runtime.DispatchResults()
	if len(results) != 1 || results[0].PluginID != "plugin-allowed" || !results[0].Success {
		t.Fatalf("expected dispatch results only for enabled plugin, got %+v", results)
	}
}

func TestRuntimeDispatchCommandWithoutAuthorizerPreservesExistingBehavior(t *testing.T) {
	t.Parallel()

	handler := &recordingCommandHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-admin", Name: "Admin Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Permissions: []string{"plugin:enable", "plugin:disable", "plugin:rollout"}, Entry: pluginsdk.PluginEntry{Module: "plugins/admin", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Command: handler},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	if err := runtime.DispatchCommand(context.Background(), eventmodel.CommandInvocation{Name: "admin", Raw: "/admin enable plugin-echo"}, eventmodel.ExecutionContext{TraceID: "trace-command", EventID: "evt-command"}); err != nil {
		t.Fatalf("dispatch command without authorizer: %v", err)
	}
	if !handler.called {
		t.Fatal("expected command handler to be called when no authorizer is configured")
	}
}

func TestRuntimeDispatchCommandStopsBeforePluginWhenAuthorizerDenies(t *testing.T) {
	t.Parallel()

	handler := &recordingCommandHandler{}
	authorizer := &recordingCommandAuthorizer{err: errors.New("runtime authorization denied")}
	supervisor := &recordingSupervisor{}
	runtime := NewInMemoryRuntime(supervisor, DirectPluginHost{})
	runtime.SetCommandAuthorizer(authorizer)
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-admin", Name: "Admin Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Permissions: []string{"plugin:enable", "plugin:disable", "plugin:rollout"}, Entry: pluginsdk.PluginEntry{Module: "plugins/admin", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Command: handler},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	err := runtime.DispatchCommand(context.Background(), eventmodel.CommandInvocation{Name: "admin", Raw: "/admin enable plugin-echo"}, eventmodel.ExecutionContext{TraceID: "trace-command", EventID: "evt-command"})
	if err == nil || !strings.Contains(err.Error(), "runtime authorization denied") {
		t.Fatalf("expected runtime authorizer to deny command, got %v", err)
	}
	if !authorizer.called {
		t.Fatal("expected runtime command authorizer to be invoked")
	}
	if handler.called {
		t.Fatal("expected plugin command handler not to be called after authorization denial")
	}
	if len(supervisor.ensured) != 0 {
		t.Fatalf("expected denied command not to reach supervisor, got %+v", supervisor.ensured)
	}
	if len(runtime.DispatchResults()) != 0 {
		t.Fatalf("expected no dispatch results when runtime authorizer blocks early, got %+v", runtime.DispatchResults())
	}
}

func TestRuntimeCommandAuthorizerDoesNotAffectJobOrScheduleDispatch(t *testing.T) {
	t.Parallel()

	authorizer := &recordingCommandAuthorizer{err: errors.New("runtime authorization denied")}
	jobHandler := &recordingJobHandler{}
	scheduleHandler := &recordingScheduleHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	runtime.SetCommandAuthorizer(authorizer)
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-job", Name: "Job Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Entry: pluginsdk.PluginEntry{Module: "plugins/job", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Job: jobHandler},
	}); err != nil {
		t.Fatalf("register job plugin: %v", err)
	}
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-schedule", Name: "Schedule Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Entry: pluginsdk.PluginEntry{Module: "plugins/schedule", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Schedule: scheduleHandler},
	}); err != nil {
		t.Fatalf("register schedule plugin: %v", err)
	}
	if err := runtime.DispatchJob(context.Background(), pluginsdk.JobInvocation{ID: "job-1", Type: "ai.chat"}, eventmodel.ExecutionContext{TraceID: "trace-job", EventID: "evt-job"}); err != nil {
		t.Fatalf("dispatch job: %v", err)
	}
	if err := runtime.DispatchSchedule(context.Background(), pluginsdk.ScheduleTrigger{ID: "schedule-1", Type: "cron"}, eventmodel.ExecutionContext{TraceID: "trace-schedule", EventID: "evt-schedule"}); err != nil {
		t.Fatalf("dispatch schedule: %v", err)
	}
	if !jobHandler.called || !scheduleHandler.called {
		t.Fatalf("expected job and schedule dispatch to remain unaffected, got job=%v schedule=%v", jobHandler.called, scheduleHandler.called)
	}
	if authorizer.called {
		t.Fatal("expected command authorizer not to be invoked for job or schedule dispatch")
	}
}

func TestRuntimeDispatchJobStopsBeforePluginWhenJobAuthorizerDenies(t *testing.T) {
	t.Parallel()

	handler := &recordingJobHandler{}
	authorizer := &recordingJobAuthorizer{err: errors.New("job authorization denied")}
	audits := NewInMemoryAuditLog()
	supervisor := &recordingSupervisor{}
	runtime := NewInMemoryRuntime(supervisor, DirectPluginHost{})
	runtime.SetAuditRecorder(audits)
	runtime.SetJobAuthorizer(authorizer)
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-ai-chat", Name: "AI Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Permissions: []string{"job:run"}, Entry: pluginsdk.PluginEntry{Module: "plugins/ai-chat", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Job: handler},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	err := runtime.DispatchJob(context.Background(), pluginsdk.JobInvocation{ID: "job-1", Type: "ai.chat", Metadata: map[string]any{"actor": "worker-user", "permission": "job:run"}}, eventmodel.ExecutionContext{TraceID: "trace-job-denied", EventID: "evt-job-denied"})
	if err == nil || !strings.Contains(err.Error(), "dispatch completed with no successful plugin deliveries") {
		t.Fatalf("expected job authorization denial to stop job dispatch, got %v", err)
	}
	if !authorizer.called {
		t.Fatal("expected runtime job authorizer to be invoked")
	}
	if handler.called {
		t.Fatal("expected job handler not to be called after authorization denial")
	}
	if len(supervisor.ensured) != 0 {
		t.Fatalf("expected denied job not to reach supervisor, got %+v", supervisor.ensured)
	}
	entries := audits.AuditEntries()
	if len(entries) != 1 {
		t.Fatalf("expected one denied job audit entry, got %+v", entries)
	}
	if entries[0].Actor != "worker-user" || entries[0].Action != "job.run" || entries[0].Permission != "job:run" || entries[0].Target != "plugin-ai-chat" || entries[0].Allowed || auditEntryReason(entries[0]) != "permission_denied" {
		t.Fatalf("expected denied job audit entry, got %+v", entries[0])
	}
}

func TestRuntimeDispatchJobWithoutPermissionMetadataDoesNotRecordDenyAudit(t *testing.T) {
	t.Parallel()

	handler := &recordingJobHandler{}
	audits := NewInMemoryAuditLog()
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	runtime.SetAuditRecorder(audits)
	runtime.SetJobAuthorizer(NewMetadataJobAuthorizer(&RBACConfig{
		ActorRoles: map[string][]string{"worker-user": {"ai-runner"}},
		Policies: map[string]pluginsdk.AuthorizationPolicy{
			"ai-runner": {Permissions: []string{"job:run"}, PluginScope: []string{"plugin-ai-chat"}},
		},
	}))
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-ai-chat", Name: "AI Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Entry: pluginsdk.PluginEntry{Module: "plugins/ai-chat", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Job: handler},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	if err := runtime.DispatchJob(context.Background(), pluginsdk.JobInvocation{ID: "job-no-permission", Type: "ai.chat", Metadata: map[string]any{"actor": "worker-user"}}, eventmodel.ExecutionContext{TraceID: "trace-job-no-permission", EventID: "evt-job-no-permission"}); err != nil {
		t.Fatalf("expected job without permission metadata to preserve compatibility, got %v", err)
	}
	if !handler.called {
		t.Fatal("expected job handler to be called when permission metadata is absent")
	}
	if entries := audits.AuditEntries(); len(entries) != 0 {
		t.Fatalf("expected compatibility path not to record deny audit, got %+v", entries)
	}
}

func TestRuntimeDispatchJobWithConfiguredMetadataAuthorizerAllowsScopedPluginAndSkipsOthers(t *testing.T) {
	t.Parallel()

	allowed := &recordingJobHandler{}
	blocked := &recordingJobHandler{}
	supervisor := &recordingSupervisor{}
	runtime := NewInMemoryRuntime(supervisor, DirectPluginHost{})
	runtime.SetJobAuthorizer(NewMetadataJobAuthorizer(&RBACConfig{
		ActorRoles: map[string][]string{"worker-user": {"ai-runner"}},
		Policies: map[string]pluginsdk.AuthorizationPolicy{
			"ai-runner": {Permissions: []string{"job:run"}, PluginScope: []string{"plugin-ai-chat"}},
		},
	}))
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-ai-chat", Name: "AI Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Permissions: []string{"job:run"}, Entry: pluginsdk.PluginEntry{Module: "plugins/ai-chat", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Job: allowed},
	}); err != nil {
		t.Fatalf("register allowed plugin: %v", err)
	}
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-workflow-demo", Name: "Workflow Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Permissions: []string{"job:run"}, Entry: pluginsdk.PluginEntry{Module: "plugins/workflow-demo", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Job: blocked},
	}); err != nil {
		t.Fatalf("register blocked plugin: %v", err)
	}
	if err := runtime.DispatchJob(context.Background(), pluginsdk.JobInvocation{ID: "job-2", Type: "ai.chat", Metadata: map[string]any{"actor": "worker-user", "permission": "job:run"}}, eventmodel.ExecutionContext{TraceID: "trace-job-allow", EventID: "evt-job-allow"}); err != nil {
		t.Fatalf("expected scoped job authorization to allow at least one plugin, got %v", err)
	}
	if !allowed.called || blocked.called {
		t.Fatalf("expected only scoped job plugin to run, allowed=%v blocked=%v", allowed.called, blocked.called)
	}
}

func TestRuntimeDispatchJobTargetsSinglePluginWhenTargetPluginIDProvided(t *testing.T) {
	t.Parallel()

	allowed := &recordingJobHandler{}
	blocked := &recordingJobHandler{}
	runtime := NewInMemoryRuntime(&recordingSupervisor{}, DirectPluginHost{})
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-ai-chat", Name: "AI Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Permissions: []string{"job:run"}, Entry: pluginsdk.PluginEntry{Module: "plugins/ai-chat", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Job: allowed},
	}); err != nil {
		t.Fatalf("register allowed plugin: %v", err)
	}
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-other-job", Name: "Other Job Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Permissions: []string{"job:run"}, Entry: pluginsdk.PluginEntry{Module: "plugins/other", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Job: blocked},
	}); err != nil {
		t.Fatalf("register blocked plugin: %v", err)
	}

	err := runtime.DispatchJob(context.Background(), pluginsdk.JobInvocation{ID: "job-targeted", Type: "ai.chat", Metadata: map[string]any{"permission": "job:run", "target_plugin_id": "plugin-ai-chat"}}, eventmodel.ExecutionContext{TraceID: "trace-job-targeted", EventID: "evt-job-targeted", Reply: &eventmodel.ReplyHandle{Capability: "onebot.reply", TargetID: "group-42"}})
	if err != nil {
		t.Fatalf("dispatch job: %v", err)
	}
	if !allowed.called || blocked.called {
		t.Fatalf("expected only targeted plugin to handle job, allowed=%v blocked=%v", allowed.called, blocked.called)
	}
}

func TestRuntimeDispatchScheduleWithConfiguredMetadataAuthorizerAllowsScopedPluginAndSkipsOthers(t *testing.T) {
	t.Parallel()

	allowed := &recordingScheduleHandler{}
	blocked := &recordingScheduleHandler{}
	supervisor := &recordingSupervisor{}
	runtime := NewInMemoryRuntime(supervisor, DirectPluginHost{})
	runtime.SetScheduleAuthorizer(NewMetadataScheduleAuthorizer(&RBACConfig{
		ActorRoles: map[string][]string{"scheduler-user": {"schedule-operator"}},
		Policies: map[string]pluginsdk.AuthorizationPolicy{
			"schedule-operator": {Permissions: []string{"schedule:manage"}, PluginScope: []string{"plugin-scheduler"}},
		},
	}))
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-scheduler", Name: "Scheduler Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Permissions: []string{"schedule:manage"}, Entry: pluginsdk.PluginEntry{Module: "plugins/scheduler", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Schedule: allowed},
	}); err != nil {
		t.Fatalf("register allowed plugin: %v", err)
	}
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-workflow-demo", Name: "Workflow Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Permissions: []string{"schedule:manage"}, Entry: pluginsdk.PluginEntry{Module: "plugins/workflow-demo", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Schedule: blocked},
	}); err != nil {
		t.Fatalf("register blocked plugin: %v", err)
	}
	if err := runtime.DispatchSchedule(context.Background(), pluginsdk.ScheduleTrigger{ID: "schedule-1", Type: "cron", Metadata: map[string]any{"actor": "scheduler-user", "permission": "schedule:manage"}}, eventmodel.ExecutionContext{TraceID: "trace-schedule-allow", EventID: "evt-schedule-allow"}); err != nil {
		t.Fatalf("expected scoped schedule authorization to allow at least one plugin, got %v", err)
	}
	if !allowed.called || blocked.called {
		t.Fatalf("expected only scoped schedule plugin to run, allowed=%v blocked=%v", allowed.called, blocked.called)
	}
}

func TestRuntimeDispatchScheduleWithConfiguredMetadataAuthorizerRecordsDeniedAudit(t *testing.T) {
	t.Parallel()

	handler := &recordingScheduleHandler{}
	audits := NewInMemoryAuditLog()
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	runtime.SetAuditRecorder(audits)
	runtime.SetScheduleAuthorizer(NewMetadataScheduleAuthorizer(&RBACConfig{
		ActorRoles: map[string][]string{"viewer-user": {"schedule-viewer"}},
		Policies: map[string]pluginsdk.AuthorizationPolicy{
			"schedule-viewer": {Permissions: []string{"schedule:view"}, PluginScope: []string{"*"}},
		},
	}))
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-scheduler", Name: "Scheduler Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Permissions: []string{"schedule:manage"}, Entry: pluginsdk.PluginEntry{Module: "plugins/scheduler", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Schedule: handler},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	err := runtime.DispatchSchedule(context.Background(), pluginsdk.ScheduleTrigger{ID: "schedule-denied", Type: "cron", Metadata: map[string]any{"actor": "viewer-user", "permission": "schedule:manage"}}, eventmodel.ExecutionContext{TraceID: "trace-schedule-denied", EventID: "evt-schedule-denied"})
	if err == nil || !strings.Contains(err.Error(), "dispatch completed with no successful plugin deliveries") {
		t.Fatalf("expected denied schedule dispatch to stop before plugin delivery, got %v", err)
	}
	if handler.called {
		t.Fatal("expected denied schedule not to reach plugin handler")
	}
	entries := audits.AuditEntries()
	if len(entries) != 1 {
		t.Fatalf("expected one denied schedule audit entry, got %+v", entries)
	}
	if entries[0].Actor != "viewer-user" || entries[0].Action != "schedule.manage" || entries[0].Permission != "schedule:manage" || entries[0].Target != "plugin-scheduler" || entries[0].Allowed || auditEntryReason(entries[0]) != "permission_denied" {
		t.Fatalf("expected denied schedule audit entry, got %+v", entries[0])
	}
}

func TestRuntimeDispatchScheduleWithoutPermissionMetadataDoesNotRecordDenyAudit(t *testing.T) {
	t.Parallel()

	handler := &recordingScheduleHandler{}
	audits := NewInMemoryAuditLog()
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	runtime.SetAuditRecorder(audits)
	runtime.SetScheduleAuthorizer(NewMetadataScheduleAuthorizer(&RBACConfig{
		ActorRoles: map[string][]string{"scheduler-user": {"schedule-operator"}},
		Policies: map[string]pluginsdk.AuthorizationPolicy{
			"schedule-operator": {Permissions: []string{"schedule:manage"}, PluginScope: []string{"plugin-scheduler"}},
		},
	}))
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-scheduler", Name: "Scheduler Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Entry: pluginsdk.PluginEntry{Module: "plugins/scheduler", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Schedule: handler},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	if err := runtime.DispatchSchedule(context.Background(), pluginsdk.ScheduleTrigger{ID: "schedule-no-permission", Type: "cron", Metadata: map[string]any{"actor": "scheduler-user"}}, eventmodel.ExecutionContext{TraceID: "trace-schedule-no-permission", EventID: "evt-schedule-no-permission"}); err != nil {
		t.Fatalf("expected schedule without permission metadata to preserve compatibility, got %v", err)
	}
	if !handler.called {
		t.Fatal("expected schedule handler to be called when permission metadata is absent")
	}
	if entries := audits.AuditEntries(); len(entries) != 0 {
		t.Fatalf("expected compatibility path not to record deny audit, got %+v", entries)
	}
}

func TestRuntimeDispatchScheduleWithConfiguredMetadataAuthorizerRecordsScopeDeniedAudit(t *testing.T) {
	t.Parallel()

	handler := &recordingScheduleHandler{}
	audits := NewInMemoryAuditLog()
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	runtime.SetAuditRecorder(audits)
	runtime.SetScheduleAuthorizer(NewMetadataScheduleAuthorizer(&RBACConfig{
		ActorRoles: map[string][]string{"scheduler-user": {"schedule-operator"}},
		Policies: map[string]pluginsdk.AuthorizationPolicy{
			"schedule-operator": {Permissions: []string{"schedule:manage"}, PluginScope: []string{"plugin-other"}},
		},
	}))
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-scheduler", Name: "Scheduler Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Permissions: []string{"schedule:manage"}, Entry: pluginsdk.PluginEntry{Module: "plugins/scheduler", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Schedule: handler},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	err := runtime.DispatchSchedule(context.Background(), pluginsdk.ScheduleTrigger{ID: "schedule-scope-denied", Type: "cron", Metadata: map[string]any{"actor": "scheduler-user", "permission": "schedule:manage"}}, eventmodel.ExecutionContext{TraceID: "trace-schedule-scope-denied", EventID: "evt-schedule-scope-denied"})
	if err == nil || !strings.Contains(err.Error(), "dispatch completed with no successful plugin deliveries") {
		t.Fatalf("expected scope-denied schedule dispatch to stop before plugin delivery, got %v", err)
	}
	if handler.called {
		t.Fatal("expected scope-denied schedule not to reach plugin handler")
	}
	entries := audits.AuditEntries()
	if len(entries) != 1 {
		t.Fatalf("expected one scope-denied schedule audit entry, got %+v", entries)
	}
	if entries[0].Actor != "scheduler-user" || entries[0].Action != "schedule.manage" || entries[0].Permission != "schedule:manage" || entries[0].Target != "plugin-scheduler" || entries[0].Allowed || auditEntryReason(entries[0]) != "plugin_scope_denied" {
		t.Fatalf("expected scope-denied schedule audit entry, got %+v", entries[0])
	}
}

func TestRuntimeDispatchScheduleRequiresDeclaredManifestPermissionWhenPermissionMetadataPresent(t *testing.T) {
	t.Parallel()

	handler := &recordingScheduleHandler{}
	audits := NewInMemoryAuditLog()
	supervisor := &recordingSupervisor{}
	runtime := NewInMemoryRuntime(supervisor, DirectPluginHost{})
	runtime.SetAuditRecorder(audits)
	runtime.SetScheduleAuthorizer(NewMetadataScheduleAuthorizer(&RBACConfig{
		ActorRoles: map[string][]string{"scheduler-user": {"schedule-operator"}},
		Policies: map[string]pluginsdk.AuthorizationPolicy{
			"schedule-operator": {Permissions: []string{"schedule:manage"}, PluginScope: []string{"*"}},
		},
	}))
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-scheduler", Name: "Scheduler Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Entry: pluginsdk.PluginEntry{Module: "plugins/scheduler", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Schedule: handler},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	err := runtime.DispatchSchedule(context.Background(), pluginsdk.ScheduleTrigger{ID: "schedule-2", Type: "cron", Metadata: map[string]any{"actor": "scheduler-user", "permission": "schedule:manage"}}, eventmodel.ExecutionContext{TraceID: "trace-schedule-manifest", EventID: "evt-schedule-manifest"})
	if err == nil || !strings.Contains(err.Error(), `registered schedule handler plugins: plugin-scheduler`) || !strings.Contains(err.Error(), `add "schedule:manage" to the plugin manifest Permissions`) {
		t.Fatalf("expected runtime schedule manifest permission gate denial, got %v", err)
	}
	if handler.called {
		t.Fatal("expected schedule handler not to run when manifest permission is missing")
	}
	if entries := audits.AuditEntries(); len(entries) != 0 {
		t.Fatalf("expected manifest permission gate schedule denial not to record deny audit, got %+v", entries)
	}
}

func TestRuntimeDispatchCommandWithConfiguredAdminAuthorizerAllowsAndDenies(t *testing.T) {
	t.Parallel()

	handler := &recordingCommandHandler{}
	audits := NewInMemoryAuditLog()
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	runtime.SetAuditRecorder(audits)
	runtime.SetCommandAuthorizer(NewAdminCommandAuthorizer(&RBACConfig{
		ActorRoles: map[string][]string{
			"admin-user":  {"admin"},
			"viewer-user": {"viewer"},
		},
		Policies: map[string]pluginsdk.AuthorizationPolicy{
			"admin":  {Permissions: []string{"plugin:enable"}, PluginScope: []string{"*"}},
			"viewer": {Permissions: []string{"plugin:disable"}, PluginScope: []string{"*"}},
		},
	}))
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-admin", Name: "Admin Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Permissions: []string{"plugin:enable", "plugin:disable", "plugin:rollout"}, Entry: pluginsdk.PluginEntry{Module: "plugins/admin", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Command: handler},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	if err := runtime.DispatchCommand(context.Background(), eventmodel.CommandInvocation{Name: "admin", Raw: "/admin enable plugin-echo", Metadata: map[string]any{"actor": "admin-user"}}, eventmodel.ExecutionContext{TraceID: "trace-admin", EventID: "evt-admin"}); err != nil {
		t.Fatalf("expected allowed admin command, got %v", err)
	}
	if !handler.called {
		t.Fatal("expected allowed admin command to reach plugin handler")
	}
	if entries := audits.AuditEntries(); len(entries) != 0 {
		t.Fatalf("expected allowed admin command not to record deny audit, got %+v", entries)
	}
	handler.called = false
	err := runtime.DispatchCommand(context.Background(), eventmodel.CommandInvocation{Name: "admin", Raw: "/admin enable plugin-echo", Metadata: map[string]any{"actor": "viewer-user"}}, eventmodel.ExecutionContext{TraceID: "trace-viewer", EventID: "evt-viewer"})
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("expected configured admin authorizer denial, got %v", err)
	}
	if handler.called {
		t.Fatal("expected denied admin command not to reach plugin handler")
	}
	entries := audits.AuditEntries()
	if len(entries) != 1 {
		t.Fatalf("expected one denied admin command audit entry, got %+v", entries)
	}
	if entries[0].Actor != "viewer-user" || entries[0].Action != "plugin.enable" || entries[0].Permission != "plugin:enable" || entries[0].Target != "plugin-echo" || entries[0].Allowed || auditEntryReason(entries[0]) != "permission_denied" {
		t.Fatalf("expected denied admin command audit entry, got %+v", entries[0])
	}
}

func TestRuntimeDispatchCommandWithConfiguredAdminAuthorizerRejectsScopeAndAllowsRollout(t *testing.T) {
	t.Parallel()

	handler := &recordingCommandHandler{}
	audits := NewInMemoryAuditLog()
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	runtime.SetAuditRecorder(audits)
	runtime.SetCommandAuthorizer(NewAdminCommandAuthorizer(&RBACConfig{
		ActorRoles: map[string][]string{
			"scoped-user":  {"scoped-admin"},
			"rollout-user": {"rollout-admin"},
		},
		Policies: map[string]pluginsdk.AuthorizationPolicy{
			"scoped-admin":  {Permissions: []string{"plugin:enable"}, PluginScope: []string{"plugin-echo"}},
			"rollout-admin": {Permissions: []string{"plugin:rollout"}, PluginScope: []string{"plugin-echo"}},
		},
	}))
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-admin", Name: "Admin Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Permissions: []string{"plugin:enable", "plugin:disable", "plugin:rollout"}, Entry: pluginsdk.PluginEntry{Module: "plugins/admin", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Command: handler},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	err := runtime.DispatchCommand(context.Background(), eventmodel.CommandInvocation{Name: "admin", Raw: "/admin enable plugin-ai-chat", Metadata: map[string]any{"actor": "scoped-user"}}, eventmodel.ExecutionContext{TraceID: "trace-scope", EventID: "evt-scope"})
	if err == nil || !strings.Contains(err.Error(), "plugin scope denied") {
		t.Fatalf("expected scope denial, got %v", err)
	}
	if handler.called {
		t.Fatal("expected scope-denied command not to reach plugin handler")
	}
	entries := audits.AuditEntries()
	if len(entries) != 1 {
		t.Fatalf("expected one scope-denied admin command audit entry, got %+v", entries)
	}
	if entries[0].Actor != "scoped-user" || entries[0].Action != "plugin.enable" || entries[0].Permission != "plugin:enable" || entries[0].Target != "plugin-ai-chat" || entries[0].Allowed || auditEntryReason(entries[0]) != "plugin_scope_denied" {
		t.Fatalf("expected scope-denied admin command audit entry, got %+v", entries[0])
	}
	handler.called = false
	if err := runtime.DispatchCommand(context.Background(), eventmodel.CommandInvocation{Name: "admin", Raw: "/admin prepare plugin-echo", Metadata: map[string]any{"actor": "rollout-user"}}, eventmodel.ExecutionContext{TraceID: "trace-rollout", EventID: "evt-rollout"}); err != nil {
		t.Fatalf("expected rollout command to be allowed, got %v", err)
	}
	if !handler.called {
		t.Fatal("expected rollout command to reach plugin handler")
	}
	if entries := audits.AuditEntries(); len(entries) != 1 {
		t.Fatalf("expected allowed rollout command not to add deny audit, got %+v", entries)
	}
}

func TestRuntimeDispatchCommandEmitsRolloutEvidenceFields(t *testing.T) {
	t.Parallel()

	buffer := &bytes.Buffer{}
	logger := NewLogger(buffer)
	tracer := NewTraceRecorder()
	handler := &recordingCommandHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	runtime.SetObservability(logger, tracer, NewMetricsRegistry())
	runtime.SetCommandAuthorizer(NewAdminCommandAuthorizer(&RBACConfig{
		ActorRoles: map[string][]string{"rollout-user": {"rollout-admin"}},
		Policies: map[string]pluginsdk.AuthorizationPolicy{
			"rollout-admin": {Permissions: []string{"plugin:rollout"}, PluginScope: []string{"plugin-echo"}},
		},
	}))
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-admin", Name: "Admin Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Permissions: []string{"plugin:rollout"}, Entry: pluginsdk.PluginEntry{Module: "plugins/admin", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Command: handler},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	if err := runtime.DispatchCommand(context.Background(), eventmodel.CommandInvocation{Name: "admin", Raw: "/admin prepare plugin-echo", Metadata: map[string]any{"actor": "rollout-user"}}, eventmodel.ExecutionContext{TraceID: "trace-rollout-observe", EventID: "evt-rollout-observe"}); err != nil {
		t.Fatalf("dispatch rollout command: %v", err)
	}
	logs := buffer.String()
	for _, expected := range []string{`"command_name":"admin"`, `"actor":"rollout-user"`, `"action":"prepare"`, `"target":"plugin-echo"`, `"permission":"plugin:rollout"`} {
		if !strings.Contains(logs, expected) {
			t.Fatalf("expected rollout observability log %q, got %s", expected, logs)
		}
	}
	if rendered := tracer.RenderTrace("trace-rollout-observe"); !strings.Contains(rendered, "runtime.dispatch") || !strings.Contains(rendered, "plugin.invoke") {
		t.Fatalf("expected rollout observability trace spans, got %s", rendered)
	}
}

func TestRuntimeDispatchCommandWithConfiguredAdminAuthorizerSupportsStructuredArguments(t *testing.T) {
	t.Parallel()

	handler := &recordingCommandHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	runtime.SetCommandAuthorizer(NewAdminCommandAuthorizer(&RBACConfig{
		ActorRoles: map[string][]string{"admin-user": {"admin"}},
		Policies: map[string]pluginsdk.AuthorizationPolicy{
			"admin": {Permissions: []string{"plugin:enable"}, PluginScope: []string{"*"}},
		},
	}))
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-admin", Name: "Admin Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Permissions: []string{"plugin:enable", "plugin:disable", "plugin:rollout"}, Entry: pluginsdk.PluginEntry{Module: "plugins/admin", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Command: handler},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	if err := runtime.DispatchCommand(context.Background(), eventmodel.CommandInvocation{Name: "admin", Arguments: []string{"enable", "plugin-echo"}, Metadata: map[string]any{"actor": "admin-user"}}, eventmodel.ExecutionContext{TraceID: "trace-args", EventID: "evt-args"}); err != nil {
		t.Fatalf("expected structured command arguments to be authorized, got %v", err)
	}
	if !handler.called {
		t.Fatal("expected structured command to reach plugin handler")
	}
}

func TestRuntimeDispatchCommandRequiresAdminHandlerPluginToDeclareManifestPermission(t *testing.T) {
	t.Parallel()

	handler := &recordingCommandHandler{}
	audits := NewInMemoryAuditLog()
	supervisor := &recordingSupervisor{}
	runtime := NewInMemoryRuntime(supervisor, DirectPluginHost{})
	runtime.SetAuditRecorder(audits)
	runtime.SetCommandAuthorizer(NewAdminCommandAuthorizer(&RBACConfig{
		ActorRoles: map[string][]string{"admin-user": {"admin"}},
		Policies: map[string]pluginsdk.AuthorizationPolicy{
			"admin": {Permissions: []string{"plugin:enable"}, PluginScope: []string{"*"}},
		},
	}))
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-admin", Name: "Admin Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Permissions: []string{"plugin:disable"}, Entry: pluginsdk.PluginEntry{Module: "plugins/admin", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Command: handler},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	err := runtime.DispatchCommand(context.Background(), eventmodel.CommandInvocation{Name: "admin", Raw: "/admin enable plugin-echo", Metadata: map[string]any{"actor": "admin-user"}}, eventmodel.ExecutionContext{TraceID: "trace-manifest", EventID: "evt-manifest"})
	if err == nil || !strings.Contains(err.Error(), `registered command handler plugins: plugin-admin`) || !strings.Contains(err.Error(), `add "plugin:enable" to the plugin manifest Permissions`) {
		t.Fatalf("expected runtime to reject command when admin handler manifest lacks required permission, got %v", err)
	}
	if handler.called {
		t.Fatal("expected command handler not to run when manifest permission is missing")
	}
	if len(supervisor.ensured) != 0 {
		t.Fatalf("expected manifest permission gate to stop before supervisor, got %+v", supervisor.ensured)
	}
	if len(runtime.DispatchResults()) != 0 {
		t.Fatalf("expected no dispatch results when manifest permission gate blocks command, got %+v", runtime.DispatchResults())
	}
	if entries := audits.AuditEntries(); len(entries) != 0 {
		t.Fatalf("expected manifest permission gate command denial not to record deny audit, got %+v", entries)
	}
}

func TestRuntimeDispatchReplayCommandUsesReplayPermissionAndManifestGate(t *testing.T) {
	t.Parallel()

	handler := &recordingCommandHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	runtime.SetCommandAuthorizer(NewAdminCommandAuthorizer(&RBACConfig{
		ActorRoles: map[string][]string{"replay-user": {"replay-role"}},
		Policies: map[string]pluginsdk.AuthorizationPolicy{
			"replay-role": {Permissions: []string{"plugin:replay"}, PluginScope: []string{"evt-allowed"}},
		},
	}))
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-admin", Name: "Admin Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Permissions: []string{"plugin:replay"}, Entry: pluginsdk.PluginEntry{Module: "plugins/admin", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Command: handler},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	if err := runtime.DispatchCommand(context.Background(), eventmodel.CommandInvocation{Name: "admin", Raw: "/admin replay evt-allowed", Metadata: map[string]any{"actor": "replay-user"}}, eventmodel.ExecutionContext{TraceID: "trace-replay", EventID: "evt-replay"}); err != nil {
		t.Fatalf("expected replay command to be allowed, got %v", err)
	}
	if !handler.called {
		t.Fatal("expected replay command to reach plugin handler")
	}
	handler.called = false
	runtime2 := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	runtime2.SetCommandAuthorizer(NewAdminCommandAuthorizer(&RBACConfig{
		ActorRoles: map[string][]string{"replay-user": {"replay-role"}},
		Policies: map[string]pluginsdk.AuthorizationPolicy{
			"replay-role": {Permissions: []string{"plugin:replay"}, PluginScope: []string{"evt-allowed"}},
		},
	}))
	if err := runtime2.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-admin", Name: "Admin Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Permissions: []string{"plugin:enable"}, Entry: pluginsdk.PluginEntry{Module: "plugins/admin", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Command: &recordingCommandHandler{}},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	err := runtime2.DispatchCommand(context.Background(), eventmodel.CommandInvocation{Name: "admin", Raw: "/admin replay evt-allowed", Metadata: map[string]any{"actor": "replay-user"}}, eventmodel.ExecutionContext{TraceID: "trace-replay-deny", EventID: "evt-replay-deny"})
	if err == nil || !strings.Contains(err.Error(), `registered command handler plugins: plugin-admin`) || !strings.Contains(err.Error(), `add "plugin:replay" to the plugin manifest Permissions`) {
		t.Fatalf("expected replay manifest permission gate denial, got %v", err)
	}
}

func TestRuntimeDispatchJobRequiresDeclaredManifestPermissionWhenPermissionMetadataPresent(t *testing.T) {
	t.Parallel()

	handler := &recordingJobHandler{}
	audits := NewInMemoryAuditLog()
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	runtime.SetAuditRecorder(audits)
	runtime.SetJobAuthorizer(NewMetadataJobAuthorizer(&RBACConfig{
		ActorRoles: map[string][]string{"worker-user": {"worker"}},
		Policies: map[string]pluginsdk.AuthorizationPolicy{
			"worker": {Permissions: []string{"job:run"}, PluginScope: []string{"*"}},
		},
	}))
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-job", Name: "Job Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Entry: pluginsdk.PluginEntry{Module: "plugins/job", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Job: handler},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	err := runtime.DispatchJob(context.Background(), pluginsdk.JobInvocation{ID: "job-manifest", Type: "ai.chat", Metadata: map[string]any{"actor": "worker-user", "permission": "job:run"}}, eventmodel.ExecutionContext{TraceID: "trace-job-manifest", EventID: "evt-job-manifest"})
	if err == nil || !strings.Contains(err.Error(), `registered job handler plugins: plugin-job`) || !strings.Contains(err.Error(), `add "job:run" to the plugin manifest Permissions`) {
		t.Fatalf("expected runtime job manifest permission gate denial, got %v", err)
	}
	if handler.called {
		t.Fatal("expected job handler not to run when manifest permission is missing")
	}
	if entries := audits.AuditEntries(); len(entries) != 0 {
		t.Fatalf("expected manifest permission gate job denial not to record deny audit, got %+v", entries)
	}
}

func TestRuntimeDispatchNonAdminCommandDoesNotRequireManifestPermission(t *testing.T) {
	t.Parallel()

	handler := &recordingCommandHandler{}
	audits := NewInMemoryAuditLog()
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	runtime.SetAuditRecorder(audits)
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{ID: "plugin-generic", Name: "Generic Plugin", Version: "0.1.0", APIVersion: "v0", Mode: pluginsdk.ModeSubprocess, Entry: pluginsdk.PluginEntry{Module: "plugins/generic", Symbol: "Plugin"}},
		Handlers: pluginsdk.Handlers{Command: handler},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	if err := runtime.DispatchCommand(context.Background(), eventmodel.CommandInvocation{Name: "generic", Raw: "/generic ping"}, eventmodel.ExecutionContext{TraceID: "trace-generic", EventID: "evt-generic"}); err != nil {
		t.Fatalf("expected non-admin command to bypass manifest permission gate, got %v", err)
	}
	if !handler.called {
		t.Fatal("expected non-admin command to reach plugin handler")
	}
	if entries := audits.AuditEntries(); len(entries) != 0 {
		t.Fatalf("expected non-admin compatibility path not to record deny audit, got %+v", entries)
	}
}

func TestRuntimeDispatchesJobViaHost(t *testing.T) {
	t.Parallel()

	handler := &recordingJobHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-job",
			Name:       "Job Plugin",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			Entry:      pluginsdk.PluginEntry{Module: "plugins/job", Symbol: "Plugin"},
		},
		Handlers: pluginsdk.Handlers{Job: handler},
	}

	if err := runtime.RegisterPlugin(plugin); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	job := pluginsdk.JobInvocation{ID: "job-1", Type: "ai.chat"}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-job", EventID: "evt-job"}

	if err := runtime.DispatchJob(context.Background(), job, ctx); err != nil {
		t.Fatalf("dispatch job: %v", err)
	}
	if !handler.called {
		t.Fatal("expected job handler to be called")
	}
	if handler.job.ID != "job-1" || handler.ctx.PluginID != "plugin-job" {
		t.Fatalf("unexpected job dispatch state job=%+v ctx=%+v", handler.job, handler.ctx)
	}
}

func TestRuntimeDispatchesScheduleViaHost(t *testing.T) {
	t.Parallel()

	handler := &recordingScheduleHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-schedule",
			Name:       "Schedule Plugin",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			Entry:      pluginsdk.PluginEntry{Module: "plugins/schedule", Symbol: "Plugin"},
		},
		Handlers: pluginsdk.Handlers{Schedule: handler},
	}

	if err := runtime.RegisterPlugin(plugin); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	trigger := pluginsdk.ScheduleTrigger{ID: "schedule-1", Type: "cron"}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-schedule", EventID: "evt-schedule"}

	if err := runtime.DispatchSchedule(context.Background(), trigger, ctx); err != nil {
		t.Fatalf("dispatch schedule: %v", err)
	}
	if !handler.called {
		t.Fatal("expected schedule handler to be called")
	}
	if handler.trigger.ID != "schedule-1" || handler.ctx.PluginID != "plugin-schedule" {
		t.Fatalf("unexpected schedule dispatch state trigger=%+v ctx=%+v", handler.trigger, handler.ctx)
	}
}

func TestRuntimeSkipsPluginsWithoutMatchingHandler(t *testing.T) {
	t.Parallel()

	supervisor := &recordingSupervisor{}
	runtime := NewInMemoryRuntime(supervisor, DirectPluginHost{})
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-event-only",
			Name:       "Event Only",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			Entry:      pluginsdk.PluginEntry{Module: "plugins/event-only", Symbol: "Plugin"},
		},
		Handlers: pluginsdk.Handlers{Event: &recordingEventHandler{}},
	}

	if err := runtime.RegisterPlugin(plugin); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	err := runtime.DispatchCommand(context.Background(), eventmodel.CommandInvocation{Name: "admin"}, eventmodel.ExecutionContext{TraceID: "trace", EventID: "evt"})
	if err == nil {
		t.Fatal("expected command dispatch with no matching handlers to fail")
	}
	if len(supervisor.ensured) != 0 {
		t.Fatalf("expected skipped plugin to avoid supervisor startup, got %+v", supervisor.ensured)
	}
	if len(runtime.DispatchResults()) != 0 {
		t.Fatalf("expected no dispatch results for skipped plugins, got %+v", runtime.DispatchResults())
	}
}

func TestRuntimeAuxiliaryInterfacesStoreMinimalState(t *testing.T) {
	t.Parallel()

	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	ctx := context.Background()

	if err := runtime.EnqueueJob(ctx, pluginsdk.JobInvocation{ID: "job-1", Type: "ai.call"}); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	if err := runtime.Schedule(ctx, pluginsdk.ScheduleTrigger{ID: "schedule-1", Type: "cron"}); err != nil {
		t.Fatalf("schedule trigger: %v", err)
	}
	if err := runtime.Persist(ctx, Record{Kind: "event", RefID: "evt-1"}); err != nil {
		t.Fatalf("persist record: %v", err)
	}
	if err := runtime.Observe(ctx, Observation{Name: "dispatch.completed"}); err != nil {
		t.Fatalf("observe: %v", err)
	}

	if len(runtime.EnqueuedJobs()) != 1 || len(runtime.ScheduledTriggers()) != 1 || len(runtime.PersistedRecords()) != 1 || len(runtime.Observations()) != 1 {
		t.Fatal("expected runtime auxiliary interfaces to store one item each")
	}
}

func TestRuntimeDispatchQueuedJobUsesPersistedEnvelope(t *testing.T) {
	t.Parallel()

	handler := &recordingJobHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:          "plugin-ai-chat",
			Name:        "AI Chat",
			Version:     "0.1.0",
			APIVersion:  "v0",
			Mode:        pluginsdk.ModeSubprocess,
			Permissions: []string{"job:run"},
			Entry:       pluginsdk.PluginEntry{Module: "plugins/plugin-ai-chat", Symbol: "Plugin"},
		},
		Handlers: pluginsdk.Handlers{Job: handler},
	}
	if err := runtime.RegisterPlugin(plugin); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	job := NewJob("job-persisted-dispatch", "ai.chat", 1, 30*time.Second)
	job.TraceID = "trace-persisted-dispatch"
	job.EventID = "evt-persisted-dispatch"
	job.RunID = "run-persisted-dispatch"
	job.Correlation = "corr-persisted-dispatch"
	job.Payload = map[string]any{
		"dispatch": map[string]any{
			"permission":       "job:run",
			"target_plugin_id": "plugin-ai-chat",
		},
		"reply_handle": map[string]any{
			"capability": "onebot.reply",
			"target_id":  "group-42",
		},
	}

	if err := runtime.DispatchQueuedJob(context.Background(), nil, job); err != nil {
		t.Fatalf("dispatch queued job: %v", err)
	}
	if !handler.called {
		t.Fatal("expected queued job dispatch to reach job handler")
	}
	if handler.job.ID != job.ID || handler.job.Type != job.Type {
		t.Fatalf("unexpected job invocation %+v", handler.job)
	}
	if got, _ := handler.job.Metadata["target_plugin_id"].(string); got != "plugin-ai-chat" {
		t.Fatalf("expected target_plugin_id metadata, got %+v", handler.job.Metadata)
	}
	if handler.ctx.TraceID != job.TraceID || handler.ctx.EventID != job.EventID || handler.ctx.RunID != job.RunID || handler.ctx.CorrelationID != job.Correlation {
		t.Fatalf("unexpected execution context %+v", handler.ctx)
	}
	if handler.ctx.Reply == nil || handler.ctx.Reply.TargetID != "group-42" {
		t.Fatalf("expected reply handle from persisted payload, got %+v", handler.ctx.Reply)
	}
}

func TestRuntimeDispatchQueuedJobRenewsHeartbeatDuringExecution(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()
	baseQueue := NewJobQueue()
	baseQueue.SetStore(store)
	baseQueue.SetWorkerIdentity("runtime-local:test-worker")
	queue := &heartbeatCountingQueue{JobQueue: baseQueue}
	base := time.Now().UTC()

	job := NewJob("job-heartbeat-runtime", "ai.chat", 1, 1500*time.Millisecond)
	if err := queue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	claimed, err := queue.MarkRunning(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("mark running: %v", err)
	}
	first, err := queue.Inspect(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("inspect first claimed job: %v", err)
	}
	if first.LeaseAcquiredAt == nil {
		t.Fatalf("expected initial lease acquisition, got %+v", first)
	}

	heartbeatCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := startQueuedJobHeartbeat(heartbeatCtx, queue, claimed)
	defer stop()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("expected runtime heartbeat renewal during execution, heartbeat_count=%d last_error=%q", queue.HeartbeatCount(), queue.LastHeartbeatError())
		}
		if queue.HeartbeatCount() >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	running, err := queue.Inspect(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("inspect running job: %v", err)
	}
	if running.LeaseAcquiredAt == nil || !running.LeaseAcquiredAt.Equal(*first.LeaseAcquiredAt) {
		t.Fatalf("expected lease acquisition time to stay fixed during renewal, first=%+v running=%+v", first, running)
	}
	if running.LeaseExpiresAt == nil || !running.LeaseExpiresAt.After(base) {
		t.Fatalf("expected renewed lease expiry, got %+v", running)
	}
}

func TestRuntimeDispatchQueuedJobFailsMalformedReplyAndMovesJobOutOfPending(t *testing.T) {
	t.Parallel()

	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	queue := NewJobQueue()
	job := NewJob("job-bad-reply-dispatch", "ai.chat", 0, 30*time.Second)
	if err := queue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	stored, err := queue.Inspect(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("inspect job: %v", err)
	}
	if err := runtime.DispatchQueuedJob(context.Background(), queue, stored); err == nil {
		t.Fatal("expected malformed reply handle to fail queued dispatch")
	}
	updated, err := queue.Inspect(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("inspect updated job: %v", err)
	}
	if updated.Status != JobStatusDead || updated.LastError == "" || updated.ReasonCode != JobReasonCodeDispatchDead {
		t.Fatalf("expected queued dispatch failure to move job to dead with reason, got %+v", updated)
	}
}

func TestRuntimeDispatchQueuedJobFailureMovesJobOutOfPendingWhenDispatchDidNotStart(t *testing.T) {
	t.Parallel()

	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	queue := NewJobQueue()
	job := NewJob("job-missing-plugin-dispatch", "ai.chat", 0, 30*time.Second)
	job.Payload = map[string]any{
		"dispatch": map[string]any{
			"permission":       "job:run",
			"target_plugin_id": "plugin-missing",
		},
		"reply_handle": map[string]any{
			"capability": "onebot.reply",
			"target_id":  "group-42",
		},
	}
	if err := queue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	stored, err := queue.Inspect(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("inspect job: %v", err)
	}
	if err := runtime.DispatchQueuedJob(context.Background(), queue, stored); err == nil {
		t.Fatal("expected dispatch failure for missing plugin target")
	}
	updated, err := queue.Inspect(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("inspect updated job: %v", err)
	}
	if updated.Status != JobStatusDead || updated.LastError == "" || updated.ReasonCode != JobReasonCodeDispatchDead {
		t.Fatalf("expected dispatch failure to move job to dead with reason, got %+v", updated)
	}
}

func TestRuntimeDispatchQueuedJobFailureMovesAlreadyRunningJobOutOfRunningAfterHeartbeat(t *testing.T) {
	t.Parallel()

	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	baseQueue := NewJobQueue()
	baseQueue.SetWorkerIdentity("runtime-local:test-worker")
	queue := &heartbeatCountingQueue{JobQueue: baseQueue}
	job := NewJob("job-running-post-heartbeat-dispatch-fail", "ai.chat", 1, 1200*time.Millisecond)
	job.Payload = map[string]any{
		"dispatch": map[string]any{
			"permission":       "job:run",
			"target_plugin_id": "plugin-missing",
		},
		"reply_handle": map[string]any{
			"capability": "onebot.reply",
			"target_id":  "group-42",
		},
	}
	if err := queue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	running, err := queue.MarkRunning(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("mark running: %v", err)
	}
	stopHeartbeat := startQueuedJobHeartbeat(context.Background(), queue, running)
	defer stopHeartbeat()

	deadline := time.Now().Add(2 * time.Second)
	for queue.HeartbeatCount() < 1 {
		if time.Now().After(deadline) {
			t.Fatalf("expected at least one heartbeat renewal before dispatch failure, last_error=%q", queue.LastHeartbeatError())
		}
		time.Sleep(20 * time.Millisecond)
	}

	stored, err := queue.Inspect(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("inspect running job after heartbeat: %v", err)
	}
	if stored.Status != JobStatusRunning || stored.HeartbeatAt == nil {
		t.Fatalf("expected heartbeated running job before failure, got %+v", stored)
	}

	if err := runtime.DispatchQueuedJob(context.Background(), queue, stored); err == nil {
		t.Fatal("expected dispatch failure for missing plugin target")
	}
	updated, err := queue.Inspect(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("inspect updated job: %v", err)
	}
	if updated.Status != JobStatusRetrying || updated.LastError == "" || updated.ReasonCode != JobReasonCodeDispatchRetry {
		t.Fatalf("expected post-heartbeat dispatch failure to move running job to retrying with dispatch reason, got %+v", updated)
	}
}

func TestRuntimeDispatchQueuedJobReturnsTransitionErrorWhenFailureHandlingCannotInspectJob(t *testing.T) {
	t.Parallel()

	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	baseQueue := NewJobQueue()
	job := NewJob("job-transition-inspect-fail", "ai.chat", 0, 30*time.Second)
	job.Payload = map[string]any{
		"dispatch": map[string]any{
			"permission":       "job:run",
			"target_plugin_id": "plugin-missing",
		},
		"reply_handle": map[string]any{
			"capability": "onebot.reply",
			"target_id":  "group-42",
		},
	}
	if err := baseQueue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	err := runtime.DispatchQueuedJob(context.Background(), failingInspectJobQueue{JobQueue: baseQueue, err: errors.New("inspect unavailable")}, job)
	if err == nil || !strings.Contains(err.Error(), "queued dispatch failure transition also failed") || !strings.Contains(err.Error(), "inspect unavailable") {
		t.Fatalf("expected combined dispatch/transition error, got %v", err)
	}
}

func TestRuntimeDispatchQueuedJobLogsTransitionFailureDetails(t *testing.T) {
	t.Parallel()

	buffer := &bytes.Buffer{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	runtime.SetObservability(NewLogger(buffer), NewTraceRecorder(), NewMetricsRegistry())
	baseQueue := NewJobQueue()
	job := NewJob("job-transition-log-fail", "ai.chat", 0, 30*time.Second)
	job.TraceID = "trace-transition-log-fail"
	job.EventID = "evt-transition-log-fail"
	job.Correlation = "corr-transition-log-fail"
	job.Payload = map[string]any{
		"dispatch": map[string]any{
			"permission":       "job:run",
			"target_plugin_id": "plugin-missing",
		},
		"reply_handle": map[string]any{
			"capability": "onebot.reply",
			"target_id":  "group-42",
		},
	}
	if err := baseQueue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	err := runtime.DispatchQueuedJob(context.Background(), failingInspectJobQueue{JobQueue: baseQueue, err: errors.New("inspect unavailable")}, job)
	if err == nil {
		t.Fatal("expected dispatch failure")
	}
	logs := buffer.String()
	for _, expected := range []string{"runtime queued job dispatch failed", "job-transition-log-fail", "plugin-missing", "inspect unavailable", "trace-transition-log-fail", "corr-transition-log-fail"} {
		if !strings.Contains(logs, expected) {
			t.Fatalf("expected logs to contain %q, got %s", expected, logs)
		}
	}
	entries := decodeRuntimeLogEntries(t, buffer)
	if len(entries) == 0 {
		t.Fatal("expected queued dispatch failure log entry")
	}
	entry := entries[len(entries)-1]
	if entry.Fields["component"] != "runtime" || entry.Fields["operation"] != "dispatch.job.queued" {
		t.Fatalf("expected queued dispatch baseline fields, got %+v", entry)
	}
	if entry.Fields["error_category"] != "internal" || entry.Fields["error_code"] != "queued_dispatch_failed" {
		t.Fatalf("expected queued dispatch error taxonomy, got %+v", entry)
	}
}

func decodeRuntimeLogEntries(t *testing.T, buffer *bytes.Buffer) []LogEntry {
	t.Helper()
	raw := strings.TrimSpace(buffer.String())
	if raw == "" {
		return nil
	}
	lines := strings.Split(raw, "\n")
	entries := make([]LogEntry, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry LogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("decode runtime log entry %q: %v", line, err)
		}
		entries = append(entries, entry)
	}
	return entries
}

func TestRuntimeRejectsInvalidDirectionInputs(t *testing.T) {
	t.Parallel()

	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})

	if err := runtime.RegisterAdapter(AdapterRegistration{}); err == nil {
		t.Fatal("expected invalid adapter registration to fail")
	}
	if err := runtime.DispatchEvent(context.Background(), eventmodel.Event{}); err == nil {
		t.Fatal("expected invalid event to fail dispatch")
	}
	if err := runtime.DispatchCommand(context.Background(), eventmodel.CommandInvocation{}, eventmodel.ExecutionContext{TraceID: "trace", EventID: "evt"}); err == nil {
		t.Fatal("expected invalid command to fail dispatch")
	}
	if err := runtime.DispatchJob(context.Background(), pluginsdk.JobInvocation{Type: "ai.chat"}, eventmodel.ExecutionContext{TraceID: "trace", EventID: "evt"}); err == nil {
		t.Fatal("expected invalid job to fail dispatch")
	}
	if err := runtime.DispatchSchedule(context.Background(), pluginsdk.ScheduleTrigger{ID: "schedule-1"}, eventmodel.ExecutionContext{TraceID: "trace", EventID: "evt"}); err == nil {
		t.Fatal("expected invalid schedule to fail dispatch")
	}
	if err := runtime.DispatchCommand(context.Background(), eventmodel.CommandInvocation{Name: "admin"}, eventmodel.ExecutionContext{}); err == nil {
		t.Fatal("expected invalid execution context to fail dispatch")
	}
}

func TestRuntimeDispatchCapturesFailureWithoutCrashingSuccessfulPlugin(t *testing.T) {
	t.Parallel()

	successHandler := &recordingEventHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})

	plugins := []pluginsdk.Plugin{
		{
			Manifest: pluginsdk.PluginManifest{
				ID:         "plugin-fail",
				Name:       "Fail Plugin",
				Version:    "0.1.0",
				APIVersion: "v0",
				Mode:       pluginsdk.ModeSubprocess,
				Entry:      pluginsdk.PluginEntry{Module: "plugins/fail", Symbol: "Plugin"},
			},
			Handlers: pluginsdk.Handlers{Event: failingEventHandler{}},
		},
		{
			Manifest: pluginsdk.PluginManifest{
				ID:         "plugin-ok",
				Name:       "Ok Plugin",
				Version:    "0.1.0",
				APIVersion: "v0",
				Mode:       pluginsdk.ModeSubprocess,
				Entry:      pluginsdk.PluginEntry{Module: "plugins/ok", Symbol: "Plugin"},
			},
			Handlers: pluginsdk.Handlers{Event: successHandler},
		},
	}

	for _, plugin := range plugins {
		if err := runtime.RegisterPlugin(plugin); err != nil {
			t.Fatalf("register plugin %s: %v", plugin.Manifest.ID, err)
		}
	}

	event := eventmodel.Event{
		EventID:        "evt-2",
		TraceID:        "trace-2",
		Source:         "onebot",
		Type:           "message.received",
		Timestamp:      time.Date(2026, 4, 2, 10, 1, 0, 0, time.UTC),
		IdempotencyKey: "onebot:msg:2",
	}

	if err := runtime.DispatchEvent(context.Background(), event); err != nil {
		t.Fatalf("expected dispatch to continue after one plugin failure, got %v", err)
	}
	if !successHandler.called {
		t.Fatal("expected successful plugin to still receive event")
	}

	results := runtime.DispatchResults()
	if len(results) != 2 {
		t.Fatalf("expected 2 dispatch results, got %+v", results)
	}
	if results[0].Success || !results[1].Success {
		t.Fatalf("expected first result failure and second success, got %+v", results)
	}
}

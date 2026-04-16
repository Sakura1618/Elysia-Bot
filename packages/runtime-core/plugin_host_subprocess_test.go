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

func TestBuildSubprocessHostRequestPreservesBeyondSupportedDeeperNestedTypeMismatch(t *testing.T) {
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

	request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err != nil {
		t.Fatalf("expected request build to stop descending beyond current helper depth, got %v", err)
	}
	if request.InstanceConfig == nil {
		t.Fatal("expected host request to preserve beyond-supported deeper nested instance_config payload")
	}
	if string(request.InstanceConfig) != `{"settings":{"labels":{"naming":{"prefix":true}}}}` {
		t.Fatalf("expected beyond-supported deeper nested mismatch to remain unmodified in raw payload, got %s", string(request.InstanceConfig))
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.InstanceConfig, &decoded); err != nil {
		t.Fatalf("expected beyond-supported deeper nested instance config payload to remain decodable, got %v", err)
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
	prefix, ok := naming["prefix"].(bool)
	if !ok || !prefix {
		t.Fatalf("expected beyond-supported deeper nested type-mismatch value to pass through unchanged, got %+v", decoded)
	}
}

func TestBuiltSubprocessFixtureReceivesBeyondSupportedDeeperNestedTypeMismatch(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-type-mismatch-not-enforced"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-type-mismatch-fixture",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedTypeMismatchFixture",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-type-mismatch-fixture", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{"prefix": true}}}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-beyond-supported-deeper-nested-type-mismatch", EventID: "evt-instance-config-beyond-supported-deeper-nested-type-mismatch"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected built fixture request to receive beyond-supported deeper nested mismatch without rejection, got %v", err)
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

func TestBuildSubprocessHostRequestPreservesBeyondSupportedDeeperNestedArrayValueMismatch(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-array-value-mismatch-request",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedArrayValueMismatchRequest",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-array-value-mismatch-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{"prefix": []any{"oops"}}}}},
	}

	request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err != nil {
		t.Fatalf("expected request build to preserve beyond-supported deeper nested array-valued mismatch, got %v", err)
	}
	if request.InstanceConfig == nil {
		t.Fatal("expected host request to preserve beyond-supported deeper nested array-valued payload")
	}
	if string(request.InstanceConfig) != `{"settings":{"labels":{"naming":{"prefix":["oops"]}}}}` {
		t.Fatalf("expected beyond-supported deeper nested array-valued mismatch to remain unmodified in raw payload, got %s", string(request.InstanceConfig))
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.InstanceConfig, &decoded); err != nil {
		t.Fatalf("expected beyond-supported deeper nested array-valued payload to remain decodable, got %v", err)
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
	prefix, ok := naming["prefix"].([]any)
	if !ok || len(prefix) != 1 {
		t.Fatalf("expected beyond-supported deeper nested array-valued mismatch to pass through unchanged, got %+v", decoded)
	}
	first, ok := prefix[0].(string)
	if !ok || first != "oops" {
		t.Fatalf("expected beyond-supported deeper nested array member to stay unchanged, got %+v", decoded)
	}
}

func TestBuiltSubprocessFixtureReceivesBeyondSupportedDeeperNestedArrayValueMismatch(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-array-value-mismatch-not-enforced"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-array-value-mismatch-fixture",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedArrayValueMismatchFixture",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-array-value-mismatch-fixture", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{"prefix": []any{"oops"}}}}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-beyond-supported-deeper-nested-array-value-mismatch", EventID: "evt-instance-config-beyond-supported-deeper-nested-array-value-mismatch"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected built fixture request to receive beyond-supported deeper nested array-valued mismatch without rejection, got %v", err)
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

func TestBuildSubprocessHostRequestPreservesBeyondSupportedDeeperNestedObjectValueMismatch(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-object-value-mismatch-request",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedObjectValueMismatchRequest",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-object-value-mismatch-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{"prefix": map[string]any{"bad": true}}}}},
	}

	request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err != nil {
		t.Fatalf("expected request build to preserve beyond-supported deeper nested object-valued mismatch, got %v", err)
	}
	if request.InstanceConfig == nil {
		t.Fatal("expected host request to preserve beyond-supported deeper nested object-valued payload")
	}
	if string(request.InstanceConfig) != `{"settings":{"labels":{"naming":{"prefix":{"bad":true}}}}}` {
		t.Fatalf("expected beyond-supported deeper nested object-valued mismatch to remain unmodified in raw payload, got %s", string(request.InstanceConfig))
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.InstanceConfig, &decoded); err != nil {
		t.Fatalf("expected beyond-supported deeper nested object-valued payload to remain decodable, got %v", err)
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
	prefix, ok := naming["prefix"].(map[string]any)
	if !ok {
		t.Fatalf("expected beyond-supported deeper nested object-valued mismatch to pass through unchanged, got %+v", decoded)
	}
	bad, ok := prefix["bad"].(bool)
	if !ok || !bad {
		t.Fatalf("expected beyond-supported deeper nested object-valued member to stay unchanged, got %+v", decoded)
	}
}

func TestBuildSubprocessHostRequestPreservesBeyondSupportedDeeperNestedObjectNodeMismatch(t *testing.T) {
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

	request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err != nil {
		t.Fatalf("expected request build to preserve beyond-supported deeper nested object-node mismatch, got %v", err)
	}
	if request.InstanceConfig == nil {
		t.Fatal("expected host request to preserve beyond-supported deeper nested object-node mismatch payload")
	}
	if string(request.InstanceConfig) != `{"settings":{"labels":{"naming":true}}}` {
		t.Fatalf("expected beyond-supported deeper nested object-node mismatch to remain unmodified in raw payload, got %s", string(request.InstanceConfig))
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.InstanceConfig, &decoded); err != nil {
		t.Fatalf("expected beyond-supported deeper nested object-node payload to remain decodable, got %v", err)
	}
	settings, ok := decoded["settings"].(map[string]any)
	if !ok {
		t.Fatalf("expected settings object to remain present, got %+v", decoded)
	}
	labels, ok := settings["labels"].(map[string]any)
	if !ok {
		t.Fatalf("expected labels object to remain present, got %+v", decoded)
	}
	naming, ok := labels["naming"].(bool)
	if !ok || !naming {
		t.Fatalf("expected beyond-supported deeper nested object-node mismatch to pass through unchanged, got %+v", decoded)
	}
}

func TestBuiltSubprocessFixtureReceivesBeyondSupportedDeeperNestedObjectNodeMismatch(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-object-node-mismatch-not-enforced"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
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
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-beyond-supported-deeper-nested-object-node-mismatch", EventID: "evt-instance-config-beyond-supported-deeper-nested-object-node-mismatch"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected built fixture request to receive beyond-supported deeper nested object-node mismatch without rejection, got %v", err)
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

func TestBuiltSubprocessFixtureReceivesBeyondSupportedDeeperNestedObjectValueMismatch(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-object-value-mismatch-not-enforced"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-object-value-mismatch-fixture",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedObjectValueMismatchFixture",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-object-value-mismatch-fixture", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{"prefix": map[string]any{"bad": true}}}}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-beyond-supported-deeper-nested-object-value-mismatch", EventID: "evt-instance-config-beyond-supported-deeper-nested-object-value-mismatch"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected built fixture request to receive beyond-supported deeper nested object-valued mismatch without rejection, got %v", err)
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

func TestBuildSubprocessHostRequestPreservesBeyondSupportedDeeperNestedRequiredMissing(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-request",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredRequest",
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
											"prefix": map[string]any{"type": "string"},
										},
									},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{}}}},
	}

	request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err != nil {
		t.Fatalf("expected request build to preserve beyond-supported deeper nested required missing boundary, got %v", err)
	}
	if request.InstanceConfig == nil {
		t.Fatal("expected host request to preserve beyond-supported deeper nested required payload")
	}
	if string(request.InstanceConfig) != `{"settings":{"labels":{"naming":{}}}}` {
		t.Fatalf("expected beyond-supported deeper nested required missing payload to remain unmodified in raw payload, got %s", string(request.InstanceConfig))
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.InstanceConfig, &decoded); err != nil {
		t.Fatalf("expected beyond-supported deeper nested required payload to remain decodable, got %v", err)
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
	if len(naming) != 0 {
		t.Fatalf("expected beyond-supported deeper nested required boundary to keep naming object empty, got %+v", decoded)
	}
	if _, exists := naming["prefix"]; exists {
		t.Fatalf("expected beyond-supported deeper nested required member to stay absent in request payload, got %+v", decoded)
	}
}

func TestBuiltSubprocessFixtureReceivesBeyondSupportedDeeperNestedRequiredMissing(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-required-not-enforced"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-fixture",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredFixture",
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
											"prefix": map[string]any{"type": "string"},
										},
									},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-fixture", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{}}}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-beyond-supported-deeper-nested-required", EventID: "evt-instance-config-beyond-supported-deeper-nested-required"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected built fixture request to receive beyond-supported deeper nested required missing without rejection, got %v", err)
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

func TestBuildSubprocessHostRequestPreservesBeyondSupportedDeeperNestedEnumOutOfSet(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-enum-request",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedEnumRequest",
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
											"prefix": map[string]any{"type": "string", "enum": []any{"hello", "world"}},
										},
									},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-enum-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{"prefix": "oops"}}}},
	}

	request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err != nil {
		t.Fatalf("expected request build to preserve beyond-supported deeper nested enum out-of-set boundary, got %v", err)
	}
	if request.InstanceConfig == nil {
		t.Fatal("expected host request to preserve beyond-supported deeper nested enum payload")
	}
	if string(request.InstanceConfig) != `{"settings":{"labels":{"naming":{"prefix":"oops"}}}}` {
		t.Fatalf("expected beyond-supported deeper nested enum payload to remain unmodified in raw payload, got %s", string(request.InstanceConfig))
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.InstanceConfig, &decoded); err != nil {
		t.Fatalf("expected beyond-supported deeper nested enum payload to remain decodable, got %v", err)
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
	if !ok || prefix != "oops" {
		t.Fatalf("expected beyond-supported deeper nested enum value to pass through unchanged, got %+v", decoded)
	}
}

func TestBuiltSubprocessFixtureReceivesBeyondSupportedDeeperNestedEnumOutOfSet(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-enum-not-enforced"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-enum-fixture",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedEnumFixture",
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
											"prefix": map[string]any{"type": "string", "enum": []any{"hello", "world"}},
										},
									},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-enum-fixture", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{"prefix": "oops"}}}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-beyond-supported-deeper-nested-enum", EventID: "evt-instance-config-beyond-supported-deeper-nested-enum"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected built fixture request to receive beyond-supported deeper nested enum out-of-set without rejection, got %v", err)
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

func TestBuildSubprocessHostRequestDoesNotInjectBeyondSupportedDeeperNestedManifestDefaultIntoInstanceConfig(t *testing.T) {
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
		t.Fatalf("expected request build to preserve beyond-supported deeper nested default-only boundary without injection, got %v", err)
	}
	if request.InstanceConfig == nil {
		t.Fatal("expected host request to preserve beyond-supported deeper nested default-only payload")
	}
	if string(request.InstanceConfig) != `{"settings":{"labels":{"naming":{}}}}` {
		t.Fatalf("expected beyond-supported deeper nested default-only payload to remain unmodified in raw payload, got %s", string(request.InstanceConfig))
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.InstanceConfig, &decoded); err != nil {
		t.Fatalf("expected beyond-supported deeper nested default-only payload to remain decodable, got %v", err)
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
	if len(naming) != 0 {
		t.Fatalf("expected beyond-supported deeper nested default-only boundary to keep naming object empty, got %+v", decoded)
	}
	if _, exists := naming["prefix"]; exists {
		t.Fatalf("expected beyond-supported deeper nested default member to stay absent in request payload, got %+v", decoded)
	}
}

func TestBuiltSubprocessFixtureDoesNotReceiveInjectedBeyondSupportedDeeperNestedManifestDefault(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-default-not-injected"))
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
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-beyond-supported-deeper-nested-default", EventID: "evt-instance-config-beyond-supported-deeper-nested-default"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected built fixture request to preserve beyond-supported deeper nested default-only boundary without injection, got %v", err)
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

func TestBuildSubprocessHostRequestPreservesBeyondSupportedDeeperNestedRequiredDefaultBoundary(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-default-request",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredDefaultRequest",
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
											"prefix": map[string]any{"type": "string", "default": "hello"},
										},
									},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-default-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{}}}},
	}

	request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err != nil {
		t.Fatalf("expected request build to preserve beyond-supported deeper nested required+default boundary without rejection, got %v", err)
	}
	if request.InstanceConfig == nil {
		t.Fatal("expected host request to preserve beyond-supported deeper nested required+default payload")
	}
	if string(request.InstanceConfig) != `{"settings":{"labels":{"naming":{}}}}` {
		t.Fatalf("expected beyond-supported deeper nested required+default payload to remain unmodified in raw payload, got %s", string(request.InstanceConfig))
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.InstanceConfig, &decoded); err != nil {
		t.Fatalf("expected beyond-supported deeper nested required+default payload to remain decodable, got %v", err)
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
	if len(naming) != 0 {
		t.Fatalf("expected beyond-supported deeper nested required+default boundary to keep naming object empty, got %+v", decoded)
	}
	if _, exists := naming["prefix"]; exists {
		t.Fatalf("expected beyond-supported deeper nested required+default member to stay absent in request payload, got %+v", decoded)
	}
}

func TestBuiltSubprocessFixtureReceivesBeyondSupportedDeeperNestedRequiredDefaultBoundary(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-required-default-not-enforced"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-default-fixture",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredDefaultFixture",
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
											"prefix": map[string]any{"type": "string", "default": "hello"},
										},
									},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-default-fixture", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{}}}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-beyond-supported-deeper-nested-required-default", EventID: "evt-instance-config-beyond-supported-deeper-nested-required-default"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected built fixture request to preserve beyond-supported deeper nested required+default boundary without rejection, got %v", err)
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

func TestBuildSubprocessHostRequestPreservesBeyondSupportedDeeperNestedEnumDefaultBoundary(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-enum-default-request",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedEnumDefaultRequest",
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
											"prefix": map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"},
										},
									},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-enum-default-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{}}}},
	}

	request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err != nil {
		t.Fatalf("expected request build to preserve beyond-supported deeper nested enum+default boundary without rejection, got %v", err)
	}
	if request.InstanceConfig == nil {
		t.Fatal("expected host request to preserve beyond-supported deeper nested enum+default payload")
	}
	if string(request.InstanceConfig) != `{"settings":{"labels":{"naming":{}}}}` {
		t.Fatalf("expected beyond-supported deeper nested enum+default payload to remain unmodified in raw payload, got %s", string(request.InstanceConfig))
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.InstanceConfig, &decoded); err != nil {
		t.Fatalf("expected beyond-supported deeper nested enum+default payload to remain decodable, got %v", err)
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
	if len(naming) != 0 {
		t.Fatalf("expected beyond-supported deeper nested enum+default boundary to keep naming object empty, got %+v", decoded)
	}
	if _, exists := naming["prefix"]; exists {
		t.Fatalf("expected beyond-supported deeper nested enum+default member to stay absent in request payload, got %+v", decoded)
	}
}

func TestBuiltSubprocessFixtureReceivesBeyondSupportedDeeperNestedEnumDefaultBoundary(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-enum-default-not-enforced"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-enum-default-fixture",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedEnumDefaultFixture",
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
											"prefix": map[string]any{"type": "string", "enum": []any{"hello", "world"}, "default": "hello"},
										},
									},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-enum-default-fixture", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{}}}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-beyond-supported-deeper-nested-enum-default", EventID: "evt-instance-config-beyond-supported-deeper-nested-enum-default"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected built fixture request to preserve beyond-supported deeper nested enum+default boundary without rejection, got %v", err)
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

func TestBuildSubprocessHostRequestPreservesBeyondSupportedDeeperNestedRequiredEnumBoundary(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-request",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredEnumRequest",
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
											"prefix": map[string]any{"type": "string", "enum": []any{"hello", "world"}},
										},
									},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{}}}},
	}

	request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err != nil {
		t.Fatalf("expected request build to preserve beyond-supported deeper nested required+enum boundary without rejection, got %v", err)
	}
	if request.InstanceConfig == nil {
		t.Fatal("expected host request to preserve beyond-supported deeper nested required+enum payload")
	}
	if string(request.InstanceConfig) != `{"settings":{"labels":{"naming":{}}}}` {
		t.Fatalf("expected beyond-supported deeper nested required+enum payload to remain unmodified in raw payload, got %s", string(request.InstanceConfig))
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.InstanceConfig, &decoded); err != nil {
		t.Fatalf("expected beyond-supported deeper nested required+enum payload to remain decodable, got %v", err)
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
	if len(naming) != 0 {
		t.Fatalf("expected beyond-supported deeper nested required+enum boundary to keep naming object empty, got %+v", decoded)
	}
	if _, exists := naming["prefix"]; exists {
		t.Fatalf("expected beyond-supported deeper nested required+enum member to stay absent in request payload, got %+v", decoded)
	}
}

func TestBuiltSubprocessFixtureReceivesBeyondSupportedDeeperNestedRequiredEnumBoundary(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-required-enum-not-enforced"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-fixture",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredEnumFixture",
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
											"prefix": map[string]any{"type": "string", "enum": []any{"hello", "world"}},
										},
									},
								},
							},
						},
					},
				},
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-fixture", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{}}}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-beyond-supported-deeper-nested-required-enum", EventID: "evt-instance-config-beyond-supported-deeper-nested-required-enum"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected built fixture request to preserve beyond-supported deeper nested required+enum boundary without rejection, got %v", err)
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

func TestBuildSubprocessHostRequestPreservesBeyondSupportedDeeperNestedRequiredEnumDefaultBoundary(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-request",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultRequest",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{}}}},
	}

	request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err != nil {
		t.Fatalf("expected request build to preserve beyond-supported deeper nested required+enum+default boundary without rejection, got %v", err)
	}
	if request.InstanceConfig == nil {
		t.Fatal("expected host request to preserve beyond-supported deeper nested required+enum+default payload")
	}
	if string(request.InstanceConfig) != `{"settings":{"labels":{"naming":{}}}}` {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default payload to remain unmodified in raw payload, got %s", string(request.InstanceConfig))
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.InstanceConfig, &decoded); err != nil {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default payload to remain decodable, got %v", err)
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
	if len(naming) != 0 {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default boundary to keep naming object empty, got %+v", decoded)
	}
	if _, exists := naming["prefix"]; exists {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default member to stay absent in request payload, got %+v", decoded)
	}
}

func TestBuildSubprocessHostRequestPreservesBeyondSupportedDeeperNestedRequiredEnumDefaultChildOmissionBoundary(t *testing.T) {
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
		t.Fatalf("expected request build to preserve beyond-supported deeper nested required+enum+default child omission boundary without rejection, got %v", err)
	}
	if request.InstanceConfig == nil {
		t.Fatal("expected host request to preserve beyond-supported deeper nested required+enum+default child omission payload")
	}
	if string(request.InstanceConfig) != `{"settings":{"labels":{}}}` {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default child omission payload to remain unmodified in raw payload, got %s", string(request.InstanceConfig))
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.InstanceConfig, &decoded); err != nil {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default child omission payload to remain decodable, got %v", err)
	}
	settings, ok := decoded["settings"].(map[string]any)
	if !ok {
		t.Fatalf("expected settings object to remain present, got %+v", decoded)
	}
	labels, ok := settings["labels"].(map[string]any)
	if !ok {
		t.Fatalf("expected labels object to remain present, got %+v", decoded)
	}
	if len(labels) != 0 {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default child omission boundary to keep labels object empty, got %+v", decoded)
	}
	if _, exists := labels["naming"]; exists {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default child omission to keep naming key absent in request payload, got %+v", decoded)
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

func TestBuiltSubprocessFixtureReceivesBeyondSupportedDeeperNestedRequiredEnumDefaultBoundary(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-required-enum-default-not-enforced"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-fixture",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultFixture",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-fixture", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{}}}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-beyond-supported-deeper-nested-required-enum-default", EventID: "evt-instance-config-beyond-supported-deeper-nested-required-enum-default"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected built fixture request to preserve beyond-supported deeper nested required+enum+default boundary without rejection, got %v", err)
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

func TestBuildSubprocessHostRequestPreservesBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitBadValue(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-bad-value-request",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitBadValueRequest",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-bad-value-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{"prefix": "oops"}}}},
	}

	request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err != nil {
		t.Fatalf("expected request build to preserve beyond-supported deeper nested required+enum+default explicit bad value without rejection, got %v", err)
	}
	if request.InstanceConfig == nil {
		t.Fatal("expected host request to preserve beyond-supported deeper nested required+enum+default explicit bad value payload")
	}
	if string(request.InstanceConfig) != `{"settings":{"labels":{"naming":{"prefix":"oops"}}}}` {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default explicit bad value payload to remain unmodified in raw payload, got %s", string(request.InstanceConfig))
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.InstanceConfig, &decoded); err != nil {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default explicit bad value payload to remain decodable, got %v", err)
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
	if !ok || prefix != "oops" {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default explicit bad value to pass through unchanged, got %+v", decoded)
	}
}

func TestBuiltSubprocessFixtureReceivesBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitBadValue(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-bad-value-not-enforced"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-bad-value-fixture",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitBadValueFixture",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-bad-value-fixture", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{"prefix": "oops"}}}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-bad-value", EventID: "evt-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-bad-value"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected built fixture request to preserve beyond-supported deeper nested required+enum+default explicit bad value without rejection, got %v", err)
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

func TestBuildSubprocessHostRequestPreservesBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitArrayValuedBadValue(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-array-valued-bad-value-request",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitArrayValuedBadValueRequest",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-array-valued-bad-value-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{"prefix": []any{"oops"}}}}},
	}

	request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err != nil {
		t.Fatalf("expected request build to preserve beyond-supported deeper nested required+enum+default explicit array-valued bad value without rejection, got %v", err)
	}
	if request.InstanceConfig == nil {
		t.Fatal("expected host request to preserve beyond-supported deeper nested required+enum+default explicit array-valued bad value payload")
	}
	if string(request.InstanceConfig) != `{"settings":{"labels":{"naming":{"prefix":["oops"]}}}}` {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default explicit array-valued bad value payload to remain unmodified in raw payload, got %s", string(request.InstanceConfig))
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.InstanceConfig, &decoded); err != nil {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default explicit array-valued bad value payload to remain decodable, got %v", err)
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
	prefix, ok := naming["prefix"].([]any)
	if !ok || len(prefix) != 1 {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default explicit array-valued bad value to pass through unchanged, got %+v", decoded)
	}
	first, ok := prefix[0].(string)
	if !ok || first != "oops" {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default explicit array-valued bad value member to stay unchanged, got %+v", decoded)
	}
}

func TestBuiltSubprocessFixtureReceivesBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitArrayValuedBadValue(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-array-valued-bad-value-not-enforced"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-array-valued-bad-value-fixture",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitArrayValuedBadValueFixture",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-array-valued-bad-value-fixture", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{"prefix": []any{"oops"}}}}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-array-valued-bad-value", EventID: "evt-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-array-valued-bad-value"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected built fixture request to preserve beyond-supported deeper nested required+enum+default explicit array-valued bad value without rejection, got %v", err)
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

func TestBuildSubprocessHostRequestPreservesBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitWrongTypeBadValue(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-wrong-type-bad-value-request",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitWrongTypeBadValueRequest",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-wrong-type-bad-value-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{"prefix": true}}}},
	}

	request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err != nil {
		t.Fatalf("expected request build to preserve beyond-supported deeper nested required+enum+default explicit wrong-type bad value without rejection, got %v", err)
	}
	if request.InstanceConfig == nil {
		t.Fatal("expected host request to preserve beyond-supported deeper nested required+enum+default explicit wrong-type bad value payload")
	}
	if string(request.InstanceConfig) != `{"settings":{"labels":{"naming":{"prefix":true}}}}` {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default explicit wrong-type bad value payload to remain unmodified in raw payload, got %s", string(request.InstanceConfig))
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.InstanceConfig, &decoded); err != nil {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default explicit wrong-type bad value payload to remain decodable, got %v", err)
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
	prefix, ok := naming["prefix"].(bool)
	if !ok || !prefix {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default explicit wrong-type bad value to pass through unchanged, got %+v", decoded)
	}
}

func TestBuiltSubprocessFixtureReceivesBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitWrongTypeBadValue(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-wrong-type-bad-value-not-enforced"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-wrong-type-bad-value-fixture",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitWrongTypeBadValueFixture",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-wrong-type-bad-value-fixture", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{"prefix": true}}}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-wrong-type-bad-value", EventID: "evt-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-wrong-type-bad-value"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected built fixture request to preserve beyond-supported deeper nested required+enum+default explicit wrong-type bad value without rejection, got %v", err)
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

func TestBuildSubprocessHostRequestPreservesBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitObjectValuedBadValue(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-object-valued-bad-value-request",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitObjectValuedBadValueRequest",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-object-valued-bad-value-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{"prefix": map[string]any{"bad": true}}}}},
	}

	request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err != nil {
		t.Fatalf("expected request build to preserve beyond-supported deeper nested required+enum+default explicit object-valued bad value without rejection, got %v", err)
	}
	if request.InstanceConfig == nil {
		t.Fatal("expected host request to preserve beyond-supported deeper nested required+enum+default explicit object-valued bad value payload")
	}
	if string(request.InstanceConfig) != `{"settings":{"labels":{"naming":{"prefix":{"bad":true}}}}}` {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default explicit object-valued bad value payload to remain unmodified in raw payload, got %s", string(request.InstanceConfig))
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.InstanceConfig, &decoded); err != nil {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default explicit object-valued bad value payload to remain decodable, got %v", err)
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
	prefix, ok := naming["prefix"].(map[string]any)
	if !ok {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default explicit object-valued bad value to pass through unchanged, got %+v", decoded)
	}
	bad, ok := prefix["bad"].(bool)
	if !ok || !bad {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default explicit object-valued bad value member to stay unchanged, got %+v", decoded)
	}
}

func TestBuiltSubprocessFixtureReceivesBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitObjectValuedBadValue(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-object-valued-bad-value-not-enforced"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-object-valued-bad-value-fixture",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitObjectValuedBadValueFixture",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-object-valued-bad-value-fixture", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": map[string]any{"prefix": map[string]any{"bad": true}}}}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-object-valued-bad-value", EventID: "evt-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-object-valued-bad-value"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected built fixture request to preserve beyond-supported deeper nested required+enum+default explicit object-valued bad value without rejection, got %v", err)
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

func TestBuildSubprocessHostRequestPreservesBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitObjectNodeBadValue(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-object-node-bad-value-request",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitObjectNodeBadValueRequest",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-object-node-bad-value-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": true}}},
	}

	request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err != nil {
		t.Fatalf("expected request build to preserve beyond-supported deeper nested required+enum+default explicit object-node bad value without rejection, got %v", err)
	}
	if request.InstanceConfig == nil {
		t.Fatal("expected host request to preserve beyond-supported deeper nested required+enum+default explicit object-node bad value payload")
	}
	if string(request.InstanceConfig) != `{"settings":{"labels":{"naming":true}}}` {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default explicit object-node bad value payload to remain unmodified in raw payload, got %s", string(request.InstanceConfig))
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.InstanceConfig, &decoded); err != nil {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default explicit object-node bad value payload to remain decodable, got %v", err)
	}
	settings, ok := decoded["settings"].(map[string]any)
	if !ok {
		t.Fatalf("expected settings object to remain present, got %+v", decoded)
	}
	labels, ok := settings["labels"].(map[string]any)
	if !ok {
		t.Fatalf("expected labels object to remain present, got %+v", decoded)
	}
	naming, ok := labels["naming"].(bool)
	if !ok || !naming {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default explicit object-node bad value to pass through unchanged, got %+v", decoded)
	}
}

func TestBuiltSubprocessFixtureReceivesBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitObjectNodeBadValue(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-object-node-bad-value-not-enforced"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-object-node-bad-value-fixture",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitObjectNodeBadValueFixture",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-object-node-bad-value-fixture", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": true}}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-object-node-bad-value", EventID: "evt-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-object-node-bad-value"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected built fixture request to preserve beyond-supported deeper nested required+enum+default explicit object-node bad value without rejection, got %v", err)
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

func TestBuildSubprocessHostRequestPreservesBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitStringValuedObjectNodeBadValue(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-string-valued-object-node-bad-value-request",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitStringValuedObjectNodeBadValueRequest",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-string-valued-object-node-bad-value-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": "oops"}}},
	}

	request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err != nil {
		t.Fatalf("expected request build to preserve beyond-supported deeper nested required+enum+default explicit string-valued object-node bad value without rejection, got %v", err)
	}
	if request.InstanceConfig == nil {
		t.Fatal("expected host request to preserve beyond-supported deeper nested required+enum+default explicit string-valued object-node bad value payload")
	}
	if string(request.InstanceConfig) != `{"settings":{"labels":{"naming":"oops"}}}` {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default explicit string-valued object-node bad value payload to remain unmodified in raw payload, got %s", string(request.InstanceConfig))
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.InstanceConfig, &decoded); err != nil {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default explicit string-valued object-node bad value payload to remain decodable, got %v", err)
	}
	settings, ok := decoded["settings"].(map[string]any)
	if !ok {
		t.Fatalf("expected settings object to remain present, got %+v", decoded)
	}
	labels, ok := settings["labels"].(map[string]any)
	if !ok {
		t.Fatalf("expected labels object to remain present, got %+v", decoded)
	}
	naming, ok := labels["naming"].(string)
	if !ok || naming != "oops" {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default explicit string-valued object-node bad value to pass through unchanged, got %+v", decoded)
	}
}

func TestBuiltSubprocessFixtureReceivesBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitStringValuedObjectNodeBadValue(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-string-valued-object-node-bad-value-not-enforced"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-string-valued-object-node-bad-value-fixture",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitStringValuedObjectNodeBadValueFixture",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-string-valued-object-node-bad-value-fixture", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": "oops"}}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-string-valued-object-node-bad-value", EventID: "evt-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-string-valued-object-node-bad-value"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected built fixture request to preserve beyond-supported deeper nested required+enum+default explicit string-valued object-node bad value without rejection, got %v", err)
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

func TestBuildSubprocessHostRequestPreservesBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitNumberValuedObjectNodeBadValue(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-number-valued-object-node-bad-value-request",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitNumberValuedObjectNodeBadValueRequest",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-number-valued-object-node-bad-value-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": 7}}},
	}

	request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err != nil {
		t.Fatalf("expected request build to preserve beyond-supported deeper nested required+enum+default explicit number-valued object-node bad value without rejection, got %v", err)
	}
	if request.InstanceConfig == nil {
		t.Fatal("expected host request to preserve beyond-supported deeper nested required+enum+default explicit number-valued object-node bad value payload")
	}
	if string(request.InstanceConfig) != `{"settings":{"labels":{"naming":7}}}` {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default explicit number-valued object-node bad value payload to remain unmodified in raw payload, got %s", string(request.InstanceConfig))
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.InstanceConfig, &decoded); err != nil {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default explicit number-valued object-node bad value payload to remain decodable, got %v", err)
	}
	settings, ok := decoded["settings"].(map[string]any)
	if !ok {
		t.Fatalf("expected settings object to remain present, got %+v", decoded)
	}
	labels, ok := settings["labels"].(map[string]any)
	if !ok {
		t.Fatalf("expected labels object to remain present, got %+v", decoded)
	}
	naming, ok := labels["naming"].(float64)
	if !ok || naming != 7 {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default explicit number-valued object-node bad value to pass through unchanged, got %+v", decoded)
	}
}

func TestBuiltSubprocessFixtureReceivesBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitNumberValuedObjectNodeBadValue(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-number-valued-object-node-bad-value-not-enforced"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-number-valued-object-node-bad-value-fixture",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitNumberValuedObjectNodeBadValueFixture",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-number-valued-object-node-bad-value-fixture", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": 7}}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-number-valued-object-node-bad-value", EventID: "evt-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-number-valued-object-node-bad-value"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected built fixture request to preserve beyond-supported deeper nested required+enum+default explicit number-valued object-node bad value without rejection, got %v", err)
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

func TestBuildSubprocessHostRequestPreservesBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitNullValuedObjectNodeBadValue(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-null-valued-object-node-bad-value-request",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitNullValuedObjectNodeBadValueRequest",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-null-valued-object-node-bad-value-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": nil}}},
	}

	request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err != nil {
		t.Fatalf("expected request build to preserve beyond-supported deeper nested required+enum+default explicit null-valued object-node bad value without rejection, got %v", err)
	}
	if request.InstanceConfig == nil {
		t.Fatal("expected host request to preserve beyond-supported deeper nested required+enum+default explicit null-valued object-node bad value payload")
	}
	if string(request.InstanceConfig) != `{"settings":{"labels":{"naming":null}}}` {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default explicit null-valued object-node bad value payload to remain unmodified in raw payload, got %s", string(request.InstanceConfig))
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.InstanceConfig, &decoded); err != nil {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default explicit null-valued object-node bad value payload to remain decodable, got %v", err)
	}
	settings, ok := decoded["settings"].(map[string]any)
	if !ok {
		t.Fatalf("expected settings object to remain present, got %+v", decoded)
	}
	labels, ok := settings["labels"].(map[string]any)
	if !ok {
		t.Fatalf("expected labels object to remain present, got %+v", decoded)
	}
	naming, exists := labels["naming"]
	if !exists || naming != nil {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default explicit null-valued object-node bad value to pass through unchanged, got %+v", decoded)
	}
}

func TestBuiltSubprocessFixtureReceivesBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitNullValuedObjectNodeBadValue(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-null-valued-object-node-bad-value-not-enforced"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-null-valued-object-node-bad-value-fixture",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitNullValuedObjectNodeBadValueFixture",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-null-valued-object-node-bad-value-fixture", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": nil}}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-null-valued-object-node-bad-value", EventID: "evt-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-null-valued-object-node-bad-value"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected built fixture request to preserve beyond-supported deeper nested required+enum+default explicit null-valued object-node bad value without rejection, got %v", err)
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

func TestBuildSubprocessHostRequestPreservesBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitArrayValuedObjectNodeBadValue(t *testing.T) {
	t.Parallel()

	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-array-valued-object-node-bad-value-request",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitArrayValuedObjectNodeBadValueRequest",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-array-valued-object-node-bad-value-request", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": []any{"oops"}}}},
	}

	request, err := buildSubprocessHostRequest("event", json.RawMessage(`{"event":"ok"}`), plugin)
	if err != nil {
		t.Fatalf("expected request build to preserve beyond-supported deeper nested required+enum+default explicit array-valued object-node bad value without rejection, got %v", err)
	}
	if request.InstanceConfig == nil {
		t.Fatal("expected host request to preserve beyond-supported deeper nested required+enum+default explicit array-valued object-node bad value payload")
	}
	if string(request.InstanceConfig) != `{"settings":{"labels":{"naming":["oops"]}}}` {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default explicit array-valued object-node bad value payload to remain unmodified in raw payload, got %s", string(request.InstanceConfig))
	}
	var decoded map[string]any
	if err := json.Unmarshal(request.InstanceConfig, &decoded); err != nil {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default explicit array-valued object-node bad value payload to remain decodable, got %v", err)
	}
	settings, ok := decoded["settings"].(map[string]any)
	if !ok {
		t.Fatalf("expected settings object to remain present, got %+v", decoded)
	}
	labels, ok := settings["labels"].(map[string]any)
	if !ok {
		t.Fatalf("expected labels object to remain present, got %+v", decoded)
	}
	naming, ok := labels["naming"].([]any)
	if !ok || len(naming) != 1 {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default explicit array-valued object-node bad value to pass through unchanged, got %+v", decoded)
	}
	first, ok := naming[0].(string)
	if !ok || first != "oops" {
		t.Fatalf("expected beyond-supported deeper nested required+enum+default explicit array-valued object-node bad value member to stay unchanged, got %+v", decoded)
	}
}

func TestBuiltSubprocessFixtureReceivesBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitArrayValuedObjectNodeBadValue(t *testing.T) {
	t.Parallel()

	host := NewSubprocessPluginHost(testBuiltPluginProcessFactory(t, "assert-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-array-valued-object-node-bad-value-not-enforced"))
	t.Cleanup(func() { shutdownSubprocessHostForTest(host) })
	plugin := pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-array-valued-object-node-bad-value-fixture",
			Name:       "InstanceConfigBeyondSupportedDeeperNestedRequiredEnumDefaultExplicitArrayValuedObjectNodeBadValueFixture",
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
			Entry: pluginsdk.PluginEntry{Module: "plugins/instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-array-valued-object-node-bad-value-fixture", Symbol: "Plugin"},
		},
		InstanceConfig: map[string]any{"settings": map[string]any{"labels": map[string]any{"naming": []any{"oops"}}}},
	}
	ctx := eventmodel.ExecutionContext{TraceID: "trace-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-array-valued-object-node-bad-value", EventID: "evt-instance-config-beyond-supported-deeper-nested-required-enum-default-explicit-array-valued-object-node-bad-value"}

	if err := host.DispatchEvent(context.Background(), plugin, testPluginEvent(), ctx); err != nil {
		t.Fatalf("expected built fixture request to preserve beyond-supported deeper nested required+enum+default explicit array-valued object-node bad value without rejection, got %v", err)
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

	reader := bufio.NewScanner(os.Stdin)
	crashed := false
	for reader.Scan() {
		line := reader.Text()
		if strings.Contains(line, `"type":"health"`) {
			if mode == "hang-health" {
				time.Sleep(5 * time.Second)
				os.Exit(0)
			}
			_, _ = os.Stdout.WriteString("{\"type\":\"health\",\"status\":\"ok\",\"message\":\"healthy\"}\n")
			continue
		}
		if strings.Contains(line, `"type":"event"`) {
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

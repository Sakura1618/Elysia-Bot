package plugintemplatesmoke

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
	runtimecore "github.com/ohmyopencode/bot-platform/packages/runtime-core"
)

const (
	pluginTemplateSmokeHelperModeHappy        = "template-smoke-subprocess"
	pluginTemplateSmokeHelperModeBadHandshake = "template-smoke-subprocess-bad-handshake"
)

type pluginTemplateSmokeHostRequest struct {
	Type           string          `json:"type"`
	PluginID       string          `json:"plugin_id,omitempty"`
	Event          json.RawMessage `json:"event,omitempty"`
	InstanceConfig json.RawMessage `json:"instance_config,omitempty"`
}

type pluginTemplateSmokeEventPayload struct {
	Event eventmodel.Event            `json:"event"`
	Ctx   eventmodel.ExecutionContext `json:"ctx"`
}

type pluginTemplateSmokeHostResponse struct {
	Type      string                               `json:"type"`
	Status    string                               `json:"status,omitempty"`
	Message   string                               `json:"message,omitempty"`
	Error     string                               `json:"error,omitempty"`
	Callback  string                               `json:"callback,omitempty"`
	ReplyText *pluginTemplateSmokeReplyTextPayload `json:"reply_text,omitempty"`
}

type pluginTemplateSmokeReplyTextPayload struct {
	Handle eventmodel.ReplyHandle `json:"handle"`
	Text   string                 `json:"text"`
}

type pluginTemplateSmokeCallbackResult struct {
	Type   string `json:"type"`
	Status string `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
}

type pluginTemplateSmokeReplyBridge struct {
	reader *bufio.Reader
	stdout *os.File
}

func TestPluginTemplateSmokeCreateRegisterRunFlow(t *testing.T) {
	t.Parallel()

	replies := &recordingReplyService{}
	runtime := runtimecore.NewInMemoryRuntime(runtimecore.NoopSupervisor{}, runtimecore.DirectPluginHost{})
	plugin := New(replies, Config{Prefix: "template: "})
	definition := plugin.Definition()

	if err := runtime.RegisterPlugin(definition); err != nil {
		t.Fatalf("register plugin template smoke: %v", err)
	}

	err := runtime.DispatchEvent(context.Background(), eventmodel.Event{
		EventID:        "evt-template-smoke",
		TraceID:        "trace-template-smoke",
		Source:         "onebot",
		Type:           "message.received",
		Timestamp:      time.Date(2026, 4, 10, 11, 0, 0, 0, time.UTC),
		IdempotencyKey: "onebot:msg:template-smoke",
		Reply:          &eventmodel.ReplyHandle{Capability: "onebot.reply", TargetID: "group-42"},
		Message:        &eventmodel.Message{Text: "hello runtime"},
	})
	if err != nil {
		t.Fatalf("dispatch event: %v", err)
	}
	if replies.text != "template: hello runtime" {
		t.Fatalf("unexpected reply text %q", replies.text)
	}
	if replies.handle.TargetID != "group-42" {
		t.Fatalf("unexpected reply handle %+v", replies.handle)
	}
	if definition.Manifest.ID != TemplatePluginID || definition.Manifest.Mode != pluginsdk.ModeSubprocess {
		t.Fatalf("unexpected template manifest %+v", definition.Manifest)
	}
}

func TestPluginTemplateSmokeSubprocessHostRoundTrip(t *testing.T) {
	t.Parallel()

	replies := &recordingReplyService{}
	host := runtimecore.NewSubprocessPluginHost(testPluginTemplateSmokeProcessFactory(t, pluginTemplateSmokeHelperModeHappy))
	t.Cleanup(func() { _ = host.Close() })
	host.SetReplyTextCallback(replies.ReplyText)

	runtime := runtimecore.NewInMemoryRuntime(runtimecore.NoopSupervisor{}, host)
	plugin := New(nil, Config{}).Definition()
	plugin.InstanceConfig = map[string]any{"prefix": "template: "}

	if err := runtime.RegisterPlugin(plugin); err != nil {
		t.Fatalf("register subprocess template smoke plugin: %v", err)
	}

	err := runtime.DispatchEvent(context.Background(), eventmodel.Event{
		EventID:        "evt-template-smoke-subprocess",
		TraceID:        "trace-template-smoke-subprocess",
		Source:         "onebot",
		Type:           "message.received",
		Timestamp:      time.Date(2026, 4, 24, 9, 0, 0, 0, time.UTC),
		IdempotencyKey: "onebot:msg:template-smoke-subprocess",
		Reply:          &eventmodel.ReplyHandle{Capability: "onebot.reply", TargetID: "group-42"},
		Message:        &eventmodel.Message{Text: "hello subprocess"},
	})
	if err != nil {
		t.Fatalf("dispatch subprocess event: %v", err)
	}

	if replies.text != "template: hello subprocess" {
		t.Fatalf("unexpected subprocess reply text %q", replies.text)
	}
	if replies.handle.Capability != "onebot.reply" || replies.handle.TargetID != "group-42" {
		t.Fatalf("unexpected subprocess reply handle %+v", replies.handle)
	}

	results := runtime.DispatchResults()
	if len(results) != 1 || !results[0].Success || results[0].PluginID != TemplatePluginID || results[0].Kind != "event" {
		t.Fatalf("unexpected subprocess dispatch results %+v", results)
	}

	stdout := strings.Join(host.StdoutLines(), "\n")
	for _, marker := range []string{"handshake-ready", `"callback":"reply_text"`, `"text":"template: hello subprocess"`, "event-ok"} {
		if !strings.Contains(stdout, marker) {
			t.Fatalf("expected subprocess stdout to include %q, got %s", marker, stdout)
		}
	}

	stderr := strings.Join(host.StderrLines(), "\n")
	if !strings.Contains(stderr, "plugin-template-smoke-subprocess-online") {
		t.Fatalf("expected subprocess stderr capture, got %s", stderr)
	}
	if plugin.Manifest.Mode != pluginsdk.ModeSubprocess {
		t.Fatalf("unexpected subprocess plugin mode %q", plugin.Manifest.Mode)
	}
}

func TestPluginTemplateSmokeSubprocessABIHealthCheckSucceeds(t *testing.T) {
	t.Parallel()

	host := runtimecore.NewSubprocessPluginHost(testPluginTemplateSmokeProcessFactory(t, pluginTemplateSmokeHelperModeHappy))
	t.Cleanup(func() { _ = host.Close() })

	if err := host.HealthCheck(context.Background()); err != nil {
		t.Fatalf("health check through template subprocess helper: %v", err)
	}

	stdout := strings.Join(host.StdoutLines(), "\n")
	for _, marker := range []string{"handshake-ready", "healthy"} {
		if !strings.Contains(stdout, marker) {
			t.Fatalf("expected ABI health stdout to include %q, got %s", marker, stdout)
		}
	}

	stderr := strings.Join(host.StderrLines(), "\n")
	if !strings.Contains(stderr, "plugin-template-smoke-subprocess-online") {
		t.Fatalf("expected ABI health stderr capture, got %s", stderr)
	}
}

func TestPluginTemplateSmokeSubprocessABIRejectsInvalidHandshake(t *testing.T) {
	t.Parallel()

	replies := &recordingReplyService{}
	host := runtimecore.NewSubprocessPluginHost(testPluginTemplateSmokeProcessFactory(t, pluginTemplateSmokeHelperModeBadHandshake))
	t.Cleanup(func() { _ = host.Close() })
	host.SetReplyTextCallback(replies.ReplyText)

	runtime := runtimecore.NewInMemoryRuntime(runtimecore.NoopSupervisor{}, host)
	plugin := New(nil, Config{}).Definition()
	plugin.InstanceConfig = map[string]any{"prefix": "template: "}

	if err := runtime.RegisterPlugin(plugin); err != nil {
		t.Fatalf("register ABI drift template smoke plugin: %v", err)
	}

	err := runtime.DispatchEvent(context.Background(), eventmodel.Event{
		EventID:        "evt-template-smoke-subprocess-bad-handshake",
		TraceID:        "trace-template-smoke-subprocess-bad-handshake",
		Source:         "onebot",
		Type:           "message.received",
		Timestamp:      time.Date(2026, 4, 24, 9, 5, 0, 0, time.UTC),
		IdempotencyKey: "onebot:msg:template-smoke-subprocess-bad-handshake",
		Reply:          &eventmodel.ReplyHandle{Capability: "onebot.reply", TargetID: "group-42"},
		Message:        &eventmodel.Message{Text: "hello incompatible child"},
	})
	if err == nil || !strings.Contains(err.Error(), "dispatch completed with no successful plugin deliveries") || !strings.Contains(err.Error(), "subprocess handshake failed") || !strings.Contains(err.Error(), "invalid handshake response") {
		t.Fatalf("expected fail-closed invalid handshake rejection, got %v", err)
	}
	if replies.text != "" {
		t.Fatalf("expected invalid handshake to prevent reply callback, got %q", replies.text)
	}

	results := runtime.DispatchResults()
	if len(results) != 1 || results[0].Success || results[0].PluginID != TemplatePluginID || results[0].Kind != "event" {
		t.Fatalf("unexpected ABI drift dispatch results %+v", results)
	}
	if !strings.Contains(results[0].Error, "subprocess handshake failed") || !strings.Contains(results[0].Error, "invalid handshake response") {
		t.Fatalf("expected failed dispatch result to record handshake ABI drift, got %+v", results[0])
	}

	stdout := strings.Join(host.StdoutLines(), "\n")
	if !strings.Contains(stdout, `"message":"abi-mismatch-handshake"`) {
		t.Fatalf("expected invalid handshake marker in stdout, got %s", stdout)
	}
	for _, unexpected := range []string{"event-ok", `"callback":"reply_text"`} {
		if strings.Contains(stdout, unexpected) {
			t.Fatalf("expected fail-closed invalid handshake to skip %q, got %s", unexpected, stdout)
		}
	}

	stderr := strings.Join(host.StderrLines(), "\n")
	if !strings.Contains(stderr, "plugin-template-smoke-subprocess-online") {
		t.Fatalf("expected invalid handshake stderr capture, got %s", stderr)
	}
}

func TestPluginTemplateSmokeConfigSchemaRejectsInvalidManifestShapeBeforeSubprocessSideEffects(t *testing.T) {
	t.Parallel()

	replies := &recordingReplyService{}
	host := runtimecore.NewSubprocessPluginHost(testPluginTemplateSmokeProcessFactory(t, pluginTemplateSmokeHelperModeHappy))
	t.Cleanup(func() { _ = host.Close() })
	host.SetReplyTextCallback(replies.ReplyText)

	runtime := runtimecore.NewInMemoryRuntime(runtimecore.NoopSupervisor{}, host)
	plugin := New(nil, Config{}).Definition()
	plugin.Manifest.ConfigSchema["type"] = "string"
	plugin.InstanceConfig = map[string]any{"prefix": "template: "}

	if err := runtime.RegisterPlugin(plugin); err != nil {
		t.Fatalf("register invalid-config-schema template smoke plugin: %v", err)
	}

	err := runtime.DispatchEvent(context.Background(), testPluginTemplateSmokeSubprocessEvent("evt-template-smoke-invalid-config-schema", "trace-template-smoke-invalid-config-schema", "onebot:msg:template-smoke-invalid-config-schema", "hello invalid config schema"))
	if err == nil || !strings.Contains(err.Error(), "dispatch completed with no successful plugin deliveries") || !strings.Contains(err.Error(), `config schema top-level type must be "object", got "string"`) {
		t.Fatalf("expected invalid manifest config schema rejection, got %v", err)
	}
	if replies.text != "" {
		t.Fatalf("expected invalid manifest config schema rejection to prevent reply callback, got %q", replies.text)
	}

	results := runtime.DispatchResults()
	if len(results) != 1 || results[0].Success || results[0].PluginID != TemplatePluginID || results[0].Kind != "event" {
		t.Fatalf("unexpected invalid manifest config schema dispatch results %+v", results)
	}
	if !strings.Contains(results[0].Error, `plugin host dispatch "`+TemplatePluginID+`"`) || !strings.Contains(results[0].Error, `config schema top-level type must be "object", got "string"`) {
		t.Fatalf("expected failed dispatch result to record manifest config schema rejection, got %+v", results[0])
	}
	if stdout := strings.Join(host.StdoutLines(), "\n"); stdout != "" {
		t.Fatalf("expected invalid manifest config schema rejection to avoid subprocess stdout side effects, got %s", stdout)
	}
	if stderr := strings.Join(host.StderrLines(), "\n"); stderr != "" {
		t.Fatalf("expected invalid manifest config schema rejection to avoid subprocess stderr side effects, got %s", stderr)
	}
}

func TestPluginTemplateSmokeConfigSchemaRejectsInvalidPrefixInstanceConfigTypeBeforeSubprocessSideEffects(t *testing.T) {
	t.Parallel()

	replies := &recordingReplyService{}
	host := runtimecore.NewSubprocessPluginHost(testPluginTemplateSmokeProcessFactory(t, pluginTemplateSmokeHelperModeHappy))
	t.Cleanup(func() { _ = host.Close() })
	host.SetReplyTextCallback(replies.ReplyText)

	runtime := runtimecore.NewInMemoryRuntime(runtimecore.NoopSupervisor{}, host)
	plugin := New(nil, Config{}).Definition()
	plugin.InstanceConfig = map[string]any{"prefix": true}

	if err := runtime.RegisterPlugin(plugin); err != nil {
		t.Fatalf("register invalid-instance-config template smoke plugin: %v", err)
	}

	err := runtime.DispatchEvent(context.Background(), testPluginTemplateSmokeSubprocessEvent("evt-template-smoke-invalid-instance-config", "trace-template-smoke-invalid-instance-config", "onebot:msg:template-smoke-invalid-instance-config", "hello invalid instance config"))
	if err == nil || !strings.Contains(err.Error(), "dispatch completed with no successful plugin deliveries") || !strings.Contains(err.Error(), `instance config property "prefix" value type must match declared type "string", got "boolean"`) {
		t.Fatalf("expected invalid prefix instance config rejection, got %v", err)
	}
	if replies.text != "" {
		t.Fatalf("expected invalid prefix instance config rejection to prevent reply callback, got %q", replies.text)
	}

	results := runtime.DispatchResults()
	if len(results) != 1 || results[0].Success || results[0].PluginID != TemplatePluginID || results[0].Kind != "event" {
		t.Fatalf("unexpected invalid prefix instance config dispatch results %+v", results)
	}
	if !strings.Contains(results[0].Error, `plugin host dispatch "`+TemplatePluginID+`"`) || !strings.Contains(results[0].Error, `instance config property "prefix" value type must match declared type "string", got "boolean"`) {
		t.Fatalf("expected failed dispatch result to record prefix instance config rejection, got %+v", results[0])
	}
	if stdout := strings.Join(host.StdoutLines(), "\n"); stdout != "" {
		t.Fatalf("expected invalid prefix instance config rejection to avoid subprocess stdout side effects, got %s", stdout)
	}
	if stderr := strings.Join(host.StderrLines(), "\n"); stderr != "" {
		t.Fatalf("expected invalid prefix instance config rejection to avoid subprocess stderr side effects, got %s", stderr)
	}
}

func testPluginTemplateSmokeSubprocessEvent(eventID string, traceID string, idempotencyKey string, text string) eventmodel.Event {
	return eventmodel.Event{
		EventID:        eventID,
		TraceID:        traceID,
		Source:         "onebot",
		Type:           "message.received",
		Timestamp:      time.Date(2026, 4, 24, 9, 10, 0, 0, time.UTC),
		IdempotencyKey: idempotencyKey,
		Reply:          &eventmodel.ReplyHandle{Capability: "onebot.reply", TargetID: "group-42"},
		Message:        &eventmodel.Message{Text: text},
	}
}

func testPluginTemplateSmokeProcessFactory(t *testing.T, mode string) func(context.Context) *exec.Cmd {
	t.Helper()
	if strings.TrimSpace(mode) == "" {
		mode = pluginTemplateSmokeHelperModeHappy
	}

	return func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestHelperPluginTemplateSmokeSubprocessProcess", "--", mode)
		cmd.Env = append(os.Environ(), "GO_WANT_PLUGIN_TEMPLATE_SMOKE_SUBPROCESS=1")
		return cmd
	}
}

func TestHelperPluginTemplateSmokeSubprocessProcess(t *testing.T) {
	if os.Getenv("GO_WANT_PLUGIN_TEMPLATE_SMOKE_SUBPROCESS") != "1" {
		return
	}
	mode := strings.TrimSpace(os.Args[len(os.Args)-1])
	if mode != pluginTemplateSmokeHelperModeHappy && mode != pluginTemplateSmokeHelperModeBadHandshake {
		os.Exit(2)
	}

	reader := bufio.NewReader(os.Stdin)
	_, _ = os.Stderr.WriteString("plugin-template-smoke-subprocess-online\n")
	handshake := pluginTemplateSmokeHostResponse{Type: "handshake", Status: "ok", Message: "handshake-ready"}
	if mode == pluginTemplateSmokeHelperModeBadHandshake {
		handshake = pluginTemplateSmokeHostResponse{Type: "event", Status: "ok", Message: "abi-mismatch-handshake"}
	}
	if err := writePluginTemplateSmokeResponse(os.Stdout, handshake); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "write handshake: %v\n", err)
		os.Exit(1)
	}
	if mode == pluginTemplateSmokeHelperModeBadHandshake {
		for {
			if _, err := reader.ReadString('\n'); err != nil {
				os.Exit(0)
			}
		}
	}

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			os.Exit(0)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var request pluginTemplateSmokeHostRequest
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "decode request: %v\n", err)
			os.Exit(1)
		}

		switch request.Type {
		case "health":
			if err := writePluginTemplateSmokeResponse(os.Stdout, pluginTemplateSmokeHostResponse{Type: "health", Status: "ok", Message: "healthy"}); err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "write health response: %v\n", err)
				os.Exit(1)
			}
		case "event":
			if err := handlePluginTemplateSmokeSubprocessEvent(reader, os.Stdout, request.Event, request.InstanceConfig); err != nil {
				if writeErr := writePluginTemplateSmokeResponse(os.Stdout, pluginTemplateSmokeHostResponse{Type: "event", Status: "error", Error: err.Error()}); writeErr != nil {
					_, _ = fmt.Fprintf(os.Stderr, "write event error response: %v\n", writeErr)
					os.Exit(1)
				}
				continue
			}
			if err := writePluginTemplateSmokeResponse(os.Stdout, pluginTemplateSmokeHostResponse{Type: "event", Status: "ok", Message: "event-ok"}); err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "write event response: %v\n", err)
				os.Exit(1)
			}
		default:
			if err := writePluginTemplateSmokeResponse(os.Stdout, pluginTemplateSmokeHostResponse{Type: request.Type, Status: "error", Error: fmt.Sprintf("unsupported request type %q", request.Type)}); err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "write unsupported request response: %v\n", err)
				os.Exit(1)
			}
		}
	}
}

func handlePluginTemplateSmokeSubprocessEvent(reader *bufio.Reader, stdout *os.File, rawPayload json.RawMessage, rawInstanceConfig json.RawMessage) error {
	var payload pluginTemplateSmokeEventPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		return fmt.Errorf("decode event payload: %w", err)
	}

	config := Config{}
	if len(rawInstanceConfig) > 0 {
		if err := json.Unmarshal(rawInstanceConfig, &config); err != nil {
			return fmt.Errorf("decode instance config: %w", err)
		}
	}

	plugin := New(pluginTemplateSmokeReplyBridge{reader: reader, stdout: stdout}, config)
	return plugin.OnEvent(payload.Event, payload.Ctx)
}

func (b pluginTemplateSmokeReplyBridge) ReplyText(handle eventmodel.ReplyHandle, text string) error {
	if err := writePluginTemplateSmokeResponse(b.stdout, pluginTemplateSmokeHostResponse{
		Type:     "callback",
		Callback: "reply_text",
		ReplyText: &pluginTemplateSmokeReplyTextPayload{
			Handle: handle,
			Text:   text,
		},
	}); err != nil {
		return fmt.Errorf("write reply_text callback: %w", err)
	}

	line, err := b.reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read reply_text callback result: %w", err)
	}

	var result pluginTemplateSmokeCallbackResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &result); err != nil {
		return fmt.Errorf("decode reply_text callback result: %w", err)
	}
	if result.Type != "callback_result" {
		return fmt.Errorf("unexpected reply_text callback result type %q", result.Type)
	}
	if result.Status != "ok" {
		if strings.TrimSpace(result.Error) == "" {
			return fmt.Errorf("reply_text callback failed")
		}
		return fmt.Errorf("reply_text callback failed: %s", result.Error)
	}
	return nil
}

func (pluginTemplateSmokeReplyBridge) ReplyImage(handle eventmodel.ReplyHandle, imageURL string) error {
	return fmt.Errorf("reply image not supported in template smoke subprocess helper: %s %s", handle.TargetID, imageURL)
}

func (pluginTemplateSmokeReplyBridge) ReplyFile(handle eventmodel.ReplyHandle, fileURL string) error {
	return fmt.Errorf("reply file not supported in template smoke subprocess helper: %s %s", handle.TargetID, fileURL)
}

func writePluginTemplateSmokeResponse(stdout *os.File, response pluginTemplateSmokeHostResponse) error {
	encoded, err := json.Marshal(response)
	if err != nil {
		return err
	}
	_, err = stdout.WriteString(string(encoded) + "\n")
	return err
}

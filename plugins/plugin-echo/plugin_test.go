package pluginecho

import (
	"errors"
	"testing"
	"time"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

type recordingReplyService struct {
	handle eventmodel.ReplyHandle
	text   string
	err    error
}

func (r *recordingReplyService) ReplyText(handle eventmodel.ReplyHandle, text string) error {
	r.handle = handle
	r.text = text
	return r.err
}

func (r *recordingReplyService) ReplyImage(handle eventmodel.ReplyHandle, imageURL string) error {
	return nil
}

func (r *recordingReplyService) ReplyFile(handle eventmodel.ReplyHandle, fileURL string) error {
	return nil
}

func TestPluginEchoRepliesToMessageEvent(t *testing.T) {
	t.Parallel()

	replies := &recordingReplyService{}
	plugin := New(replies, Config{Prefix: "echo: "})

	err := plugin.OnEvent(eventmodel.Event{
		EventID:        "evt-echo",
		TraceID:        "trace-echo",
		Source:         "onebot",
		Type:           "message.received",
		Timestamp:      time.Date(2026, 4, 2, 15, 0, 0, 0, time.UTC),
		IdempotencyKey: "onebot:msg:echo",
		Message:        &eventmodel.Message{Text: "hello"},
	}, eventmodel.ExecutionContext{
		TraceID: "trace-echo",
		EventID: "evt-echo",
		Reply:   &eventmodel.ReplyHandle{Capability: "onebot.reply", TargetID: "group-42"},
	})
	if err != nil {
		t.Fatalf("on event: %v", err)
	}
	if replies.text != "echo: hello" {
		t.Fatalf("unexpected reply text %q", replies.text)
	}
	if replies.handle.TargetID != "group-42" {
		t.Fatalf("unexpected reply handle %+v", replies.handle)
	}
}

func TestPluginEchoRequiresReplyHandle(t *testing.T) {
	t.Parallel()

	plugin := New(&recordingReplyService{}, Config{})
	err := plugin.OnEvent(eventmodel.Event{
		EventID:        "evt-echo",
		TraceID:        "trace-echo",
		Source:         "onebot",
		Type:           "message.received",
		Timestamp:      time.Date(2026, 4, 2, 15, 0, 0, 0, time.UTC),
		IdempotencyKey: "onebot:msg:echo",
		Message:        &eventmodel.Message{Text: "hello"},
	}, eventmodel.ExecutionContext{TraceID: "trace-echo", EventID: "evt-echo"})
	if err == nil {
		t.Fatal("expected missing reply handle to fail")
	}
}

func TestPluginEchoReturnsReplyError(t *testing.T) {
	t.Parallel()

	plugin := New(&recordingReplyService{err: errors.New("send failed")}, Config{})
	err := plugin.OnEvent(eventmodel.Event{
		EventID:        "evt-echo",
		TraceID:        "trace-echo",
		Source:         "onebot",
		Type:           "message.received",
		Timestamp:      time.Date(2026, 4, 2, 15, 0, 0, 0, time.UTC),
		IdempotencyKey: "onebot:msg:echo",
		Message:        &eventmodel.Message{Text: "hello"},
	}, eventmodel.ExecutionContext{
		TraceID: "trace-echo",
		EventID: "evt-echo",
		Reply:   &eventmodel.ReplyHandle{Capability: "onebot.reply", TargetID: "group-42"},
	})
	if err == nil {
		t.Fatal("expected reply service error to bubble up")
	}
}

func TestPluginEchoManifestAdoptsV1Contract(t *testing.T) {
	t.Parallel()

	plugin := New(&recordingReplyService{}, Config{})
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
	if manifest.Publish.SourceURI != pluginEchoPublishSourceURI {
		t.Fatalf("manifest publish sourceUri = %q, want %q", manifest.Publish.SourceURI, pluginEchoPublishSourceURI)
	}
	if manifest.Publish.RuntimeVersionRange != pluginEchoRuntimeVersionRange {
		t.Fatalf("manifest publish runtimeVersionRange = %q, want %q", manifest.Publish.RuntimeVersionRange, pluginEchoRuntimeVersionRange)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("expected plugin-echo manifest to validate, got %v", err)
	}
}

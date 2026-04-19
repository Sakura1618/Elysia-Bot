package adapterwebhook

import (
	"bytes"
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
	runtimecore "github.com/ohmyopencode/bot-platform/packages/runtime-core"
)

type recordingDispatcher struct {
	event eventmodel.Event
	err   error
}

type auditRecorder struct {
	entries []pluginsdk.AuditEntry
}

type failingSecretProvider struct {
	err error
}

func (d *recordingDispatcher) DispatchEvent(ctx context.Context, event eventmodel.Event) error {
	d.event = event
	return d.err
}

func (r *auditRecorder) RecordAudit(entry pluginsdk.AuditEntry) error {
	r.entries = append(r.entries, entry)
	return nil
}

func (p failingSecretProvider) ResolveSecret(context.Context, string) (string, error) {
	return "", p.err
}

func TestWebhookAdapterMapsAndDispatchesEvent(t *testing.T) {
	t.Parallel()

	dispatcher := &recordingDispatcher{}
	adapter := New("secret", dispatcher, runtimecore.NewLogger(&bytes.Buffer{}), nil)
	request := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"event_type":"webhook.received","source":"webhook","actor_id":"svc-1","text":"hello"}`))
	request.Header.Set("X-Webhook-Token", "secret")
	recorder := httptest.NewRecorder()

	adapter.HandleWebhook(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	if dispatcher.event.Type != "webhook.received" || dispatcher.event.Source != "webhook" {
		t.Fatalf("unexpected dispatched event %+v", dispatcher.event)
	}
}

func TestWebhookAdapterRejectsUnauthorizedRequest(t *testing.T) {
	t.Parallel()

	audits := &auditRecorder{}
	logs := &bytes.Buffer{}
	tracer := runtimecore.NewTraceRecorder()
	adapter := New("secret", &recordingDispatcher{}, runtimecore.NewLogger(logs), audits)
	adapter.Tracer = tracer
	request := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"event_type":"webhook.received","source":"webhook"}`))
	request.RemoteAddr = "127.0.0.1:9999"
	recorder := httptest.NewRecorder()

	adapter.HandleWebhook(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("expected application/json content type, got %q", contentType)
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["status"] != "error" || payload["code"] != webhookUnauthorizedCode || payload["message"] != webhookUnauthorizedMessage {
		t.Fatalf("expected stable unauthorized failure payload, got %+v", payload)
	}
	traceID, _ := payload["trace_id"].(string)
	if traceID == "" {
		t.Fatalf("expected trace_id in unauthorized failure payload, got %+v", payload)
	}
	if _, ok := payload["event_id"]; ok {
		t.Fatalf("expected unauthorized rejection before event creation, got %+v", payload)
	}
	if len(audits.entries) != 1 || audits.entries[0].Action != "webhook.reject.unauthorized" || audits.entries[0].Actor != "127.0.0.1:9999" || audits.entries[0].Allowed || audits.entries[0].Reason != "webhook_unauthorized" {
		t.Fatalf("unexpected audit entries %+v", audits.entries)
	}
	assertRejectObservability(t, logs, tracer, traceID, "adapter.ingress.unauthorized.failure", "webhook request rejected as unauthorized", webhookUnauthorizedCode, webhookUnauthorizedCode)
}

func TestWebhookAdapterRejectsInvalidEventPayload(t *testing.T) {
	t.Parallel()

	audits := &auditRecorder{}
	logs := &bytes.Buffer{}
	tracer := runtimecore.NewTraceRecorder()
	adapter := New("", &recordingDispatcher{}, runtimecore.NewLogger(logs), audits)
	adapter.Tracer = tracer
	request := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"source":"webhook"}`))
	recorder := httptest.NewRecorder()

	adapter.HandleWebhook(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("expected application/json content type, got %q", contentType)
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["status"] != "error" || payload["code"] != webhookInvalidEventCode || payload["message"] != "event_type is required" {
		t.Fatalf("expected stable invalid-event failure payload, got %+v", payload)
	}
	traceID, _ := payload["trace_id"].(string)
	if traceID == "" {
		t.Fatalf("expected trace_id in invalid-event failure payload, got %+v", payload)
	}
	if _, ok := payload["event_id"]; ok {
		t.Fatalf("expected invalid-event rejection before event creation, got %+v", payload)
	}
	if len(audits.entries) != 1 || audits.entries[0].Action != "webhook.reject.invalid_event" || audits.entries[0].Target != "webhook" || audits.entries[0].Allowed || audits.entries[0].Reason != "webhook_invalid_event" {
		t.Fatalf("unexpected audit entries %+v", audits.entries)
	}
	assertRejectObservability(t, logs, tracer, traceID, "adapter.ingress.invalid_event.failure", "webhook payload rejected as invalid event", webhookInvalidEventCode, webhookInvalidEventCode)
}

func TestWebhookAdapterAuditsMalformedPayload(t *testing.T) {
	t.Parallel()

	audits := &auditRecorder{}
	logs := &bytes.Buffer{}
	tracer := runtimecore.NewTraceRecorder()
	adapter := New("", &recordingDispatcher{}, runtimecore.NewLogger(logs), audits)
	adapter.Tracer = tracer
	request := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"event_type":`))
	request.RemoteAddr = "10.0.0.8:8080"
	recorder := httptest.NewRecorder()

	adapter.HandleWebhook(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("expected application/json content type, got %q", contentType)
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["status"] != "error" || payload["code"] != webhookInvalidPayloadCode || payload["message"] != webhookInvalidPayloadMessage {
		t.Fatalf("expected stable invalid-payload failure payload, got %+v", payload)
	}
	traceID, _ := payload["trace_id"].(string)
	if traceID == "" {
		t.Fatalf("expected trace_id in invalid-payload failure payload, got %+v", payload)
	}
	if _, ok := payload["event_id"]; ok {
		t.Fatalf("expected invalid-payload rejection before event creation, got %+v", payload)
	}
	if len(audits.entries) != 1 || audits.entries[0].Action != "webhook.reject.invalid_payload" || audits.entries[0].Actor != "10.0.0.8:8080" || audits.entries[0].Allowed || audits.entries[0].Reason != "webhook_invalid_payload" {
		t.Fatalf("unexpected audit entries %+v", audits.entries)
	}
	assertRejectObservability(t, logs, tracer, traceID, "adapter.ingress.invalid_payload.failure", "webhook payload rejected as invalid", webhookInvalidPayloadCode, webhookInvalidPayloadCode)
}

func TestWebhookAdapterRejectsTrailingExtraJSONAsInvalidPayload(t *testing.T) {
	t.Parallel()

	audits := &auditRecorder{}
	logs := &bytes.Buffer{}
	tracer := runtimecore.NewTraceRecorder()
	adapter := New("", &recordingDispatcher{}, runtimecore.NewLogger(logs), audits)
	adapter.Tracer = tracer
	request := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"event_type":"webhook.received","source":"webhook"}{"unexpected":true}`))
	request.RemoteAddr = "10.0.0.9:8080"
	recorder := httptest.NewRecorder()

	adapter.HandleWebhook(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("expected application/json content type, got %q", contentType)
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["status"] != "error" || payload["code"] != webhookInvalidPayloadCode || payload["message"] != webhookInvalidPayloadMessage {
		t.Fatalf("expected stable invalid-payload failure payload, got %+v", payload)
	}
	traceID, _ := payload["trace_id"].(string)
	if traceID == "" {
		t.Fatalf("expected trace_id in invalid-payload failure payload, got %+v", payload)
	}
	if _, ok := payload["event_id"]; ok {
		t.Fatalf("expected invalid-payload rejection before event creation, got %+v", payload)
	}
	if len(audits.entries) != 1 || audits.entries[0].Action != "webhook.reject.invalid_payload" || audits.entries[0].Actor != "10.0.0.9:8080" || audits.entries[0].Allowed || audits.entries[0].Reason != "webhook_invalid_payload" {
		t.Fatalf("unexpected audit entries %+v", audits.entries)
	}
	assertRejectObservability(t, logs, tracer, traceID, "adapter.ingress.invalid_payload.failure", "webhook payload rejected as invalid", webhookInvalidPayloadCode, webhookInvalidPayloadCode)
}

func TestWebhookAdapterRejectsUnknownTopLevelFieldAsInvalidPayload(t *testing.T) {
	t.Parallel()

	audits := &auditRecorder{}
	logs := &bytes.Buffer{}
	tracer := runtimecore.NewTraceRecorder()
	adapter := New("", &recordingDispatcher{}, runtimecore.NewLogger(logs), audits)
	adapter.Tracer = tracer
	request := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"event_type":"webhook.received","source":"webhook","unknown_field":true}`))
	request.RemoteAddr = "10.0.0.10:8080"
	recorder := httptest.NewRecorder()

	adapter.HandleWebhook(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("expected application/json content type, got %q", contentType)
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["status"] != "error" || payload["code"] != webhookInvalidPayloadCode || payload["message"] != webhookInvalidPayloadMessage {
		t.Fatalf("expected stable invalid-payload failure payload, got %+v", payload)
	}
	traceID, _ := payload["trace_id"].(string)
	if traceID == "" {
		t.Fatalf("expected trace_id in invalid-payload failure payload, got %+v", payload)
	}
	if _, ok := payload["event_id"]; ok {
		t.Fatalf("expected invalid-payload rejection before event creation, got %+v", payload)
	}
	if len(audits.entries) != 1 || audits.entries[0].Action != "webhook.reject.invalid_payload" || audits.entries[0].Actor != "10.0.0.10:8080" || audits.entries[0].Allowed || audits.entries[0].Reason != "webhook_invalid_payload" {
		t.Fatalf("unexpected audit entries %+v", audits.entries)
	}
	assertRejectObservability(t, logs, tracer, traceID, "adapter.ingress.invalid_payload.failure", "webhook payload rejected as invalid", webhookInvalidPayloadCode, webhookInvalidPayloadCode)
}

func TestWebhookAdapterResponseContainsEventIDs(t *testing.T) {
	t.Parallel()

	adapter := New("", &recordingDispatcher{}, runtimecore.NewLogger(&bytes.Buffer{}), nil)
	request := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"event_type":"webhook.received","source":"webhook"}`))
	recorder := httptest.NewRecorder()

	adapter.HandleWebhook(recorder, request)

	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["event_id"] == "" || payload["trace_id"] == "" {
		t.Fatalf("expected event_id and trace_id in response, got %+v", payload)
	}
}

func TestWebhookAdapterRecordsIngressObservability(t *testing.T) {
	t.Parallel()

	logs := &bytes.Buffer{}
	tracer := runtimecore.NewTraceRecorder()
	dispatcher := &recordingDispatcher{}
	adapter := New("", dispatcher, runtimecore.NewLogger(logs), nil)
	adapter.Tracer = tracer
	request := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"event_type":"webhook.received","source":"webhook","actor_id":"svc-1","text":"hello"}`))
	recorder := httptest.NewRecorder()

	adapter.HandleWebhook(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	if !strings.Contains(logs.String(), "webhook:webhook:webhook.received:") {
		t.Fatalf("expected webhook correlation in log, got %s", logs.String())
	}
	if !strings.Contains(tracer.RenderTrace(dispatcher.event.TraceID), "adapter.ingress") {
		t.Fatalf("expected ingress span, got %s", tracer.RenderTrace(dispatcher.event.TraceID))
	}
}

func TestWebhookAdapterObservabilityCoversDispatchFailure(t *testing.T) {
	t.Parallel()

	logs := &bytes.Buffer{}
	dispatcher := &recordingDispatcher{err: errors.New("dispatch boom")}
	adapter := New("", dispatcher, runtimecore.NewLogger(logs), nil)
	adapter.Tracer = runtimecore.NewTraceRecorder()
	request := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"event_type":"webhook.received","source":"webhook","actor_id":"svc-1","text":"hello"}`))
	recorder := httptest.NewRecorder()

	adapter.HandleWebhook(recorder, request)

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("expected application/json content type, got %q", contentType)
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["status"] != "error" || payload["code"] != dispatchFailureCode || payload["message"] != dispatchFailureMessage {
		t.Fatalf("expected stable failure payload, got %+v", payload)
	}
	if payload["trace_id"] == "" || payload["event_id"] == "" {
		t.Fatalf("expected trace_id and event_id in failure payload, got %+v", payload)
	}
	if payload["trace_id"] != dispatcher.event.TraceID || payload["event_id"] != dispatcher.event.EventID {
		t.Fatalf("expected caller-facing ids to match dispatched event, got payload=%+v event=%+v", payload, dispatcher.event)
	}
	if strings.Contains(recorder.Body.String(), "dispatch boom") {
		t.Fatalf("expected internal dispatch error to stay hidden from caller, got %s", recorder.Body.String())
	}
	if !strings.Contains(logs.String(), "webhook ingress dispatch failed") || !strings.Contains(logs.String(), "dispatch boom") || !strings.Contains(logs.String(), dispatchFailureCode) || !strings.Contains(logs.String(), dispatchFailureReasonGeneric) {
		t.Fatalf("expected failure observability log with root cause, stable code, and generic failure reason, got %s", logs.String())
	}
	trace := adapter.Tracer.RenderTrace(dispatcher.event.TraceID)
	if !strings.Contains(trace, "adapter.ingress") || !strings.Contains(trace, "adapter.ingress.failure") {
		t.Fatalf("expected ingress and failure spans on dispatch failure, got %s", trace)
	}
	if trace == "" {
		t.Fatalf("expected non-empty trace render on failure")
	}
	spans := adapter.Tracer.SpansByTrace(dispatcher.event.TraceID)
	if len(spans) != 2 {
		t.Fatalf("expected ingress and failure spans, got %+v", spans)
	}
	matchedFailureSpan := false
	for _, span := range spans {
		if span.SpanName == "adapter.ingress.failure" {
			matchedFailureSpan = true
			if span.Metadata["failure_reason"] != dispatchFailureReasonGeneric {
				t.Fatalf("expected failure span metadata to classify generic dispatch failure, got %+v", span)
			}
		}
	}
	if !matchedFailureSpan {
		t.Fatalf("expected dispatch failure span, got %+v", spans)
	}
}

func TestWebhookAdapterClassifiesCanceledDispatchFailureInternally(t *testing.T) {
	t.Parallel()

	logs := &bytes.Buffer{}
	dispatcher := &recordingDispatcher{err: context.Canceled}
	adapter := New("", dispatcher, runtimecore.NewLogger(logs), nil)
	adapter.Tracer = runtimecore.NewTraceRecorder()
	request := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"event_type":"webhook.received","source":"webhook","actor_id":"svc-1","text":"hello"}`))
	recorder := httptest.NewRecorder()

	adapter.HandleWebhook(recorder, request)

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", recorder.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["status"] != "error" || payload["code"] != dispatchFailureCode || payload["message"] != dispatchFailureMessage {
		t.Fatalf("expected stable dispatch failure payload, got %+v", payload)
	}
	if payload["trace_id"] != dispatcher.event.TraceID || payload["event_id"] != dispatcher.event.EventID {
		t.Fatalf("expected caller-facing ids to match dispatched event, got payload=%+v event=%+v", payload, dispatcher.event)
	}
	if strings.Contains(recorder.Body.String(), "context canceled") {
		t.Fatalf("expected canceled dispatch root cause to stay hidden from caller, got %s", recorder.Body.String())
	}
	if !strings.Contains(logs.String(), dispatchFailureCode) || !strings.Contains(logs.String(), dispatchFailureReasonCanceled) || !strings.Contains(logs.String(), "context canceled") {
		t.Fatalf("expected internal log to preserve canceled dispatch classification and root cause, got %s", logs.String())
	}
	spans := adapter.Tracer.SpansByTrace(dispatcher.event.TraceID)
	if len(spans) != 2 {
		t.Fatalf("expected ingress and failure spans for canceled dispatch failure, got %+v", spans)
	}
	matchedFailureSpan := false
	for _, span := range spans {
		if span.SpanName == "adapter.ingress.failure" {
			matchedFailureSpan = true
			if span.Metadata["failure_reason"] != dispatchFailureReasonCanceled {
				t.Fatalf("expected failure span metadata to classify canceled dispatch failure, got %+v", span)
			}
		}
	}
	if !matchedFailureSpan {
		t.Fatalf("expected canceled dispatch failure span, got %+v", spans)
	}
}

func TestWebhookMapPayloadProducesUniqueCorrelationKeys(t *testing.T) {
	t.Parallel()

	adapter := New("", &recordingDispatcher{}, runtimecore.NewLogger(&bytes.Buffer{}), nil)
	adapter.Now = func() time.Time { return time.Date(2026, 4, 3, 21, 0, 0, 0, time.UTC) }

	first, err := adapter.MapPayload(WebhookPayload{EventType: "webhook.received", Source: "webhook", ActorID: "svc-1", Text: "hello"})
	if err != nil {
		t.Fatalf("map first payload: %v", err)
	}
	second, err := adapter.MapPayload(WebhookPayload{EventType: "webhook.received", Source: "webhook", ActorID: "svc-1", Text: "hello"})
	if err != nil {
		t.Fatalf("map second payload: %v", err)
	}
	if first.IdempotencyKey == second.IdempotencyKey {
		t.Fatalf("expected unique webhook correlation keys, got %q", first.IdempotencyKey)
	}
}

func TestWebhookAdapterResolvesTokenFromSecretRegistry(t *testing.T) {
	t.Setenv("BOT_PLATFORM_WEBHOOK_TOKEN", "secret-from-registry")
	audits := runtimecore.NewInMemoryAuditLog()
	registry := runtimecore.NewSecretRegistry(runtimecore.EnvSecretProvider{}, audits)
	adapter, err := NewWithSecretRef("BOT_PLATFORM_WEBHOOK_TOKEN", registry, &recordingDispatcher{}, runtimecore.NewLogger(&bytes.Buffer{}), audits)
	if err != nil {
		t.Fatalf("new adapter with secret ref: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"event_type":"webhook.received","source":"webhook"}`))
	request.Header.Set("X-Webhook-Token", "secret-from-registry")
	recorder := httptest.NewRecorder()

	adapter.HandleWebhook(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	entries := audits.AuditEntries()
	if len(entries) != 1 || entries[0].Action != "secret.read" || !entries[0].Allowed {
		t.Fatalf("expected successful secret read audit, got %+v", entries)
	}
}

func TestWebhookAdapterUsesRuntimeControlledSourceOverride(t *testing.T) {
	t.Parallel()

	dispatcher := &recordingDispatcher{}
	adapter := New("secret", dispatcher, runtimecore.NewLogger(&bytes.Buffer{}), nil)
	adapter.Source = "webhook-main"
	adapter.Platform = "webhook/http"
	adapter.InstanceID = "adapter-webhook-main"
	request := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"event_type":"message.received","source":"client-spoofed","actor_id":"svc-1","text":"hello"}`))
	request.Header.Set("X-Webhook-Token", "secret")
	recorder := httptest.NewRecorder()

	adapter.HandleWebhook(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	if dispatcher.event.Source != "webhook-main" {
		t.Fatalf("expected runtime-controlled source override, got %+v", dispatcher.event)
	}
	if dispatcher.event.Metadata["ingress_source"] != "client-spoofed" || dispatcher.event.Metadata["adapter_instance_id"] != "adapter-webhook-main" {
		t.Fatalf("expected ingress metadata to preserve client source separately, got %+v", dispatcher.event.Metadata)
	}
}

func TestWebhookAdapterReturnsInternalErrorWhenSecretMissing(t *testing.T) {
	t.Parallel()

	audits := runtimecore.NewInMemoryAuditLog()
	logs := &bytes.Buffer{}
	tracer := runtimecore.NewTraceRecorder()
	registry := runtimecore.NewSecretRegistry(runtimecore.EnvSecretProvider{}, audits)
	dispatcher := &recordingDispatcher{}
	adapter, err := NewWithSecretRef("BOT_PLATFORM_MISSING_SECRET", registry, dispatcher, runtimecore.NewLogger(logs), audits)
	if err != nil {
		t.Fatalf("new adapter with secret ref: %v", err)
	}
	adapter.Tracer = tracer
	request := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"event_type":"webhook.received","source":"webhook"}`))
	recorder := httptest.NewRecorder()

	adapter.HandleWebhook(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("expected application/json content type, got %q", contentType)
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["status"] != "error" || payload["code"] != secretResolutionFailureCode || payload["message"] != secretResolutionMessage {
		t.Fatalf("expected stable secret failure payload, got %+v", payload)
	}
	traceID, _ := payload["trace_id"].(string)
	if traceID == "" {
		t.Fatalf("expected trace_id in failure payload, got %+v", payload)
	}
	if _, ok := payload["event_id"]; ok {
		t.Fatalf("expected secret resolution failure to happen before event creation, got %+v", payload)
	}
	if strings.Contains(recorder.Body.String(), "BOT_PLATFORM_MISSING_SECRET") || strings.Contains(recorder.Body.String(), "not found in environment") {
		t.Fatalf("expected secret ref and provider detail to stay hidden from client, got %s", recorder.Body.String())
	}
	if dispatcher.event.EventID != "" {
		t.Fatalf("expected dispatch not to run when secret resolution fails, got %+v", dispatcher.event)
	}
	if !strings.Contains(logs.String(), "webhook secret resolution failed") || !strings.Contains(logs.String(), secretResolutionFailureCode) || !strings.Contains(logs.String(), "BOT_PLATFORM_MISSING_SECRET") || !strings.Contains(logs.String(), "secret_not_found") {
		t.Fatalf("expected internal log to preserve root cause, stable code, and failure reason, got %s", logs.String())
	}
	trace := adapter.Tracer.RenderTrace(traceID)
	if !strings.Contains(trace, "adapter.ingress.secret_resolution.failure") {
		t.Fatalf("expected secret resolution failure span, got %s", trace)
	}
	spans := adapter.Tracer.SpansByTrace(traceID)
	if len(spans) != 1 || spans[0].SpanName != "adapter.ingress.secret_resolution.failure" || spans[0].Metadata["failure_reason"] != "secret_not_found" {
		t.Fatalf("expected trace metadata to classify secret_not_found, got %+v", spans)
	}
	entries := audits.AuditEntries()
	if len(entries) != 1 || entries[0].Action != "secret.read" || entries[0].Allowed || entries[0].Reason != "secret_not_found" {
		t.Fatalf("expected failed secret read audit, got %+v", entries)
	}
}

func TestWebhookAdapterClassifiesGenericSecretResolverFailureInternally(t *testing.T) {
	t.Parallel()

	audits := runtimecore.NewInMemoryAuditLog()
	logs := &bytes.Buffer{}
	tracer := runtimecore.NewTraceRecorder()
	registry := runtimecore.NewSecretRegistry(failingSecretProvider{err: errors.New("provider temporarily unavailable")}, audits)
	dispatcher := &recordingDispatcher{}
	adapter, err := NewWithSecretRef("BOT_PLATFORM_WEBHOOK_TOKEN", registry, dispatcher, runtimecore.NewLogger(logs), audits)
	if err != nil {
		t.Fatalf("new adapter with secret ref: %v", err)
	}
	adapter.Tracer = tracer
	request := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"event_type":"webhook.received","source":"webhook"}`))
	recorder := httptest.NewRecorder()

	adapter.HandleWebhook(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", recorder.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["status"] != "error" || payload["code"] != secretResolutionFailureCode || payload["message"] != secretResolutionMessage {
		t.Fatalf("expected stable secret failure payload, got %+v", payload)
	}
	traceID, _ := payload["trace_id"].(string)
	if traceID == "" {
		t.Fatalf("expected trace_id in failure payload, got %+v", payload)
	}
	if strings.Contains(recorder.Body.String(), "provider temporarily unavailable") || strings.Contains(recorder.Body.String(), "BOT_PLATFORM_WEBHOOK_TOKEN") {
		t.Fatalf("expected caller-facing payload to hide generic resolver internals, got %s", recorder.Body.String())
	}
	if dispatcher.event.EventID != "" {
		t.Fatalf("expected dispatch not to run when secret resolution fails, got %+v", dispatcher.event)
	}
	if !strings.Contains(logs.String(), secretResolutionFailureCode) || !strings.Contains(logs.String(), "secret_read_failed") || !strings.Contains(logs.String(), "provider temporarily unavailable") {
		t.Fatalf("expected internal log to preserve generic failure classification and root cause, got %s", logs.String())
	}
	spans := adapter.Tracer.SpansByTrace(traceID)
	if len(spans) != 1 || spans[0].SpanName != "adapter.ingress.secret_resolution.failure" || spans[0].Metadata["failure_reason"] != "secret_read_failed" {
		t.Fatalf("expected trace metadata to classify generic secret resolver failure, got %+v", spans)
	}
	entries := audits.AuditEntries()
	if len(entries) != 1 || entries[0].Action != "secret.read" || entries[0].Allowed || entries[0].Reason != "secret_read_failed" {
		t.Fatalf("expected failed secret read audit with generic reason, got %+v", entries)
	}
}

func TestWebhookAdapterClassifiesCanceledSecretResolverFailureInternally(t *testing.T) {
	t.Parallel()

	audits := runtimecore.NewInMemoryAuditLog()
	logs := &bytes.Buffer{}
	tracer := runtimecore.NewTraceRecorder()
	registry := runtimecore.NewSecretRegistry(failingSecretProvider{err: context.Canceled}, audits)
	dispatcher := &recordingDispatcher{}
	adapter, err := NewWithSecretRef("BOT_PLATFORM_WEBHOOK_TOKEN", registry, dispatcher, runtimecore.NewLogger(logs), audits)
	if err != nil {
		t.Fatalf("new adapter with secret ref: %v", err)
	}
	adapter.Tracer = tracer
	request := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"event_type":"webhook.received","source":"webhook"}`))
	recorder := httptest.NewRecorder()

	adapter.HandleWebhook(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", recorder.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["status"] != "error" || payload["code"] != secretResolutionFailureCode || payload["message"] != secretResolutionMessage {
		t.Fatalf("expected stable secret failure payload, got %+v", payload)
	}
	traceID, _ := payload["trace_id"].(string)
	if traceID == "" {
		t.Fatalf("expected trace_id in failure payload, got %+v", payload)
	}
	if strings.Contains(recorder.Body.String(), "context canceled") || strings.Contains(recorder.Body.String(), "BOT_PLATFORM_WEBHOOK_TOKEN") {
		t.Fatalf("expected caller-facing payload to hide canceled resolver internals, got %s", recorder.Body.String())
	}
	if dispatcher.event.EventID != "" {
		t.Fatalf("expected dispatch not to run when secret resolution fails, got %+v", dispatcher.event)
	}
	if !strings.Contains(logs.String(), secretResolutionFailureCode) || !strings.Contains(logs.String(), "secret_read_canceled") || !strings.Contains(logs.String(), "context canceled") {
		t.Fatalf("expected internal log to preserve canceled failure classification and root cause, got %s", logs.String())
	}
	spans := adapter.Tracer.SpansByTrace(traceID)
	if len(spans) != 1 || spans[0].SpanName != "adapter.ingress.secret_resolution.failure" || spans[0].Metadata["failure_reason"] != "secret_read_canceled" {
		t.Fatalf("expected trace metadata to classify canceled secret resolver failure, got %+v", spans)
	}
	entries := audits.AuditEntries()
	if len(entries) != 1 || entries[0].Action != "secret.read" || entries[0].Allowed || entries[0].Reason != "secret_read_canceled" {
		t.Fatalf("expected failed secret read audit with canceled reason, got %+v", entries)
	}
}

func TestWebhookAdapterRejectsInvalidSecretRefSafely(t *testing.T) {
	t.Parallel()

	audits := runtimecore.NewInMemoryAuditLog()
	logs := &bytes.Buffer{}
	registry := runtimecore.NewSecretRegistry(runtimecore.EnvSecretProvider{}, audits)
	adapter, err := NewWithSecretRef("db.password", registry, &recordingDispatcher{}, runtimecore.NewLogger(logs), audits)
	if err == nil || err.Error() != "secret ref must use BOT_PLATFORM_ prefix" {
		t.Fatalf("expected constructor-time invalid secret ref error, got adapter=%v err=%v", adapter, err)
	}
	if logs.String() != "" {
		t.Fatalf("expected invalid secret ref constructor path not to emit runtime logs, got %s", logs.String())
	}
	entries := audits.AuditEntries()
	if len(entries) != 0 {
		t.Fatalf("expected constructor-time invalid secret ref to avoid runtime audit entries, got %+v", entries)
	}
}

func assertRejectObservability(t *testing.T, logs *bytes.Buffer, tracer *runtimecore.TraceRecorder, traceID, spanName, logMessage, failureCode, failureReason string) {
	t.Helper()

	entries := decodeLogEntries(t, logs)
	if len(entries) != 1 {
		t.Fatalf("expected one reject log entry, got %+v", entries)
	}
	entry := entries[0]
	if entry.TraceID != traceID || entry.Message != logMessage || entry.Level != "error" {
		t.Fatalf("expected reject log to match trace/message/level, got %+v", entry)
	}
	if entry.Fields["failure_code"] != failureCode || entry.Fields["failure_reason"] != failureReason {
		t.Fatalf("expected reject log fields to preserve stable failure metadata, got %+v", entry)
	}

	trace := tracer.RenderTrace(traceID)
	if !strings.Contains(trace, spanName) {
		t.Fatalf("expected rendered trace to include %s, got %s", spanName, trace)
	}
	spans := tracer.SpansByTrace(traceID)
	if len(spans) != 1 {
		t.Fatalf("expected one reject span, got %+v", spans)
	}
	if spans[0].SpanName != spanName || spans[0].Metadata["failure_code"] != failureCode || spans[0].Metadata["failure_reason"] != failureReason {
		t.Fatalf("expected reject span metadata to match trace_id-backed failure evidence, got %+v", spans)
	}
}

func decodeLogEntries(t *testing.T, logs *bytes.Buffer) []runtimecore.LogEntry {
	t.Helper()

	raw := strings.TrimSpace(logs.String())
	if raw == "" {
		t.Fatalf("expected reject log output")
	}
	lines := strings.Split(raw, "\n")
	entries := make([]runtimecore.LogEntry, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry runtimecore.LogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("decode log entry %q: %v", line, err)
		}
		entries = append(entries, entry)
	}
	return entries
}

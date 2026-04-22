package runtimecore

import (
	"strings"
	"testing"
	"time"
)

func TestTraceRecorderLocksActive2CanonicalSpanNames(t *testing.T) {
	t.Parallel()

	recorder := NewTraceRecorder()
	base := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	index := 0
	recorder.now = func() time.Time {
		value := base.Add(time.Duration(index) * time.Second)
		index++
		return value
	}

	canonicalSpanNames := []string{
		"adapter.ingress",
		"runtime.event.dispatch",
		"job.enqueue",
		"job.dispatch",
		"workflow.start_or_resume",
		"plugin.dispatch",
		"reply.dispatch",
	}

	for _, spanName := range canonicalSpanNames {
		finish := recorder.StartSpan("trace-active2", spanName, "evt-active2", "plugin-echo", "run-active2", "corr-active2", nil)
		finish()
	}

	spans := recorder.SpansByTrace("trace-active2")
	if len(spans) != len(canonicalSpanNames) {
		t.Fatalf("expected %d canonical spans, got %d", len(canonicalSpanNames), len(spans))
	}
	for i, expected := range canonicalSpanNames {
		if spans[i].SpanName != expected {
			t.Fatalf("expected span %d to be %q, got %q", i, expected, spans[i].SpanName)
		}
	}

	rendered := recorder.RenderTrace("trace-active2")
	for _, expected := range canonicalSpanNames {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("expected rendered trace to contain %q, got %s", expected, rendered)
		}
	}
}

func TestTraceRecorderSpanCarriesFiveIDContext(t *testing.T) {
	t.Parallel()

	recorder := NewTraceRecorder()
	finish := recorder.StartSpan("trace-ctx-1", "plugin.dispatch", "evt-ctx-1", "plugin-echo", "run-ctx-1", "corr-ctx-1", map[string]any{"dispatch_kind": "event"})
	finish()

	spans := recorder.SpansByTrace("trace-ctx-1")
	if len(spans) != 1 {
		t.Fatalf("expected one span, got %d", len(spans))
	}

	span := spans[0]
	if span.TraceID != "trace-ctx-1" || span.EventID != "evt-ctx-1" || span.PluginID != "plugin-echo" || span.RunID != "run-ctx-1" || span.CorrelationID != "corr-ctx-1" {
		t.Fatalf("expected five-ID span context to round-trip, got %+v", span)
	}
}

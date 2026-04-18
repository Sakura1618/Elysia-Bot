package runtimecore

import (
	"context"
	"fmt"
	"strings"
	"time"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
)

type EventJournalReader interface {
	LoadEvent(context.Context, string) (eventmodel.Event, error)
}

type EventReplayer struct {
	journal EventJournalReader
	runtime *InMemoryRuntime
	store   replayOperationRecorder
	now     func() time.Time
}

type replayOperationRecorder interface {
	SaveReplayOperationRecord(context.Context, ReplayOperationRecord) error
}

type ReplayPolicyDeclaration struct {
	Namespace             string   `json:"namespace"`
	IdentityIsolation     string   `json:"identityIsolation"`
	ReplayOfReplay        string   `json:"replayOfReplay"`
	EntryPoints           []string `json:"entryPoints,omitempty"`
	SupportedModes        []string `json:"supportedModes,omitempty"`
	UnsupportedModes      []string `json:"unsupportedModes,omitempty"`
	VerificationEndpoints []string `json:"verificationEndpoints,omitempty"`
	Facts                 []string `json:"facts,omitempty"`
	Summary               string   `json:"summary,omitempty"`
}

const replayNamespace = "runtime.replay.isolated"

func ReplayPolicy() ReplayPolicyDeclaration {
	declaration := ReplayPolicyDeclaration{
		Namespace:             replayNamespace,
		IdentityIsolation:     "rewrite event_id|trace_id|idempotency_key with replay namespace prefix and timestamp suffix",
		ReplayOfReplay:        "reject replay-tagged events and replay identity prefixes before redispatch",
		EntryPoints:           []string{"/admin replay <event-id>"},
		SupportedModes:        []string{"single-event-explicit-replay", "isolated-replay-identity", "admin-command-authorized-entry"},
		UnsupportedModes:      []string{"batch-replay", "dry-run", "replay-write-api", "approval-workflow", "automatic-journal-rewrite"},
		VerificationEndpoints: []string{"GET /api/console", "go test ./packages/runtime-core ./apps/runtime -run Replay"},
		Facts: []string{
			"replay currently re-dispatches exactly one stored event selected by event_id",
			"replay identity is isolated from the original event by rewritten event_id, trace_id, and idempotency_key",
			"events already marked as replay or already using replay identity prefixes are rejected",
			"explicit replay attempts are persisted as operational records and remain visible after restart",
		},
	}
	declaration.Summary = "namespace=" + declaration.Namespace + "; single-event explicit replay only; isolated replay identity rewrite; replay-of-replay rejected; replay attempts persist as operational records; no batch replay or dry-run"
	return declaration
}

func NewEventReplayer(journal EventJournalReader, runtime *InMemoryRuntime) *EventReplayer {
	replayer := &EventReplayer{journal: journal, runtime: runtime, now: time.Now().UTC}
	if recorder, ok := journal.(replayOperationRecorder); ok {
		replayer.store = recorder
	}
	return replayer
}

func NewEventReplayerWithStore(journal EventJournalReader, runtime *InMemoryRuntime, store replayOperationRecorder) *EventReplayer {
	replayer := NewEventReplayer(journal, runtime)
	replayer.store = store
	return replayer
}

func (r *EventReplayer) ReplayEvent(ctx context.Context, eventID string) (eventmodel.Event, error) {
	if r.journal == nil {
		return eventmodel.Event{}, fmt.Errorf("event journal is required")
	}
	if r.runtime == nil {
		return eventmodel.Event{}, fmt.Errorf("runtime is required")
	}
	if eventID == "" {
		return eventmodel.Event{}, fmt.Errorf("event id is required")
	}

	original, err := r.journal.LoadEvent(ctx, eventID)
	if err != nil {
		return eventmodel.Event{}, err
	}
	if isReplayTagged(original) {
		return eventmodel.Event{}, fmt.Errorf("cannot replay a replayed event")
	}

	replayed := original
	now := r.now()
	suffix := fmt.Sprintf("%d", now.UnixNano())
	replayID := replayOperationID(original.EventID, now)
	replayed.EventID = "replay-" + original.EventID + "-" + suffix
	replayed.TraceID = "replay-" + original.TraceID + "-" + suffix
	replayed.IdempotencyKey = "replay:" + original.EventID + ":" + suffix
	replayed.Timestamp = now
	replayed.Metadata = cloneMetadata(original.Metadata)
	replayed.Metadata["replay"] = true
	replayed.Metadata["replay_of"] = original.EventID
	replayed.Metadata["replay_isolated"] = true
	replayed.Metadata["replay_namespace"] = replayNamespace

	if err := r.runtime.DispatchEvent(ctx, replayed); err != nil {
		r.recordReplayResult(ctx, ReplayOperationRecord{
			ReplayID:      replayID,
			SourceEventID: original.EventID,
			ReplayEventID: replayed.EventID,
			Status:        "failed",
			Reason:        err.Error(),
			OccurredAt:    now,
			UpdatedAt:     now,
		})
		return replayed, err
	}
	r.recordReplayResult(ctx, ReplayOperationRecord{
		ReplayID:      replayID,
		SourceEventID: original.EventID,
		ReplayEventID: replayed.EventID,
		Status:        "succeeded",
		Reason:        "replay_dispatched",
		OccurredAt:    now,
		UpdatedAt:     now,
	})
	return replayed, nil
}

func (r *EventReplayer) recordReplayResult(ctx context.Context, record ReplayOperationRecord) {
	if r == nil || r.store == nil {
		return
	}
	_ = r.store.SaveReplayOperationRecord(ctx, record)
}

func replayOperationID(sourceEventID string, occurredAt time.Time) string {
	trimmed := strings.TrimSpace(sourceEventID)
	if trimmed == "" {
		trimmed = "unknown"
	}
	return "replay-op-" + trimmed + "-" + fmt.Sprintf("%d", occurredAt.UTC().UnixNano())
}

func cloneMetadata(metadata map[string]any) map[string]any {
	if metadata == nil {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(metadata)+3)
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}

func isReplayTagged(event eventmodel.Event) bool {
	if strings.HasPrefix(event.EventID, "replay-") {
		return true
	}
	if strings.HasPrefix(event.TraceID, "replay-") {
		return true
	}
	if strings.HasPrefix(event.IdempotencyKey, "replay:") {
		return true
	}
	if event.Metadata == nil {
		return false
	}
	if replay, ok := event.Metadata["replay"].(bool); ok && replay {
		return true
	}
	if isolated, ok := event.Metadata["replay_isolated"].(bool); ok && isolated {
		return true
	}
	return false
}

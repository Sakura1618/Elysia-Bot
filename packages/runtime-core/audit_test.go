package runtimecore

import (
	"errors"
	"strings"
	"testing"

	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

type failingAuditRecorder struct{}

func (failingAuditRecorder) RecordAudit(entry pluginsdk.AuditEntry) error {
	return errors.New("sink failed")
}

func TestInMemoryAuditLogStoresEntriesInOrder(t *testing.T) {
	t.Parallel()

	log := NewInMemoryAuditLog()
	entries := []pluginsdk.AuditEntry{
		{Actor: "admin-user", Action: "enable", Target: "plugin-echo", Allowed: true, OccurredAt: "2026-04-03T12:00:00Z"},
		{
			Actor:          "guest-user",
			Action:         "admin",
			Target:         "/admin enable plugin-echo",
			Allowed:        false,
			OccurredAt:     "2026-04-03T12:01:00Z",
			TraceID:        "trace-denied",
			EventID:        "evt-denied",
			PluginID:       "plugin-echo",
			RunID:          "run-denied",
			CorrelationID:  "corr-denied",
			ErrorCategory:  "authorization",
			ErrorCode:      "permission_denied",
		},
	}
	setAuditEntryReason(&entries[1], "permission_denied")
	for _, entry := range entries {
		if err := log.RecordAudit(entry); err != nil {
			t.Fatalf("record audit entry: %v", err)
		}
	}

	stored := log.AuditEntries()
	if len(stored) != 2 || stored[0].Actor != "admin-user" || stored[1].Allowed || auditEntryReason(stored[1]) != "permission_denied" {
		t.Fatalf("unexpected stored audit entries %+v", stored)
	}
	if stored[0].TraceID != "" || stored[0].EventID != "" || stored[0].PluginID != "" || stored[0].RunID != "" || stored[0].CorrelationID != "" || stored[0].ErrorCategory != "" || stored[0].ErrorCode != "" {
		t.Fatalf("expected audit entry without optional observability fields to remain valid, got %+v", stored[0])
	}
	if stored[1] != entries[1] {
		t.Fatalf("expected audit entry with optional observability fields to round-trip, got %+v", stored[1])
	}
	stored[0].Actor = "mutated"
	stored[1].TraceID = "mutated"
	refetched := log.AuditEntries()
	if refetched[0].Actor != "admin-user" || refetched[1].TraceID != "trace-denied" {
		t.Fatal("expected audit entries to be returned as a copy")
	}
}

func TestMultiAuditRecorderReturnsJoinedErrors(t *testing.T) {
	t.Parallel()

	recorder := NewMultiAuditRecorder(NewInMemoryAuditLog(), failingAuditRecorder{})
	err := recorder.RecordAudit(pluginsdk.AuditEntry{Actor: "admin-user", Action: "enable", Target: "plugin-echo", Allowed: true, OccurredAt: "2026-04-03T12:00:00Z"})
	if err == nil || !strings.Contains(err.Error(), "sink failed") {
		t.Fatalf("expected joined audit recorder failure, got %v", err)
	}
}

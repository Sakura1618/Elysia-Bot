package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
)

func (a *runtimeApp) handleWorkflowMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `method not allowed`, http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		ActorID  string `json:"actor_id"`
		Message  string `json:"message"`
		TargetID string `json:"target_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, `invalid payload`, http.StatusBadRequest)
		return
	}
	message := strings.TrimSpace(payload.Message)
	if message == `` {
		http.Error(w, `message is required`, http.StatusBadRequest)
		return
	}
	actorID := strings.TrimSpace(payload.ActorID)
	if actorID == `` {
		actorID = `user-1`
	}
	targetID := strings.TrimSpace(payload.TargetID)
	if targetID == `` {
		targetID = `group-42`
	}
	before := a.replies.Count()
	event := workflowDemoEvent(message, actorID, targetID)
	duplicate, err := a.persistAndDispatchEvent(r.Context(), event)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	workflowID := `workflow-` + actorID
	if a.workflowRuntime != nil {
		instances, listErr := a.workflowRuntime.List(r.Context())
		if listErr == nil {
			for i := len(instances) - 1; i >= 0; i-- {
				instance := instances[i]
				if strings.HasPrefix(instance.WorkflowID, `workflow-`+actorID) {
					workflowID = instance.WorkflowID
					break
				}
			}
		}
	}
	w.Header().Set(`Content-Type`, `application/json`)
	_ = json.NewEncoder(w).Encode(map[string]any{
		`status`:      `ok`,
		`duplicate`:   duplicate,
		`event_id`:    event.EventID,
		`trace_id`:    event.TraceID,
		`workflow_id`: workflowID,
		`replies`:     a.replies.Since(before),
	})
}

func workflowDemoEvent(message string, actorID string, targetID string) eventmodel.Event {
	now := time.Now().UTC()
	eventID := fmt.Sprintf(`evt-workflow-%d`, now.UnixNano())
	traceID := fmt.Sprintf(`trace-workflow-%d`, now.UnixNano())
	return eventmodel.Event{
		EventID:        eventID,
		TraceID:        traceID,
		Source:         `runtime-workflow-demo`,
		Type:           `message.received`,
		Timestamp:      now,
		IdempotencyKey: `runtime-workflow-demo:` + eventID,
		Actor:          &eventmodel.Actor{ID: actorID, Type: `user`, DisplayName: actorID},
		Channel:        &eventmodel.Channel{ID: targetID, Type: `group`, Title: targetID},
		Message:        &eventmodel.Message{ID: `msg-` + eventID, Text: message},
		Reply: &eventmodel.ReplyHandle{
			Capability: `onebot.reply`,
			TargetID:   targetID,
			MessageID:  `msg-` + eventID,
			Metadata: map[string]any{
				`message_type`: `group`,
				`group_id`:     42,
				`user_id`:      10001,
			},
		},
	}
}

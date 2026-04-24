package runtimecore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

type PluginManifestReader interface {
	LoadPluginManifest(pluginID string) (pluginsdk.PluginManifest, error)
}

type InMemoryRolloutManager struct {
	current   PluginManifestReader
	candidate PluginManifestReader
	heads     map[string]RolloutHeadState
	store     RolloutStateStore
	now       func() time.Time
}

var errRolloutInvalidTransition = errors.New("invalid rollout transition")

type RolloutPolicyDeclaration struct {
	EntryPoints           []string `json:"entryPoints,omitempty"`
	PreflightChecks       []string `json:"preflightChecks,omitempty"`
	ActivationChecks      []string `json:"activationChecks,omitempty"`
	AuditReasons          []string `json:"auditReasons,omitempty"`
	RecordStore           string   `json:"recordStore,omitempty"`
	SupportedModes        []string `json:"supportedModes,omitempty"`
	UnsupportedModes      []string `json:"unsupportedModes,omitempty"`
	VerificationEndpoints []string `json:"verificationEndpoints,omitempty"`
	Facts                 []string `json:"facts,omitempty"`
	Summary               string   `json:"summary,omitempty"`
}

func RolloutPolicy() RolloutPolicyDeclaration {
	declaration := RolloutPolicyDeclaration{
		EntryPoints:      []string{"/admin prepare <plugin-id>", "/admin activate <plugin-id>"},
		PreflightChecks:  []string{"manifest.id-match", "manifest.mode-match", "manifest.api-version-match", "manifest.version-changed"},
		ActivationChecks: []string{"prepared-record-required", "prepare-time-drift-recheck", "lifecycle-enable-before-activated-mark"},
		AuditReasons:     []string{"rollout_prepared", "rollout_activated", "rollout_drifted", "rollout_failed"},
		RecordStore:      "sqlite-current-runtime-rollout-operations",
		SupportedModes:   []string{"manual-prepare-activate", "manifest-preflight", "activate-time-drift-recheck", "minimal-audit-reasons"},
		UnsupportedModes: []string{"rollback", "staged-rollout", "health-check-gate", "automatic-rollback", "persisted-rollout-history", "approval-workflow", "schema-migration-gate"},
		VerificationEndpoints: []string{
			"GET /api/console",
			"go test ./packages/runtime-core ./plugins/plugin-admin ./apps/runtime -run Rollout",
		},
		Facts: []string{
			"rollout is currently a manual admin chain limited to /admin prepare <plugin-id> and /admin activate <plugin-id>",
			"prepare records only minimal manifest compatibility facts for ID, Mode, APIVersion, and Version change",
			"activate re-checks current and candidate manifests before lifecycle enable so prepare-time drift is rejected",
			"rollout prepare and activate attempts are persisted as operational records and remain visible after restart",
		},
	}
	declaration.Summary = "manual /admin prepare|activate only; preflight checks ID|Mode|APIVersion|Version; activate re-checks drift before lifecycle enable; rollout attempts persist as operational records; audit reasons are minimal; no rollback or staged rollout"
	return declaration
}

func NewInMemoryRolloutManager(current, candidate PluginManifestReader) *InMemoryRolloutManager {
	return &InMemoryRolloutManager{current: current, candidate: candidate, heads: map[string]RolloutHeadState{}, now: time.Now().UTC}
}

func NewSQLiteRolloutManager(current, candidate PluginManifestReader, store RolloutStateStore) *InMemoryRolloutManager {
	manager := NewInMemoryRolloutManager(current, candidate)
	manager.store = store
	manager.restorePersistedRecords()
	return manager
}

func (m *InMemoryRolloutManager) Prepare(pluginID string) (pluginsdk.RolloutRecord, error) {
	head, record, err := m.evaluate(pluginID)
	operationID := m.persistOperationResult("prepare", pluginID, record, err)
	m.persistHead(head, operationID)
	if err != nil {
		return pluginsdk.RolloutRecord{}, err
	}
	if record.Status == pluginsdk.RolloutStatusRejected {
		return record, fmt.Errorf("rollout preflight rejected: %s", record.Reason)
	}
	return record, nil
}

func (m *InMemoryRolloutManager) Activate(pluginID string) (pluginsdk.RolloutRecord, error) {
	head, record, err := m.transition(pluginID, rolloutTransitionActivate)
	if err != nil {
		return pluginsdk.RolloutRecord{}, err
	}
	operationID := m.persistOperationResult("activate", pluginID, record, nil)
	m.persistHead(head, operationID)
	return record, nil
}

func (m *InMemoryRolloutManager) CanActivate(pluginID string) (pluginsdk.RolloutRecord, error) {
	_, record, err := m.transition(pluginID, rolloutTransitionCanActivate)
	return record, err
}

func (m *InMemoryRolloutManager) Canary(pluginID string) (pluginsdk.RolloutRecord, error) {
	head, record, err := m.transition(pluginID, rolloutTransitionCanary)
	if err != nil {
		return pluginsdk.RolloutRecord{}, err
	}
	operationID := m.persistOperationResult("canary", pluginID, record, nil)
	m.persistHead(head, operationID)
	return record, nil
}

func (m *InMemoryRolloutManager) Rollback(pluginID string) (pluginsdk.RolloutRecord, error) {
	head, record, err := m.transition(pluginID, rolloutTransitionRollback)
	if err != nil {
		return pluginsdk.RolloutRecord{}, err
	}
	operationID := m.persistOperationResult("rollback", pluginID, record, nil)
	m.persistHead(head, operationID)
	return record, nil
}

func (m *InMemoryRolloutManager) CanRollback(pluginID string) (pluginsdk.RolloutRecord, error) {
	_, record, err := m.transition(pluginID, rolloutTransitionCanRollback)
	return record, err
}

func (m *InMemoryRolloutManager) transition(pluginID string, transition rolloutTransition) (RolloutHeadState, pluginsdk.RolloutRecord, error) {
	if m == nil {
		err := fmt.Errorf("rollout manager is required")
		return RolloutHeadState{}, pluginsdk.RolloutRecord{}, err
	}
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		err := fmt.Errorf("plugin id is required")
		return RolloutHeadState{}, pluginsdk.RolloutRecord{}, err
	}
	currentHead, ok := m.head(pluginID)
	if !ok {
		err := fmt.Errorf("rollout record for %q not found", pluginID)
		m.persistOperationResult("activate", pluginID, pluginsdk.RolloutRecord{PluginID: pluginID}, err)
		return RolloutHeadState{}, pluginsdk.RolloutRecord{}, err
	}
	switch transition {
	case rolloutTransitionCanActivate:
		preparedRecord, err := m.validatePreparedState(pluginID, currentHead)
		if err != nil {
			return RolloutHeadState{}, pluginsdk.RolloutRecord{}, err
		}
		return currentHead, preparedRecord, nil
	case rolloutTransitionActivate:
		if _, err := m.validatePreparedState(pluginID, currentHead); err != nil {
			return RolloutHeadState{}, pluginsdk.RolloutRecord{}, err
		}
		nextHead := currentHead
		nextHead.Phase = string(pluginsdk.RolloutPhaseStable)
		nextHead.Status = string(pluginsdk.RolloutStatusActivated)
		nextHead.Reason = ""
		if nextHead.Candidate != nil {
			nextHead.Stable = *nextHead.Candidate
			nextHead.Active = *nextHead.Candidate
			nextHead.Candidate = nil
		}
		return nextHead, rolloutRecordFromHead(nextHead), nil
	case rolloutTransitionCanary:
		if _, err := m.validatePreparedState(pluginID, currentHead); err != nil {
			return RolloutHeadState{}, pluginsdk.RolloutRecord{}, err
		}
		if currentHead.Phase != string(pluginsdk.RolloutPhaseCandidate) {
			err := fmt.Errorf("%w: rollout canary requires candidate phase for %q", errRolloutInvalidTransition, pluginID)
			return RolloutHeadState{}, pluginsdk.RolloutRecord{}, err
		}
		nextHead := currentHead
		nextHead.Phase = string(pluginsdk.RolloutPhaseCanary)
		nextHead.Status = string(pluginsdk.RolloutStatusCanarying)
		nextHead.Reason = ""
		if nextHead.Candidate != nil {
			nextHead.Active = *nextHead.Candidate
		}
		return nextHead, rolloutRecordFromHead(nextHead), nil
	case rolloutTransitionCanRollback:
		if !rolloutHeadCanRollback(currentHead) {
			err := fmt.Errorf("%w: rollout rollback is not available for %q in phase %q", errRolloutInvalidTransition, pluginID, currentHead.Phase)
			return RolloutHeadState{}, pluginsdk.RolloutRecord{}, err
		}
		return currentHead, rolloutRollbackRecordFromHead(currentHead), nil
	case rolloutTransitionRollback:
		if !rolloutHeadCanRollback(currentHead) {
			err := fmt.Errorf("%w: rollout rollback is not available for %q in phase %q", errRolloutInvalidTransition, pluginID, currentHead.Phase)
			return RolloutHeadState{}, pluginsdk.RolloutRecord{}, err
		}
		nextHead := currentHead
		nextHead.Phase = string(pluginsdk.RolloutPhaseStable)
		nextHead.Status = string(pluginsdk.RolloutStatusRolledBack)
		nextHead.Reason = ""
		nextHead.Active = nextHead.Stable
		return nextHead, rolloutRollbackRecordFromHead(nextHead), nil
	default:
		return RolloutHeadState{}, pluginsdk.RolloutRecord{}, fmt.Errorf("%w: unsupported rollout transition %q", errRolloutInvalidTransition, transition)
	}
}

func (m *InMemoryRolloutManager) validatePreparedState(pluginID string, currentHead RolloutHeadState) (pluginsdk.RolloutRecord, error) {
	if currentHead.Phase != string(pluginsdk.RolloutPhaseCandidate) && currentHead.Phase != string(pluginsdk.RolloutPhaseCanary) {
		err := fmt.Errorf("rollout record for %q is not prepared", pluginID)
		m.persistOperationResult("activate", pluginID, rolloutRecordFromHead(currentHead), err)
		return pluginsdk.RolloutRecord{}, err
	}
	validatedHead, currentRecord, err := m.evaluate(pluginID)
	if err != nil {
		m.persistOperationResult("activate", pluginID, rolloutRecordFromHead(currentHead), err)
		return pluginsdk.RolloutRecord{}, err
	}
	if currentRecord.Status != pluginsdk.RolloutStatusPrepared {
		m.heads[pluginID] = validatedHead
		err := fmt.Errorf("rollout record for %q drifted after prepare: %s", pluginID, currentRecord.Reason)
		operationID := m.persistOperationResult("activate", pluginID, currentRecord, err)
		m.persistHead(validatedHead, operationID)
		return pluginsdk.RolloutRecord{}, err
	}
	if validatedHead.Candidate == nil || currentHead.Candidate == nil || validatedHead.Stable.Version != currentHead.Stable.Version || validatedHead.Candidate.Version != currentHead.Candidate.Version {
		m.heads[pluginID] = validatedHead
		err := fmt.Errorf("rollout record for %q drifted after prepare: current=%s/%s candidate=%s/%s", pluginID, rolloutRecordFromHead(currentHead).CurrentVersion, currentRecord.CurrentVersion, rolloutRecordFromHead(currentHead).CandidateVersion, currentRecord.CandidateVersion)
		operationID := m.persistOperationResult("activate", pluginID, currentRecord, err)
		m.persistHead(validatedHead, operationID)
		return pluginsdk.RolloutRecord{}, err
	}
	if currentHead.Phase == string(pluginsdk.RolloutPhaseCanary) {
		currentRecord.Phase = pluginsdk.RolloutPhaseCanary
		currentRecord.Status = pluginsdk.RolloutStatusCanarying
	}
	return currentRecord, nil
}

func (m *InMemoryRolloutManager) evaluate(pluginID string) (RolloutHeadState, pluginsdk.RolloutRecord, error) {
	if m == nil || m.current == nil || m.candidate == nil {
		return RolloutHeadState{}, pluginsdk.RolloutRecord{}, fmt.Errorf("rollout manifest readers are required")
	}
	current, err := m.current.LoadPluginManifest(pluginID)
	if err != nil {
		return RolloutHeadState{}, pluginsdk.RolloutRecord{}, err
	}
	candidate, err := m.candidate.LoadPluginManifest(pluginID)
	if err != nil {
		return RolloutHeadState{}, pluginsdk.RolloutRecord{}, err
	}
	head := RolloutHeadState{
		PluginID: pluginID,
		Stable:   rolloutSnapshotStateFromManifest(current),
		Active:   rolloutSnapshotStateFromManifest(current),
		Candidate: &RolloutSnapshotState{
			Version:    strings.TrimSpace(candidate.Version),
			APIVersion: strings.TrimSpace(candidate.APIVersion),
			Mode:       strings.TrimSpace(candidate.Mode),
		},
		Phase:  string(pluginsdk.RolloutPhaseCandidate),
		Status: string(pluginsdk.RolloutStatusPrepared),
	}
	record := rolloutRecordFromHead(head)
	switch {
	case current.ID != pluginID:
		record.Status = pluginsdk.RolloutStatusRejected
		record.Reason = fmt.Sprintf("current manifest id mismatch: expected=%s actual=%s", pluginID, current.ID)
	case candidate.ID != pluginID:
		record.Status = pluginsdk.RolloutStatusRejected
		record.Reason = fmt.Sprintf("candidate manifest id mismatch: expected=%s actual=%s", pluginID, candidate.ID)
	case current.Mode != candidate.Mode:
		record.Status = pluginsdk.RolloutStatusRejected
		record.Reason = fmt.Sprintf("mode mismatch: current=%s candidate=%s", current.Mode, candidate.Mode)
	case candidate.APIVersion == "":
		record.Status = pluginsdk.RolloutStatusRejected
		record.Reason = "candidate apiVersion is required"
	case current.APIVersion != candidate.APIVersion:
		record.Status = pluginsdk.RolloutStatusRejected
		record.Reason = fmt.Sprintf("apiVersion mismatch: current=%s candidate=%s", current.APIVersion, candidate.APIVersion)
	case current.Version == candidate.Version:
		record.Status = pluginsdk.RolloutStatusRejected
		record.Reason = "candidate version must differ from current version"
	default:
		record.Status = pluginsdk.RolloutStatusPrepared
	}
	head.Status = string(record.Status)
	head.Reason = strings.TrimSpace(record.Reason)
	if record.Status == pluginsdk.RolloutStatusRejected {
		head.Phase = string(pluginsdk.RolloutPhaseStable)
	}
	return head, record, nil
}

func (m *InMemoryRolloutManager) Record(pluginID string) (pluginsdk.RolloutRecord, bool) {
	if m == nil {
		return pluginsdk.RolloutRecord{}, false
	}
	head, ok := m.head(pluginID)
	if !ok {
		return pluginsdk.RolloutRecord{}, false
	}
	return rolloutRecordFromHead(head), true
}

func (m *InMemoryRolloutManager) persistOperationResult(action string, pluginID string, record pluginsdk.RolloutRecord, actionErr error) string {
	if m == nil {
		return ""
	}
	now := m.currentTime()
	status := string(record.Status)
	if strings.TrimSpace(status) == "" {
		status = "failed"
	}
	reason := strings.TrimSpace(record.Reason)
	if actionErr != nil {
		reason = strings.TrimSpace(actionErr.Error())
	}
	resolvedPluginID := strings.TrimSpace(record.PluginID)
	if resolvedPluginID == "" {
		resolvedPluginID = strings.TrimSpace(pluginID)
	}
	operationID := rolloutOperationID(action, resolvedPluginID, now)
	if m.store != nil {
		_ = m.store.SaveRolloutOperationRecord(context.Background(), RolloutOperationRecord{
			OperationID:      operationID,
			PluginID:         resolvedPluginID,
			Action:           strings.TrimSpace(action),
			CurrentVersion:   record.CurrentVersion,
			CandidateVersion: record.CandidateVersion,
			Status:           status,
			Reason:           reason,
			OccurredAt:       now,
			UpdatedAt:        now,
		})
	}
	return operationID
}

func (m *InMemoryRolloutManager) persistHead(head RolloutHeadState, operationID string) {
	if m == nil {
		return
	}
	pluginID := strings.TrimSpace(head.PluginID)
	if pluginID == "" {
		return
	}
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return
	}
	head.PluginID = pluginID
	head.LastOperationID = operationID
	head.UpdatedAt = m.currentTime()
	head.Phase = strings.TrimSpace(head.Phase)
	head.Status = strings.TrimSpace(head.Status)
	head.Reason = strings.TrimSpace(head.Reason)
	if m.store != nil {
		_ = m.store.SaveRolloutHead(context.Background(), head)
	}
	m.heads[pluginID] = head
}

func (m *InMemoryRolloutManager) currentTime() time.Time {
	if m != nil && m.now != nil {
		return m.now().UTC()
	}
	return time.Now().UTC()
}

func (m *InMemoryRolloutManager) restorePersistedRecords() {
	if m == nil || m.store == nil {
		return
	}
	heads, err := m.store.ListRolloutHeads(context.Background())
	if err == nil {
		for _, persisted := range heads {
			pluginID := strings.TrimSpace(persisted.PluginID)
			if pluginID == "" {
				continue
			}
			m.heads[pluginID] = normalizeRolloutHeadState(persisted)
		}
	}
	if len(m.heads) > 0 {
		return
	}
	records, err := m.store.ListRolloutOperationRecords(context.Background())
	if err != nil {
		return
	}
	for _, persisted := range records {
		pluginID := strings.TrimSpace(persisted.PluginID)
		if pluginID == "" {
			continue
		}
		if _, ok := m.heads[pluginID]; ok {
			continue
		}
		m.heads[pluginID] = rolloutHeadFromOperationRecord(persisted)
	}
}

func (m *InMemoryRolloutManager) head(pluginID string) (RolloutHeadState, bool) {
	if m == nil {
		return RolloutHeadState{}, false
	}
	head, ok := m.heads[strings.TrimSpace(pluginID)]
	if !ok {
		return RolloutHeadState{}, false
	}
	return normalizeRolloutHeadState(head), true
}

func rolloutSnapshotStateFromManifest(manifest pluginsdk.PluginManifest) RolloutSnapshotState {
	return RolloutSnapshotState{
		Version:    strings.TrimSpace(manifest.Version),
		APIVersion: strings.TrimSpace(manifest.APIVersion),
		Mode:       strings.TrimSpace(manifest.Mode),
	}
}

func rolloutHeadPhaseForRecord(record pluginsdk.RolloutRecord) string {
	if strings.TrimSpace(string(record.Phase)) != "" {
		return strings.TrimSpace(string(record.Phase))
	}
	switch record.Status {
	case pluginsdk.RolloutStatusPrepared:
		return string(pluginsdk.RolloutPhaseCandidate)
	case pluginsdk.RolloutStatusCanarying:
		return string(pluginsdk.RolloutPhaseCanary)
	case pluginsdk.RolloutStatusActivated:
		return string(pluginsdk.RolloutPhaseStable)
	case pluginsdk.RolloutStatusRolledBack:
		return string(pluginsdk.RolloutPhaseStable)
	case pluginsdk.RolloutStatusRejected:
		if strings.TrimSpace(record.CandidateVersion) != "" {
			return string(pluginsdk.RolloutPhaseCandidate)
		}
		return string(pluginsdk.RolloutPhaseStable)
	default:
		return string(pluginsdk.RolloutPhaseStable)
	}
}

func rolloutRecordFromHead(state RolloutHeadState) pluginsdk.RolloutRecord {
	state = normalizeRolloutHeadState(state)
	record := pluginsdk.RolloutRecord{
		PluginID:       strings.TrimSpace(state.PluginID),
		StableVersion:  strings.TrimSpace(state.Stable.Version),
		ActiveVersion:  strings.TrimSpace(state.Active.Version),
		CurrentVersion: strings.TrimSpace(state.Active.Version),
		Phase:          pluginsdk.RolloutPhase(strings.TrimSpace(state.Phase)),
		Status:         pluginsdk.RolloutStatus(strings.TrimSpace(state.Status)),
		Reason:         strings.TrimSpace(state.Reason),
	}
	if record.CurrentVersion == "" {
		record.CurrentVersion = strings.TrimSpace(state.Stable.Version)
		record.ActiveVersion = record.CurrentVersion
	}
	if state.Candidate != nil {
		record.CandidateVersion = strings.TrimSpace(state.Candidate.Version)
	}
	return record
}

func rolloutRollbackRecordFromHead(state RolloutHeadState) pluginsdk.RolloutRecord {
	record := rolloutRecordFromHead(state)
	record.Phase = pluginsdk.RolloutPhaseRollback
	record.Status = pluginsdk.RolloutStatusRolledBack
	record.CurrentVersion = strings.TrimSpace(state.Stable.Version)
	record.ActiveVersion = strings.TrimSpace(state.Stable.Version)
	return record
}

func normalizeRolloutHeadState(state RolloutHeadState) RolloutHeadState {
	state.PluginID = strings.TrimSpace(state.PluginID)
	state.Stable = normalizeRolloutSnapshotState(state.Stable)
	state.Active = normalizeRolloutSnapshotState(state.Active)
	if state.Candidate != nil {
		candidate := normalizeRolloutSnapshotState(*state.Candidate)
		if candidate.Version == "" {
			state.Candidate = nil
		} else {
			state.Candidate = &candidate
		}
	}
	state.Phase = strings.TrimSpace(state.Phase)
	if state.Phase == "" {
		state.Phase = string(pluginsdk.RolloutPhaseStable)
	}
	state.Status = strings.TrimSpace(state.Status)
	if state.Status == "" {
		state.Status = string(pluginsdk.RolloutStatusActivated)
	}
	state.Reason = strings.TrimSpace(state.Reason)
	state.LastOperationID = strings.TrimSpace(state.LastOperationID)
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now().UTC()
	} else {
		state.UpdatedAt = state.UpdatedAt.UTC()
	}
	return state
}

func rolloutHeadFromOperationRecord(record RolloutOperationRecord) RolloutHeadState {
	head := RolloutHeadState{
		PluginID: record.PluginID,
		Stable: RolloutSnapshotState{
			Version: strings.TrimSpace(record.CurrentVersion),
		},
		Active: RolloutSnapshotState{
			Version: strings.TrimSpace(record.CurrentVersion),
		},
		Phase:           rolloutHeadPhaseForRecord(pluginsdk.RolloutRecord{PluginID: record.PluginID, CurrentVersion: record.CurrentVersion, CandidateVersion: record.CandidateVersion, Status: pluginsdk.RolloutStatus(record.Status)}),
		Status:          strings.TrimSpace(record.Status),
		Reason:          strings.TrimSpace(record.Reason),
		LastOperationID: strings.TrimSpace(record.OperationID),
		UpdatedAt:       record.UpdatedAt,
	}
	if strings.TrimSpace(record.CandidateVersion) != "" {
		head.Candidate = &RolloutSnapshotState{Version: strings.TrimSpace(record.CandidateVersion)}
	}
	if head.Status == string(pluginsdk.RolloutStatusCanarying) && head.Candidate != nil {
		head.Active = *head.Candidate
	}
	if head.Status == string(pluginsdk.RolloutStatusActivated) && head.Candidate != nil {
		head.Stable = *head.Candidate
		head.Active = *head.Candidate
		head.Candidate = nil
	}
	return normalizeRolloutHeadState(head)
}

func rolloutHeadCanRollback(state RolloutHeadState) bool {
	state = normalizeRolloutHeadState(state)
	return state.Phase == string(pluginsdk.RolloutPhaseCanary) || state.Status == string(pluginsdk.RolloutStatusActivated)
}

type rolloutTransition string

const (
	rolloutTransitionCanActivate rolloutTransition = "can_activate"
	rolloutTransitionActivate    rolloutTransition = "activate"
	rolloutTransitionCanary      rolloutTransition = "canary"
	rolloutTransitionCanRollback rolloutTransition = "can_rollback"
	rolloutTransitionRollback    rolloutTransition = "rollback"
)

func rolloutOperationID(action, pluginID string, occurredAt time.Time) string {
	trimmedAction := strings.TrimSpace(action)
	if trimmedAction == "" {
		trimmedAction = "rollout"
	}
	trimmedPluginID := strings.TrimSpace(pluginID)
	if trimmedPluginID == "" {
		trimmedPluginID = "unknown"
	}
	return "rollout-op-" + trimmedAction + "-" + trimmedPluginID + "-" + fmt.Sprintf("%d", occurredAt.UTC().UnixNano())
}

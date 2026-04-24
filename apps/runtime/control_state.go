package main

import (
	"context"
	"fmt"
	"strings"

	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
	runtimecore "github.com/ohmyopencode/bot-platform/packages/runtime-core"
)

type runtimeControlStateStore interface {
	runtimecore.PluginManifestStateStore
	runtimecore.PluginEnabledStateStore
	runtimecore.PluginConfigStateStore
	runtimecore.PluginStatusSnapshotStore
	runtimecore.SessionStateStore
	runtimecore.AdapterInstanceStateStore
	runtimecore.RBACStateStore
	runtimecore.AuditLogReader
	runtimecore.AuditRecorder
	runtimecore.DispatchResultRecorder
	runtimecore.ReplayOperationStateStore
	runtimecore.RolloutStateStore
	Counts(context.Context) (map[string]int, error)
}

type runtimeReplayOperationStore interface {
	SaveReplayOperationRecord(context.Context, runtimecore.ReplayOperationRecord) error
}

type runtimePersistedPluginManifestReader struct {
	store runtimecore.PluginManifestStateStore
}

func (r runtimePersistedPluginManifestReader) LoadPluginManifest(pluginID string) (pluginsdk.PluginManifest, error) {
	if r.store == nil {
		return pluginsdk.PluginManifest{}, fmt.Errorf("plugin manifest store is required")
	}
	return r.store.LoadPluginManifest(context.Background(), pluginID)
}

func runtimeSelectedControlStateStore(settings appRuntimeSettings, smokeStore runtimeSmokeStore, sqliteState *runtimecore.SQLiteStateStore) (runtimeControlStateStore, error) {
	if runtimeControlBackend(settings) != "postgres" {
		if sqliteState == nil {
			return nil, fmt.Errorf("sqlite state store is required")
		}
		return sqliteState, nil
	}
	postgresStore, ok := smokeStore.(postgresRuntimeSmokeStore)
	if !ok || postgresStore.store == nil {
		return nil, fmt.Errorf("selected control state store for backend %q is unavailable", strings.TrimSpace(settings.SmokeStoreBackend))
	}
	return postgresStore.store, nil
}

func runtimeControlBackend(settings appRuntimeSettings) string {
	if strings.EqualFold(strings.TrimSpace(settings.SmokeStoreBackend), "postgres") {
		return "postgres"
	}
	return "sqlite"
}

func runtimeControlStateSource(settings appRuntimeSettings, suffix string) string {
	suffix = strings.TrimSpace(suffix)
	if suffix == "" {
		return runtimeControlBackend(settings)
	}
	return runtimeControlBackend(settings) + "-" + suffix
}

func runtimeRBACSnapshotSource(settings appRuntimeSettings) string {
	backend := runtimeControlBackend(settings)
	return backend + "-rbac-snapshot+" + backend + "-operator-identities"
}

func runtimeRolloutRecordStore(settings appRuntimeSettings) string {
	return runtimeControlBackend(settings) + "-current-runtime-rollout-operations"
}

func runtimeRolloutPolicy(settings appRuntimeSettings) runtimecore.RolloutPolicyDeclaration {
	policy := runtimecore.RolloutPolicy()
	policy.RecordStore = runtimeRolloutRecordStore(settings)
	return policy
}

func runtimeControlCountKeys() []string {
	return []string{
		"plugin_registry",
		"plugin_enabled_overlays",
		"plugin_configs",
		"plugin_status_snapshots",
		"rollout_heads",
		"adapter_instances",
		"sessions",
		"operator_identities",
		"rbac_snapshots",
		"replay_operation_records",
		"rollout_operation_records",
		"audit_log",
	}
}

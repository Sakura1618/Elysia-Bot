package runtimecore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type PluginLifecycleService struct {
	store *SQLiteStateStore
}

func NewPluginLifecycleService(store *SQLiteStateStore) *PluginLifecycleService {
	return &PluginLifecycleService{store: store}
}

func (s *PluginLifecycleService) Enable(pluginID string) error {
	return s.setEnabled(pluginID, true)
}

func (s *PluginLifecycleService) Disable(pluginID string) error {
	return s.setEnabled(pluginID, false)
}

func (s *PluginLifecycleService) PluginEnabled(pluginID string) bool {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return false
	}
	if s.store == nil {
		return true
	}
	if _, err := s.store.LoadPluginManifest(context.Background(), pluginID); err != nil {
		return true
	}
	state, err := s.store.LoadPluginEnabledState(context.Background(), pluginID)
	if err == nil {
		return state.Enabled
	}
	if err == sql.ErrNoRows {
		return true
	}
	return true
}

func (s *PluginLifecycleService) setEnabled(pluginID string, enabled bool) error {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return fmt.Errorf("plugin id is required")
	}
	if s.store == nil {
		return fmt.Errorf("plugin enabled state store is required")
	}
	if _, err := s.store.LoadPluginManifest(context.Background(), pluginID); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("plugin %q is not registered", pluginID)
		}
		return fmt.Errorf("load plugin manifest: %w", err)
	}
	if err := s.store.SavePluginEnabledState(context.Background(), pluginID, enabled); err != nil {
		return err
	}
	return nil
}

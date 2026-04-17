package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	runtimecore "github.com/ohmyopencode/bot-platform/packages/runtime-core"
	pluginecho "github.com/ohmyopencode/bot-platform/plugins/plugin-echo"
)

const pluginConfigStateKindPersistedInput = "plugin-owned-persisted-input"

type appPluginConfigBinding struct {
	PluginID              string
	ConfigStateKind       string
	ConfigPath            string
	DefaultTypedConfig    any
	DecodeAndValidate     func([]byte) (json.RawMessage, map[string]any, any, error)
	ProjectResponseConfig func(any) (map[string]any, error)
}

type appPluginConfigRegistry map[string]appPluginConfigBinding

type decodedAppPluginConfig struct {
	RawConfigJSON  json.RawMessage
	InstanceConfig map[string]any
	TypedConfig    any
	ResponseConfig map[string]any
}

type loadedAppPluginConfig struct {
	TypedConfig    any
	InstanceConfig map[string]any
}

func newAppPluginConfigRegistry() appPluginConfigRegistry {
	return appPluginConfigRegistry{
		"plugin-echo": {
			PluginID:           "plugin-echo",
			ConfigStateKind:    pluginConfigStateKindPersistedInput,
			ConfigPath:         "/demo/plugins/plugin-echo/config",
			DefaultTypedConfig: pluginecho.Config{Prefix: "echo: "},
			DecodeAndValidate: func(raw []byte) (json.RawMessage, map[string]any, any, error) {
				encoded, instanceConfig, typedConfig, err := validateAndDecodeEchoConfig(raw)
				if err != nil {
					return nil, nil, nil, err
				}
				return encoded, instanceConfig, typedConfig, nil
			},
			ProjectResponseConfig: func(decoded any) (map[string]any, error) {
				typedConfig, ok := decoded.(pluginecho.Config)
				if !ok {
					return nil, fmt.Errorf("plugin-echo config binding decoded %T, want pluginecho.Config", decoded)
				}
				return map[string]any{"prefix": typedConfig.Prefix}, nil
			},
		},
	}
}

func (r appPluginConfigRegistry) Lookup(pluginID string) (appPluginConfigBinding, bool) {
	binding, ok := r[pluginID]
	return binding, ok
}

func (r appPluginConfigRegistry) ConsoleBindings() map[string]runtimecore.ConsolePluginConfigBinding {
	bindings := make(map[string]runtimecore.ConsolePluginConfigBinding, len(r))
	for pluginID, binding := range r {
		bindings[pluginID] = runtimecore.ConsolePluginConfigBinding{StateKind: binding.ConfigStateKind}
	}
	return bindings
}

func (b appPluginConfigBinding) Decode(raw []byte) (decodedAppPluginConfig, error) {
	if b.DecodeAndValidate == nil {
		return decodedAppPluginConfig{}, fmt.Errorf("plugin config binding %q decode function is required", b.PluginID)
	}
	rawConfigJSON, instanceConfig, typedConfig, err := b.DecodeAndValidate(raw)
	if err != nil {
		return decodedAppPluginConfig{}, err
	}
	responseConfig := map[string]any{}
	if b.ProjectResponseConfig != nil {
		responseConfig, err = b.ProjectResponseConfig(typedConfig)
		if err != nil {
			return decodedAppPluginConfig{}, err
		}
	}
	return decodedAppPluginConfig{
		RawConfigJSON:  rawConfigJSON,
		InstanceConfig: instanceConfig,
		TypedConfig:    typedConfig,
		ResponseConfig: responseConfig,
	}, nil
}

func loadPersistedPluginConfig(state *runtimecore.SQLiteStateStore, binding appPluginConfigBinding) (loadedAppPluginConfig, error) {
	if state == nil {
		return loadedAppPluginConfig{TypedConfig: binding.DefaultTypedConfig}, nil
	}
	stored, err := state.LoadPluginConfig(context.Background(), binding.PluginID)
	if err == sql.ErrNoRows {
		return loadedAppPluginConfig{TypedConfig: binding.DefaultTypedConfig}, nil
	}
	if err != nil {
		return loadedAppPluginConfig{}, err
	}
	decoded, err := binding.Decode(stored.RawConfig)
	if err != nil {
		return loadedAppPluginConfig{}, err
	}
	return loadedAppPluginConfig{TypedConfig: decoded.TypedConfig, InstanceConfig: decoded.InstanceConfig}, nil
}

func validateAndDecodeEchoConfig(raw []byte) (json.RawMessage, map[string]any, pluginecho.Config, error) {
	var rawConfig map[string]any
	if err := json.Unmarshal(raw, &rawConfig); err != nil {
		return nil, nil, pluginecho.Config{}, fmt.Errorf("plugin-echo config must be a JSON object: %w", err)
	}
	if rawConfig == nil {
		return nil, nil, pluginecho.Config{}, errors.New("plugin-echo config must be a JSON object")
	}
	if prefix, exists := rawConfig["prefix"]; exists {
		if _, ok := prefix.(string); !ok {
			return nil, nil, pluginecho.Config{}, errors.New(`plugin-echo config property "prefix" must be a string`)
		}
	}
	encoded, err := json.Marshal(rawConfig)
	if err != nil {
		return nil, nil, pluginecho.Config{}, fmt.Errorf("marshal plugin-echo config: %w", err)
	}
	var typedConfig pluginecho.Config
	if err := json.Unmarshal(encoded, &typedConfig); err != nil {
		return nil, nil, pluginecho.Config{}, fmt.Errorf("decode plugin-echo config: %w", err)
	}
	return encoded, rawConfig, typedConfig, nil
}

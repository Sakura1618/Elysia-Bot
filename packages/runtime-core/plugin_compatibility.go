package runtimecore

import (
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

type pluginCompatibilityPreflightRequest struct {
	hostMode              string
	supportedAPIVersion   string
	currentRuntimeVersion string
}

type pluginCompatibilityPreflightResult struct {
	normalizedInstanceConfig map[string]any
}

func subprocessPluginCompatibilityPreflightRequest() pluginCompatibilityPreflightRequest {
	return pluginCompatibilityPreflightRequest{
		hostMode:              pluginsdk.ModeSubprocess,
		supportedAPIVersion:   supportedSubprocessPluginAPIVersion,
		currentRuntimeVersion: currentRuntimeVersion,
	}
}

func evaluatePluginCompatibilityPreflight(request pluginCompatibilityPreflightRequest, manifest pluginsdk.PluginManifest, instanceConfig map[string]any) (pluginCompatibilityPreflightResult, error) {
	if err := validatePluginCompatibilityManifestPreflight(request, manifest); err != nil {
		return pluginCompatibilityPreflightResult{}, err
	}
	normalizedInstanceConfig, err := normalizeSubprocessInstanceConfig(manifest, instanceConfig)
	if err != nil {
		return pluginCompatibilityPreflightResult{}, err
	}
	return pluginCompatibilityPreflightResult{normalizedInstanceConfig: normalizedInstanceConfig}, nil
}

func validatePluginCompatibilityManifestPreflight(request pluginCompatibilityPreflightRequest, manifest pluginsdk.PluginManifest) error {
	return validateSubprocessManifestCompatibility(manifest, request)
}

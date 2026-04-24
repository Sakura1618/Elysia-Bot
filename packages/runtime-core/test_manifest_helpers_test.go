package runtimecore

import (
	"strings"

	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

func withTestSchemaVersion(manifest pluginsdk.PluginManifest) pluginsdk.PluginManifest {
	if strings.TrimSpace(manifest.SchemaVersion) == "" {
		manifest.SchemaVersion = pluginsdk.SupportedPluginManifestSchemaVersion
	}
	return manifest
}

func withTestSchemaPlugin(plugin pluginsdk.Plugin) pluginsdk.Plugin {
	plugin.Manifest = withTestSchemaVersion(plugin.Manifest)
	return plugin
}

func registerPluginWithTestSchema(registrar interface{ RegisterPlugin(pluginsdk.Plugin) error }, plugin pluginsdk.Plugin) error {
	return registrar.RegisterPlugin(withTestSchemaPlugin(plugin))
}

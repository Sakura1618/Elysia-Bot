package plugintemplatesmoke

import (
	"errors"
	"fmt"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

const (
	TemplatePluginID                  = "plugin-template-smoke"
	TemplatePluginName                = "Plugin Template Smoke"
	TemplatePluginModule              = "plugins/plugin-template-smoke"
	TemplatePluginSymbol              = "Plugin"
	TemplatePluginPublishSourceType   = pluginsdk.PublishSourceTypeGit
	TemplatePluginPublishSourceURI    = "https://github.com/ohmyopencode/bot-platform/tree/main/plugins/plugin-template-smoke"
	TemplatePluginRuntimeVersionRange = ">=0.1.0 <1.0.0"
)

type Config struct {
	Prefix string `json:"prefix"`
}

type Plugin struct {
	Manifest     pluginsdk.PluginManifest
	Config       Config
	ReplyService pluginsdk.ReplyService
}

func New(replyService pluginsdk.ReplyService, config Config) Plugin {
	return Plugin{
		Manifest:     Manifest(),
		Config:       config,
		ReplyService: replyService,
	}
}

func Manifest() pluginsdk.PluginManifest {
	return pluginsdk.PluginManifest{
		SchemaVersion: pluginsdk.SupportedPluginManifestSchemaVersion,
		ID:            TemplatePluginID,
		Name:          TemplatePluginName,
		Version:       "0.1.0",
		APIVersion:    "v0",
		Mode:          pluginsdk.ModeSubprocess,
		Permissions: []string{
			"reply:send",
		},
		ConfigSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prefix": map[string]any{"type": "string"},
			},
		},
		Publish: &pluginsdk.PluginPublish{
			SourceType:          TemplatePluginPublishSourceType,
			SourceURI:           TemplatePluginPublishSourceURI,
			RuntimeVersionRange: TemplatePluginRuntimeVersionRange,
		},
		Entry: pluginsdk.PluginEntry{Module: TemplatePluginModule, Symbol: TemplatePluginSymbol},
	}
}

func (p Plugin) Definition() pluginsdk.Plugin {
	return pluginsdk.Plugin{
		Manifest: p.Manifest,
		Handlers: pluginsdk.Handlers{Event: p},
	}
}

func (p Plugin) OnEvent(event eventmodel.Event, ctx eventmodel.ExecutionContext) error {
	if event.Type != "message.received" || event.Message == nil {
		return nil
	}
	if ctx.Reply == nil {
		return errors.New("reply handle is required")
	}
	if p.ReplyService == nil {
		return errors.New("reply service is required")
	}

	message := event.Message.Text
	if p.Config.Prefix != "" {
		message = fmt.Sprintf("%s%s", p.Config.Prefix, message)
	}

	return p.ReplyService.ReplyText(*ctx.Reply, message)
}

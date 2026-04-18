package pluginworkflowdemo

import (
	"context"
	"errors"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
	runtimecore "github.com/ohmyopencode/bot-platform/packages/runtime-core"
)

type WorkflowService interface {
	StartOrResume(context.Context, string, string, string, string, runtimecore.Workflow) (runtimecore.WorkflowTransition, error)
}

type Plugin struct {
	Manifest     pluginsdk.PluginManifest
	ReplyService pluginsdk.ReplyService
	Workflows    WorkflowService
}

func New(replyService pluginsdk.ReplyService, workflows WorkflowService) *Plugin {
	return &Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-workflow-demo",
			Name:       "Workflow Demo Plugin",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			Permissions: []string{
				"reply:send",
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/plugin-workflow-demo", Symbol: "Plugin"},
		},
		ReplyService: replyService,
		Workflows:    workflows,
	}
}

func (p *Plugin) Definition() pluginsdk.Plugin {
	return pluginsdk.Plugin{Manifest: p.Manifest, Handlers: pluginsdk.Handlers{Event: p}}
}

func (p *Plugin) OnEvent(event eventmodel.Event, ctx eventmodel.ExecutionContext) error {
	if event.Type != "message.received" || event.Message == nil || ctx.Reply == nil {
		return nil
	}
	if p.ReplyService == nil || p.Workflows == nil {
		return errors.New("reply service and workflow runtime are required")
	}
	workflowID := workflowSessionID(event)
	transition, err := p.Workflows.StartOrResume(context.Background(), workflowID, p.Manifest.ID, event.Type, event.EventID, demoWorkflow(workflowID, event.Message.Text))
	if err != nil {
		return err
	}
	if transition.Started {
		return p.ReplyService.ReplyText(*ctx.Reply, "workflow started, please send another message to continue")
	}
	if transition.Resumed {
		return p.ReplyService.ReplyText(*ctx.Reply, "workflow resumed and completed")
	}
	return nil
}

func demoWorkflow(workflowID string, greeting string) runtimecore.Workflow {
	return runtimecore.NewWorkflow(
		workflowID,
		runtimecore.WorkflowStep{Kind: runtimecore.WorkflowStepKindPersist, Name: "greeting", Value: greeting},
		runtimecore.WorkflowStep{Kind: runtimecore.WorkflowStepKindWaitEvent, Name: "wait-confirm", Value: "message.received"},
		runtimecore.WorkflowStep{Kind: runtimecore.WorkflowStepKindCompensate, Name: "complete"},
	)
}

func workflowSessionID(event eventmodel.Event) string {
	if event.Actor != nil && event.Actor.ID != "" {
		return "workflow-" + event.Actor.ID
	}
	return "workflow-anon"
}

package pluginworkflowdemo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
	runtimecore "github.com/ohmyopencode/bot-platform/packages/runtime-core"
)

const (
	pluginWorkflowDemoPublishSourceURI    = "https://github.com/ohmyopencode/bot-platform/tree/main/plugins/plugin-workflow-demo"
	pluginWorkflowDemoRuntimeVersionRange = ">=0.1.0 <1.0.0"
)

type WorkflowService interface {
	StartOrResume(context.Context, string, string, string, string, runtimecore.Workflow) (runtimecore.WorkflowTransition, error)
	ResumeFromChildJob(context.Context, string, string, runtimecore.WorkflowChildJobResume) (runtimecore.WorkflowTransition, error)
}

type Plugin struct {
	Manifest     pluginsdk.PluginManifest
	ReplyService pluginsdk.ReplyService
	Queue        JobQueue
	Workflows    WorkflowService
	Now          func() time.Time
}

type JobQueue interface {
	Enqueue(context.Context, pluginsdk.Job) error
	Inspect(context.Context, string) (pluginsdk.Job, error)
	Cancel(context.Context, string) (pluginsdk.Job, error)
}

func New(replyService pluginsdk.ReplyService, queue JobQueue, workflows WorkflowService) *Plugin {
	return &Plugin{
		Manifest: pluginsdk.PluginManifest{
			SchemaVersion: pluginsdk.SupportedPluginManifestSchemaVersion,
			ID:            "plugin-workflow-demo",
			Name:          "Workflow Demo Plugin",
			Version:       "0.1.0",
			APIVersion:    "v0",
			Mode:          pluginsdk.ModeSubprocess,
			Permissions: []string{
				"reply:send",
				"job:enqueue",
			},
			Publish: &pluginsdk.PluginPublish{
				SourceType:          pluginsdk.PublishSourceTypeGit,
				SourceURI:           pluginWorkflowDemoPublishSourceURI,
				RuntimeVersionRange: pluginWorkflowDemoRuntimeVersionRange,
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/plugin-workflow-demo", Symbol: "Plugin"},
		},
		ReplyService: replyService,
		Queue:        queue,
		Workflows:    workflows,
		Now:          time.Now().UTC,
	}
}

func (p *Plugin) Definition() pluginsdk.Plugin {
	return pluginsdk.Plugin{Manifest: p.Manifest, Handlers: pluginsdk.Handlers{Event: p}}
}

func (p *Plugin) OnEvent(event eventmodel.Event, ctx eventmodel.ExecutionContext) error {
	if event.Type != "message.received" || event.Message == nil || ctx.Reply == nil {
		return nil
	}
	if p.ReplyService == nil || p.Queue == nil || p.Workflows == nil {
		return errors.New("reply service, job queue, and workflow runtime are required")
	}
	if err := ctx.Reply.Validate(); err != nil {
		return err
	}
	workflowID := workflowSessionID(event)
	jobID := workflowChildJobID(workflowID)
	workflowContext := runtimecore.WithWorkflowObservabilityContext(context.Background(), runtimecore.WorkflowObservabilityContextFromExecutionContext(ctx))
	transition, existingWorkflow, workflowFound, err := p.inspectWorkflow(workflowContext, workflowID)
	if err != nil {
		return err
	}
	if workflowFound && existingWorkflow.Workflow.Completed {
		workflowID = workflowRunID(event, p.Now())
		jobID = workflowChildJobID(workflowID)
		transition = runtimecore.WorkflowTransition{}
		workflowFound = false
	}
	_, jobFound, err := p.inspectChildJob(workflowContext, jobID)
	if err != nil {
		return err
	}
	if !workflowFound && jobFound {
		if _, err := p.Queue.Cancel(workflowContext, jobID); err == nil {
			jobFound = false
		} else if !strings.Contains(strings.ToLower(err.Error()), "cannot be cancelled") {
			return err
		}
	}
	if !jobFound {
		if err := p.Queue.Enqueue(workflowContext, workflowChildJob(event, ctx, jobID, p.Manifest.ID, workflowID)); err != nil {
			return err
		}
	}
	transition, err = p.Workflows.StartOrResume(workflowContext, workflowID, p.Manifest.ID, event.Type, event.EventID, demoWorkflow(workflowID, jobID, event.Message.Text))
	if err != nil {
		return err
	}
	return p.replyForTransition(*ctx.Reply, transition)
}

func (p *Plugin) replyForTransition(handle eventmodel.ReplyHandle, transition runtimecore.WorkflowTransition) error {
	reply := runtimecore.WithReplyObservabilityMetadata(handle, transition.Instance.ObservabilityContext().ExecutionContext())
	if transition.Started {
		return p.ReplyService.ReplyText(reply, "workflow started, child job queued; wait for its result, then send another message to continue")
	}
	if transition.Resumed {
		if transition.Instance.Workflow.Compensated {
			return p.ReplyService.ReplyText(reply, "workflow resumed after child job failure and compensation completed")
		}
		return p.ReplyService.ReplyText(reply, "workflow resumed after child job success and completed")
	}
	return nil
}

func demoWorkflow(workflowID string, jobID string, greeting string) runtimecore.Workflow {
	return runtimecore.NewWorkflow(
		workflowID,
		runtimecore.WorkflowStep{Kind: runtimecore.WorkflowStepKindPersist, Name: "greeting", Value: greeting},
		runtimecore.WorkflowStep{Kind: runtimecore.WorkflowStepKindCallJob, Name: "child-job", Value: jobID},
		runtimecore.WorkflowStep{Kind: runtimecore.WorkflowStepKindWaitEvent, Name: "wait-confirm", Value: "message.received"},
		runtimecore.WorkflowStep{Kind: runtimecore.WorkflowStepKindPersist, Name: "resume-note", Value: "resumed-after-child-job"},
		runtimecore.WorkflowStep{Kind: runtimecore.WorkflowStepKindCompensate, Name: "complete"},
	)
}

func workflowSessionID(event eventmodel.Event) string {
	if event.Actor != nil && event.Actor.ID != "" {
		return "workflow-" + event.Actor.ID
	}
	return "workflow-anon"
}

func workflowRunID(event eventmodel.Event, now time.Time) string {
	base := workflowSessionID(event)
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return fmt.Sprintf("%s-run-%d", base, now.UnixNano())
}

func workflowChildJobID(workflowID string) string {
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return "job-workflow-demo-anon"
	}
	return "job-workflow-demo-" + workflowID
}

func workflowChildJob(event eventmodel.Event, ctx eventmodel.ExecutionContext, jobID string, pluginID string, workflowID string) pluginsdk.Job {
	job := pluginsdk.NewJob(jobID, "ai.chat", 0, 30*time.Second)
	job.TraceID = ctx.TraceID
	job.EventID = ctx.EventID
	job.RunID = ctx.RunID
	job.Correlation = ctx.CorrelationID
	if strings.TrimSpace(job.Correlation) == "" {
		job.Correlation = event.IdempotencyKey
	}
	job.Payload = map[string]any{
		"prompt": workflowDemoPrompt(event.Message.Text),
		"dispatch": map[string]any{
			"actor":            "runtime-job-runner",
			"permission":       "job:run",
			"target_plugin_id": "plugin-ai-chat",
		},
		"reply_target": replyTarget(ctx),
		"reply_handle": replyHandlePayload(ctx),
		"session_id":   workflowSessionID(event),
		"workflow": map[string]any{
			"workflow_id": workflowID,
			"plugin_id":   pluginID,
		},
	}
	return job
}

func workflowDemoPrompt(greeting string) string {
	greeting = strings.TrimSpace(greeting)
	if greeting == "" {
		greeting = "workflow"
	}
	return fmt.Sprintf("workflow demo child job: %s", greeting)
}

func replyTarget(ctx eventmodel.ExecutionContext) string {
	if ctx.Reply == nil {
		return ""
	}
	return ctx.Reply.TargetID
}

func replyHandlePayload(ctx eventmodel.ExecutionContext) map[string]any {
	if ctx.Reply == nil {
		return nil
	}
	payload := map[string]any{
		"capability": ctx.Reply.Capability,
		"target_id":  ctx.Reply.TargetID,
		"message_id": ctx.Reply.MessageID,
	}
	if len(ctx.Reply.Metadata) > 0 {
		metadata := make(map[string]any, len(ctx.Reply.Metadata))
		for key, value := range ctx.Reply.Metadata {
			metadata[key] = value
		}
		payload["metadata"] = metadata
	}
	return payload
}

func (p *Plugin) inspectChildJob(ctx context.Context, jobID string) (pluginsdk.Job, bool, error) {
	job, err := p.Queue.Inspect(ctx, jobID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			return pluginsdk.Job{}, false, nil
		}
		return pluginsdk.Job{}, false, err
	}
	return job, true, nil
}

func (p *Plugin) inspectWorkflow(ctx context.Context, workflowID string) (runtimecore.WorkflowTransition, runtimecore.WorkflowInstanceState, bool, error) {
	if p.Workflows == nil {
		return runtimecore.WorkflowTransition{}, runtimecore.WorkflowInstanceState{}, false, nil
	}
	loader, ok := p.Workflows.(interface {
		Load(context.Context, string) (runtimecore.WorkflowInstanceState, error)
	})
	if !ok {
		return runtimecore.WorkflowTransition{}, runtimecore.WorkflowInstanceState{}, false, nil
	}
	instance, err := loader.Load(ctx, workflowID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no rows") {
			return runtimecore.WorkflowTransition{}, runtimecore.WorkflowInstanceState{}, false, nil
		}
		return runtimecore.WorkflowTransition{}, runtimecore.WorkflowInstanceState{}, false, err
	}
	return runtimecore.WorkflowTransition{Instance: instance}, instance, true, nil
}

package pluginaichat

import (
	"context"
	"errors"
	"time"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

const (
	pluginAIChatPublishSourceURI    = "https://github.com/ohmyopencode/bot-platform/tree/main/plugins/plugin-ai-chat"
	pluginAIChatRuntimeVersionRange = ">=0.1.0 <1.0.0"
)

type AIProvider interface {
	Generate(ctx context.Context, prompt string) (string, error)
}

type SessionStore interface {
	SaveSession(ctx context.Context, session pluginsdk.SessionState) error
}

type JobQueue interface {
	Enqueue(context.Context, pluginsdk.Job) error
	MarkRunning(context.Context, string) (pluginsdk.Job, error)
	Complete(context.Context, string) (pluginsdk.Job, error)
	Fail(context.Context, string, string) (pluginsdk.Job, error)
	Inspect(context.Context, string) (pluginsdk.Job, error)
}

type Plugin struct {
	Manifest     pluginsdk.PluginManifest
	Queue        JobQueue
	Provider     AIProvider
	Sessions     SessionStore
	ReplyService pluginsdk.ReplyService
	Now          func() time.Time
}

func New(queue JobQueue, provider AIProvider, sessions SessionStore, replyService pluginsdk.ReplyService) *Plugin {
	return &Plugin{
		Manifest: pluginsdk.PluginManifest{
			SchemaVersion: pluginsdk.SupportedPluginManifestSchemaVersion,
			ID:            "plugin-ai-chat",
			Name:          "AI Chat Plugin",
			Version:       "0.1.0",
			APIVersion:    "v0",
			Mode:          pluginsdk.ModeSubprocess,
			Permissions: []string{
				"reply:send",
				"job:enqueue",
				"job:run",
				"session:write",
			},
			Publish: &pluginsdk.PluginPublish{
				SourceType:          pluginsdk.PublishSourceTypeGit,
				SourceURI:           pluginAIChatPublishSourceURI,
				RuntimeVersionRange: pluginAIChatRuntimeVersionRange,
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/plugin-ai-chat", Symbol: "Plugin"},
		},
		Queue:        queue,
		Provider:     provider,
		Sessions:     sessions,
		ReplyService: replyService,
		Now:          time.Now().UTC,
	}
}

func (p *Plugin) Definition() pluginsdk.Plugin {
	return pluginsdk.Plugin{Manifest: p.Manifest, Handlers: pluginsdk.Handlers{Event: p, Job: p}}
}

func (p *Plugin) OnEvent(event eventmodel.Event, ctx eventmodel.ExecutionContext) error {
	if event.Type != "message.received" || event.Message == nil {
		return nil
	}
	if p.Queue == nil || p.Sessions == nil {
		return errors.New("queue and session store are required")
	}
	if ctx.Reply == nil {
		return errors.New("reply handle is required")
	}
	if err := ctx.Reply.Validate(); err != nil {
		return err
	}
	job := pluginsdk.NewJob("job-ai-chat-"+event.EventID, "ai.chat", 1, 30*time.Second)
	job.TraceID = ctx.TraceID
	job.EventID = ctx.EventID
	job.RunID = ctx.RunID
	job.Correlation = ctx.CorrelationID
	if job.Correlation == "" {
		job.Correlation = event.IdempotencyKey
	}
	job.Payload = map[string]any{
		"prompt": event.Message.Text,
		"dispatch": map[string]any{
			"actor":            "runtime-job-runner",
			"permission":       "job:run",
			"target_plugin_id": p.Manifest.ID,
		},
		"reply_target": replyTarget(ctx),
		"reply_handle": replyHandlePayload(ctx),
		"session_id":   sessionID(event),
	}
	if err := p.Queue.Enqueue(context.Background(), job); err != nil {
		return err
	}
	return p.Sessions.SaveSession(context.Background(), pluginsdk.SessionState{
		SessionID: sessionID(event),
		PluginID:  p.Manifest.ID,
		State:     map[string]any{"last_prompt": event.Message.Text},
	})
}

func (p *Plugin) ProcessJob(ctx context.Context, jobID string, reply eventmodel.ReplyHandle) error {
	if p.Queue == nil || p.Provider == nil || p.ReplyService == nil {
		return errors.New("queue, provider, and reply service are required")
	}
	job, err := p.Queue.Inspect(ctx, jobID)
	if err != nil {
		return err
	}
	if job.Status != pluginsdk.JobStatusRunning {
		job, err = p.Queue.MarkRunning(ctx, jobID)
		if err != nil {
			return err
		}
	}
	prompt, _ := job.Payload["prompt"].(string)

	result, err := p.Provider.Generate(ctx, prompt)
	if err != nil {
		updated, failErr := p.Queue.Fail(ctx, jobID, err.Error())
		if failErr != nil {
			return failErr
		}
		if updated.Status == pluginsdk.JobStatusDead {
			return p.ReplyService.ReplyText(reply, "AI request failed: "+err.Error())
		}
		return err
	}
	if err := p.ReplyService.ReplyText(reply, result); err != nil {
		updated, failErr := p.Queue.Fail(ctx, jobID, err.Error())
		if failErr != nil {
			return failErr
		}
		if updated.Status == pluginsdk.JobStatusDead {
			return nil
		}
		return err
	}
	_, err = p.Queue.Complete(ctx, jobID)
	return err
}

func (p *Plugin) OnJob(job pluginsdk.JobInvocation, ctx eventmodel.ExecutionContext) error {
	if ctx.Reply == nil {
		return errors.New("reply handle is required for ai job dispatch")
	}
	reply := withObservabilityMetadata(*ctx.Reply, ctx, p.Manifest.ID)
	return p.ProcessJob(context.Background(), job.ID, reply)
}

func withObservabilityMetadata(handle eventmodel.ReplyHandle, ctx eventmodel.ExecutionContext, pluginID string) eventmodel.ReplyHandle {
	metadata := make(map[string]any, len(handle.Metadata)+5)
	for key, value := range handle.Metadata {
		metadata[key] = value
	}
	metadata["trace_id"] = ctx.TraceID
	metadata["event_id"] = ctx.EventID
	metadata["run_id"] = ctx.RunID
	metadata["correlation_id"] = ctx.CorrelationID
	metadata["plugin_id"] = pluginID
	handle.Metadata = metadata
	return handle
}

func sessionID(event eventmodel.Event) string {
	if event.Actor != nil && event.Actor.ID != "" {
		return "session-" + event.Actor.ID
	}
	return "session-anon"
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

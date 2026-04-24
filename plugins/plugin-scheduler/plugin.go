package pluginscheduler

import (
	"errors"
	"fmt"
	"sort"
	"time"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

const (
	pluginSchedulerPublishSourceURI    = "https://github.com/ohmyopencode/bot-platform/tree/main/plugins/plugin-scheduler"
	pluginSchedulerRuntimeVersionRange = ">=0.1.0 <1.0.0"
)

type SchedulerService interface {
	Register(plan pluginsdk.SchedulePlan) error
	Plan(id string) (pluginsdk.SchedulePlan, error)
	Trigger(id string) (eventmodel.Event, error)
	Cancel(id string) error
	Plans() []pluginsdk.SchedulePlan
}

type Plugin struct {
	Manifest     pluginsdk.PluginManifest
	Scheduler    SchedulerService
	ReplyService pluginsdk.ReplyService
}

func New(scheduler SchedulerService, replyService pluginsdk.ReplyService) Plugin {
	return Plugin{
		Manifest: pluginsdk.PluginManifest{
			SchemaVersion: pluginsdk.SupportedPluginManifestSchemaVersion,
			ID:            "plugin-scheduler",
			Name:          "Scheduler Plugin",
			Version:       "0.1.0",
			APIVersion:    "v0",
			Mode:          pluginsdk.ModeSubprocess,
			Permissions:   []string{"reply:send", "schedule:manage"},
			Publish: &pluginsdk.PluginPublish{
				SourceType:          pluginsdk.PublishSourceTypeGit,
				SourceURI:           pluginSchedulerPublishSourceURI,
				RuntimeVersionRange: pluginSchedulerRuntimeVersionRange,
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/plugin-scheduler", Symbol: "Plugin"},
		},
		Scheduler:    scheduler,
		ReplyService: replyService,
	}
}

func (p Plugin) Definition() pluginsdk.Plugin {
	return pluginsdk.Plugin{Manifest: p.Manifest, Handlers: pluginsdk.Handlers{Event: p}}
}

func (p Plugin) CreateCronPlan(id, cronExpr, message string) error {
	if p.Scheduler == nil {
		return errors.New("scheduler service is required")
	}
	return p.Scheduler.Register(pluginsdk.SchedulePlan{
		ID:        id,
		Kind:      pluginsdk.ScheduleKindCron,
		CronExpr:  cronExpr,
		Source:    "scheduler",
		EventType: "schedule.triggered",
		Metadata:  map[string]any{"message": message},
	})
}

func (p Plugin) CreateDelayPlan(id string, delaySeconds int, message string) error {
	if p.Scheduler == nil {
		return errors.New("scheduler service is required")
	}
	return p.Scheduler.Register(pluginsdk.SchedulePlan{
		ID:        id,
		Kind:      pluginsdk.ScheduleKindDelay,
		Delay:     time.Duration(delaySeconds) * time.Second,
		Source:    "scheduler",
		EventType: "schedule.triggered",
		Metadata:  map[string]any{"message": message},
	})
}

func (p Plugin) ListPlanIDs() []string {
	if p.Scheduler == nil {
		return nil
	}
	plans := p.Scheduler.Plans()
	ids := make([]string, 0, len(plans))
	for _, plan := range plans {
		ids = append(ids, plan.ID)
	}
	sort.Strings(ids)
	return ids
}

func (p Plugin) CancelPlan(id string) error {
	if p.Scheduler == nil {
		return errors.New("scheduler service is required")
	}
	return p.Scheduler.Cancel(id)
}

func (p Plugin) OnEvent(event eventmodel.Event, ctx eventmodel.ExecutionContext) error {
	if event.Type != "schedule.triggered" {
		return nil
	}
	if ctx.Reply == nil {
		return errors.New("reply handle is required")
	}
	if p.ReplyService == nil {
		return errors.New("reply service is required")
	}
	message, _ := event.Metadata["message"].(string)
	if message == "" {
		message = fmt.Sprintf("scheduled event: %s", event.System.Name)
	}
	return p.ReplyService.ReplyText(*ctx.Reply, message)
}

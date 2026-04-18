package pluginadmin

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

type PluginLifecycleService interface {
	Enable(pluginID string) error
	Disable(pluginID string) error
}

type RolloutManager interface {
	Prepare(pluginID string) (pluginsdk.RolloutRecord, error)
	CanActivate(pluginID string) (pluginsdk.RolloutRecord, error)
	Activate(pluginID string) (pluginsdk.RolloutRecord, error)
	Record(pluginID string) (pluginsdk.RolloutRecord, bool)
}

type ReplayService interface {
	ReplayEvent(eventID string) (eventmodel.Event, error)
}

type AuditRecorder interface {
	RecordAudit(entry pluginsdk.AuditEntry) error
}

type CurrentAuthorizerProvider interface {
	CurrentAuthorizer() *pluginsdk.Authorizer
}

type RolePolicy = pluginsdk.AuthorizationPolicy

type Plugin struct {
	Manifest      pluginsdk.PluginManifest
	Lifecycle     PluginLifecycleService
	Rollouts      RolloutManager
	Replay        ReplayService
	AuditTrail    []pluginsdk.AuditEntry
	AuditRecorder AuditRecorder
	Authorizer    *pluginsdk.Authorizer
	Provider      CurrentAuthorizerProvider
	CurrentTime   func() time.Time
}


func New(lifecycle PluginLifecycleService, rollouts RolloutManager, replay ReplayService, provider CurrentAuthorizerProvider, actorRoles map[string][]string, policies map[string]RolePolicy, recorder AuditRecorder) *Plugin {
	return &Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-admin",
			Name:       "Admin Plugin",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			Permissions: []string{
				"plugin:enable",
				"plugin:disable",
				"plugin:replay",
				"plugin:rollout",
			},
			Entry: pluginsdk.PluginEntry{Module: "plugins/plugin-admin", Symbol: "Plugin"},
		},
		Lifecycle:     lifecycle,
		Rollouts:      rollouts,
		Replay:        replay,
		AuditRecorder: recorder,
		Provider:      provider,
		Authorizer:    pluginsdk.NewAuthorizer(actorRoles, policies),
		CurrentTime:   time.Now().UTC,
	}
}

func (p *Plugin) Definition() pluginsdk.Plugin {
	return pluginsdk.Plugin{Manifest: p.Manifest, Handlers: pluginsdk.Handlers{Command: p}}
}

func (p *Plugin) OnCommand(command eventmodel.CommandInvocation, ctx eventmodel.ExecutionContext) error {
	actor := actorFromCommand(command)

	parts := commandArguments(command)
	if len(parts) < 3 {
		return p.recordAndJoin(actor, command.Name, command.Raw, "", false, errors.New("admin command requires action and target"))
	}

	action := parts[1]
	target := parts[2]
	if action == "prepare" || action == "activate" {
		return p.handleRollout(actor, action, target)
	}
	if action == "replay" {
		return p.handleReplay(actor, target)
	}
	permission := permissionForAction(action)
	if permission == "" {
		return p.recordAndJoin(actor, action, target, "", false, fmt.Errorf("unsupported admin action %q", action))
	}
	decision := p.currentAuthorizer().Authorize(actor, permission, target)
	if !decision.Allowed {
		return p.recordAndJoin(actor, action, target, permission, false, errors.New(decision.Reason))
	}

	var err error
	switch action {
	case "enable":
		err = p.Lifecycle.Enable(target)
	case "disable":
		err = p.Lifecycle.Disable(target)
	}
	return p.recordAndJoin(actor, action, target, permission, true, err)
}

func (p *Plugin) handleReplay(actor, eventID string) error {
	permission := permissionForAction("replay")
	decision := p.currentAuthorizer().AuthorizeTarget(actor, permission, pluginsdk.AuthorizationTargetEvent, eventID)
	if !decision.Allowed {
		return p.recordAndJoin(actor, "replay", eventID, permission, false, errors.New(decision.Reason))
	}
	if p.Replay == nil {
		return p.recordAndJoin(actor, "replay", eventID, permission, true, errors.New("replay service is required"))
	}
	_, err := p.Replay.ReplayEvent(eventID)
	return p.recordAndJoin(actor, "replay", eventID, permission, true, err)
}

func (p *Plugin) handleRollout(actor, action, target string) error {
	permission := permissionForAction(action)
	if permission == "" {
		return p.recordAndJoin(actor, action, target, "", false, fmt.Errorf("unsupported admin action %q", action))
	}
	decision := p.currentAuthorizer().Authorize(actor, permission, target)
	if !decision.Allowed {
		return p.recordAndJoin(actor, action, target, permission, false, errors.New(decision.Reason))
	}
	if p.Rollouts == nil {
		return p.recordAndJoin(actor, action, target, permission, true, errors.New("rollout manager is required"))
	}
	var err error
	switch action {
	case "prepare":
		_, err = p.Rollouts.Prepare(target)
	case "activate":
		if _, err = p.Rollouts.CanActivate(target); err != nil {
			break
		}
		if p.Lifecycle == nil {
			err = errors.New("plugin lifecycle service is required")
			break
		}
		if err = p.Lifecycle.Enable(target); err != nil {
			break
		}
		_, err = p.Rollouts.Activate(target)
	}
	return p.recordAndJoin(actor, action, target, permission, true, err)
}

func (p *Plugin) AuditLog() []pluginsdk.AuditEntry {
	return append([]pluginsdk.AuditEntry(nil), p.AuditTrail...)
}

func (p *Plugin) recordAndJoin(actor, action, target, permission string, allowed bool, actionErr error) error {
	auditErr := p.recordAudit(actor, action, target, permission, allowed, auditReason(action, actionErr))
	if actionErr != nil && auditErr != nil {
		return errors.Join(actionErr, fmt.Errorf("record audit: %w", auditErr))
	}
	if actionErr != nil {
		return actionErr
	}
	return auditErr
}

func (p *Plugin) recordAudit(actor, action, target, permission string, allowed bool, reason string) error {
	entry := pluginsdk.AuditEntry{
		Actor:      actor,
		Permission: permission,
		Action:     action,
		Target:     target,
		Allowed:    allowed,
		OccurredAt: p.CurrentTime().Format(time.RFC3339),
	}
	setAuditReason(&entry, reason)
	p.AuditTrail = append(p.AuditTrail, entry)
	if p.AuditRecorder != nil {
		return p.AuditRecorder.RecordAudit(entry)
	}
	return nil
}

func setAuditReason(entry *pluginsdk.AuditEntry, reason string) {
	if entry == nil || reason == "" {
		return
	}
	value := reflect.ValueOf(entry).Elem()
	field := value.FieldByName("Reason")
	if field.IsValid() && field.CanSet() && field.Kind() == reflect.String {
		field.SetString(reason)
	}
}

func auditReason(action string, actionErr error) string {
	if actionErr == nil {
		switch action {
		case "prepare":
			return "rollout_prepared"
		case "activate":
			return "rollout_activated"
		default:
			return ""
		}
	}
	message := actionErr.Error()
	switch {
	case strings.Contains(message, "permission denied"):
		return "permission_denied"
	case strings.Contains(message, "plugin scope denied"):
		return "scope_denied"
	case strings.Contains(message, "rollout is not prepared"):
		return "rollout_not_prepared"
	case strings.Contains(message, "drifted after prepare"):
		return "rollout_drifted"
	case strings.Contains(message, "cannot replay a replayed event"):
		return "replay_rejected"
	case action == "replay":
		return "replay_failed"
	case action == "prepare" || action == "activate":
		return "rollout_failed"
	default:
		return "action_failed"
	}
}

func actorFromCommand(command eventmodel.CommandInvocation) string {
	if command.Metadata == nil {
		return ""
	}
	actor, _ := command.Metadata["actor"].(string)
	return actor
}

func commandArguments(command eventmodel.CommandInvocation) []string {
	if len(command.Arguments) >= 2 {
		return append([]string{command.Name}, command.Arguments...)
	}
	return strings.Fields(command.Raw)
}

func permissionForAction(action string) string {
	switch action {
	case "enable":
		return "plugin:enable"
	case "disable":
		return "plugin:disable"
	case "prepare":
		return "plugin:rollout"
	case "activate":
		return "plugin:rollout"
	case "replay":
		return "plugin:replay"
	default:
		return ""
	}
}

func (p *Plugin) currentAuthorizer() *pluginsdk.Authorizer {
	if p != nil && p.Provider != nil {
		if authorizer := p.Provider.CurrentAuthorizer(); authorizer != nil {
			return authorizer
		}
	}
	if p == nil || p.Authorizer == nil {
		return pluginsdk.NewAuthorizer(nil, nil)
	}
	return p.Authorizer
}

package pluginadmin

import (
	"errors"
	"fmt"
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
		return p.recordAndJoin(command, ctx, actor, command.Name, command.Raw, "", false, errors.New("admin command requires action and target"))
	}

	action := parts[1]
	target := parts[2]
	if action == "prepare" || action == "activate" {
		return p.handleRollout(command, ctx, actor, action, target)
	}
	if action == "replay" {
		return p.handleReplay(command, ctx, actor, target)
	}
	permission := permissionForAction(action)
	if permission == "" {
		return p.recordAndJoin(command, ctx, actor, action, target, "", false, fmt.Errorf("unsupported admin action %q", action))
	}
	decision := p.currentAuthorizer().Authorize(actor, permission, target)
	if !decision.Allowed {
		return p.recordAndJoin(command, ctx, actor, action, target, permission, false, errors.New(decision.Reason))
	}

	var err error
	switch action {
	case "enable":
		err = p.Lifecycle.Enable(target)
	case "disable":
		err = p.Lifecycle.Disable(target)
	}
	return p.recordAndJoin(command, ctx, actor, action, target, permission, true, err)
}

func (p *Plugin) handleReplay(command eventmodel.CommandInvocation, ctx eventmodel.ExecutionContext, actor, eventID string) error {
	permission := permissionForAction("replay")
	decision := p.currentAuthorizer().AuthorizeTarget(actor, permission, pluginsdk.AuthorizationTargetEvent, eventID)
	if !decision.Allowed {
		return p.recordAndJoin(command, ctx, actor, "replay", eventID, permission, false, errors.New(decision.Reason))
	}
	if p.Replay == nil {
		return p.recordAndJoin(command, ctx, actor, "replay", eventID, permission, true, errors.New("replay service is required"))
	}
	_, err := p.Replay.ReplayEvent(eventID)
	return p.recordAndJoin(command, ctx, actor, "replay", eventID, permission, true, err)
}

func (p *Plugin) handleRollout(command eventmodel.CommandInvocation, ctx eventmodel.ExecutionContext, actor, action, target string) error {
	permission := permissionForAction(action)
	if permission == "" {
		return p.recordAndJoin(command, ctx, actor, action, target, "", false, fmt.Errorf("unsupported admin action %q", action))
	}
	decision := p.currentAuthorizer().Authorize(actor, permission, target)
	if !decision.Allowed {
		return p.recordAndJoin(command, ctx, actor, action, target, permission, false, errors.New(decision.Reason))
	}
	if p.Rollouts == nil {
		return p.recordAndJoin(command, ctx, actor, action, target, permission, true, errors.New("rollout manager is required"))
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
	return p.recordAndJoin(command, ctx, actor, action, target, permission, true, err)
}

func (p *Plugin) AuditLog() []pluginsdk.AuditEntry {
	return append([]pluginsdk.AuditEntry(nil), p.AuditTrail...)
}

func (p *Plugin) recordAndJoin(command eventmodel.CommandInvocation, executionContext eventmodel.ExecutionContext, actor, action, target, permission string, allowed bool, actionErr error) error {
	auditErr := p.recordAudit(command, executionContext, actor, action, target, permission, allowed, auditReason(action, permission, actionErr))
	if actionErr != nil && auditErr != nil {
		return errors.Join(actionErr, fmt.Errorf("record audit: %w", auditErr))
	}
	if actionErr != nil {
		return actionErr
	}
	return auditErr
}

func (p *Plugin) recordAudit(command eventmodel.CommandInvocation, executionContext eventmodel.ExecutionContext, actor, action, target, permission string, allowed bool, reason string) error {
	reason = strings.TrimSpace(reason)
	entry := pluginsdk.AuditEntry{
		Actor:      strings.TrimSpace(actor),
		Permission: strings.TrimSpace(permission),
		Action:     auditActionName(action, permission),
		Target:     strings.TrimSpace(target),
		Allowed:    allowed,
		OccurredAt: p.CurrentTime().Format(time.RFC3339),
	}
	if reason != "" {
		entry.Reason = reason
		if shouldNormalizeOperatorAudit(permission) {
			if allowed {
				entry.ErrorCategory = "operator"
				entry.ErrorCode = reason
			} else {
				entry.ErrorCategory = "authorization"
				entry.ErrorCode = reason
			}
		}
	}
	applyAuditExecutionContext(&entry, executionContext)
	if sessionID := commandSessionIDFromMetadata(command); sessionID != "" {
		entry.SessionID = sessionID
	}
	p.AuditTrail = append(p.AuditTrail, entry)
	if p.AuditRecorder != nil {
		return p.AuditRecorder.RecordAudit(entry)
	}
	return nil
}

func auditActionName(action, permission string) string {
	permission = strings.TrimSpace(permission)
	switch permission {
	case "plugin:enable":
		return "plugin.enable"
	case "plugin:disable":
		return "plugin.disable"
	}
	if permission != "" {
		if !shouldNormalizeOperatorAudit(permission) {
			return strings.TrimSpace(action)
		}
		return strings.ReplaceAll(permission, ":", ".")
	}
	return strings.TrimSpace(action)
}

func shouldNormalizeOperatorAudit(permission string) bool {
	permission = strings.TrimSpace(permission)
	switch permission {
	case "plugin:enable", "plugin:disable":
		return true
	default:
		return false
	}
}

func applyAuditExecutionContext(entry *pluginsdk.AuditEntry, ctx eventmodel.ExecutionContext) {
	if entry == nil {
		return
	}
	ctx = normalizeAuditExecutionContext(ctx)
	if strings.TrimSpace(entry.TraceID) == "" {
		entry.TraceID = strings.TrimSpace(ctx.TraceID)
	}
	if strings.TrimSpace(entry.EventID) == "" {
		entry.EventID = strings.TrimSpace(ctx.EventID)
	}
	if strings.TrimSpace(entry.PluginID) == "" {
		entry.PluginID = strings.TrimSpace(ctx.PluginID)
	}
	if strings.TrimSpace(entry.RunID) == "" {
		entry.RunID = strings.TrimSpace(ctx.RunID)
	}
	if strings.TrimSpace(entry.CorrelationID) == "" {
		entry.CorrelationID = strings.TrimSpace(ctx.CorrelationID)
	}
	if strings.TrimSpace(entry.SessionID) == "" && ctx.Metadata != nil {
		if sessionID, _ := ctx.Metadata["session_id"].(string); strings.TrimSpace(sessionID) != "" {
			entry.SessionID = strings.TrimSpace(sessionID)
		}
	}
}

func normalizeAuditExecutionContext(ctx eventmodel.ExecutionContext) eventmodel.ExecutionContext {
	ctx.TraceID = strings.TrimSpace(ctx.TraceID)
	ctx.EventID = strings.TrimSpace(ctx.EventID)
	ctx.PluginID = strings.TrimSpace(ctx.PluginID)
	ctx.RunID = strings.TrimSpace(ctx.RunID)
	ctx.CorrelationID = strings.TrimSpace(ctx.CorrelationID)
	if ctx.RunID == "" && ctx.EventID != "" {
		ctx.RunID = "run-" + ctx.EventID
	}
	if ctx.CorrelationID == "" {
		ctx.CorrelationID = ctx.EventID
	}
	return ctx
}

func auditReason(action, permission string, actionErr error) string {
	if actionErr == nil {
		switch action {
		case "enable":
			return "plugin_enabled"
		case "disable":
			return "plugin_disabled"
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
		if shouldNormalizeOperatorAudit(permission) {
			return "plugin_scope_denied"
		}
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

func commandSessionIDFromMetadata(command eventmodel.CommandInvocation) string {
	if command.Metadata == nil {
		return ""
	}
	sessionID, _ := command.Metadata["session_id"].(string)
	return strings.TrimSpace(sessionID)
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

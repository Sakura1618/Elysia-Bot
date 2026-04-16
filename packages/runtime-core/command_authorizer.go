package runtimecore

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

type AdminCommandAuthorizer struct {
	authorizer *pluginsdk.Authorizer
	actorRoles map[string][]string
	policies   map[string]pluginsdk.AuthorizationPolicy
}

type EventAuthorizer interface {
	AuthorizeEvent(context.Context, eventmodel.Event, eventmodel.ExecutionContext, pluginsdk.Plugin) error
}

type JobAuthorizer interface {
	AuthorizeJob(context.Context, pluginsdk.JobInvocation, eventmodel.ExecutionContext, pluginsdk.Plugin) error
}

type ScheduleAuthorizer interface {
	AuthorizeSchedule(context.Context, pluginsdk.ScheduleTrigger, eventmodel.ExecutionContext, pluginsdk.Plugin) error
}

type MetadataEventAuthorizer struct {
	authorizer *pluginsdk.Authorizer
}

type MetadataJobAuthorizer struct {
	authorizer *pluginsdk.Authorizer
}

type MetadataScheduleAuthorizer struct {
	authorizer *pluginsdk.Authorizer
}

type ConsoleReadRequestAuthorizer interface {
	AuthorizeConsoleRead(context.Context, *http.Request) error
}

type ConsoleReadAuthorizer struct {
	authorizer *pluginsdk.Authorizer
	permission string
}

const (
	ConsoleReadActorHeader = "X-Bot-Platform-Actor"
	consoleReadTarget      = "console"
)

func NewAdminCommandAuthorizer(cfg *RBACConfig) *AdminCommandAuthorizer {
	if cfg == nil {
		return nil
	}
	return &AdminCommandAuthorizer{authorizer: pluginsdk.NewAuthorizer(cfg.ActorRoles, cfg.Policies), actorRoles: cfg.ActorRoles, policies: cfg.Policies}
}

func NewMetadataEventAuthorizer(cfg *RBACConfig) *MetadataEventAuthorizer {
	if cfg == nil {
		return nil
	}
	return &MetadataEventAuthorizer{authorizer: pluginsdk.NewAuthorizer(cfg.ActorRoles, cfg.Policies)}
}

func NewMetadataJobAuthorizer(cfg *RBACConfig) *MetadataJobAuthorizer {
	if cfg == nil {
		return nil
	}
	return &MetadataJobAuthorizer{authorizer: pluginsdk.NewAuthorizer(cfg.ActorRoles, cfg.Policies)}
}

func NewMetadataScheduleAuthorizer(cfg *RBACConfig) *MetadataScheduleAuthorizer {
	if cfg == nil {
		return nil
	}
	return &MetadataScheduleAuthorizer{authorizer: pluginsdk.NewAuthorizer(cfg.ActorRoles, cfg.Policies)}
}

func NewConsoleReadAuthorizer(cfg *RBACConfig) *ConsoleReadAuthorizer {
	if cfg == nil || strings.TrimSpace(cfg.ConsoleReadPermission) == "" {
		return nil
	}
	return &ConsoleReadAuthorizer{authorizer: pluginsdk.NewAuthorizer(cfg.ActorRoles, cfg.Policies), permission: cfg.ConsoleReadPermission}
}

func AuthorizeRBACAction(cfg *RBACConfig, actor, permission, target string) error {
	if cfg == nil {
		return nil
	}
	if strings.TrimSpace(permission) == "" || strings.TrimSpace(target) == "" {
		return nil
	}
	decision := pluginsdk.NewAuthorizer(cfg.ActorRoles, cfg.Policies).Authorize(actor, permission, target)
	if decision.Allowed {
		return nil
	}
	if decision.Reason == "" {
		return errors.New("permission denied")
	}
	return errors.New(decision.Reason)
}

func (a *AdminCommandAuthorizer) AuthorizeCommand(_ context.Context, command eventmodel.CommandInvocation, _ eventmodel.ExecutionContext) error {
	if a == nil || a.authorizer == nil {
		return nil
	}
	actor, action, target, permission, targetKind := commandAuthorizationFields(command)
	if action == "" || target == "" || permission == "" {
		return nil
	}
	decision := a.authorizeTarget(actor, permission, targetKind, target)
	if decision.Allowed {
		return nil
	}
	if decision.Reason == "" {
		return errors.New("permission denied")
	}
	return errors.New(decision.Reason)
}

func (a *AdminCommandAuthorizer) authorizeTarget(actor, permission string, kind adminTargetKind, target string) pluginsdk.AuthorizationDecision {
	if kind != adminTargetEvent {
		return a.authorizer.Authorize(actor, permission, target)
	}
	hasPermission := false
	for _, role := range a.actorRoles[actor] {
		policy := a.policies[role]
		if !policyHasPermission(policy, permission) {
			continue
		}
		hasPermission = true
		for _, allowedTarget := range adminPolicyTargets(policy, kind) {
			if allowedTarget == "*" || allowedTarget == target {
				return pluginsdk.AuthorizationDecision{Allowed: true, Permission: permission}
			}
		}
	}
	if len(a.actorRoles[actor]) == 0 || !hasPermission {
		return pluginsdk.AuthorizationDecision{Allowed: false, Permission: permission, Reason: "permission denied"}
	}
	return pluginsdk.AuthorizationDecision{Allowed: false, Permission: permission, Reason: "plugin scope denied"}
}

func (a *MetadataEventAuthorizer) AuthorizeEvent(_ context.Context, event eventmodel.Event, _ eventmodel.ExecutionContext, plugin pluginsdk.Plugin) error {
	if a == nil || a.authorizer == nil {
		return nil
	}
	actor, permission := eventAuthorizationFields(event)
	if permission == "" {
		return nil
	}
	decision := a.authorizer.Authorize(actor, permission, plugin.Manifest.ID)
	if decision.Allowed {
		return nil
	}
	if decision.Reason == "" {
		return errors.New("permission denied")
	}
	return errors.New(decision.Reason)
}

func (a *MetadataJobAuthorizer) AuthorizeJob(_ context.Context, job pluginsdk.JobInvocation, _ eventmodel.ExecutionContext, plugin pluginsdk.Plugin) error {
	if a == nil || a.authorizer == nil {
		return nil
	}
	actor, permission := metadataAuthorizationFields(job.Metadata)
	if permission == "" {
		return nil
	}
	decision := a.authorizer.Authorize(actor, permission, plugin.Manifest.ID)
	if decision.Allowed {
		return nil
	}
	if decision.Reason == "" {
		return errors.New("permission denied")
	}
	return errors.New(decision.Reason)
}

func (a *MetadataScheduleAuthorizer) AuthorizeSchedule(_ context.Context, trigger pluginsdk.ScheduleTrigger, _ eventmodel.ExecutionContext, plugin pluginsdk.Plugin) error {
	if a == nil || a.authorizer == nil {
		return nil
	}
	actor, permission := metadataAuthorizationFields(trigger.Metadata)
	if permission == "" {
		return nil
	}
	decision := a.authorizer.Authorize(actor, permission, plugin.Manifest.ID)
	if decision.Allowed {
		return nil
	}
	if decision.Reason == "" {
		return errors.New("permission denied")
	}
	return errors.New(decision.Reason)
}

func (a *ConsoleReadAuthorizer) AuthorizeConsoleRead(_ context.Context, request *http.Request) error {
	if a == nil || a.authorizer == nil {
		return nil
	}
	actor := strings.TrimSpace(request.Header.Get(ConsoleReadActorHeader))
	decision := a.authorizer.Authorize(actor, a.permission, consoleReadTarget)
	if decision.Allowed {
		return nil
	}
	if decision.Reason == "" {
		return errors.New("permission denied")
	}
	return errors.New(decision.Reason)
}

type adminTargetKind string

const (
	adminTargetPlugin adminTargetKind = "plugin"
	adminTargetEvent  adminTargetKind = "event"
)

func commandAuthorizationFields(command eventmodel.CommandInvocation) (actor, action, target, permission string, targetKind adminTargetKind) {
	actor, _ = command.Metadata["actor"].(string)
	if command.Name != "admin" {
		return actor, "", "", "", adminTargetPlugin
	}
	parts := commandArguments(command)
	if len(parts) < 3 {
		return actor, "", "", "", adminTargetPlugin
	}
	action = parts[1]
	target = parts[2]
	permission = adminPermissionForAction(action)
	targetKind = adminTargetKindForAction(action)
	return actor, action, target, permission, targetKind
}

func adminTargetKindForAction(action string) adminTargetKind {
	if action == "replay" {
		return adminTargetEvent
	}
	return adminTargetPlugin
}

func policyHasPermission(policy pluginsdk.AuthorizationPolicy, permission string) bool {
	for _, candidate := range policy.Permissions {
		if candidate == permission {
			return true
		}
	}
	return false
}

func adminPolicyTargets(policy pluginsdk.AuthorizationPolicy, kind adminTargetKind) []string {
	if kind != adminTargetEvent {
		return policy.PluginScope
	}
	value := reflect.ValueOf(policy)
	field := value.FieldByName("EventScope")
	if field.IsValid() && field.Kind() == reflect.Slice {
		targets := make([]string, 0, field.Len())
		for i := 0; i < field.Len(); i++ {
			if field.Index(i).Kind() == reflect.String {
				targets = append(targets, field.Index(i).String())
			}
		}
		if len(targets) > 0 {
			return targets
		}
	}
	return policy.PluginScope
}

func eventAuthorizationFields(event eventmodel.Event) (actor, permission string) {
	if event.Actor != nil {
		actor = event.Actor.ID
	}
	if actor == "" {
		actor, permission = metadataAuthorizationFields(event.Metadata)
		return actor, permission
	}
	_, permission = metadataAuthorizationFields(event.Metadata)
	return actor, permission
}

func metadataAuthorizationFields(metadata map[string]any) (actor, permission string) {
	if metadata == nil {
		return "", ""
	}
	actor, _ = metadata["actor"].(string)
	permission, _ = metadata["permission"].(string)
	return actor, permission
}

func commandArguments(command eventmodel.CommandInvocation) []string {
	if len(command.Arguments) >= 2 {
		return append([]string{command.Name}, command.Arguments...)
	}
	return strings.Fields(command.Raw)
}

func adminPermissionForAction(action string) string {
	switch action {
	case "enable":
		return "plugin:enable"
	case "disable":
		return "plugin:disable"
	case "prepare", "activate":
		return "plugin:rollout"
	case "replay":
		return "plugin:replay"
	default:
		return ""
	}
}

package runtimecore

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

type AdminCommandAuthorizer struct {
	provider CurrentAuthorizerProvider
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
	provider CurrentAuthorizerProvider
}

type MetadataJobAuthorizer struct {
	provider CurrentAuthorizerProvider
}

type MetadataScheduleAuthorizer struct {
	provider CurrentAuthorizerProvider
}

type ConsoleReadRequestAuthorizer interface {
	AuthorizeConsoleRead(context.Context, *http.Request) error
}

type ConsoleReadAuthorizer struct {
	provider CurrentAuthorizerProvider
}

type CurrentAuthorizerProvider interface {
	CurrentAuthorizer() *pluginsdk.Authorizer
	CurrentSnapshot() *RBACAuthorizerSnapshot
}

type RBACAuthorizerSnapshot struct {
	ConsoleReadPermission string
	ActorRoles            map[string][]string
	Policies              map[string]pluginsdk.AuthorizationPolicy
	Authorizer            *pluginsdk.Authorizer
}

type CurrentRBACAuthorizerProvider struct {
	mu       sync.RWMutex
	snapshot *RBACAuthorizerSnapshot
}

const (
	ConsoleReadActorHeader = "X-Bot-Platform-Actor"
	consoleReadTarget      = "console"
)

func NewAdminCommandAuthorizer(cfg *RBACConfig) *AdminCommandAuthorizer {
	if cfg == nil {
		return nil
	}
	return NewAdminCommandAuthorizerFromProvider(NewCurrentRBACAuthorizerProviderFromConfig(cfg))
}

func NewMetadataEventAuthorizer(cfg *RBACConfig) *MetadataEventAuthorizer {
	if cfg == nil {
		return nil
	}
	return NewMetadataEventAuthorizerFromProvider(NewCurrentRBACAuthorizerProviderFromConfig(cfg))
}

func NewMetadataJobAuthorizer(cfg *RBACConfig) *MetadataJobAuthorizer {
	if cfg == nil {
		return nil
	}
	return NewMetadataJobAuthorizerFromProvider(NewCurrentRBACAuthorizerProviderFromConfig(cfg))
}

func NewMetadataScheduleAuthorizer(cfg *RBACConfig) *MetadataScheduleAuthorizer {
	if cfg == nil {
		return nil
	}
	return NewMetadataScheduleAuthorizerFromProvider(NewCurrentRBACAuthorizerProviderFromConfig(cfg))
}

func NewConsoleReadAuthorizer(cfg *RBACConfig) *ConsoleReadAuthorizer {
	if cfg == nil || strings.TrimSpace(cfg.ConsoleReadPermission) == "" {
		return nil
	}
	return NewCurrentConsoleReadAuthorizer(NewCurrentRBACAuthorizerProviderFromConfig(cfg))
}

func NewAdminCommandAuthorizerFromProvider(provider CurrentAuthorizerProvider) *AdminCommandAuthorizer {
	if isNilCurrentAuthorizerProvider(provider) {
		return nil
	}
	return &AdminCommandAuthorizer{provider: provider}
}

func NewMetadataEventAuthorizerFromProvider(provider CurrentAuthorizerProvider) *MetadataEventAuthorizer {
	if isNilCurrentAuthorizerProvider(provider) {
		return nil
	}
	return &MetadataEventAuthorizer{provider: provider}
}

func NewMetadataJobAuthorizerFromProvider(provider CurrentAuthorizerProvider) *MetadataJobAuthorizer {
	if isNilCurrentAuthorizerProvider(provider) {
		return nil
	}
	return &MetadataJobAuthorizer{provider: provider}
}

func NewMetadataScheduleAuthorizerFromProvider(provider CurrentAuthorizerProvider) *MetadataScheduleAuthorizer {
	if isNilCurrentAuthorizerProvider(provider) {
		return nil
	}
	return &MetadataScheduleAuthorizer{provider: provider}
}

func NewCurrentConsoleReadAuthorizer(provider CurrentAuthorizerProvider) *ConsoleReadAuthorizer {
	if isNilCurrentAuthorizerProvider(provider) {
		return nil
	}
	return &ConsoleReadAuthorizer{provider: provider}
}

func AuthorizeRBACAction(cfg *RBACConfig, actor, permission, target string) error {
	if cfg == nil {
		return nil
	}
	return AuthorizeRBACActionWithProvider(NewCurrentRBACAuthorizerProviderFromConfig(cfg), actor, permission, target)
}

func AuthorizeRBACActionWithProvider(provider CurrentAuthorizerProvider, actor, permission, target string) error {
	if isNilCurrentAuthorizerProvider(provider) {
		return nil
	}
	if strings.TrimSpace(permission) == "" || strings.TrimSpace(target) == "" {
		return nil
	}
	snapshot := provider.CurrentSnapshot()
	if snapshot == nil || snapshot.Authorizer == nil {
		return errors.New("permission denied")
	}
	decision := snapshot.Authorizer.Authorize(actor, permission, target)
	if decision.Allowed {
		return nil
	}
	if decision.Reason == "" {
		return errors.New("permission denied")
	}
	return errors.New(decision.Reason)
}

func (a *AdminCommandAuthorizer) AuthorizeCommand(_ context.Context, command eventmodel.CommandInvocation, _ eventmodel.ExecutionContext) error {
	if a == nil || a.provider == nil {
		return nil
	}
	actor, action, target, permission, targetKind := commandAuthorizationFields(command)
	if action == "" || target == "" || permission == "" {
		return nil
	}
	snapshot := a.provider.CurrentSnapshot()
	if snapshot == nil || snapshot.Authorizer == nil {
		return errors.New("permission denied")
	}
	decision := a.authorizeTarget(snapshot, actor, permission, targetKind, target)
	if decision.Allowed {
		return nil
	}
	if decision.Reason == "" {
		return errors.New("permission denied")
	}
	return errors.New(decision.Reason)
}

func (a *AdminCommandAuthorizer) authorizeTarget(snapshot *RBACAuthorizerSnapshot, actor, permission string, kind adminTargetKind, target string) pluginsdk.AuthorizationDecision {
	if snapshot == nil || snapshot.Authorizer == nil {
		return pluginsdk.AuthorizationDecision{Allowed: false, Permission: permission, Reason: "permission denied"}
	}
	return snapshot.Authorizer.AuthorizeTarget(actor, permission, toAuthorizationTargetKind(kind), target)
}

func (a *MetadataEventAuthorizer) AuthorizeEvent(_ context.Context, event eventmodel.Event, _ eventmodel.ExecutionContext, plugin pluginsdk.Plugin) error {
	authorizer := a.currentAuthorizer()
	if authorizer == nil {
		return nil
	}
	actor, permission := eventAuthorizationFields(event)
	if permission == "" {
		return nil
	}
	decision := authorizer.Authorize(actor, permission, plugin.Manifest.ID)
	if decision.Allowed {
		return nil
	}
	if decision.Reason == "" {
		return errors.New("permission denied")
	}
	return errors.New(decision.Reason)
}

func (a *MetadataJobAuthorizer) AuthorizeJob(_ context.Context, job pluginsdk.JobInvocation, _ eventmodel.ExecutionContext, plugin pluginsdk.Plugin) error {
	authorizer := a.currentAuthorizer()
	if authorizer == nil {
		return nil
	}
	actor, permission := metadataAuthorizationFields(job.Metadata)
	if permission == "" {
		return nil
	}
	decision := authorizer.Authorize(actor, permission, plugin.Manifest.ID)
	if decision.Allowed {
		return nil
	}
	if decision.Reason == "" {
		return errors.New("permission denied")
	}
	return errors.New(decision.Reason)
}

func (a *MetadataScheduleAuthorizer) AuthorizeSchedule(_ context.Context, trigger pluginsdk.ScheduleTrigger, _ eventmodel.ExecutionContext, plugin pluginsdk.Plugin) error {
	authorizer := a.currentAuthorizer()
	if authorizer == nil {
		return nil
	}
	actor, permission := metadataAuthorizationFields(trigger.Metadata)
	if permission == "" {
		return nil
	}
	decision := authorizer.Authorize(actor, permission, plugin.Manifest.ID)
	if decision.Allowed {
		return nil
	}
	if decision.Reason == "" {
		return errors.New("permission denied")
	}
	return errors.New(decision.Reason)
}

func (a *MetadataEventAuthorizer) currentAuthorizer() *pluginsdk.Authorizer {
	if a == nil || a.provider == nil {
		return nil
	}
	return a.provider.CurrentAuthorizer()
}

func (a *MetadataJobAuthorizer) currentAuthorizer() *pluginsdk.Authorizer {
	if a == nil || a.provider == nil {
		return nil
	}
	return a.provider.CurrentAuthorizer()
}

func (a *MetadataScheduleAuthorizer) currentAuthorizer() *pluginsdk.Authorizer {
	if a == nil || a.provider == nil {
		return nil
	}
	return a.provider.CurrentAuthorizer()
}

func (a *ConsoleReadAuthorizer) AuthorizeConsoleRead(_ context.Context, request *http.Request) error {
	if a == nil || a.provider == nil {
		return nil
	}
	snapshot := a.provider.CurrentSnapshot()
	if snapshot == nil || snapshot.Authorizer == nil {
		return errors.New("permission denied")
	}
	permission := strings.TrimSpace(snapshot.ConsoleReadPermission)
	if permission == "" {
		return nil
	}
	actor, err := RequestActorID(request.Context(), false, request.Header.Get(ConsoleReadActorHeader))
	if err != nil {
		return err
	}
	decision := snapshot.Authorizer.Authorize(actor, permission, consoleReadTarget)
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

func toAuthorizationTargetKind(kind adminTargetKind) pluginsdk.AuthorizationTargetKind {
	if kind == adminTargetEvent {
		return pluginsdk.AuthorizationTargetEvent
	}
	return pluginsdk.AuthorizationTargetPlugin
}

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

func NewCurrentRBACAuthorizerProvider() *CurrentRBACAuthorizerProvider {
	return &CurrentRBACAuthorizerProvider{}
}

func NewCurrentRBACAuthorizerProviderFromConfig(cfg *RBACConfig) *CurrentRBACAuthorizerProvider {
	if cfg == nil {
		return nil
	}
	provider := NewCurrentRBACAuthorizerProvider()
	provider.SetSnapshot(NewRBACAuthorizerSnapshot(cfg.ActorRoles, cfg.Policies, cfg.ConsoleReadPermission))
	return provider
}

func NewCurrentRBACAuthorizerProviderFromStore(ctx context.Context, store RBACStateStore) (*CurrentRBACAuthorizerProvider, error) {
	provider := NewCurrentRBACAuthorizerProvider()
	if err := provider.ReloadFromStore(ctx, store); err != nil {
		return nil, err
	}
	return provider, nil
}

func (p *CurrentRBACAuthorizerProvider) CurrentAuthorizer() *pluginsdk.Authorizer {
	snapshot := p.CurrentSnapshot()
	if snapshot == nil {
		return nil
	}
	return snapshot.Authorizer
}

func (p *CurrentRBACAuthorizerProvider) CurrentSnapshot() *RBACAuthorizerSnapshot {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.snapshot
}

func (p *CurrentRBACAuthorizerProvider) SetSnapshot(snapshot *RBACAuthorizerSnapshot) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.snapshot = snapshot
}

func (p *CurrentRBACAuthorizerProvider) ReloadFromStore(ctx context.Context, store RBACStateStore) error {
	if p == nil {
		return errors.New("current rbac authorizer provider is required")
	}
	if store == nil {
		return errors.New("runtime state store is required")
	}
	snapshotState, err := store.LoadRBACSnapshot(ctx, CurrentRBACSnapshotKey)
	if err != nil {
		if errors.Is(err, errStateStoreNoRows) {
			p.SetSnapshot(nil)
			return nil
		}
		return err
	}
	identities, err := store.ListOperatorIdentities(ctx)
	if err != nil {
		return err
	}
	p.SetSnapshot(NewRBACAuthorizerSnapshotFromState(snapshotState, identities))
	return nil
}

func NewRBACAuthorizerSnapshot(actorRoles map[string][]string, policies map[string]pluginsdk.AuthorizationPolicy, consoleReadPermission string) *RBACAuthorizerSnapshot {
	clonedActorRoles := cloneActorRoles(actorRoles)
	clonedPolicies := cloneAuthorizationPolicies(policies)
	return &RBACAuthorizerSnapshot{
		ConsoleReadPermission: strings.TrimSpace(consoleReadPermission),
		ActorRoles:            clonedActorRoles,
		Policies:              clonedPolicies,
		Authorizer:            pluginsdk.NewAuthorizer(clonedActorRoles, clonedPolicies),
	}
}

func NewRBACAuthorizerSnapshotFromState(snapshot RBACSnapshotState, identities []OperatorIdentityState) *RBACAuthorizerSnapshot {
	actorRoles := make(map[string][]string, len(identities))
	for _, identity := range identities {
		actorID := strings.TrimSpace(identity.ActorID)
		if actorID == "" {
			continue
		}
		actorRoles[actorID] = append([]string(nil), identity.Roles...)
	}
	return NewRBACAuthorizerSnapshot(actorRoles, snapshot.Policies, snapshot.ConsoleReadPermission)
}

func PersistConfiguredRBACState(ctx context.Context, store RBACStateStore, cfg *RBACConfig) error {
	if cfg == nil || store == nil {
		return nil
	}
	actorIDs := make([]string, 0, len(cfg.ActorRoles))
	for actorID := range cfg.ActorRoles {
		actorIDs = append(actorIDs, actorID)
	}
	sort.Strings(actorIDs)
	identities := make([]OperatorIdentityState, 0, len(actorIDs))
	for _, actorID := range actorIDs {
		identities = append(identities, OperatorIdentityState{ActorID: actorID, Roles: cfg.ActorRoles[actorID]})
	}
	return store.ReplaceCurrentRBACState(ctx, identities, RBACSnapshotState{
		SnapshotKey:           CurrentRBACSnapshotKey,
		ConsoleReadPermission: cfg.ConsoleReadPermission,
		Policies:              cloneAuthorizationPolicies(cfg.Policies),
	})
}

func cloneActorRoles(actorRoles map[string][]string) map[string][]string {
	cloned := make(map[string][]string, len(actorRoles))
	for actor, roles := range actorRoles {
		cloned[actor] = append([]string(nil), roles...)
	}
	return cloned
}

func cloneAuthorizationPolicies(policies map[string]pluginsdk.AuthorizationPolicy) map[string]pluginsdk.AuthorizationPolicy {
	cloned := make(map[string]pluginsdk.AuthorizationPolicy, len(policies))
	for role, policy := range policies {
		cloned[role] = pluginsdk.AuthorizationPolicy{
			Permissions: append([]string(nil), policy.Permissions...),
			PluginScope: append([]string(nil), policy.PluginScope...),
			EventScope:  append([]string(nil), policy.EventScope...),
		}
	}
	return cloned
}

func isNilCurrentAuthorizerProvider(provider CurrentAuthorizerProvider) bool {
	if provider == nil {
		return true
	}
	value := reflect.ValueOf(provider)
	switch value.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Slice, reflect.Interface, reflect.Func:
		return value.IsNil()
	default:
		return false
	}
}

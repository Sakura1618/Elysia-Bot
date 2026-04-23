import type { ConsolePayload } from './types';

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null;
}

function isNumber(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value);
}

function isString(value: unknown): value is string {
  return typeof value === 'string';
}

function isBoolean(value: unknown): value is boolean {
  return typeof value === 'boolean';
}

function isStringArray(value: unknown): value is string[] {
  return Array.isArray(value) && value.every(isString);
}

function isStringOrNull(value: unknown): value is string | null {
  return value === null || isString(value);
}

function isStringArrayOrUndefined(value: unknown): value is string[] | undefined {
  return value === undefined || isStringArray(value);
}

function isRecordOrUndefined(value: unknown): value is Record<string, unknown> | undefined {
  return value === undefined || isRecord(value);
}

function isNumberRecordOrUndefined(value: unknown): value is Record<string, number> | undefined {
  return value === undefined || (isRecord(value) && Object.values(value).every(isNumber));
}

function hasAdapterShape(value: unknown): boolean {
  if (!isRecord(value)) {
    return false;
  }
  return (
    isString(value.id) &&
    isString(value.adapter) &&
    isString(value.source) &&
    isRecordOrUndefined(value.config) &&
    (value.status === undefined || isString(value.status)) &&
    (value.health === undefined || isString(value.health)) &&
    isBoolean(value.online) &&
    (value.statusSource === undefined || isString(value.statusSource)) &&
    (value.configSource === undefined || isString(value.configSource)) &&
    isBoolean(value.statePersisted) &&
    (value.updatedAt === undefined || isString(value.updatedAt)) &&
    (value.summary === undefined || isString(value.summary))
  );
}

function hasPluginShape(value: unknown): boolean {
  if (!isRecord(value) || !isRecord(value.entry)) {
    return false;
  }
  return (
    isString(value.id) &&
    isString(value.name) &&
    isString(value.version) &&
    isString(value.apiVersion) &&
    isString(value.mode) &&
    isStringArray(value.permissions) &&
    isRecordOrUndefined(value.configSchema) &&
    isRecordOrUndefined(value.config) &&
    (value.publish === undefined ||
      (isRecord(value.publish) &&
        isString(value.publish.sourceType) &&
        isString(value.publish.sourceUri) &&
        isString(value.publish.runtimeVersionRange))) &&
    (value.configStateKind === undefined || isString(value.configStateKind)) &&
    (value.configSource === undefined || isString(value.configSource)) &&
    (value.configPersisted === undefined || isBoolean(value.configPersisted)) &&
    (value.configUpdatedAt === undefined || isString(value.configUpdatedAt)) &&
    isBoolean(value.enabled) &&
    (value.enabledStateSource === undefined || isString(value.enabledStateSource)) &&
    (value.enabledStatePersisted === undefined || isBoolean(value.enabledStatePersisted)) &&
    (value.enabledStateUpdatedAt === undefined || isString(value.enabledStateUpdatedAt)) &&
    (value.statusSource === undefined || isString(value.statusSource)) &&
    (value.statusEvidence === undefined || isString(value.statusEvidence)) &&
    (value.statusSummary === undefined || isString(value.statusSummary)) &&
    (value.runtimeStateLive === undefined || isBoolean(value.runtimeStateLive)) &&
    (value.statusPersisted === undefined || isBoolean(value.statusPersisted)) &&
    (value.lastDispatchKind === undefined || isString(value.lastDispatchKind)) &&
    (value.lastDispatchSuccess === undefined || isBoolean(value.lastDispatchSuccess)) &&
    (value.lastDispatchError === undefined || isString(value.lastDispatchError)) &&
    (value.lastDispatchAt === undefined || isString(value.lastDispatchAt)) &&
    (value.lastRecoveredAt === undefined || isString(value.lastRecoveredAt)) &&
    (value.lastRecoveryFailureCount === undefined || isNumber(value.lastRecoveryFailureCount)) &&
    (value.currentFailureStreak === undefined || isNumber(value.currentFailureStreak)) &&
    (value.statusLevel === undefined || isString(value.statusLevel)) &&
    (value.statusRecovery === undefined || isString(value.statusRecovery)) &&
    (value.statusStaleness === undefined || isString(value.statusStaleness))
  );
}

function hasRBACDeclarationShape(value: unknown): boolean {
  if (!isRecord(value)) {
    return false;
  }
  return (
    (value.actor === undefined || isString(value.actor)) &&
    (value.permission === undefined || isString(value.permission)) &&
    (value.targetPluginId === undefined || isString(value.targetPluginId)) &&
    (value.dispatchKind === undefined || isString(value.dispatchKind)) &&
    isBoolean(value.runtimeAuthorizerEnabled) &&
    (value.runtimeAuthorizerScope === undefined || isString(value.runtimeAuthorizerScope)) &&
    isBoolean(value.manifestGateEnabled) &&
    (value.manifestGateScope === undefined || isString(value.manifestGateScope)) &&
    isBoolean(value.jobTargetFilterEnabled) &&
    isStringArrayOrUndefined(value.facts) &&
    (value.summary === undefined || isString(value.summary))
  );
}

function hasJobShape(value: unknown): boolean {
  if (!isRecord(value)) {
    return false;
  }
  return (
    isString(value.id) &&
    isString(value.type) &&
    isString(value.status) &&
    isRecordOrUndefined(value.payload) &&
    isNumber(value.retryCount) &&
    isNumber(value.maxRetries) &&
    isNumber(value.timeout) &&
    isString(value.lastError) &&
    isString(value.createdAt) &&
    isStringOrNull(value.startedAt) &&
    isStringOrNull(value.finishedAt) &&
    isStringOrNull(value.nextRunAt) &&
    isBoolean(value.deadLetter) &&
    isString(value.correlation) &&
    (value.workerId === undefined || isString(value.workerId)) &&
    (value.leaseAcquiredAt === undefined || isStringOrNull(value.leaseAcquiredAt)) &&
    (value.leaseExpiresAt === undefined || isStringOrNull(value.leaseExpiresAt)) &&
    (value.heartbeatAt === undefined || isStringOrNull(value.heartbeatAt)) &&
    (value.reasonCode === undefined || isString(value.reasonCode)) &&
    (value.targetPluginId === undefined || isString(value.targetPluginId)) &&
    (value.dispatchMetadataPresent === undefined || isBoolean(value.dispatchMetadataPresent)) &&
    (value.dispatchContractPresent === undefined || isBoolean(value.dispatchContractPresent)) &&
    (value.queueContractComplete === undefined || isBoolean(value.queueContractComplete)) &&
    (value.dispatchReady === undefined || isBoolean(value.dispatchReady)) &&
    (value.queueStateSummary === undefined || isString(value.queueStateSummary)) &&
    (value.dispatchSummary === undefined || isString(value.dispatchSummary)) &&
    (value.queueContractSummary === undefined || isString(value.queueContractSummary)) &&
    (value.leaseSummary === undefined || isString(value.leaseSummary)) &&
    (value.dispatchPermission === undefined || isString(value.dispatchPermission)) &&
    (value.dispatchActor === undefined || isString(value.dispatchActor)) &&
    (value.dispatchRbac === undefined || hasRBACDeclarationShape(value.dispatchRbac)) &&
    (value.replyHandlePresent === undefined || isBoolean(value.replyHandlePresent)) &&
    (value.replyHandleCapability === undefined || isString(value.replyHandleCapability)) &&
    (value.replyContractPresent === undefined || isBoolean(value.replyContractPresent)) &&
    (value.replySummary === undefined || isString(value.replySummary)) &&
    (value.sessionIDPresent === undefined || isBoolean(value.sessionIDPresent)) &&
    (value.replyTargetPresent === undefined || isBoolean(value.replyTargetPresent)) &&
    (value.recoverySummary === undefined || isString(value.recoverySummary))
  );
}

function hasScheduleShape(value: unknown): boolean {
  if (!isRecord(value)) {
    return false;
  }
  return (
    isString(value.id) &&
    isString(value.kind) &&
    isString(value.source) &&
    isString(value.eventType) &&
    isString(value.cronExpr) &&
    isNumber(value.delayMs) &&
    isStringOrNull(value.executeAt) &&
    isStringOrNull(value.dueAt) &&
    (value.dueAtSource === undefined || isString(value.dueAtSource)) &&
    (value.dueAtEvidence === undefined || isString(value.dueAtEvidence)) &&
    (value.dueAtPersisted === undefined || isBoolean(value.dueAtPersisted)) &&
    (value.dueReady === undefined || isBoolean(value.dueReady)) &&
    (value.overdue === undefined || isBoolean(value.overdue)) &&
    (value.claimOwner === undefined || isString(value.claimOwner)) &&
    (value.claimedAt === undefined || isStringOrNull(value.claimedAt)) &&
    (value.claimed === undefined || isBoolean(value.claimed)) &&
    (value.recoveryState === undefined || isString(value.recoveryState)) &&
    (value.dueStateSummary === undefined || isString(value.dueStateSummary)) &&
    (value.dueSummary === undefined || isString(value.dueSummary)) &&
    (value.scheduleSummary === undefined || isString(value.scheduleSummary)) &&
    isString(value.createdAt) &&
    isString(value.updatedAt)
  );
}

function hasWorkflowShape(value: unknown): boolean {
  if (!isRecord(value)) {
    return false;
  }
  return (
    isString(value.id) &&
    isString(value.pluginId) &&
    (value.traceId === undefined || isString(value.traceId)) &&
    (value.eventId === undefined || isString(value.eventId)) &&
    (value.runId === undefined || isString(value.runId)) &&
    (value.correlationId === undefined || isString(value.correlationId)) &&
    isString(value.status) &&
    isNumber(value.currentIndex) &&
    (value.waitingFor === undefined || isString(value.waitingFor)) &&
    (value.sleepingUntil === undefined || isStringOrNull(value.sleepingUntil)) &&
    isBoolean(value.completed) &&
    isBoolean(value.compensated) &&
    isRecordOrUndefined(value.state) &&
    (value.lastEventId === undefined || isString(value.lastEventId)) &&
    (value.lastEventType === undefined || isString(value.lastEventType)) &&
    (value.statusSource === undefined || isString(value.statusSource)) &&
    isBoolean(value.statePersisted) &&
    (value.runtimeOwner === undefined || isString(value.runtimeOwner)) &&
    isString(value.createdAt) &&
    isString(value.updatedAt) &&
    (value.summary === undefined || isString(value.summary))
  );
}

function hasAlertShape(value: unknown): boolean {
  if (!isRecord(value)) {
    return false;
  }
  return (
    isString(value.id) &&
    isString(value.objectType) &&
    isString(value.objectId) &&
    isString(value.failureType) &&
    isString(value.firstOccurredAt) &&
    isString(value.latestOccurredAt) &&
    isString(value.latestReason) &&
    (value.traceId === undefined || isString(value.traceId)) &&
    (value.eventId === undefined || isString(value.eventId)) &&
    (value.runId === undefined || isString(value.runId)) &&
    (value.correlation === undefined || isString(value.correlation)) &&
    isString(value.createdAt)
  );
}

function hasAuditShape(value: unknown): boolean {
  if (!isRecord(value)) {
    return false;
  }
  return (
    isString(value.actor) &&
    (value.permission === undefined || isString(value.permission)) &&
    isString(value.action) &&
    isString(value.target) &&
    isBoolean(value.allowed) &&
    (value.reason === undefined || isString(value.reason)) &&
    (value.trace_id === undefined || isString(value.trace_id)) &&
    (value.event_id === undefined || isString(value.event_id)) &&
    (value.plugin_id === undefined || isString(value.plugin_id)) &&
    (value.run_id === undefined || isString(value.run_id)) &&
    (value.session_id === undefined || isString(value.session_id)) &&
    (value.correlation_id === undefined || isString(value.correlation_id)) &&
    (value.error_category === undefined || isString(value.error_category)) &&
    (value.error_code === undefined || isString(value.error_code)) &&
    isString(value.occurred_at)
  );
}

function hasReplayOperationShape(value: unknown): boolean {
  if (!isRecord(value)) {
    return false;
  }
  return (
    isString(value.replayId) &&
    isString(value.sourceEventId) &&
    isString(value.replayEventId) &&
    isString(value.status) &&
    (value.reason === undefined || isString(value.reason)) &&
    (value.occurredAt === undefined || isString(value.occurredAt)) &&
    (value.updatedAt === undefined || isString(value.updatedAt)) &&
    (value.stateSource === undefined || isString(value.stateSource)) &&
    isBoolean(value.persisted) &&
    (value.summary === undefined || isString(value.summary))
  );
}

function hasRolloutOperationShape(value: unknown): boolean {
  if (!isRecord(value)) {
    return false;
  }
  return (
    isString(value.operationId) &&
    isString(value.pluginId) &&
    isString(value.action) &&
    (value.currentVersion === undefined || isString(value.currentVersion)) &&
    (value.candidateVersion === undefined || isString(value.candidateVersion)) &&
    isString(value.status) &&
    (value.reason === undefined || isString(value.reason)) &&
    (value.occurredAt === undefined || isString(value.occurredAt)) &&
    (value.updatedAt === undefined || isString(value.updatedAt)) &&
    (value.stateSource === undefined || isString(value.stateSource)) &&
    isBoolean(value.persisted) &&
    (value.summary === undefined || isString(value.summary))
  );
}

function hasAuthorizationPolicyShape(value: unknown): boolean {
  if (!isRecord(value)) {
    return false;
  }
  return (
    isStringArray(value.permissions) &&
    isStringArrayOrUndefined(value.pluginScope) &&
    isStringArrayOrUndefined(value.eventScope)
  );
}

function hasConfigShape(value: unknown): boolean {
  if (!isRecord(value) || !isRecord(value.Runtime)) {
    return false;
  }

  const rbac = value.RBAC;
  return (
    isString(value.Runtime.Environment) &&
    isString(value.Runtime.LogLevel) &&
    isNumber(value.Runtime.HTTPPort) &&
    (value.AIChat === undefined || isRecord(value.AIChat)) &&
    (value.Secrets === undefined || isRecord(value.Secrets)) &&
    (rbac === undefined ||
      (isRecord(rbac) &&
        isRecord(rbac.ActorRoles) &&
        Object.values(rbac.ActorRoles).every(isStringArray) &&
        isRecord(rbac.Policies) &&
        Object.values(rbac.Policies).every(hasAuthorizationPolicyShape) &&
        (rbac.ConsoleReadPermission === undefined || isString(rbac.ConsoleReadPermission))))
  );
}

function hasMetaShape(value: unknown): boolean {
  if (!isRecord(value)) {
    return false;
  }
  return (
    isString(value.runtime_entry) &&
    isStringArray(value.demo_paths) &&
    isString(value.sqlite_path) &&
    isNumber(value.scheduler_interval_ms) &&
    (value.ai_worker_enabled === undefined || isBoolean(value.ai_worker_enabled)) &&
    (value.ai_job_dispatcher_registered === undefined || isBoolean(value.ai_job_dispatcher_registered)) &&
    isString(value.console_mode) &&
    (value.generated_at === undefined || isString(value.generated_at)) &&
    isStringArrayOrUndefined(value.plugin_operator_actions) &&
    (value.plugin_operator_scope === undefined || isString(value.plugin_operator_scope)) &&
    isStringArrayOrUndefined(value.plugin_config_operator_actions) &&
    (value.plugin_config_operator_scope === undefined || isString(value.plugin_config_operator_scope)) &&
    isStringArrayOrUndefined(value.job_operator_actions) &&
    (value.job_operator_scope === undefined || isString(value.job_operator_scope)) &&
    isStringArrayOrUndefined(value.schedule_operator_actions) &&
    (value.schedule_operator_scope === undefined || isString(value.schedule_operator_scope)) &&
    (value.rbac_console_read_actor_header === undefined || isString(value.rbac_console_read_actor_header)) &&
    (value.rbac_console_read_permission === undefined || isBoolean(value.rbac_console_read_permission)) &&
    isStringArrayOrUndefined(value.rbac_console_limitations) &&
    (value.request_identity === undefined ||
      (isRecord(value.request_identity) &&
        (value.request_identity.actor_id === undefined || isString(value.request_identity.actor_id)) &&
        (value.request_identity.token_id === undefined || isString(value.request_identity.token_id)) &&
        (value.request_identity.auth_method === undefined || isString(value.request_identity.auth_method)) &&
        (value.request_identity.session_id === undefined || isString(value.request_identity.session_id)))) &&
    isStringArrayOrUndefined(value.verification_endpoints)
  );
}

function hasRecoveryShape(value: unknown): boolean {
  if (!isRecord(value)) {
    return false;
  }
  return (
    (value.recoveredAt === undefined || isString(value.recoveredAt)) &&
    isNumber(value.totalJobs) &&
    isNumber(value.recoveredJobs) &&
    isNumber(value.recoveredRunning) &&
    isNumber(value.retriedJobs) &&
    isNumber(value.deadJobs) &&
    isNumberRecordOrUndefined(value.statusCounts) &&
    isNumber(value.totalSchedules) &&
    isNumber(value.recoveredSchedules) &&
    isNumber(value.invalidSchedules) &&
    isNumberRecordOrUndefined(value.scheduleKinds) &&
    (value.summary === undefined || isString(value.summary))
  );
}

function hasObservabilityShape(value: unknown): boolean {
  if (!isRecord(value)) {
    return false;
  }
  return (
    isNumber(value.jobDispatchReady) &&
    isNumber(value.scheduleDueReady) &&
    (value.jobStateSource === undefined || isString(value.jobStateSource)) &&
    (value.scheduleStateSource === undefined || isString(value.scheduleStateSource)) &&
    (value.logStateSource === undefined || isString(value.logStateSource)) &&
    (value.traceStateSource === undefined || isString(value.traceStateSource)) &&
    (value.metricsStateSource === undefined || isString(value.metricsStateSource)) &&
    isStringArrayOrUndefined(value.verificationEndpoints) &&
    (value.summary === undefined || isString(value.summary))
  );
}

export function isConsolePayload(value: unknown): value is ConsolePayload {
  if (!isRecord(value) || !isRecord(value.status)) {
    return false;
  }

  return (
    isNumber(value.status.adapters) &&
    isNumber(value.status.plugins) &&
    isNumber(value.status.jobs) &&
    isNumber(value.status.schedules) &&
    Array.isArray(value.adapters) &&
    value.adapters.every(hasAdapterShape) &&
    Array.isArray(value.alerts) &&
    value.alerts.every(hasAlertShape) &&
    Array.isArray(value.replayOps) &&
    value.replayOps.every(hasReplayOperationShape) &&
    Array.isArray(value.rolloutOps) &&
    value.rolloutOps.every(hasRolloutOperationShape) &&
    Array.isArray(value.plugins) &&
    value.plugins.every(hasPluginShape) &&
    Array.isArray(value.jobs) &&
    value.jobs.every(hasJobShape) &&
    Array.isArray(value.schedules) &&
    value.schedules.every(hasScheduleShape) &&
    Array.isArray(value.workflows) &&
    value.workflows.every(hasWorkflowShape) &&
    isStringArray(value.logs) &&
    Array.isArray(value.audits) &&
    value.audits.every(hasAuditShape) &&
    hasConfigShape(value.config) &&
    hasMetaShape(value.meta) &&
    hasRecoveryShape(value.recovery) &&
    hasObservabilityShape(value.observability)
  );
}

export function parseConsolePayload(value: unknown): ConsolePayload {
  if (!isConsolePayload(value)) {
    throw new Error('Console API payload shape is incompatible with Console Web A11');
  }
  return {
    ...value,
    jobs: value.jobs.map((job) => ({
      ...job,
      payload: job.payload ?? {},
    })),
  };
}

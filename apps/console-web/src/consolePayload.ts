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

function isStringArray(value: unknown): value is string[] {
  return Array.isArray(value) && value.every(isString);
}

function hasJobShape(value: unknown): boolean {
  if (!isRecord(value)) {
    return false;
  }
  return (
    isString(value.id) &&
    isString(value.type) &&
    isString(value.status) &&
    isNumber(value.retryCount) &&
    isNumber(value.maxRetries) &&
    isNumber(value.timeout) &&
    isString(value.lastError) &&
    isString(value.createdAt) &&
    typeof value.deadLetter === 'boolean' &&
    isString(value.correlation)
  );
}

function hasAdapterShape(value: unknown): boolean {
	if (!isRecord(value)) {
		return false;
	}
	return (
		isString(value.id) &&
		isString(value.adapter) &&
		isString(value.source) &&
		(value.config === undefined || isRecord(value.config)) &&
		(value.status === undefined || isString(value.status)) &&
		(value.health === undefined || isString(value.health)) &&
		typeof value.online === 'boolean' &&
		(value.statusSource === undefined || isString(value.statusSource)) &&
		(value.configSource === undefined || isString(value.configSource)) &&
		typeof value.statePersisted === 'boolean' &&
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
		(value.publish === undefined || (
			isRecord(value.publish) &&
			isString(value.publish.sourceType) &&
			isString(value.publish.sourceUri) &&
			isString(value.publish.runtimeVersionRange)
		)) &&
		(value.configStateKind === undefined || isString(value.configStateKind)) &&
		(value.configSource === undefined || isString(value.configSource)) &&
		(value.configPersisted === undefined || typeof value.configPersisted === 'boolean') &&
		(value.configUpdatedAt === undefined || isString(value.configUpdatedAt)) &&
		(value.enabled === undefined || typeof value.enabled === 'boolean') &&
		(value.enabledStateSource === undefined || isString(value.enabledStateSource)) &&
		(value.enabledStatePersisted === undefined || typeof value.enabledStatePersisted === 'boolean') &&
		(value.enabledStateUpdatedAt === undefined || isString(value.enabledStateUpdatedAt)) &&
		(value.statusSource === undefined || isString(value.statusSource)) &&
		(value.statusEvidence === undefined || isString(value.statusEvidence)) &&
		(value.statusSummary === undefined || isString(value.statusSummary)) &&
		(value.runtimeStateLive === undefined || typeof value.runtimeStateLive === 'boolean') &&
		(value.statusPersisted === undefined || typeof value.statusPersisted === 'boolean') &&
		(value.lastDispatchKind === undefined || isString(value.lastDispatchKind)) &&
		(value.lastDispatchAt === undefined || isString(value.lastDispatchAt)) &&
		(value.lastRecoveredAt === undefined || isString(value.lastRecoveredAt)) &&
		(value.lastRecoveryFailureCount === undefined || isNumber(value.lastRecoveryFailureCount)) &&
		(value.currentFailureStreak === undefined || isNumber(value.currentFailureStreak))
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
		(value.executeAt === null || isString(value.executeAt)) &&
		(value.dueAt === null || isString(value.dueAt)) &&
		isString(value.createdAt) &&
		isString(value.updatedAt)
	);
}

export function isConsolePayload(value: unknown): value is ConsolePayload {
  if (!isRecord(value) || !isRecord(value.status) || !isRecord(value.config) || !isRecord(value.config.Runtime)) {
    return false;
  }

  if (!isRecord(value.meta)) {
    return false;
  }

	return (
    isNumber(value.status.adapters) &&
    isNumber(value.status.plugins) &&
    isNumber(value.status.jobs) &&
    isNumber(value.status.schedules) &&
		Array.isArray(value.adapters) &&
		value.adapters.every(hasAdapterShape) &&
    Array.isArray(value.plugins) &&
    value.plugins.every(hasPluginShape) &&
		Array.isArray(value.jobs) &&
		value.jobs.every(hasJobShape) &&
		Array.isArray(value.schedules) &&
		value.schedules.every(hasScheduleShape) &&
		isStringArray(value.logs) &&
    isString(value.config.Runtime.Environment) &&
    isString(value.config.Runtime.LogLevel) &&
    isNumber(value.config.Runtime.HTTPPort) &&
		isString(value.meta.runtime_entry) &&
		isStringArray(value.meta.demo_paths) &&
		isString(value.meta.sqlite_path) &&
		isNumber(value.meta.scheduler_interval_ms) &&
		(value.meta.ai_worker_enabled === undefined || typeof value.meta.ai_worker_enabled === 'boolean') &&
		(value.meta.ai_job_dispatcher_registered === undefined || typeof value.meta.ai_job_dispatcher_registered === 'boolean') &&
		isString(value.meta.console_mode) &&
		isRecord(value.observability) &&
		isNumber(value.observability.jobDispatchReady) &&
		isNumber(value.observability.scheduleDueReady) &&
		(value.observability.jobStateSource === undefined || isString(value.observability.jobStateSource)) &&
		(value.observability.scheduleStateSource === undefined || isString(value.observability.scheduleStateSource)) &&
		(value.observability.logStateSource === undefined || isString(value.observability.logStateSource)) &&
		(value.observability.traceStateSource === undefined || isString(value.observability.traceStateSource)) &&
		(value.observability.metricsStateSource === undefined || isString(value.observability.metricsStateSource)) &&
		(value.observability.verificationEndpoints === undefined || isStringArray(value.observability.verificationEndpoints)) &&
		(value.observability.summary === undefined || isString(value.observability.summary))
	);
}

export function parseConsolePayload(value: unknown): ConsolePayload {
  if (!isConsolePayload(value)) {
    throw new Error('Console API payload shape is incompatible with Console Web v0');
  }
  return value;
}

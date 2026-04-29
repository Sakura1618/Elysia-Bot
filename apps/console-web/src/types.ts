export type RuntimeStatus = {
  adapters: number;
  plugins: number;
  jobs: number;
  schedules: number;
};

export type AdapterInstance = {
  id: string;
  adapter: string;
  source: string;
  config?: Record<string, unknown>;
  status?: string;
  health?: string;
  online: boolean;
  statusSource?: string;
  configSource?: string;
  statePersisted: boolean;
  updatedAt?: string;
  summary?: string;
};

export type PluginEntry = {
  module?: string;
  symbol?: string;
  binary?: string;
};

export type PluginPublish = {
  sourceType: string;
  sourceUri: string;
  runtimeVersionRange: string;
};

export type PluginManifest = {
  id: string;
  name: string;
  version: string;
  apiVersion: string;
  mode: string;
  permissions: string[];
  configSchema?: Record<string, unknown>;
  config?: Record<string, unknown>;
  entry: PluginEntry;
  publish?: PluginPublish;
  configStateKind?: string;
  configSource?: string;
  configPersisted?: boolean;
  configUpdatedAt?: string;
  enabled: boolean;
  enabledStateSource?: string;
  enabledStatePersisted?: boolean;
  enabledStateUpdatedAt?: string;
  statusSource?: string;
  statusEvidence?: string;
  statusSummary?: string;
  runtimeStateLive?: boolean;
  statusPersisted?: boolean;
  lastDispatchKind?: string;
  lastDispatchSuccess?: boolean;
  lastDispatchError?: string;
  lastDispatchAt?: string;
  lastRecoveredAt?: string;
  lastRecoveryFailureCount?: number;
  currentFailureStreak?: number;
  statusLevel?: string;
  statusRecovery?: string;
  statusStaleness?: string;
};

export type JobStatus = 'pending' | 'paused' | 'running' | 'retrying' | 'cancelled' | 'failed' | 'dead' | 'done';

export type ConsoleRBACDeclaration = {
  actor?: string;
  permission?: string;
  targetPluginId?: string;
  dispatchKind?: string;
  runtimeAuthorizerEnabled: boolean;
  runtimeAuthorizerScope?: string;
  manifestGateEnabled: boolean;
  manifestGateScope?: string;
  jobTargetFilterEnabled: boolean;
  facts?: string[];
  summary?: string;
};

export type Job = {
  id: string;
  type: string;
  traceId?: string;
  eventId?: string;
  runId?: string;
  status: JobStatus;
  payload: Record<string, unknown>;
  retryCount: number;
  maxRetries: number;
  timeout: number;
  lastError: string;
  createdAt: string;
  startedAt: string | null;
  finishedAt: string | null;
  nextRunAt: string | null;
  workerId?: string;
  leaseAcquiredAt?: string | null;
  leaseExpiresAt?: string | null;
  heartbeatAt?: string | null;
  reasonCode?: string;
  deadLetter: boolean;
  correlation: string;
  targetPluginId?: string;
  dispatchMetadataPresent?: boolean;
  dispatchContractPresent?: boolean;
  queueContractComplete?: boolean;
  dispatchReady?: boolean;
  queueStateSummary?: string;
  dispatchSummary?: string;
  queueContractSummary?: string;
  leaseSummary?: string;
  dispatchPermission?: string;
  dispatchActor?: string;
  dispatchRbac?: ConsoleRBACDeclaration;
  replyHandlePresent?: boolean;
  replyHandleCapability?: string;
  replyContractPresent?: boolean;
  replySummary?: string;
  sessionIDPresent?: boolean;
  replyTargetPresent?: boolean;
  recoverySummary?: string;
};

export type ScheduleKind = 'cron' | 'delay' | 'one-shot';

export type Schedule = {
  id: string;
  kind: ScheduleKind;
  source: string;
  eventType: string;
  cronExpr: string;
  delayMs: number;
  executeAt: string | null;
  dueAt: string | null;
  dueAtSource?: string;
  dueAtEvidence?: string;
  dueAtPersisted?: boolean;
  dueReady?: boolean;
  overdue?: boolean;
  claimOwner?: string;
  claimedAt?: string | null;
  claimed?: boolean;
  recoveryState?: string;
  dueStateSummary?: string;
  dueSummary?: string;
  scheduleSummary?: string;
  createdAt: string;
  updatedAt: string;
};

export type Workflow = {
  id: string;
  pluginId: string;
  traceId?: string;
  eventId?: string;
  runId?: string;
  correlationId?: string;
  status: string;
  currentIndex: number;
  waitingFor?: string;
  sleepingUntil?: string | null;
  completed: boolean;
  compensated: boolean;
  state?: Record<string, unknown>;
  lastEventId?: string;
  lastEventType?: string;
  statusSource?: string;
  statePersisted: boolean;
  runtimeOwner?: string;
  createdAt: string;
  updatedAt: string;
  summary?: string;
};

export type AlertRecord = {
  id: string;
  objectType: string;
  objectId: string;
  failureType: string;
  firstOccurredAt: string;
  latestOccurredAt: string;
  latestReason: string;
  traceId?: string;
  eventId?: string;
  runId?: string;
  correlation?: string;
  createdAt: string;
};

export type AuditEntry = {
  actor: string;
  permission?: string;
  action: string;
  target: string;
  allowed: boolean;
  reason?: string;
  trace_id?: string;
  event_id?: string;
  plugin_id?: string;
  run_id?: string;
  session_id?: string;
  correlation_id?: string;
  error_category?: string;
  error_code?: string;
  occurred_at: string;
};

export type ReplayOperation = {
  replayId: string;
  sourceEventId: string;
  replayEventId: string;
  status: string;
  reason?: string;
  occurredAt?: string;
  updatedAt?: string;
  stateSource?: string;
  persisted: boolean;
  summary?: string;
};

export type RolloutOperation = {
  operationId: string;
  pluginId: string;
  action: string;
  currentVersion?: string;
  candidateVersion?: string;
  status: string;
  reason?: string;
  occurredAt?: string;
  updatedAt?: string;
  stateSource?: string;
  persisted: boolean;
  summary?: string;
};

export type ConsoleRecovery = {
  recoveredAt?: string;
  totalJobs: number;
  recoveredJobs: number;
  recoveredRunning: number;
  retriedJobs: number;
  deadJobs: number;
  statusCounts?: Record<string, number>;
  totalSchedules: number;
  recoveredSchedules: number;
  invalidSchedules: number;
  scheduleKinds?: Record<string, number>;
  summary?: string;
};

export type ConsoleAuthorizationPolicy = {
  permissions: string[];
  pluginScope?: string[];
  eventScope?: string[];
};

export type ConsoleRBACConfig = {
  ActorRoles: Record<string, string[]>;
  Policies: Record<string, ConsoleAuthorizationPolicy>;
  ConsoleReadPermission?: string;
};

export type ConsoleRolloutPolicyDeclaration = {
  entryPoints?: string[];
  preflightChecks?: string[];
  activationChecks?: string[];
  auditReasons?: string[];
  recordStore?: string;
  supportedModes?: string[];
  unsupportedModes?: string[];
  verificationEndpoints?: string[];
  facts?: string[];
  summary?: string;
};

export type ConsoleConfig = {
  Runtime: {
    Environment: string;
    LogLevel: string;
    HTTPPort: number;
  };
  AIChat?: {
    Provider?: string;
    Endpoint?: string;
    Model?: string;
    RequestTimeoutMs?: number;
  };
  Secrets?: {
    WebhookTokenRef?: string;
    AIChatAPIKeyRef?: string;
  };
  RBAC?: ConsoleRBACConfig;
};

export type ConsoleMeta = {
  runtime_entry: string;
  demo_paths: string[];
  sqlite_path: string;
  scheduler_interval_ms: number;
  ai_worker_enabled?: boolean;
  ai_job_dispatcher_registered?: boolean;
  console_mode: string;
  generated_at?: string;
  plugin_operator_actions?: string[];
  plugin_operator_scope?: string;
  plugin_config_operator_actions?: string[];
  plugin_config_operator_scope?: string;
  job_operator_actions?: string[];
  job_operator_scope?: string;
  schedule_operator_actions?: string[];
  schedule_operator_scope?: string;
  rbac_console_read_actor_header?: string;
  rbac_console_read_permission?: boolean;
  rbac_console_limitations?: string[];
  rollout_policy?: ConsoleRolloutPolicyDeclaration;
  rollout_record_store?: string;
  rollout_record_read_model?: string;
  rollout_record_persisted?: boolean;
  rollout_head_read_model?: string;
  rollout_head_persisted?: boolean;
  rollout_console_limitations?: string[];
  verification_endpoints?: string[];
  request_identity?: {
    actor_id?: string;
    token_id?: string;
    auth_method?: string;
    session_id?: string;
  };
  [key: string]: unknown;
};

export type ConsoleObservability = {
  jobDispatchReady: number;
  scheduleDueReady: number;
  jobStateSource?: string;
  scheduleStateSource?: string;
  logStateSource?: string;
  traceStateSource?: string;
  metricsStateSource?: string;
  verificationEndpoints?: string[];
  alertability?: {
    baseline?: {
      id: string;
      severity: string;
      signal: string;
      condition: string;
      verificationEndpoints?: string[];
      summary?: string;
    }[];
    activeFindings?: {
      ruleId: string;
      severity: string;
      status: string;
      summary: string;
      evidence?: string[];
    }[];
    summary?: string;
  };
  summary?: string;
};

export type ConsolePayload = {
  status: RuntimeStatus;
  adapters: AdapterInstance[];
  alerts: AlertRecord[];
  replayOps: ReplayOperation[];
  rolloutOps: RolloutOperation[];
  rolloutHeads: {
    pluginId: string;
    stable: {
      version?: string;
      apiVersion?: string;
      mode?: string;
    };
    active: {
      version?: string;
      apiVersion?: string;
      mode?: string;
    };
    candidate?: {
      version?: string;
      apiVersion?: string;
      mode?: string;
    };
    phase: string;
    status: string;
    reason?: string;
    lastOperationId?: string;
    updatedAt?: string;
    stateSource?: string;
    persisted: boolean;
    summary?: string;
  }[];
  plugins: PluginManifest[];
  jobs: Job[];
  schedules: Schedule[];
  workflows: Workflow[];
  logs: string[];
  audits: AuditEntry[];
  config: ConsoleConfig;
  meta: ConsoleMeta;
  recovery: ConsoleRecovery;
  observability: ConsoleObservability;
};

export type ConsoleFilters = {
  logQuery: string;
  jobQuery: string;
  pluginID: string;
};

export type OperatorActionResult = {
  status?: string;
  action?: string;
  target?: string;
  accepted?: boolean;
  reason?: string;
  [key: string]: unknown;
};

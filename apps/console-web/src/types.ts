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

export type PluginManifest = {
  id: string;
  name: string;
  version: string;
  apiVersion: string;
  mode: 'inproc' | 'subprocess' | 'remote';
  permissions: string[];
  configSchema?: Record<string, unknown>;
  entry: PluginEntry;
  configStateKind?: string;
  configSource?: string;
  configPersisted?: boolean;
  configUpdatedAt?: string;
  enabled?: boolean;
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

export type JobStatus = 'pending' | 'running' | 'retrying' | 'failed' | 'dead' | 'done';

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
  deadLetter: boolean;
  correlation: string;
  dispatchReady?: boolean;
  queueStateSummary?: string;
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
  dueReady?: boolean;
  dueStateSummary?: string;
  scheduleSummary?: string;
  createdAt: string;
  updatedAt: string;
};

export type ConsoleConfig = {
  Runtime: {
    Environment: string;
    LogLevel: string;
    HTTPPort: number;
  };
};

export type ConsoleMeta = {
  runtime_entry: string;
  demo_paths: string[];
  sqlite_path: string;
  scheduler_interval_ms: number;
  ai_worker_enabled?: boolean;
  ai_job_dispatcher_registered?: boolean;
  console_mode: string;
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
  summary?: string;
};

export type ConsolePayload = {
  status: RuntimeStatus;
  adapters: AdapterInstance[];
  plugins: PluginManifest[];
  jobs: Job[];
  schedules: Schedule[];
  logs: string[];
  config: ConsoleConfig;
  meta: ConsoleMeta;
  observability: ConsoleObservability;
};

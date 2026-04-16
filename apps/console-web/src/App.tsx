import { useEffect, useState, type ReactNode } from 'react';
import { buildConsoleRequestURL, getConsoleApiURL } from './consoleApiClient';
import { parseConsolePayload } from './consolePayload';
import type { AdapterInstance, ConsolePayload, Job, PluginManifest, Schedule } from './types';

const consoleApiURL = getConsoleApiURL();

const statusLabels: Array<{ key: keyof ConsolePayload['status']; label: string }> = [
  { key: 'adapters', label: 'Adapters' },
  { key: 'plugins', label: 'Plugins' },
  { key: 'jobs', label: 'Jobs' },
  { key: 'schedules', label: 'Schedules' },
];

function PluginRecoveryFacts(props: { plugin: PluginManifest }) {
  const { plugin } = props;

  if (plugin.lastDispatchSuccess === undefined) {
    return <div className="cell-subtitle">recovery facts: no runtime evidence in this process</div>;
  }

  return (
    <>
      <div className="cell-subtitle">current failure streak: {plugin.currentFailureStreak ?? 0}</div>
      {plugin.lastRecoveredAt ? (
        <>
          <div className="cell-subtitle">last recovered at: {plugin.lastRecoveredAt}</div>
          <div className="cell-subtitle">failures before recovery: {plugin.lastRecoveryFailureCount ?? 0}</div>
        </>
      ) : (
        <div className="cell-subtitle">last recovered at: none observed in this process</div>
      )}
    </>
  );
}

function PluginEvidenceTime(props: { plugin: PluginManifest }) {
  const { plugin } = props;

  if (plugin.lastDispatchSuccess === undefined) {
    return <div className="cell-subtitle">last dispatch at: none observed in this process</div>;
  }

  return (
    <>
      <div className="cell-subtitle">
        last dispatch at: {plugin.lastDispatchAt ?? 'runtime evidence observed in this process, timestamp unavailable'}
      </div>
      <div className="cell-subtitle">evidence time scope: this process only, not persisted history</div>
    </>
  );
}

function PluginStateContract(props: { plugin: PluginManifest }) {
  const { plugin } = props;

  return (
    <>
      <div className="cell-subtitle">
        plugin config: {plugin.configStateKind ?? 'not declared'}
        {plugin.configPersisted ? ` via ${plugin.configSource ?? 'persisted config store'}` : ' not persisted for this plugin row'}
      </div>
      <div className="cell-subtitle">
        plugin config updated at: {plugin.configUpdatedAt ?? 'no persisted plugin-owned config captured'}
      </div>
      <div className="cell-subtitle">
        enabled overlay: {plugin.enabledStateSource ?? 'runtime-default-enabled'} / {plugin.enabled ? 'enabled' : 'disabled'}
        {plugin.enabledStatePersisted ? ' / persisted operator override' : ' / runtime default only'}
      </div>
      <div className="cell-subtitle">
        status snapshot: {plugin.statusSource ?? 'runtime-registry'} / {plugin.statusEvidence ?? 'manifest-only'}
      </div>
    </>
  );
}

function PluginPublishContract(props: { plugin: PluginManifest }) {
  const { plugin } = props;

  if (!plugin.publish) {
    return <div className="cell-subtitle">publish metadata: not declared in this registered plugin manifest</div>;
  }

  return (
    <>
      <div className="cell-subtitle">publish source type: {plugin.publish.sourceType}</div>
      <div className="cell-subtitle">
        publish source URI:{' '}
        <a href={plugin.publish.sourceUri} target="_blank" rel="noreferrer">
          {plugin.publish.sourceUri}
        </a>
      </div>
      <div className="cell-subtitle">runtime version range: {plugin.publish.runtimeVersionRange}</div>
    </>
  );
}

type PluginAttentionState = 'failing' | 'recovered' | 'normal';

function getPluginAttentionState(plugin: PluginManifest): PluginAttentionState {
  if ((plugin.currentFailureStreak ?? 0) > 0) {
    return 'failing';
  }
  if (plugin.lastRecoveredAt) {
    return 'recovered';
  }
  return 'normal';
}

function getPluginAttentionPriority(plugin: PluginManifest): number {
  const attentionState = getPluginAttentionState(plugin);
  if (attentionState === 'failing') {
    return 0;
  }
  if (attentionState === 'recovered') {
    return 1;
  }
  return 2;
}

type PluginAttentionSummary = {
  failing: number;
  recovered: number;
};

function summarizePluginAttention(plugins: PluginManifest[]): PluginAttentionSummary {
  return plugins.reduce<PluginAttentionSummary>(
    (summary, plugin) => {
      const attentionState = getPluginAttentionState(plugin);
      if (attentionState === 'failing') {
        summary.failing += 1;
      } else if (attentionState === 'recovered') {
        summary.recovered += 1;
      }
      return summary;
    },
    { failing: 0, recovered: 0 },
  );
}

function isPluginNeedingAttention(plugin: PluginManifest): boolean {
  return getPluginAttentionState(plugin) !== 'normal';
}

function SectionCard(props: { title: string; description: string; children: ReactNode }) {
  const { title, description, children } = props;
  return (
    <section className="panel">
      <div className="section-heading">
        <div>
          <h2>{title}</h2>
          <p>{description}</p>
        </div>
      </div>
      {children}
    </section>
  );
}

function AdapterTable({ adapters }: { adapters: AdapterInstance[] }) {
	return (
		<div className="table-wrap">
			<table>
				<thead>
					<tr>
						<th>ID</th>
						<th>Adapter / Source</th>
						<th>Status</th>
						<th>Health</th>
						<th>Online</th>
						<th>Updated At</th>
						<th>Summary</th>
					</tr>
				</thead>
				<tbody>
					{adapters.map((adapter) => (
						<tr key={adapter.id}>
							<td>
								<div className="cell-title">{adapter.id}</div>
								<div className="cell-subtitle">{adapter.statePersisted ? 'persisted adapter lifecycle facts' : 'runtime-only adapter lifecycle facts'}</div>
							</td>
							<td>
								<div className="cell-title">{adapter.adapter} / {adapter.source}</div>
								<div className="cell-subtitle">{adapter.statusSource ?? 'status source unavailable'}</div>
							</td>
							<td>{adapter.status ?? '—'}</td>
							<td>{adapter.health ?? '—'}</td>
							<td>
								<span className={`badge ${adapter.online ? 'badge-done' : 'badge-dead'}`}>
									{adapter.online ? 'online' : 'offline'}
								</span>
							</td>
							<td>{adapter.updatedAt ?? '—'}</td>
							<td>{adapter.summary ?? '—'}</td>
						</tr>
					))}
				</tbody>
			</table>
		</div>
	);
}

function PluginTable(props: {
  plugins: PluginManifest[];
  currentPluginId: string;
  onLocatePlugin: (pluginId: string) => void;
}) {
  const { plugins, currentPluginId, onLocatePlugin } = props;
  const prioritizedPlugins = plugins
    .map((plugin, index) => ({ plugin, index }))
    .sort((left, right) => {
      const priorityDelta = getPluginAttentionPriority(left.plugin) - getPluginAttentionPriority(right.plugin);
      if (priorityDelta !== 0) {
        return priorityDelta;
      }
      return left.index - right.index;
    })
    .map(({ plugin }) => plugin);

  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            <th>ID</th>
            <th>Name</th>
            <th>Version</th>
            <th>Mode</th>
              <th>API</th>
				<th>Permissions</th>
				<th>Status source</th>
				<th>Last Dispatch</th>
			</tr>
          </thead>
        <tbody>
          {prioritizedPlugins.map((plugin) => {
            const isLocated = currentPluginId === plugin.id;
            const attentionState = getPluginAttentionState(plugin);

            return (
              <tr key={plugin.id} className={attentionState === 'normal' ? undefined : `plugin-row-${attentionState}`}>
                <td>
                  <div className="cell-title">{plugin.id}</div>
                  <div className="cell-subtitle">{plugin.entry.binary ?? plugin.entry.module ?? 'internal entry'}</div>
                  <div className="cell-subtitle plugin-attention-actions">
                    {attentionState === 'failing' ? <span className="badge badge-attention">Needs attention</span> : null}
                    {attentionState === 'recovered' ? <span className="badge badge-recovered">Recovered recently</span> : null}
                    <button
                      type="button"
                      className="badge plugin-locate-button"
                      onClick={() => onLocatePlugin(plugin.id)}
                      disabled={isLocated}
                      aria-label={isLocated ? `Currently filtered to plugin ${plugin.id}` : `Filter to plugin ${plugin.id}`}
                    >
                      {isLocated ? 'Located' : 'Locate'}
                    </button>
                  </div>
                </td>
                <td>{plugin.name}</td>
                <td>{plugin.version}</td>
                <td>
                  <span className="badge">{plugin.mode}</span>
                  <PluginPublishContract plugin={plugin} />
                </td>
                <td>{plugin.apiVersion}</td>
				<td>{plugin.permissions.length > 0 ? plugin.permissions.join(', ') : 'read-only'}</td>
				<td>
				  <div>{plugin.statusSource ?? 'runtime-registry'}</div>
				  <div className="cell-subtitle">
				    {plugin.statusSummary ?? (plugin.runtimeStateLive ? 'runtime evidence available' : 'manifest-only evidence')}
				  </div>
				  <PluginStateContract plugin={plugin} />
				  <div className="cell-subtitle">recovery: {plugin.statusRecovery ?? 'no-runtime-evidence'}</div>
				  <div className="cell-subtitle">evidence staleness: {plugin.statusStaleness ?? 'static-registration'}</div>
				</td>
				<td>
				  {plugin.lastDispatchSuccess === undefined ? (
				 	<div>
				   <span className="badge">static</span>
				   <div className="cell-subtitle">{plugin.statusEvidence ?? 'manifest-only'}</div>
				   <PluginEvidenceTime plugin={plugin} />
				   <div className="cell-subtitle">no live runtime dispatch captured in this process</div>
				   <PluginRecoveryFacts plugin={plugin} />
				 </div>
				  ) : plugin.lastDispatchSuccess ? (
				 <div>
				   <span className="badge badge-done">{plugin.lastDispatchKind ?? 'runtime'} success</span>
				   <div className="cell-subtitle">{plugin.statusEvidence ?? 'runtime-dispatch-result'}</div>
				   <PluginEvidenceTime plugin={plugin} />
				   <div className="cell-subtitle">dispatch kind: {plugin.lastDispatchKind ?? 'unknown'}</div>
				   <div className="cell-subtitle">recovery: {plugin.statusRecovery ?? 'last-dispatch-succeeded'}</div>
				   <div className="cell-subtitle">evidence staleness: {plugin.statusStaleness ?? 'process-local-volatile'}</div>
				   <PluginRecoveryFacts plugin={plugin} />
				 </div>
				  ) : (
				 <div>
				   <span className="badge badge-dead">{plugin.lastDispatchKind ?? 'runtime'} failed</span>
				   <div className="cell-subtitle">{plugin.statusEvidence ?? 'runtime-dispatch-result'}</div>
				   <PluginEvidenceTime plugin={plugin} />
				   <div className="cell-subtitle">dispatch kind: {plugin.lastDispatchKind ?? 'unknown'}</div>
				   <div className="cell-subtitle">recovery: {plugin.statusRecovery ?? 'last-dispatch-failed'}</div>
				   <div className="cell-subtitle">evidence staleness: {plugin.statusStaleness ?? 'process-local-volatile'}</div>
				   <PluginRecoveryFacts plugin={plugin} />
				   {plugin.statusEvidence === 'runtime-dispatch-result:instance-config-reject' ? (
				  <div className="cell-subtitle">dispatch stopped before subprocess launch: instance-config rejected</div>
				   ) : null}
				   <div className="cell-subtitle">{plugin.lastDispatchError || 'dispatch failed'}</div>
				 </div>
				              )}
				            </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function JobTable({ jobs }: { jobs: Job[] }) {
  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            <th>ID</th>
            <th>Type</th>
            <th>Status</th>
	            <th>Retries</th>
	            <th>Async state</th>
	            <th>Timeout</th>
            <th>Correlation</th>
            <th>Last Error</th>
          </tr>
        </thead>
        <tbody>
          {jobs.map((job) => (
            <tr key={job.id}>
              <td>
                <div className="cell-title">{job.id}</div>
                <div className="cell-subtitle">Created {job.createdAt}</div>
              </td>
              <td>{job.type}</td>
              <td>
                <span className={`badge badge-${job.status}`}>{job.status}</span>
              </td>
	              <td>
	                {job.retryCount}/{job.maxRetries}
	              </td>
	              <td>
	                <div>{job.queueStateSummary ?? '—'}</div>
	                <div className="cell-subtitle">{job.dispatchReady ? 'dispatch-ready from persisted queue state' : job.recoverySummary || 'waiting for persisted queue state'}</div>
	              </td>
	              <td>{formatDuration(job.timeout)}</td>
              <td>{job.correlation}</td>
              <td>{job.lastError || '—'}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function ScheduleTable({ schedules }: { schedules: Schedule[] }) {
	return (
		<div className="table-wrap">
			<table>
				<thead>
					<tr>
						<th>ID</th>
						<th>Kind</th>
						<th>Source</th>
						<th>Event Type</th>
						<th>Next Due</th>
						<th>Async state</th>
						<th>Config</th>
					</tr>
				</thead>
				<tbody>
					{schedules.map((schedule) => (
						<tr key={schedule.id}>
							<td>
								<div className="cell-title">{schedule.id}</div>
								<div className="cell-subtitle">Created {schedule.createdAt}</div>
							</td>
							<td>
								<span className="badge">{schedule.kind}</span>
							</td>
							<td>{schedule.source}</td>
							<td>{schedule.eventType}</td>
							<td>{schedule.dueAt ?? schedule.executeAt ?? '—'}</td>
							<td>
								<div>{schedule.dueStateSummary ?? '—'}</div>
								<div className="cell-subtitle">{schedule.scheduleSummary ?? 'persisted schedule state unavailable'}</div>
							</td>
							<td>{formatScheduleConfig(schedule)}</td>
						</tr>
					))}
				</tbody>
			</table>
		</div>
	);
}

function formatDuration(durationNs: number) {
  if (durationNs % 1_000_000_000 === 0) {
    return `${durationNs / 1_000_000_000}s`;
  }
  if (durationNs % 1_000_000 === 0) {
    return `${durationNs / 1_000_000}ms`;
  }
	return `${durationNs}ns`;
}

function formatScheduleConfig(schedule: Schedule) {
	if (schedule.kind === 'cron') {
		return schedule.cronExpr || '—';
	}
	if (schedule.kind === 'delay') {
		return `${schedule.delayMs}ms`;
	}
	return schedule.executeAt ?? '—';
}

function App() {
  const [data, setData] = useState<ConsolePayload | null>(null);
  const [error, setError] = useState<string>('');
  const [logQuery, setLogQuery] = useState<string>(() => new URLSearchParams(window.location.search).get('log_query') ?? '');
  const [jobQuery, setJobQuery] = useState<string>(() => new URLSearchParams(window.location.search).get('job_query') ?? '');
  const [pluginQuery, setPluginQuery] = useState<string>(() => new URLSearchParams(window.location.search).get('plugin_id') ?? '');
  const [attentionOnly, setAttentionOnly] = useState(false);
  const normalizedLogQuery = logQuery.trim();
  const normalizedJobQuery = jobQuery.trim();
  const normalizedPluginQuery = pluginQuery.trim();

  useEffect(() => {
    let active = true;

    async function loadConsoleData() {
      try {
        const requestURL = buildConsoleRequestURL(window.location.origin, normalizedLogQuery, normalizedJobQuery, normalizedPluginQuery);
        const response = await fetch(requestURL, {
          headers: { Accept: 'application/json' },
        });
        if (!response.ok) {
          throw new Error(`Console API request failed: ${response.status}`);
        }
        const payload = parseConsolePayload(await response.json());
        if (!active) {
          return;
        }
        setData(payload);
        setError('');
      } catch (fetchError) {
        if (!active) {
          return;
        }
        setError(fetchError instanceof Error ? fetchError.message : 'Failed to load Console API payload');
      }
    }

    void loadConsoleData();

    return () => {
      active = false;
    };
  }, [normalizedLogQuery, normalizedJobQuery, normalizedPluginQuery]);

  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    if (normalizedLogQuery === '') {
      params.delete('log_query');
    } else {
      params.set('log_query', normalizedLogQuery);
    }
    if (normalizedJobQuery === '') {
      params.delete('job_query');
    } else {
      params.set('job_query', normalizedJobQuery);
    }
    if (normalizedPluginQuery === '') {
      params.delete('plugin_id');
    } else {
      params.set('plugin_id', normalizedPluginQuery);
    }
    const next = params.toString();
    const nextURL = `${window.location.pathname}${next ? `?${next}` : ''}`;
    window.history.replaceState(null, '', nextURL);
  }, [normalizedLogQuery, normalizedJobQuery, normalizedPluginQuery]);

  useEffect(() => {
    function syncFiltersFromLocation() {
      const params = new URLSearchParams(window.location.search);
      setLogQuery(params.get('log_query') ?? '');
      setJobQuery(params.get('job_query') ?? '');
      setPluginQuery(params.get('plugin_id') ?? '');
    }

    window.addEventListener('popstate', syncFiltersFromLocation);
    return () => {
      window.removeEventListener('popstate', syncFiltersFromLocation);
    };
  }, []);

  if (!data && !error) {
    return (
      <div className="app-shell">
        <section className="panel state-panel">
          <p className="eyebrow">bot-platform / Console Web v0</p>
          <h1>Loading read-only operations panel</h1>
          <p className="hero-copy">Requesting the live Console API payload from {consoleApiURL}.</p>
        </section>
      </div>
    );
  }

  if (!data) {
    return (
      <div className="app-shell">
        <section className="panel state-panel state-panel-error">
          <p className="eyebrow">bot-platform / Console Web v0</p>
          <h1>Console API unavailable</h1>
          <p className="hero-copy">The read-only panel now expects live data from {consoleApiURL}.</p>
          <code className="log-line">{error}</code>
        </section>
      </div>
    );
  }

  const pluginAttentionSummary = summarizePluginAttention(data.plugins);
  const visiblePlugins = attentionOnly ? data.plugins.filter(isPluginNeedingAttention) : data.plugins;

  return (
    <div className="app-shell">
      <header className="hero panel">
        <div>
          <p className="eyebrow">bot-platform / Console Web v0</p>
          <h1>Read-only operations panel</h1>
          <p className="hero-copy">
            This console view now renders the live read-only Console API payload exposed by the runtime.
          </p>
        </div>
        <div className="login-card">
          <h2>Login placeholder</h2>
          <p>Authentication is intentionally out of scope for this slice.</p>
          <div className="login-placeholder">
            <span className="label">Mode</span>
            <strong>Local read-only preview</strong>
          </div>
          <div className="login-placeholder muted">
            <span className="label">Next step</span>
            <strong>Keep auth and write actions out of scope while read APIs stabilize</strong>
          </div>
        </div>
      </header>

      <SectionCard title="Runtime status" description="Snapshot of the local runtime aggregate returned by ConsoleAPI.Status().">
        <div className="stats-grid">
          {statusLabels.map(({ key, label }) => (
            <article className="stat-card" key={key}>
              <span className="label">{label}</span>
              <strong>{data.status[key]}</strong>
            </article>
          ))}
        </div>
      </SectionCard>

      <SectionCard
        title="Runtime diagnostics"
        description="Read-only runtime entry metadata used for local debugging and path discovery."
      >
        <div className="stats-grid">
          <article className="stat-card">
            <span className="label">Runtime entry</span>
            <strong>{data.meta.runtime_entry}</strong>
          </article>
          <article className="stat-card">
            <span className="label">Console mode</span>
            <strong>{data.meta.console_mode}</strong>
          </article>
          <article className="stat-card">
            <span className="label">SQLite path</span>
            <strong>{data.meta.sqlite_path}</strong>
          </article>
          <article className="stat-card">
            <span className="label">AI dispatcher</span>
            <strong>{data.meta.ai_job_dispatcher_registered ? 'registered' : data.meta.ai_worker_enabled ? 'legacy-worker-enabled' : 'not-registered'}</strong>
          </article>
        </div>
        <pre className="config-viewer">{JSON.stringify(data.meta.demo_paths, null, 2)}</pre>
      </SectionCard>

		<SectionCard
			title="Async observability"
			description="Derived from persisted job/schedule state plus runtime log, trace, and metric sources rather than frontend-only guesses."
		>
			<div className="stats-grid">
				<article className="stat-card">
					<span className="label">Jobs dispatch-ready</span>
					<strong>{data.observability.jobDispatchReady}</strong>
				</article>
				<article className="stat-card">
					<span className="label">Schedules due-ready</span>
					<strong>{data.observability.scheduleDueReady}</strong>
				</article>
				<article className="stat-card">
					<span className="label">Metrics source</span>
					<strong>{data.observability.metricsStateSource ?? '—'}</strong>
				</article>
				<article className="stat-card">
					<span className="label">Trace source</span>
					<strong>{data.observability.traceStateSource ?? '—'}</strong>
				</article>
			</div>
			<div className="cell-subtitle">{data.observability.summary ?? 'No async observability summary available.'}</div>
			<pre className="config-viewer">{JSON.stringify(data.observability.verificationEndpoints ?? [], null, 2)}</pre>
		</SectionCard>

		<SectionCard
			title="Adapter lifecycle"
			description="Read-only adapter registration and health facts already present in /api/console.adapters[], shown alongside plugin lifecycle state."
		>
			{data.adapters.length === 0 ? (
				<div className="empty-state">No adapter lifecycle facts available yet.</div>
			) : (
				<AdapterTable adapters={data.adapters} />
			)}
		</SectionCard>

      <SectionCard
			title="Plugin lifecycle"
			description="Read-only plugin lifecycle view that keeps plugin-owned persisted config, runtime/operator enabled overlay, and runtime status evidence as separate contracts."
      >
        <div className="plugin-attention-toolbar">
          <div className="plugin-attention-summary" aria-live="polite">
            <span className="label">Attention summary</span>
            <strong>
              {pluginAttentionSummary.failing} failing / {pluginAttentionSummary.recovered} recovered
            </strong>
            <div className="cell-subtitle">Uses the current read-only plugin result set only; no cross-process history is implied.</div>
          </div>
          <label className="plugin-attention-toggle" htmlFor="plugin-attention-only">
            <input
              id="plugin-attention-only"
              type="checkbox"
              checked={attentionOnly}
              onChange={(event) => setAttentionOnly(event.target.checked)}
            />
            <span>Attention only</span>
          </label>
        </div>
        <div className="filter-row">
          <label className="filter-label" htmlFor="plugin-query">
            Plugin ID
          </label>
          <input
            id="plugin-query"
            className="filter-input"
            type="search"
            value={pluginQuery}
            onChange={(event) => setPluginQuery(event.target.value)}
            placeholder="Filter plugins by exact plugin ID for recent failure/recovery evidence"
          />
        </div>
        {visiblePlugins.length === 0 ? (
          <div className="empty-state">
            {data.plugins.length === 0
              ? normalizedPluginQuery === ''
                ? 'No plugins available yet.'
                : 'No plugins match the current plugin ID filter.'
              : 'No failing or recently recovered plugins in the current result set.'}
          </div>
        ) : (
          <PluginTable
            plugins={visiblePlugins}
            currentPluginId={normalizedPluginQuery}
            onLocatePlugin={setPluginQuery}
          />
        )}
      </SectionCard>

      <SectionCard title="Job list" description="Current in-memory queue projection with status, retries, and error context.">
        <div className="filter-row">
          <label className="filter-label" htmlFor="job-query">
            Job filter
          </label>
          <input
            id="job-query"
            className="filter-input"
            type="search"
            value={jobQuery}
            onChange={(event) => setJobQuery(event.target.value)}
            placeholder="Filter jobs by id, type, status, correlation, or last error"
          />
        </div>
        {data.jobs.length === 0 ? (
          <div className="empty-state">
            {normalizedJobQuery === '' ? 'No jobs available yet.' : 'No jobs match the current read-only filter.'}
          </div>
        ) : (
          <JobTable jobs={data.jobs} />
        )}
      </SectionCard>

		<SectionCard
			title="Schedule list"
			description="Read-only persisted schedule projection aligned to the real schedule plan store rather than runtime-only traces."
		>
			{data.schedules.length === 0 ? (
				<div className="empty-state">No persisted schedules available yet.</div>
			) : (
				<ScheduleTable schedules={data.schedules} />
			)}
		</SectionCard>

      <SectionCard title="Log viewer" description="String-based logs mirror the current ConsoleAPI.Logs() surface.">
        <div className="filter-row">
          <label className="filter-label" htmlFor="log-query">
            Log filter
          </label>
          <input
            id="log-query"
            className="filter-input"
            type="search"
            value={logQuery}
            onChange={(event) => setLogQuery(event.target.value)}
            placeholder="Filter logs by substring"
          />
        </div>
        {data.logs.length === 0 ? (
          <div className="empty-state">
            {normalizedLogQuery === '' ? 'No logs available yet.' : 'No logs match the current read-only filter.'}
          </div>
        ) : (
        <div className="log-list" role="log" aria-label="runtime logs">
          {data.logs.map((line, index) => (
            <code key={`${index}-${line}`} className="log-line">
              {line}
            </code>
          ))}
        </div>
        )}
      </SectionCard>

      <SectionCard title="Config viewer" description="Raw read-only config snapshot based on runtime-core Config JSON/YAML fields.">
        <pre className="config-viewer">{JSON.stringify(data.config, null, 2)}</pre>
      </SectionCard>
    </div>
  );
}

export default App;

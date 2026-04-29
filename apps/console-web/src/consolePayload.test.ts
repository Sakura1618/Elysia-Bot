import { describe, expect, it } from 'vitest';
import { cloneMockConsoleData } from './mock-console-data';
import { parseConsolePayload } from './consolePayload';

describe('parseConsolePayload', () => {
  it('accepts the expanded A11 console payload with routes, operator meta, and evidence sections', () => {
    const payload = cloneMockConsoleData();

    const parsed = parseConsolePayload(payload);

    expect(parsed.meta.console_mode).toBe('read+operator-plugin-enable-disable+plugin-config+job-control+schedule-cancel');
    expect(parsed.meta.job_operator_actions).toEqual([
      '/demo/jobs/{job-id}/pause',
      '/demo/jobs/{job-id}/resume',
      '/demo/jobs/{job-id}/cancel',
      '/demo/jobs/{job-id}/retry',
    ]);
    expect(parsed.meta.job_operator_scope).toBe('queued jobs for pause|resume|cancel, dead-letter jobs for retry');
    expect(parsed.meta.schedule_operator_actions).toEqual(['/demo/schedules/{schedule-id}/cancel']);
    expect(parsed.meta.rbac_console_read_actor_header).toBe('X-Bot-Platform-Actor');
    expect(parsed.plugins[0]?.config).toEqual({ prefix: 'persisted: ' });
    expect(parsed.alerts[0]?.objectId).toBe('job-dead-letter-console');
    expect(parsed.workflows[0]?.status).toBe('waiting_event');
    expect(parsed.workflows[0]?.traceId).toBe('trace-workflow-console');
    expect(parsed.workflows[0]?.eventId).toBe('evt-workflow-console-origin');
    expect(parsed.workflows[0]?.runId).toBe('run-workflow-console');
    expect(parsed.workflows[0]?.correlationId).toBe('corr-workflow-console');
    expect(parsed.audits[0]?.occurred_at).toBe('2026-04-19T11:43:00Z');
    expect(parsed.audits[0]?.trace_id).toBe('trace-console-audit-plugin-disable');
    expect(parsed.audits[0]?.session_id).toBe('session-operator-bearer-admin-user');
    expect(parsed.audits[0]?.error_code).toBe('plugin_disabled');
    expect(parsed.audits[2]?.session_id).toBe('session-operator-bearer-job-operator');
    expect(parsed.rolloutHeads[0]?.pluginId).toBe('plugin-echo');
    expect(parsed.rolloutHeads[0]?.phase).toBe('canary');
    expect(parsed.rolloutHeads[0]?.active.version).toBe('0.2.0-candidate');
    expect(parsed.rolloutHeads[0]?.stateSource).toBe('sqlite-rollout-heads');
    expect(parsed.meta.rollout_policy).toEqual({
      entryPoints: ['/admin prepare <plugin-id>', '/admin activate <plugin-id>'],
      preflightChecks: ['manifest.id-match', 'manifest.mode-match', 'manifest.api-version-match', 'manifest.version-changed'],
      activationChecks: ['prepared-record-required', 'prepare-time-drift-recheck', 'lifecycle-enable-before-activated-mark'],
      auditReasons: ['rollout_prepared', 'rollout_activated', 'rollout_drifted', 'rollout_failed'],
      recordStore: 'sqlite-current-runtime-rollout-operations',
      supportedModes: ['manual-prepare-activate', 'manifest-preflight', 'activate-time-drift-recheck', 'minimal-audit-reasons'],
      unsupportedModes: [
        'rollback',
        'staged-rollout',
        'health-check-gate',
        'automatic-rollback',
        'persisted-rollout-history',
        'approval-workflow',
        'schema-migration-gate',
      ],
      verificationEndpoints: ['GET /api/console', 'go test ./packages/runtime-core ./plugins/plugin-admin ./apps/runtime -run Rollout'],
      facts: [
        'rollout is currently a manual admin chain limited to /admin prepare <plugin-id> and /admin activate <plugin-id>',
        'prepare records only minimal manifest compatibility facts for ID, Mode, APIVersion, and Version change',
        'activate re-checks current and candidate manifests before lifecycle enable so prepare-time drift is rejected',
        'rollout prepare and activate attempts are persisted as operational records and remain visible after restart',
      ],
      summary:
        'manual /admin prepare|activate only; preflight checks ID|Mode|APIVersion|Version; activate re-checks drift before lifecycle enable; rollout attempts persist as operational records; audit reasons are minimal; no rollback or staged rollout',
    });
    expect(parsed.meta.rollout_record_store).toBe('sqlite-current-runtime-rollout-operations');
    expect(parsed.meta.request_identity).toEqual({
      actor_id: 'viewer-user',
      token_id: 'viewer-main',
      auth_method: 'bearer',
      session_id: 'session-operator-bearer-viewer-user',
    });
    expect(parsed.recovery.totalSchedules).toBe(1);
  });

  it('rejects payloads when plugin config is not an object', () => {
    const payload = cloneMockConsoleData();
    payload.plugins[0] = { ...payload.plugins[0], config: 'persisted: ' as never };

    expect(() => parseConsolePayload(payload)).toThrow('Console API payload shape is incompatible with Console Web A11');
  });

  it('accepts runtime jobs that omit payload when there is no payload data', () => {
    const payload = cloneMockConsoleData();
    delete (payload.jobs[1] as Record<string, unknown>).payload;

    const parsed = parseConsolePayload(payload);

    expect(parsed.jobs[1]?.payload).toEqual({});
  });

  it('rejects jobs when payload is present but not an object', () => {
    const payload = cloneMockConsoleData();
    payload.jobs[1] = { ...payload.jobs[1], payload: 'not-an-object' as never };

    expect(() => parseConsolePayload(payload)).toThrow('Console API payload shape is incompatible with Console Web A11');
  });

  it('rejects payloads when meta job operator actions are not string arrays', () => {
    const payload = cloneMockConsoleData();
    payload.meta.job_operator_actions = [123 as never];

    expect(() => parseConsolePayload(payload)).toThrow('Console API payload shape is incompatible with Console Web A11');
  });

  it('rejects payloads when workflows do not include persisted state flags', () => {
    const payload = cloneMockConsoleData();
    payload.workflows[0] = { ...payload.workflows[0], statePersisted: undefined as never };

    expect(() => parseConsolePayload(payload)).toThrow('Console API payload shape is incompatible with Console Web A11');
  });

  it('rejects payloads when audits no longer use the runtime occurred_at field', () => {
    const payload = cloneMockConsoleData();
    payload.audits[0] = { ...payload.audits[0], occurred_at: 123 as never };

    expect(() => parseConsolePayload(payload)).toThrow('Console API payload shape is incompatible with Console Web A11');
  });

	it('rejects payloads when rollout heads stop exposing required stable or active snapshots', () => {
		const payload = cloneMockConsoleData();
		payload.rolloutHeads[0] = { ...payload.rolloutHeads[0], stable: 'not-a-snapshot' as never };

		expect(() => parseConsolePayload(payload)).toThrow('Console API payload shape is incompatible with Console Web A11');
	});

  it('rejects payloads when rollout policy declaration fields stop using strings and string arrays', () => {
    const payload = cloneMockConsoleData();
    payload.meta.rollout_policy = { ...payload.meta.rollout_policy, entryPoints: [123 as never] };

    expect(() => parseConsolePayload(payload)).toThrow('Console API payload shape is incompatible with Console Web A11');
  });

  it('rejects payloads when request identity metadata stops using strings', () => {
    const payload = cloneMockConsoleData();
    payload.meta.request_identity = { ...payload.meta.request_identity, actor_id: 123 as never };

    expect(() => parseConsolePayload(payload)).toThrow('Console API payload shape is incompatible with Console Web A11');
  });
});

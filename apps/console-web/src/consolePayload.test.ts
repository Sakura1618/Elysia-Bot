import { describe, expect, it } from 'vitest';
import { cloneMockConsoleData } from './mock-console-data';
import { parseConsolePayload } from './consolePayload';

describe('parseConsolePayload', () => {
  it('accepts the expanded A11 console payload with routes, operator meta, and evidence sections', () => {
    const payload = cloneMockConsoleData();

    const parsed = parseConsolePayload(payload);

    expect(parsed.meta.console_mode).toBe('read+operator-plugin-enable-disable+plugin-config+job-retry+schedule-cancel');
    expect(parsed.meta.job_operator_actions).toEqual(['/demo/jobs/{job-id}/retry']);
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

  it('rejects payloads when request identity metadata stops using strings', () => {
    const payload = cloneMockConsoleData();
    payload.meta.request_identity = { ...payload.meta.request_identity, actor_id: 123 as never };

    expect(() => parseConsolePayload(payload)).toThrow('Console API payload shape is incompatible with Console Web A11');
  });
});

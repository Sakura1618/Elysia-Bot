import { parseConsolePayload } from './consolePayload';
import type { ConsoleFilters, ConsolePayload, OperatorActionResult } from './types';

export const DEFAULT_OPERATOR_BEARER_TOKEN = '';
export const LOCAL_OPERATOR_BEARER_TOKEN_STORAGE_KEY = 'bot-platform.console.operator-bearer-token';
export const AUTO_REFRESH_STORAGE_KEY = 'bot-platform.console.auto-refresh';

const consoleApiURL = import.meta.env.VITE_CONSOLE_API_URL ?? '/api/console';
const runtimeBaseURL = import.meta.env.VITE_RUNTIME_BASE_URL ?? '';

function trimTrailingSlash(value: string): string {
  return value.endsWith('/') ? value.slice(0, -1) : value;
}

export function getConsoleApiURL(): string {
  return consoleApiURL;
}

export function getStoredOperatorBearerToken(): string {
  const stored = window.localStorage.getItem(LOCAL_OPERATOR_BEARER_TOKEN_STORAGE_KEY);
  if (stored === null) {
    return DEFAULT_OPERATOR_BEARER_TOKEN;
  }
  return stored.trim();
}

export function persistOperatorBearerToken(token: string): string {
  const normalized = token.trim();
  if (normalized === '') {
    window.localStorage.removeItem(LOCAL_OPERATOR_BEARER_TOKEN_STORAGE_KEY);
    return normalized;
  }
  window.localStorage.setItem(LOCAL_OPERATOR_BEARER_TOKEN_STORAGE_KEY, normalized);
  return normalized;
}

export function getStoredAutoRefresh(): boolean {
  const stored = window.localStorage.getItem(AUTO_REFRESH_STORAGE_KEY);
  if (stored === null) {
    return true;
  }
  return stored === 'true';
}

export function persistAutoRefresh(enabled: boolean): void {
  window.localStorage.setItem(AUTO_REFRESH_STORAGE_KEY, enabled ? 'true' : 'false');
}

export function buildConsoleRequestURL(origin: string, filters: ConsoleFilters): URL {
  const requestURL = new URL(consoleApiURL, origin);
  const normalizedLogQuery = filters.logQuery.trim();
  const normalizedJobQuery = filters.jobQuery.trim();
  const normalizedPluginID = filters.pluginID.trim();

  if (normalizedLogQuery !== '') {
    requestURL.searchParams.set('log_query', normalizedLogQuery);
  }
  if (normalizedJobQuery !== '') {
    requestURL.searchParams.set('job_query', normalizedJobQuery);
  }
  if (normalizedPluginID !== '') {
    requestURL.searchParams.set('plugin_id', normalizedPluginID);
  }

  return requestURL;
}

export function buildRuntimeRequestURL(origin: string, path: string): URL {
  const normalizedPath = path.startsWith('/') ? path : `/${path}`;
  const base = runtimeBaseURL.trim() === '' ? origin : trimTrailingSlash(runtimeBaseURL);
  return new URL(normalizedPath, base);
}

export function createAuthorizationHeaders(bearerToken: string, hasBody = false): Headers {
  const headers = new Headers({
    Accept: 'application/json',
  });
  if (hasBody) {
    headers.set('Content-Type', 'application/json');
  }
  const normalizedToken = bearerToken.trim();
  if (normalizedToken !== '') {
    headers.set('Authorization', `Bearer ${normalizedToken}`);
  }
  return headers;
}

export async function fetchConsolePayload(origin: string, bearerToken: string, filters: ConsoleFilters): Promise<ConsolePayload> {
  const response = await fetch(buildConsoleRequestURL(origin, filters), {
    headers: createAuthorizationHeaders(bearerToken),
  });
  if (!response.ok) {
    throw new Error(await response.text() || `Console API request failed: ${response.status}`);
  }
  return parseConsolePayload(await response.json());
}

export async function postOperatorAction(
  origin: string,
  bearerToken: string,
  path: string,
  body?: Record<string, unknown>,
): Promise<OperatorActionResult> {
  const response = await fetch(buildRuntimeRequestURL(origin, path), {
    method: 'POST',
    headers: createAuthorizationHeaders(bearerToken, body !== undefined),
    body: body === undefined ? undefined : JSON.stringify(body),
  });

  const responseText = await response.text();
  if (!response.ok) {
    throw new Error(responseText || `Operator action failed: ${response.status}`);
  }
  if (responseText.trim() === '') {
    return {};
  }
  return JSON.parse(responseText) as OperatorActionResult;
}

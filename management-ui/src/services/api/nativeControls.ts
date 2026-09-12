import { apiClient } from './client';

export type LimitPolicy = { rpm: number; concurrency: number };
export type FingerprintMode = 'off' | 'device' | 'session' | 'full';
export type HeaderMode = 'off' | 'clean' | 'client';
export type ControlScope = 'client' | 'credential';
export type CodexVersionSettings = { manual_version: string; automatic: boolean };
export type CodexVersionStatus = {
  settings: CodexVersionSettings;
  builtin_version: string;
  synced_version: string;
  effective_version: string;
  effective_source: 'manual' | 'synced' | 'builtin';
  source: string;
  interval_hours: number;
  last_checked_ms: number;
  last_synced_ms: number;
  last_error: string;
  syncing: boolean;
};
type Wire = Record<string, unknown>;
const obj = (v: unknown): Wire => (v && typeof v === 'object' ? (v as Wire) : {});
const num = (v: unknown) => (typeof v === 'number' && Number.isFinite(v) ? v : 0);
const str = (v: unknown) => (typeof v === 'string' ? v : '');
export const nativeControlsApi = {
  codexVersion: (signal?: AbortSignal) =>
    apiClient.get<CodexVersionStatus>('/native/headers/client-version', { signal }),
  setCodexVersion: (settings: CodexVersionSettings, signal?: AbortSignal) =>
    apiClient.put<CodexVersionStatus>('/native/headers/client-version', settings, { signal }),
  syncCodexVersion: (signal?: AbortSignal) =>
    apiClient.post<CodexVersionStatus>('/native/headers/client-version/sync', {}, { signal }),
  async identity(scope: ControlScope, target: string, signal?: AbortSignal) {
    const v = await apiClient.post<Wire>(
      '/native/identity',
      scope === 'client' ? { key: target } : { credential: target },
      { signal }
    );
    return {
      id: str(v.id),
      provider: str(v.provider),
      fingerprint: v.fingerprint === true,
      headers: v.headers === true,
    };
  },
  async limits(scope: ControlScope, id: string, signal?: AbortSignal) {
    const v = await apiClient.get<Wire>(`/native/limits/${scope}/${encodeURIComponent(id)}`, {
      signal,
    });
    const p = obj(v.policy);
    return {
      policy: { rpm: num(p.rpm), concurrency: num(p.concurrency) },
      active: num(v.active),
      rejected: num(v.rejected_this_run),
    };
  },
  setLimits: (scope: ControlScope, id: string, policy: LimitPolicy, signal?: AbortSignal) =>
    apiClient.put(`/native/limits/${scope}/${encodeURIComponent(id)}`, policy, { signal }),
  async fingerprint(id: string, signal?: AbortSignal): Promise<FingerprintMode> {
    const v = await apiClient.get<Wire>(`/native/fingerprint/${encodeURIComponent(id)}`, {
      signal,
    });
    if (v.mode === 'device' || v.mode === 'session' || v.mode === 'full') return v.mode;
    return 'off';
  },
  setFingerprint: (id: string, mode: FingerprintMode, signal?: AbortSignal) =>
    apiClient.put(`/native/fingerprint/${encodeURIComponent(id)}`, { mode }, { signal }),
  async headers(id: string, signal?: AbortSignal): Promise<HeaderMode> {
    const v = await apiClient.get<Wire>(`/native/headers/${encodeURIComponent(id)}`, { signal });
    return v.mode === 'clean' || v.mode === 'client' ? v.mode : 'off';
  },
  setHeaders: (id: string, mode: HeaderMode, signal?: AbortSignal) =>
    apiClient.put(`/native/headers/${encodeURIComponent(id)}`, { mode }, { signal }),
  async modelPrices(model: string, signal?: AbortSignal) {
    const v = await apiClient.get<Wire>('/native/pricing/catalog/model', {
      params: { model },
      signal,
    });
    const rate = (v: unknown) => (typeof v === 'number' && Number.isFinite(v) ? v : null);
    return {
      source: str(v.source),
      updated: num(v.updated_ms),
      hash: str(v.hash),
      rates: (Array.isArray(v.rates) ? v.rates : []).map(obj).map((r) => ({
        tier: str(r.tier) || 'standard',
        minContext: num(r.min_context),
        input: rate(r.input),
        output: rate(r.output),
        cacheRead: rate(r.cache_read),
        cacheWrite: rate(r.cache_write),
        cacheWrite1h: rate(r.cache_write_1h),
      })),
    };
  },
};

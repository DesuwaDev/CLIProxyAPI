import { apiClient } from './client';
import type { HistoryFilter } from './nativeManagement';

type Wire = Record<string, unknown>;
const obj = (v: unknown): Wire => (v && typeof v === 'object' ? (v as Wire) : {});
const str = (v: unknown) => (typeof v === 'string' ? v : '');
const nullable = (v: unknown) => (typeof v === 'number' && Number.isFinite(v) ? v : null);
const num = (v: unknown) => nullable(v) ?? 0;
const list = (v: unknown): Wire[] => (Array.isArray(v) ? v.map(obj) : []);

export type Percentiles = {
  samples: number;
  average: number | null;
  p50: number | null;
  p95: number | null;
  p99: number | null;
  maximum: number | null;
};
export type Analytics = {
  duration: Percentiles;
  ttft: Percentiles;
  histogram: { label: string; count: number }[];
  rpm: number;
  tpm: number;
  cacheReadRate: number | null;
};
export type RequestObservation = {
  id: string;
  sequence: number;
  requestId: string;
  started: number;
  ended: number;
  endpoint: string;
  keyHash: string;
  httpStatus: number;
  duration: number;
  outcome: string;
  stream: boolean;
  streamError: boolean;
  inspectionComplete: boolean;
};
export type CatalogSettings = { source: string; automatic: boolean; intervalHours: number };
export type CatalogStatus = {
  settings: CatalogSettings;
  models: number;
  updated: number;
  hash: string;
  skipped: number;
  lastAttempt: number;
  lastError: string;
  syncing: boolean;
};
const percentiles = (value: unknown): Percentiles => {
  const v = obj(value);
  return {
    samples: num(v.samples),
    average: nullable(v.average_ms),
    p50: nullable(v.p50_ms),
    p95: nullable(v.p95_ms),
    p99: nullable(v.p99_ms),
    maximum: nullable(v.max_ms),
  };
};
const observation = (v: Wire): RequestObservation => ({
  id: str(v.id),
  sequence: num(v.sequence),
  requestId: str(v.request_id),
  started: num(v.started_ms),
  ended: num(v.ended_ms),
  endpoint: str(v.endpoint),
  keyHash: str(v.key_hash),
  httpStatus: num(v.http_status),
  duration: num(v.duration_ms),
  outcome: str(v.outcome),
  stream: v.stream === true,
  streamError: v.stream_error === true,
  inspectionComplete: v.inspection_complete === true,
});

export const nativeInsightsApi = {
  async analytics(filter: HistoryFilter, signal?: AbortSignal): Promise<Analytics> {
    const v = await apiClient.get<Wire>('/native/history/analytics', {
      params: { from: filter.from, to: filter.to },
      signal,
    });
    return {
      duration: percentiles(v.duration),
      ttft: percentiles(v.ttft),
      histogram: list(v.histogram).map((b) => ({ label: str(b.label), count: num(b.count) })),
      rpm: num(v.rpm),
      tpm: num(v.tpm),
      cacheReadRate: nullable(v.cache_read_rate),
    };
  },
  async requests(
    filter: { from: number; to: number; before?: number; requestId?: string; outcome?: string },
    signal?: AbortSignal
  ) {
    const v = await apiClient.get<Wire>('/native/diagnostics/requests', {
      params: {
        from: filter.from,
        to: filter.to,
        before: filter.before,
        request_id: filter.requestId || undefined,
        outcome: filter.outcome || undefined,
      },
      signal,
    });
    return {
      summary: {
        total: num(obj(v.summary).total),
        success: num(obj(v.summary).success),
        errors: num(obj(v.summary).errors),
        unknown: num(obj(v.summary).unknown),
        average: num(obj(v.summary).average_ms),
      },
      requests: list(v.requests).map(observation),
      next: num(v.next_before),
      dropped: num(v.dropped_this_run),
      failedWrites: num(v.failed_writes_this_run),
    };
  },
  async request(id: string, signal?: AbortSignal) {
    return observation(
      await apiClient.get<Wire>(`/native/diagnostics/requests/${encodeURIComponent(id)}`, {
        signal,
      })
    );
  },
  async catalog(signal?: AbortSignal): Promise<CatalogStatus> {
    const v = await apiClient.get<Wire>('/native/pricing/catalog', { signal }),
      s = obj(v.settings);
    return {
      settings: {
        source: str(s.source),
        automatic: s.automatic === true,
        intervalHours: num(s.interval_hours),
      },
      models: num(v.models),
      updated: num(v.updated_ms),
      hash: str(v.hash),
      skipped: num(v.skipped),
      lastAttempt: num(v.last_attempt_ms),
      lastError: str(v.last_error),
      syncing: v.syncing === true,
    };
  },
  setCatalog: (s: CatalogSettings) =>
    apiClient.put('/native/pricing/catalog', {
      source: s.source,
      automatic: s.automatic,
      interval_hours: s.intervalHours,
    }),
  syncCatalog: (signal: AbortSignal) =>
    apiClient.post('/native/pricing/catalog/sync', {}, { signal, timeout: 0 }),
};

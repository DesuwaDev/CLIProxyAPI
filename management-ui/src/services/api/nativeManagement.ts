import { apiClient } from './client';

export type NativeModuleName =
  | 'history'
  | 'pricing'
  | 'accounts'
  | 'diagnostics'
  | 'limits'
  | 'fingerprint'
  | 'headers'
  | 'wire'
  | 'risk'
  | 'inventory';
export type NativeStatus = {
  modules: { name: NativeModuleName; enabled: boolean }[];
  written: number;
  failedWrites: number;
  maintenanceFailures: number;
  lastWrite: number;
  retentionDays: number;
  diagnosticDropped: number;
  diagnosticFailed: number;
};
export type UsageAggregate = {
  group: string;
  attempts: number;
  failures: number;
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
  reasoning: number;
  total: number;
  cost: number;
  unpriced: number;
  latency: number;
};
export type RequestEvent = {
  id: string;
  sequence: number;
  requestId: string;
  traceId: string;
  upstreamRequestId: string;
  reasoningEffort: string;
  requestedTier: string;
  responseTier: string;
  pricingRule: string;
  timestamp: number;
  provider: string;
  model: string;
  alias: string;
  account: string;
  keyHash: string;
  endpoint: string;
  tier: string;
  stream: boolean;
  failed: boolean;
  statusCode: number;
  failureCode: string;
  total: number;
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
  reasoning: number;
  cost: number | null;
  latency: number;
  ttft: number;
  observedTTFT: number | null;
  latencyObserved: boolean;
  quality: string;
};
export type AccountSnapshot = {
  account: string;
  provider: string;
  label: string;
  state: string;
  disabled: boolean;
  quotaExceeded: boolean;
  nextRetry: number;
  quotaObserved: number;
  checkedAt: number;
};
export type AccountAction = {
  account: string;
  code: string;
  firstSeen: number;
  lastSeen: number;
  hits: number;
  status: 'pending' | 'ignored' | 'resolved';
};
export type PriceRule = {
  id: string;
  provider: string;
  model: string;
  tier: string;
  minContext: number;
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
};
export type HistoryFilter = {
  from: number;
  to: number;
  model?: string;
  provider?: string;
  account?: string;
  keyHash?: string;
  requestId?: string;
  failed?: string;
  before?: number;
  limit?: number;
  cursor?: string;
  sort?: 'time' | 'recent' | 'latency';
  minLatency?: number;
  maxLatency?: number;
};
type Wire = Record<string, unknown>;
const object = (value: unknown): Wire =>
  value && typeof value === 'object' ? (value as Wire) : {};
const list = (value: unknown): Wire[] => (Array.isArray(value) ? value.map(object) : []);
const str = (value: unknown) => (typeof value === 'string' ? value : '');
const num = (value: unknown) => (typeof value === 'number' && Number.isFinite(value) ? value : 0);
const base = '/native';
const params = (f: HistoryFilter) => ({
  from: f.from,
  to: f.to,
  model: f.model || undefined,
  provider: f.provider || undefined,
  account: f.account || undefined,
  key_hash: f.keyHash || undefined,
  request_id: f.requestId || undefined,
  failed: f.failed || undefined,
  before: f.before || undefined,
  limit: f.limit || undefined,
  cursor: f.cursor || undefined,
  sort: f.sort,
  min_latency: f.minLatency,
  max_latency: f.maxLatency,
});
export const normalizeAggregate = (v: Wire): UsageAggregate => ({
  group: str(v.group),
  attempts: num(v.attempts),
  failures: num(v.failures),
  input: num(v.input_tokens),
  output: num(v.output_tokens),
  cacheRead: num(v.cache_read_tokens),
  cacheWrite: num(v.cache_write_tokens),
  reasoning: num(v.reasoning_tokens),
  total: num(v.total_tokens),
  cost: num(v.estimated_cost_usd),
  unpriced: num(v.unpriced_attempts),
  latency: num(v.average_latency_ms),
});
const snapshot = (v: Wire): AccountSnapshot => ({
  account: str(v.account),
  provider: str(v.provider),
  label: str(v.label),
  state: str(v.state),
  disabled: v.disabled === true,
  quotaExceeded: v.quota_exceeded === true,
  nextRetry: num(v.next_retry_ms),
  quotaObserved: num(v.quota_observed_ms),
  checkedAt: num(v.checked_at_ms),
});

export const nativeManagementApi = {
  async status(signal?: AbortSignal): Promise<NativeStatus> {
    const v = await apiClient.get<Wire>(`${base}/status`, { signal });
    const modules = list(v.modules).flatMap((m) =>
      [
        'history',
        'pricing',
        'accounts',
        'diagnostics',
        'limits',
        'fingerprint',
        'headers',
        'wire',
        'risk',
        'inventory',
      ].includes(str(m.name))
        ? [{ name: str(m.name) as NativeModuleName, enabled: m.enabled === true }]
        : []
    );
    return {
      modules,
      written: num(v.written_this_run),
      failedWrites: num(v.failed_writes_this_run),
      maintenanceFailures: num(v.maintenance_failures_this_run),
      lastWrite: num(v.last_write_ms),
      retentionDays: num(v.retention_days),
      diagnosticDropped: num(v.diagnostic_dropped_this_run),
      diagnosticFailed: num(v.diagnostic_failed_writes_this_run),
    };
  },
  setModule: (name: NativeModuleName, enabled: boolean) =>
    apiClient.put(`${base}/modules/${name}`, { enabled }),
  async summary(filter: HistoryFilter, group = '', signal?: AbortSignal) {
    const v = await apiClient.get<Wire>(`${base}/history/summary`, {
      params: { ...params(filter), group },
      signal,
    });
    return { groups: list(v.groups).map(normalizeAggregate), truncated: v.truncated === true };
  },
  async events(filter: HistoryFilter, signal?: AbortSignal) {
    const v = await apiClient.get<Wire>(`${base}/history/events`, {
      params: params(filter),
      signal,
    });
    const events: RequestEvent[] = list(v.events).map(normalizeEvent);
    return { events, next: num(v.next_before), cursor: str(v.next_cursor) };
  },
  async trace(id: string, signal?: AbortSignal) {
    const v = await apiClient.get<Wire>(`${base}/history/trace/${encodeURIComponent(id)}`, {
      signal,
    });
    return { events: list(v.events).map(normalizeEvent), truncated: v.truncated === true };
  },
  async models(filter: HistoryFilter, signal?: AbortSignal) {
    const v = await apiClient.get<Wire>(`${base}/history/models`, {
      params: params(filter),
      signal,
    });
    return {
      models: Array.isArray(v.models)
        ? v.models.filter((m): m is string => typeof m === 'string')
        : [],
      truncated: v.truncated === true,
    };
  },
  aliases: (signal?: AbortSignal) =>
    apiClient.get<Record<string, string>>(`${base}/history/aliases`, { signal }),
  setAlias: (keyHash: string, label: string) =>
    apiClient.put(`${base}/history/aliases`, { key_hash: keyHash, label }),
  async export(
    filter: HistoryFilter,
    signal?: AbortSignal,
    progress?: (count: number) => void
  ): Promise<Blob> {
    const chunks: string[] = [];
    let before = 0,
      cursor = '',
      count = 0,
      size = 0;
    for (let page = 0; page < 100; page++) {
      signal?.throwIfAborted();
      const response = await apiClient.getRaw(`${base}/history/export`, {
        params: params({ ...filter, before, cursor, limit: 500 }),
        responseType: 'text',
        signal,
      });
      const text = str(response.data);
      size += new TextEncoder().encode(text).byteLength;
      if (size > 64 * 1024 * 1024) throw new Error('EXPORT_LIMIT');
      chunks.push(text);
      count += text.split('\n').filter(Boolean).length;
      progress?.(count);
      cursor = str(response.headers['x-next-cursor']);
      before = cursor ? 0 : Number(response.headers['x-next-before'] || 0);
      if (!before && !cursor) {
        signal?.throwIfAborted();
        return new Blob(chunks, { type: 'application/x-ndjson' });
      }
    }
    throw new Error('EXPORT_LIMIT');
  },
  async accounts(signal?: AbortSignal) {
    const v = await apiClient.get<Wire>(`${base}/accounts`, { signal });
    return list(v.accounts).map(snapshot);
  },
  inspect: () => apiClient.post(`${base}/accounts/inspect`),
  async accountHistory(account: string, signal?: AbortSignal) {
    const v = await apiClient.get<Wire>(`${base}/accounts/history`, {
      params: { account },
      signal,
    });
    return list(v.snapshots).map(snapshot);
  },
  async actions(signal?: AbortSignal) {
    const v = await apiClient.get<Wire>(`${base}/accounts/actions`, { signal });
    return {
      truncated: v.truncated === true,
      actions: list(v.actions).map((a) => ({
        account: str(a.account),
        code: str(a.code),
        firstSeen: num(a.first_seen_ms),
        lastSeen: num(a.last_seen_ms),
        hits: num(a.hits),
        status: str(a.status) as AccountAction['status'],
      })),
    };
  },
  setAction: (a: AccountAction, status: AccountAction['status']) =>
    apiClient.put(`${base}/accounts/actions`, { account: a.account, code: a.code, status }),
  async prices(signal?: AbortSignal): Promise<PriceRule[]> {
    const v = await apiClient.get<Wire>(`${base}/pricing`, { signal });
    return list(v.rules).map((r) => ({
      id: str(r.id),
      provider: str(r.provider),
      model: str(r.model),
      tier: str(r.tier),
      minContext: num(r.min_context),
      input: num(r.input_per_million),
      output: num(r.output_per_million),
      cacheRead: num(r.cache_read_per_million),
      cacheWrite: num(r.cache_write_per_million),
    }));
  },
  setPrices: (rules: PriceRule[]) =>
    apiClient.put(`${base}/pricing`, {
      rules: rules.map((r) => ({
        id: r.id,
        provider: r.provider,
        model: r.model,
        tier: r.tier,
        min_context: r.minContext,
        input_per_million: r.input,
        output_per_million: r.output,
        cache_read_per_million: r.cacheRead,
        cache_write_per_million: r.cacheWrite,
      })),
    }),
};

export function normalizeEvent(e: Wire): RequestEvent {
  const tokens = object(e.tokens),
    input = object(tokens.input),
    output = object(tokens.output);
  return {
    id: str(e.id),
    sequence: num(e.sequence),
    requestId: str(e.request_id),
    traceId: str(e.trace_id),
    upstreamRequestId: str(e.upstream_request_id),
    reasoningEffort: str(e.reasoning_effort),
    requestedTier: str(e.requested_tier),
    responseTier: str(e.response_tier),
    pricingRule: str(e.pricing_rule),
    timestamp: num(e.timestamp_ms),
    provider: str(e.provider),
    model: str(e.model),
    alias: str(e.alias),
    account: str(e.account),
    keyHash: str(e.key_hash),
    endpoint: str(e.endpoint),
    tier: str(e.service_tier),
    stream: e.stream === true,
    failed: e.failed === true,
    statusCode: num(e.status_code),
    failureCode: str(e.failure_code),
    total: num(tokens.total_tokens),
    input: num(input.total_tokens),
    output: num(output.total_tokens),
    cacheRead: num(input.cache_read_tokens),
    cacheWrite: num(input.cache_write_tokens),
    reasoning: num(output.reasoning_tokens),
    cost: e.cost_usd == null ? null : num(e.cost_usd),
    latency: num(e.latency_ms),
    ttft: num(e.ttft_ms),
    observedTTFT: e.ttft_observed_ms == null ? null : num(e.ttft_observed_ms),
    latencyObserved: e.latency_observed === true,
    quality: str(tokens.quality),
  };
}

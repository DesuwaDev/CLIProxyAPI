import { apiClient } from './client';

export type RiskMode = 'off' | 'observe' | 'block';
export type RiskRule = { id: string; pattern: string; regex: boolean; enabled: boolean };
export type RiskEndpoint = {
  id: string;
  name: string;
  url: string;
  model: string;
  protocol: 'guard_json' | 'qwen3guard' | 'moderations';
  enabled: boolean;
  token?: string;
  clearToken?: boolean;
  hasToken: boolean;
};
export type RiskConfig = {
  version: number;
  mode: RiskMode;
  strategy: 'keywords' | 'api' | 'both';
  providers: string[];
  modelFilter: 'all' | 'include' | 'exclude';
  models: string[];
  latestTurnOnly: boolean;
  failClosed: boolean;
  recordPass: boolean;
  concurrency: number;
  chunkSize: number;
  maxInputChars: number;
  hashBlock: boolean;
  hashTTL: number;
  sessionBlock: boolean;
  sessionTTL: number;
  rules: RiskRule[];
  endpoints: RiskEndpoint[];
  thresholds: Record<string, number>;
};
export type RiskResult = {
  decision: string;
  source: string;
  category: string;
  scores: Record<string, number>;
  ruleId: string;
  endpointId: string;
  errorCode: string;
  latency: number;
  chunks: number;
};
export type RiskEvent = {
  id: string;
  timestamp: number;
  traceId: string;
  account: string;
  keyHash: string;
  sessionHash: string;
  inputHash: string;
  inputChars: number;
  model: string;
  provider: string;
  mode: RiskMode;
  configVersion: number;
  blocked: boolean;
  result: RiskResult;
};
export type RiskBlock = { kind: string; target: string; expires: number; reason: string };
export type EndpointStatus = {
  calls: number;
  success: number;
  errors: number;
  active: number;
  lastLatency: number;
  lastStatus: number;
  lastError: string;
  checkedAt: number;
};
type Raw = Record<string, unknown>;
const obj = (v: unknown): Raw => (v && typeof v === 'object' ? (v as Raw) : {});
const n = (v: unknown) => (typeof v === 'number' && Number.isFinite(v) ? v : 0);
const s = (v: unknown) => (typeof v === 'string' ? v : '');
const strings = (v: unknown) => (Array.isArray(v) ? v.map(s) : []);
const list = (v: unknown) => (Array.isArray(v) ? v : []);
const scores = (v: unknown) =>
  Object.fromEntries(Object.entries(obj(v)).map(([k, value]) => [k, n(value)]));
function config(v: unknown): RiskConfig {
  const d = obj(v);
  return {
    version: n(d.version),
    mode: s(d.mode) as RiskMode,
    strategy: s(d.strategy) as RiskConfig['strategy'],
    providers: strings(d.providers),
    modelFilter: s(d.model_filter) as RiskConfig['modelFilter'],
    models: strings(d.models),
    latestTurnOnly: d.latest_turn_only === true,
    failClosed: d.fail_closed === true,
    recordPass: d.record_pass === true,
    concurrency: n(d.concurrency),
    chunkSize: n(d.chunk_size),
    maxInputChars: n(d.max_input_chars),
    hashBlock: d.hash_block === true,
    hashTTL: n(d.hash_ttl_seconds),
    sessionBlock: d.session_block === true,
    sessionTTL: n(d.session_ttl_seconds),
    thresholds: scores(d.thresholds),
    rules: list(d.rules).map((v) => {
      const r = obj(v);
      return {
        id: s(r.id),
        pattern: s(r.pattern),
        regex: r.regex === true,
        enabled: r.enabled === true,
      };
    }),
    endpoints: list(d.endpoints).map((v) => {
      const e = obj(v);
      return {
        id: s(e.id),
        name: s(e.name),
        url: s(e.url),
        model: s(e.model),
        protocol: s(e.protocol) as RiskEndpoint['protocol'],
        enabled: e.enabled === true,
        hasToken: e.has_token === true,
      };
    }),
  };
}
function payload(c: RiskConfig) {
  return {
    version: c.version,
    mode: c.mode,
    strategy: c.strategy,
    providers: c.providers,
    model_filter: c.modelFilter,
    models: c.models,
    latest_turn_only: c.latestTurnOnly,
    fail_closed: c.failClosed,
    record_pass: c.recordPass,
    concurrency: c.concurrency,
    chunk_size: c.chunkSize,
    max_input_chars: c.maxInputChars,
    hash_block: c.hashBlock,
    hash_ttl_seconds: c.hashTTL,
    session_block: c.sessionBlock,
    session_ttl_seconds: c.sessionTTL,
    rules: c.rules,
    thresholds: c.thresholds,
    endpoints: c.endpoints.map((e) => ({
      id: e.id,
      name: e.name,
      url: e.url,
      model: e.model,
      protocol: e.protocol,
      enabled: e.enabled,
      token: e.token || '',
      clear_token: e.clearToken || false,
      has_token: e.hasToken,
    })),
  };
}
function result(v: unknown): RiskResult {
  const d = obj(v);
  return {
    decision: s(d.decision),
    source: s(d.source),
    category: s(d.category),
    scores: scores(d.scores),
    ruleId: s(d.rule_id),
    endpointId: s(d.endpoint_id),
    errorCode: s(d.error_code),
    latency: n(d.latency_ms),
    chunks: n(d.chunks),
  };
}
function event(v: unknown): RiskEvent {
  const d = obj(v);
  return {
    id: s(d.id),
    timestamp: n(d.timestamp_ms),
    traceId: s(d.trace_id),
    account: s(d.account),
    keyHash: s(d.key_hash),
    sessionHash: s(d.session_hash),
    inputHash: s(d.input_hash),
    inputChars: n(d.input_chars),
    model: s(d.model),
    provider: s(d.provider),
    mode: s(d.mode) as RiskMode,
    configVersion: n(d.config_version),
    blocked: d.blocked === true,
    result: result(d.result),
  };
}
const base = '/native/risk';
export const nativeRiskApi = {
  config: async (signal?: AbortSignal) => config(await apiClient.get(base + '/config', { signal })),
  save: async (c: RiskConfig, signal?: AbortSignal) =>
    config(await apiClient.put(base + '/config', payload(c), { signal })),
  status: async (signal?: AbortSignal) => {
    const d = obj(await apiClient.get(base + '/status', { signal }));
    return {
      mode: s(d.mode),
      active: n(d.active),
      queued: n(d.queued),
      capacity: n(d.queue_capacity),
      dropped: n(d.dropped),
      failedWrites: n(d.failed_writes),
      counts: scores(d.last_24h),
      blocked: n(d.blocked_24h),
      endpoints: Object.fromEntries(
        Object.entries(obj(d.endpoints)).map(([key, v]) => {
          const e = obj(v);
          return [
            key,
            {
              calls: n(e.calls),
              success: n(e.success),
              errors: n(e.errors),
              active: n(e.active),
              lastLatency: n(e.last_latency_ms),
              lastStatus: n(e.last_status),
              lastError: s(e.last_error),
              checkedAt: n(e.checked_at_ms),
            } satisfies EndpointStatus,
          ];
        })
      ),
    };
  },
  events: async (decision: string, before: number, signal?: AbortSignal) => {
    const d = obj(
      await apiClient.get(base + '/events', {
        params: { decision, before: before || undefined },
        signal,
      })
    );
    return { events: list(d.events).map(event), next: n(d.next) };
  },
  blocks: async (signal?: AbortSignal) => {
    const d = obj(await apiClient.get(base + '/blocks', { signal }));
    return {
      blocks: list(d.blocks).map((v) => {
        const b = obj(v);
        return {
          kind: s(b.kind),
          target: s(b.target),
          expires: n(b.expires_ms),
          reason: s(b.reason),
        };
      }),
      truncated: d.truncated === true,
    };
  },
  unblock: (b: RiskBlock, signal?: AbortSignal) =>
    apiClient.delete(
      base + '/blocks/' + encodeURIComponent(b.kind) + '/' + encodeURIComponent(b.target),
      { signal }
    ),
  test: async (text: string, endpointId: string, signal?: AbortSignal) =>
    result(await apiClient.post(base + '/test', { text, endpoint_id: endpointId }, { signal })),
};

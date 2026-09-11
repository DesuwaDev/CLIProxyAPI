import { apiClient } from './client';
import type { ControlScope } from './nativeControls';
export type InventoryProfile = {
  name: string;
  notes: string;
  tags: string[];
  disabled: boolean;
  expires: number;
  models: string[];
  budget: number | null;
  version: number;
};
export type InventoryTotals = {
  requests: number;
  failures: number;
  tokens: number;
  cost: number;
  unpriced: number;
  first: number;
  last: number;
};
export type InventoryWindow = {
  id: string;
  usedPercent: number;
  start: number;
  reset: number;
  observed: number;
};
export type InventoryItem = {
  scope: ControlScope;
  target: string;
  profile: InventoryProfile;
  totals: InventoryTotals;
  account: { fileName: string; label: string; provider: string; windows: InventoryWindow[] } | null;
};
export type InventoryForecast = {
  window: InventoryWindow;
  usage: InventoryTotals;
  estimatedTotal: number | null;
  estimatedRemaining: number | null;
  coverage: string;
  reason: string;
};
type Raw = Record<string, unknown>;
const obj = (v: unknown): Raw => (v && typeof v === 'object' ? (v as Raw) : {});
const n = (v: unknown) => (typeof v === 'number' && Number.isFinite(v) ? v : 0);
const str = (v: unknown) => (typeof v === 'string' ? v : '');
const arr = (v: unknown): unknown[] => (Array.isArray(v) ? v : []);
const nullable = (v: unknown) => (typeof v === 'number' && Number.isFinite(v) ? v : null);
function profile(v: unknown): InventoryProfile {
  const d = obj(v);
  return {
    name: str(d.name),
    notes: str(d.notes),
    tags: arr(d.tags).map(str),
    disabled: d.disabled === true,
    expires: n(d.expires_ms),
    models: arr(d.models).map(str),
    budget: nullable(d.budget_usd),
    version: n(d.version),
  };
}
function totals(v: unknown): InventoryTotals {
  const d = obj(v);
  return {
    requests: n(d.requests),
    failures: n(d.failures),
    tokens: n(d.tokens),
    cost: n(d.cost_usd),
    unpriced: n(d.unpriced),
    first: n(d.first_ms),
    last: n(d.last_ms),
  };
}
function window(v: unknown): InventoryWindow {
  const d = obj(v);
  return {
    id: str(d.id),
    usedPercent: n(d.used_percent),
    start: n(d.start_ms),
    reset: n(d.reset_ms),
    observed: n(d.observed_ms),
  };
}
function item(v: unknown): InventoryItem {
  const d = obj(v),
    a = obj(d.account);
  return {
    scope: str(d.scope) as ControlScope,
    target: str(d.target),
    profile: profile(d.profile),
    totals: totals(d.totals),
    account: d.account
      ? {
          fileName: str(a.file_name),
          label: str(a.label),
          provider: str(a.provider),
          windows: arr(a.windows).map(window),
        }
      : null,
  };
}
function forecast(v: unknown): InventoryForecast {
  const d = obj(v);
  return {
    window: window(d.window),
    usage: totals(d.usage),
    estimatedTotal: nullable(d.estimated_total_usd),
    estimatedRemaining: nullable(d.estimated_remaining_usd),
    coverage: str(d.coverage),
    reason: str(d.reason),
  };
}
const path = (scope: ControlScope, target: string) =>
  '/native/inventory/' + scope + '/' + encodeURIComponent(target);
export const nativeInventoryApi = {
  list: async (signal?: AbortSignal) => {
    const d = obj(await apiClient.get('/native/inventory', { signal }));
    return { items: arr(d.items).map(item), truncated: d.truncated === true };
  },
  get: async (scope: ControlScope, target: string, signal?: AbortSignal) => {
    const d = obj(await apiClient.get(path(scope, target), { signal }));
    return { item: item(d.item), forecasts: arr(d.forecasts).map(forecast) };
  },
  save: async (scope: ControlScope, target: string, p: InventoryProfile, signal?: AbortSignal) =>
    profile(
      await apiClient.put(
        path(scope, target),
        {
          name: p.name,
          notes: p.notes,
          tags: p.tags.map((v) => v.trim()).filter(Boolean),
          disabled: p.disabled,
          expires_ms: p.expires,
          models: p.models.map((v) => v.trim()).filter(Boolean),
          budget_usd: p.budget,
          version: p.version,
        },
        { signal }
      )
    ),
  windows: (target: string, windows: InventoryWindow[], signal?: AbortSignal) =>
    apiClient.put(
      '/native/inventory/windows/' + encodeURIComponent(target),
      {
        windows: windows.map((w) => ({
          id: w.id,
          used_percent: w.usedPercent,
          start_ms: w.start,
          reset_ms: w.reset,
          observed_ms: w.observed,
        })),
      },
      { signal }
    ),
};

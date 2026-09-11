import { afterEach, expect, test } from 'bun:test';
import { apiClient } from '../src/services/api/client';
import { nativeManagementApi as api } from '../src/services/api/nativeManagement';
import { historyWorkbook } from '../src/features/nativeManagement/exportHistory';

const originalGet = apiClient.get;
const originalRaw = apiClient.getRaw;
afterEach(() => {
  apiClient.get = originalGet;
  apiClient.getRaw = originalRaw;
});

test('native Excel export preserves unknown cells and treats untrusted names as strings; cancellation yields no workbook', async () => {
  const jsonl = new Blob([
    JSON.stringify({
      timestamp_ms: 1000,
      model: '=SUM(1,2)<&',
      cost_usd: null,
      ttft_observed_ms: 0,
      latency_observed: false,
      tokens: { total_tokens: 1 },
    }) + '\n',
  ]);
  const controller = new AbortController();
  const blob = await historyWorkbook(jsonl, controller.signal);
  const bytes = new Uint8Array(await blob.arrayBuffer());
  expect([...bytes.slice(0, 4)]).toEqual([80, 75, 3, 4]);
  const text = new TextDecoder().decode(bytes);
  expect(text).toContain('t="inlineStr"><is><t xml:space="preserve">=SUM(1,2)&lt;&amp;');
  expect(text).not.toContain('<f>');
  expect(text).not.toContain('r="R2"');
  expect(text).not.toContain('r="S2"');
  expect(text).toContain('<c r="T2"><v>0</v></c>');
  controller.abort();
  await expect(historyWorkbook(jsonl, controller.signal)).rejects.toThrow();
});

test('native history preserves canonical totals and distinguishes unknown from explicitly free costs', async () => {
  apiClient.get = (async () => ({
    events: [null, 0, 0.0054].map((cost_usd, sequence) => ({
      id: String(sequence),
      sequence,
      cost_usd,
      tokens: {
        total_tokens: 1100,
        quality: 'complete',
        input: { total_tokens: 1000, cache_read_tokens: 200, cache_write_tokens: 0 },
        output: { total_tokens: 100, reasoning_tokens: 20 },
      },
    })),
    next_before: 17,
  })) as typeof apiClient.get;
  const result = await api.events({ from: 1, to: 100 });
  expect(result.next).toBe(17);
  expect(result.events.map((e) => e.cost)).toEqual([null, 0, 0.0054]);
  expect(result.events[0]).toMatchObject({
    total: 1100,
    input: 1000,
    output: 100,
    cacheRead: 200,
    reasoning: 20,
  });
});

test('native export follows the exclusive cursor with a fixed range and shared authenticated client', async () => {
  const requests: unknown[] = [];
  apiClient.getRaw = (async (url, config) => {
    requests.push({ url, params: config?.params });
    return {
      data: requests.length === 1 ? 'first\n' : 'second\n',
      headers: requests.length === 1 ? { 'x-next-before': '42' } : {},
    };
  }) as typeof apiClient.getRaw;
  expect(await (await api.export({ from: 100, to: 200, model: 'smoke', before: 99 })).text()).toBe(
    'first\nsecond\n'
  );
  expect(requests).toMatchObject([
    {
      url: '/native/history/export',
      params: { from: 100, to: 200, model: 'smoke', before: undefined, limit: 500 },
    },
    {
      url: '/native/history/export',
      params: { from: 100, to: 200, model: 'smoke', before: 42, limit: 500 },
    },
  ]);
});

test('native module discovery exposes optional controls while rejecting unknown modules', async () => {
  apiClient.get = (async () => ({
    modules: [
      { name: 'risk', enabled: true },
      { name: 'inventory', enabled: false },
      { name: 'headers', enabled: true },
      { name: 'unknown', enabled: true },
    ],
  })) as typeof apiClient.get;
  expect((await api.status()).modules).toEqual([
    { name: 'risk', enabled: true },
    { name: 'inventory', enabled: false },
    { name: 'headers', enabled: true },
  ]);
});

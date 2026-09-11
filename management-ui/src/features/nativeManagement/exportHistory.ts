import {
  nativeManagementApi,
  normalizeEvent,
  type HistoryFilter,
} from '@/services/api/nativeManagement';

const encoder = new TextEncoder();
const limit = 64 * 1024 * 1024;
const crcTable = Uint32Array.from({ length: 256 }, (_, i) => {
  let n = i;
  for (let bit = 0; bit < 8; bit++) n = n & 1 ? 0xedb88320 ^ (n >>> 1) : n >>> 1;
  return n >>> 0;
});
const crc32 = (data: Uint8Array) => {
  let crc = 0xffffffff;
  for (const byte of data) crc = crcTable[(crc ^ byte) & 255] ^ (crc >>> 8);
  return (crc ^ 0xffffffff) >>> 0;
};

// Minimal ZIP STORE package: no compression worker, external library or formula cells.
export function workbookArchive(files: Record<string, string>): Blob {
  const chunks: Uint8Array<ArrayBuffer>[] = [],
    directory: Uint8Array<ArrayBuffer>[] = [];
  let offset = 0,
    directorySize = 0;
  for (const [path, text] of Object.entries(files)) {
    const name = encoder.encode(path),
      data = encoder.encode(text),
      crc = crc32(data);
    if (offset + data.length + name.length + 30 > limit) throw new Error('EXPORT_LIMIT');
    const local = new Uint8Array(30 + name.length),
      view = new DataView(local.buffer);
    view.setUint32(0, 0x04034b50, true);
    view.setUint16(4, 20, true);
    view.setUint16(6, 0x800, true);
    view.setUint16(12, 33, true); // ZIP date: 1980-01-01.
    view.setUint32(14, crc, true);
    view.setUint32(18, data.length, true);
    view.setUint32(22, data.length, true);
    view.setUint16(26, name.length, true);
    local.set(name, 30);
    const central = new Uint8Array(46 + name.length),
      cv = new DataView(central.buffer);
    cv.setUint32(0, 0x02014b50, true);
    cv.setUint16(4, 20, true);
    cv.setUint16(6, 20, true);
    cv.setUint16(8, 0x800, true);
    cv.setUint16(14, 33, true);
    cv.setUint32(16, crc, true);
    cv.setUint32(20, data.length, true);
    cv.setUint32(24, data.length, true);
    cv.setUint16(28, name.length, true);
    cv.setUint32(42, offset, true);
    central.set(name, 46);
    chunks.push(local, data);
    directory.push(central);
    directorySize += central.length;
    offset += local.length + data.length;
  }
  if (offset + directorySize + 22 > limit) throw new Error('EXPORT_LIMIT');
  const end = new Uint8Array(22),
    ev = new DataView(end.buffer);
  ev.setUint32(0, 0x06054b50, true);
  ev.setUint16(8, directory.length, true);
  ev.setUint16(10, directory.length, true);
  ev.setUint32(12, directorySize, true);
  ev.setUint32(16, offset, true);
  return new Blob([...chunks, ...directory, end], {
    type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
  });
}
// XML 1.0 forbids these controls, even when encoded as character references.
const xml = (value: string) =>
  value
    // eslint-disable-next-line no-control-regex
    .replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f]/g, '')
    .replace(
      /[&<>"']/g,
      (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&apos;' })[c]!
    );
const column = (index: number): string =>
  index < 26
    ? String.fromCharCode(65 + index)
    : column(Math.floor(index / 26) - 1) + column(index % 26);

export async function historyWorkbook(jsonl: Blob, signal: AbortSignal): Promise<Blob> {
  const columns = [
    'timestamp_utc',
    'request_id',
    'trace_id',
    'provider',
    'model',
    'alias',
    'account',
    'key_hash',
    'endpoint',
    'status',
    'failure_code',
    'input_tokens',
    'output_tokens',
    'cache_read_tokens',
    'cache_write_tokens',
    'reasoning_tokens',
    'total_tokens',
    'estimated_cost_usd',
    'duration_ms',
    'ttft_ms',
    'requested_tier',
    'response_tier',
    'reasoning_effort',
    'upstream_request_id',
    'pricing_rule',
    'token_quality',
  ];
  const rows: string[] = [];
  let bytes = 0;
  const add = (cells: (string | number | null)[]) => {
    const n = rows.length + 1;
    const row =
      `<row r="${n}">` +
      cells
        .map((v, i) =>
          v === null
            ? ''
            : typeof v === 'number'
              ? `<c r="${column(i)}${n}"><v>${v}</v></c>`
              : `<c r="${column(i)}${n}" t="inlineStr"><is><t xml:space="preserve">${xml(v)}</t></is></c>`
        )
        .join('') +
      '</row>';
    bytes += encoder.encode(row).length;
    if (bytes > limit - 8192) throw new Error('EXPORT_LIMIT');
    rows.push(row);
  };
  add(columns);
  const lines = (await jsonl.text()).split('\n').filter(Boolean);
  if (lines.length > 50000) throw new Error('EXPORT_LIMIT');
  for (let i = 0; i < lines.length; i++) {
    if (i % 250 === 0) {
      await new Promise<void>((resolve) => setTimeout(resolve, 0));
      signal.throwIfAborted();
    }
    const e = normalizeEvent(JSON.parse(lines[i]));
    add([
      new Date(e.timestamp).toISOString(),
      e.requestId,
      e.traceId,
      e.provider,
      e.model,
      e.alias,
      e.account,
      e.keyHash,
      e.endpoint,
      e.failed ? 'failed' : 'success',
      e.failureCode,
      e.input,
      e.output,
      e.cacheRead,
      e.cacheWrite,
      e.reasoning,
      e.total,
      e.cost,
      e.latencyObserved ? e.latency : null,
      e.observedTTFT,
      e.requestedTier,
      e.responseTier,
      e.reasoningEffort,
      e.upstreamRequestId,
      e.pricingRule,
      e.quality,
    ]);
  }
  signal.throwIfAborted();
  const ns = 'http://schemas.openxmlformats.org/spreadsheetml/2006/main';
  return workbookArchive({
    '[Content_Types].xml':
      '<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>',
    '_rels/.rels':
      '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>',
    'xl/workbook.xml': `<workbook xmlns="${ns}" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="CPA requests" sheetId="1" r:id="rId1"/></sheets></workbook>`,
    'xl/_rels/workbook.xml.rels':
      '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>',
    'xl/worksheets/sheet1.xml': `<worksheet xmlns="${ns}"><sheetViews><sheetView workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/></sheetView></sheetViews><sheetData>${rows.join('')}</sheetData><autoFilter ref="A1:${column(columns.length - 1)}${rows.length}"/></worksheet>`,
  });
}
export async function exportHistory(
  filter: HistoryFilter,
  format: 'jsonl' | 'xlsx',
  signal: AbortSignal,
  progress: (count: number) => void
) {
  const jsonl = await nativeManagementApi.export(filter, signal, progress);
  signal.throwIfAborted();
  return format === 'xlsx' ? historyWorkbook(jsonl, signal) : jsonl;
}

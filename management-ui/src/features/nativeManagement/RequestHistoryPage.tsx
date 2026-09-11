import { useSearchParams } from 'react-router-dom';
import { useInventory } from './inventoryContext';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { number, date, short, money } from './format';
import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import {
  nativeManagementApi as api,
  type HistoryFilter,
  type RequestEvent,
} from '@/services/api/nativeManagement';
import { useNativeQuery } from './useNativeQuery';
import { DataTable, FeatureGate, Page, QueryState, RangeControl } from './components';
import styles from './NativeManagement.module.scss';
import { exportHistory } from './exportHistory';
import { CopyValue, TracePanel } from './TracePanel';

const columnKeys = [
  'time',
  'model',
  'account',
  'key',
  'status',
  'tokens',
  'estimated_cost',
  'duration',
  'ttft',
  'provider',
  'requestId',
  'requestedTier',
  'responseTier',
  'reasoningEffort',
] as const;
type Column = (typeof columnKeys)[number];
function savedColumns(): Column[] {
  try {
    const value: unknown = JSON.parse(
      localStorage.getItem('cpa.native.history.columns.v1') || 'null'
    );
    if (Array.isArray(value)) {
      const valid = columnKeys.filter((k) => value.includes(k));
      if (valid.length) return valid;
    }
  } catch {
    /* Preferences must not prevent the page from loading. */
  }
  return columnKeys.slice(0, 8);
}

export function RequestHistoryPage() {
  return (
    <FeatureGate module="history">
      <HistoryContent />
    </FeatureGate>
  );
}
function HistoryContent() {
  const { t } = useTranslation();
  const [searchParams] = useSearchParams();
  const inventory = useInventory();
  const accountName = (id: string) => {
    const item = inventory.items.find((v) => v.scope === 'credential' && v.target === id);
    return item?.profile.name || item?.account?.fileName || item?.account?.label || short(id);
  };
  const queryDate = (key: string) => {
    const ms = Number(searchParams.get(key));
    return ms > 0 && Number.isFinite(ms) && ms < 253402300799000
      ? new Date(ms - new Date(ms).getTimezoneOffset() * 60000).toISOString().slice(0, 16)
      : '';
  };
  const [hours, setHours] = useState(24),
    [to, setTo] = useState(() => Date.now() + 1),
    [before, setBefore] = useState<string[]>([]);
  const [draft, setDraft] = useState({
      model: searchParams.get('model') || '',
      provider: searchParams.get('provider') || '',
      account: searchParams.get('account') || '',
      keyHash: searchParams.get('keyHash') || '',
      requestId: searchParams.get('requestId') || '',
      failed: '',
      minLatency: '',
      maxLatency: '',
      sort: 'recent' as 'recent' | 'latency',
      fromLocal: queryDate('from'),
      toLocal: queryDate('to'),
    }),
    [filters, setFilters] = useState(draft);
  const [selected, setSelected] = useState<RequestEvent | null>(null),
    [label, setLabel] = useState(''),
    [busy, setBusy] = useState(false),
    [message, setMessage] = useState('');
  const exportController = useRef<AbortController | null>(null);
  const [columns, setColumns] = useState<Column[]>(savedColumns),
    [exporting, setExporting] = useState(false),
    [progress, setProgress] = useState(0);
  useEffect(() => {
    try {
      localStorage.setItem('cpa.native.history.columns.v1', JSON.stringify(columns));
    } catch {
      /* Optional browser preference. */
    }
  }, [columns]);
  useEffect(() => () => exportController.current?.abort(), []);
  const from = filters.fromLocal ? new Date(filters.fromLocal).getTime() : to - hours * 3600000;
  const until = filters.toLocal ? new Date(filters.toLocal).getTime() : to;
  const filter: HistoryFilter = {
    ...filters,
    from,
    to: until,
    minLatency: filters.minLatency === '' ? undefined : Number(filters.minLatency),
    maxLatency: filters.maxLatency === '' ? undefined : Number(filters.maxLatency),
    cursor: before[before.length - 1],
    limit: 100,
  };
  const loader = useCallback(
    async (signal: AbortSignal) => {
      const [result, aliases, models] = await Promise.all([
        api.events(
          {
            ...filters,
            from,
            to: until,
            minLatency: filters.minLatency === '' ? undefined : Number(filters.minLatency),
            maxLatency: filters.maxLatency === '' ? undefined : Number(filters.maxLatency),
            cursor: before[before.length - 1],
            limit: 100,
          },
          signal
        ),
        api.aliases(signal),
        api.models({ from, to: until }, signal),
      ]);
      return { ...result, aliases, models };
    },
    [from, until, filters, before]
  );
  const query = useNativeQuery(loader);
  const reset = () => {
    setBefore([]);
    setTo(Date.now() + 1);
    setSelected(null);
  };
  useHeaderRefresh(reset);
  async function download(format: 'jsonl' | 'xlsx') {
    setExporting(true);
    setProgress(0);
    setMessage('');
    const controller = new AbortController();
    exportController.current = controller;
    try {
      const blob = await exportHistory(filter, format, controller.signal, setProgress);
      if (controller.signal.aborted) return;
      const url = URL.createObjectURL(blob),
        a = document.createElement('a');
      a.href = url;
      a.download = 'cpa-requests.' + format;
      a.click();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (e) {
      if (controller.signal.aborted) setMessage(t('native.cancelled'));
      else
        setMessage(
          e instanceof Error && e.message === 'EXPORT_LIMIT' ? t('native.export_limit') : String(e)
        );
    } finally {
      setExporting(false);
      exportController.current = null;
    }
  }
  async function saveAlias() {
    if (!selected) return;
    setBusy(true);
    setMessage('');
    try {
      await api.setAlias(selected.keyHash, label);
      await query.refresh();
      setMessage(t('native.saved'));
    } catch (e) {
      setMessage(String(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Page title={t('native.requests')} description={t('native.requests_hint')}>
      <RangeControl
        hours={hours}
        onChange={(v) => {
          setHours(v);
          setFilters({ ...filters, fromLocal: '', toLocal: '' });
          setDraft({ ...draft, fromLocal: '', toLocal: '' });
          reset();
        }}
        onRefresh={reset}
      />
      <Card>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (
              Boolean(draft.fromLocal) !== Boolean(draft.toLocal) ||
              (draft.fromLocal &&
                (new Date(draft.toLocal).getTime() <= new Date(draft.fromLocal).getTime() ||
                  new Date(draft.toLocal).getTime() - new Date(draft.fromLocal).getTime() >
                    366 * 86400000)) ||
              (draft.minLatency !== '' &&
                draft.maxLatency !== '' &&
                Number(draft.minLatency) > Number(draft.maxLatency))
            ) {
              setMessage(t('native.invalid_filters'));
              return;
            }
            setFilters(draft);
            setMessage('');
            reset();
          }}
        >
          <div className={styles.filters}>
            {(['model', 'provider', 'account', 'keyHash', 'requestId'] as const).map((key) => (
              <Input
                key={key}
                label={t('native.' + key)}
                list={key === 'model' ? 'native-history-models' : undefined}
                value={draft[key]}
                onChange={(e) => setDraft({ ...draft, [key]: e.target.value })}
              />
            ))}
            <datalist id="native-history-models">
              {query.data?.models.models.map((v) => (
                <option value={v} key={v} />
              ))}
            </datalist>
            {(['fromLocal', 'toLocal'] as const).map((key) => (
              <Input
                key={key}
                type="datetime-local"
                label={t('native.' + key)}
                value={draft[key]}
                onChange={(e) => setDraft({ ...draft, [key]: e.target.value })}
              />
            ))}
            {(['minLatency', 'maxLatency'] as const).map((key) => (
              <Input
                key={key}
                type="number"
                min={0}
                max={Number.MAX_SAFE_INTEGER}
                step={1}
                label={t('native.' + key)}
                value={draft[key]}
                onChange={(e) => setDraft({ ...draft, [key]: e.target.value })}
              />
            ))}
            <label>
              {t('native.sort')}
              <select
                className="input"
                value={draft.sort}
                onChange={(e) =>
                  setDraft({ ...draft, sort: e.target.value as 'recent' | 'latency' })
                }
              >
                <option value="recent">{t('native.sort_time')}</option>
                <option value="latency">{t('native.sort_latency')}</option>
              </select>
            </label>
            <label>
              {t('native.status')}
              <select
                className="input"
                value={draft.failed}
                onChange={(e) => setDraft({ ...draft, failed: e.target.value })}
              >
                <option value="">{t('native.all')}</option>
                <option value="false">{t('native.success')}</option>
                <option value="true">{t('native.failures')}</option>
              </select>
            </label>
          </div>
          <div className={styles.actions}>
            <Button type="submit" disabled={exporting}>
              {t('native.apply')}
            </Button>
            <Button
              type="button"
              variant="secondary"
              disabled={exporting || query.loading || !!query.error}
              onClick={() => void download('jsonl')}
            >
              {t('native.export')}
            </Button>
            <Button
              type="button"
              variant="secondary"
              disabled={exporting || query.loading || !!query.error}
              onClick={() => void download('xlsx')}
            >
              {t('native.export_excel')}
            </Button>
            {exporting && (
              <>
                <span role="status">{t('native.export_progress', { count: progress })}</span>
                <Button
                  type="button"
                  variant="secondary"
                  onClick={() => exportController.current?.abort()}
                >
                  {t('native.cancel')}
                </Button>
              </>
            )}
          </div>
          <p className={styles.hint}>{t('native.history_filter_hint')}</p>
          {query.data?.models.truncated && (
            <p className={styles.hint}>{t('native.models_truncated')}</p>
          )}
        </form>
      </Card>
      <QueryState {...query} />
      {message && <p role="status">{message}</p>}
      <Card>
        <details className={styles.columnPicker}>
          <summary>{t('native.columns')}</summary>
          <div className={styles.actions}>
            {columnKeys.map((key) => (
              <label key={key}>
                <input
                  type="checkbox"
                  checked={columns.includes(key)}
                  disabled={columns.length === 1 && columns.includes(key)}
                  onChange={(e) =>
                    setColumns(
                      columnKeys.filter((k) => (k === key ? e.target.checked : columns.includes(k)))
                    )
                  }
                />
                {t('native.' + key)}
              </label>
            ))}
          </div>
        </details>
        <DataTable
          headers={['', ...columns.map((k) => t('native.' + k))]}
          rows={(query.data?.events ?? []).map((e) => ({
            key: e.id,
            cells: [
              <Button
                type="button"
                size="sm"
                variant="ghost"
                onClick={() => {
                  setSelected(e);
                  setLabel(query.data?.aliases[e.keyHash] ?? '');
                  setMessage('');
                }}
              >
                {t('native.details')}
              </Button>,
              ...columns.map((k) => {
                switch (k) {
                  case 'time':
                    return date(e.timestamp);
                  case 'model':
                    return e.model;
                  case 'account':
                    return accountName(e.account);
                  case 'key':
                    return query.data?.aliases[e.keyHash] || short(e.keyHash);
                  case 'status':
                    return (
                      <span
                        className={styles.statusPill}
                        title={e.failed ? e.failureCode : undefined}
                        data-tone={e.failed ? 'danger' : 'success'}
                      >
                        {e.failed
                          ? t('common.failure') + (e.statusCode ? ' · ' + e.statusCode : '')
                          : t('native.success')}
                      </span>
                    );
                  case 'tokens':
                    return number(e.total);
                  case 'estimated_cost':
                    return money(e.cost, t('native.unknown'));
                  case 'duration':
                    return e.latencyObserved ? e.latency + ' ms' : t('native.no_measurement');
                  case 'ttft':
                    return e.observedTTFT === null
                      ? t('native.no_measurement')
                      : e.observedTTFT + ' ms';
                  case 'requestId':
                    return <CopyValue value={e.requestId} />;
                  default:
                    return e[k] || '—';
                }
              }),
            ],
          }))}
        />
        <div className={styles.pagination}>
          <Button
            type="button"
            variant="secondary"
            disabled={before.length === 0 || query.loading}
            onClick={() => setBefore(before.slice(0, -1))}
          >
            {t('native.previous')}
          </Button>
          <span>{before.length + 1}</span>
          <Button
            type="button"
            variant="secondary"
            disabled={!query.data?.cursor || query.loading}
            onClick={() => setBefore([...before, query.data!.cursor])}
          >
            {t('native.next')}
          </Button>
        </div>
      </Card>
      {searchParams.get('trace') && (
        <TracePanel key={searchParams.get('trace')} id={searchParams.get('trace')!} />
      )}
      {selected && (
        <Card
          title={t('native.details')}
          extra={
            <Button type="button" variant="ghost" onClick={() => setSelected(null)}>
              {t('native.close')}
            </Button>
          }
        >
          <DataTable
            headers={[t('native.field'), t('native.value')]}
            rows={(
              [
                'requestId',
                'traceId',
                'upstreamRequestId',
                'model',
                'alias',
                'provider',
                'account',
                'keyHash',
                'endpoint',
                'tier',
                'requestedTier',
                'responseTier',
                'reasoningEffort',
                'pricingRule',
                'quality',
                'failureCode',
                'input',
                'output',
                'cacheRead',
                'cacheWrite',
                'reasoning',
                'observedTTFT',
              ] as const
            ).map((k) => ({
              key: k,
              cells: [
                t('native.' + k),
                k === 'requestId' || k === 'traceId' || k === 'upstreamRequestId' ? (
                  <CopyValue value={selected[k]} />
                ) : selected[k] == null ? (
                  t('native.no_measurement')
                ) : selected[k] === '' ? (
                  '—'
                ) : (
                  String(selected[k])
                ),
              ],
            }))}
          />
          {selected.keyHash && (
            <form
              className={styles.toolbar}
              onSubmit={(e) => {
                e.preventDefault();
                void saveAlias();
              }}
            >
              <Input
                label={t('native.key_alias')}
                value={label}
                onChange={(e) => setLabel(e.target.value)}
                maxLength={128}
              />
              <Button type="submit" disabled={busy}>
                {t('native.save')}
              </Button>
            </form>
          )}
          {selected.traceId ? (
            <TracePanel key={selected.traceId} id={selected.traceId} />
          ) : (
            <p className={styles.hint}>{t('native.observation_unavailable')}</p>
          )}
        </Card>
      )}
    </Page>
  );
}

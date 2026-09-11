import { useCallback, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { nativeInsightsApi, type RequestObservation } from '@/services/api/nativeInsights';
import { useNativeQuery } from './useNativeQuery';
import { DataTable, FeatureGate, Page, QueryState, RangeControl } from './components';
import { TracePanel } from './TracePanel';
import { date } from './format';
import styles from './NativeManagement.module.scss';

export function DiagnosticsPage() {
  return (
    <FeatureGate module="diagnostics">
      <DiagnosticsContent />
    </FeatureGate>
  );
}
function DiagnosticsContent() {
  const { t } = useTranslation();
  const [hours, setHours] = useState(24),
    [to, setTo] = useState(() => Date.now() + 1),
    [cursors, setCursors] = useState<number[]>([]);
  const [draft, setDraft] = useState({ requestId: '', outcome: '' }),
    [filters, setFilters] = useState(draft);
  const [selected, setSelected] = useState<RequestObservation | null>(null);
  const reset = () => {
    setTo(Date.now() + 1);
    setCursors([]);
    setSelected(null);
  };
  useHeaderRefresh(reset);
  const loader = useCallback(
    (signal: AbortSignal) =>
      nativeInsightsApi.requests(
        { from: to - hours * 3600000, to, ...filters, before: cursors[cursors.length - 1] },
        signal
      ),
    [hours, to, filters, cursors]
  );
  const query = useNativeQuery(loader);
  return (
    <Page title={t('native.diagnostics')} description={t('native.diagnostics_hint')}>
      <RangeControl
        hintKey="native.downstream_note"
        hours={hours}
        onChange={(v) => {
          setHours(v);
          reset();
        }}
        onRefresh={reset}
      />
      <Card>
        <form
          className={styles.toolbar}
          onSubmit={(e) => {
            e.preventDefault();
            setFilters(draft);
            reset();
          }}
        >
          <Input
            label={t('native.requestId')}
            value={draft.requestId}
            onChange={(e) => setDraft({ ...draft, requestId: e.target.value })}
          />
          <label>
            {t('native.outcome')}
            <select
              className="input"
              value={draft.outcome}
              onChange={(e) => setDraft({ ...draft, outcome: e.target.value })}
            >
              <option value="">{t('native.all')}</option>
              {[
                'http_success',
                'http_error',
                'stream_error',
                'stream_unknown',
                'client_cancelled',
                'write_error',
                'panic',
              ].map((v) => (
                <option key={v} value={v}>
                  {t('native.outcome_' + v)}
                </option>
              ))}
            </select>
          </label>
          <Button type="submit">{t('native.apply')}</Button>
        </form>
      </Card>
      <QueryState {...query} />
      {query.data && (query.data.dropped > 0 || query.data.failedWrites > 0) && (
        <p role="alert" className="error-box">
          {t('native.diagnostics_loss', {
            dropped: query.data.dropped,
            failed: query.data.failedWrites,
          })}
        </p>
      )}
      {query.data && (
        <div className={styles.metrics}>
          {[
            [t('native.downstream_count'), query.data.summary.total],
            [
              t('native.insight_success_rate'),
              query.data.summary.total
                ? ((query.data.summary.success / query.data.summary.total) * 100).toFixed(1) + '%'
                : '—',
            ],
            [t('native.downstream_errors'), query.data.summary.errors],
            [t('native.downstream_unknown'), query.data.summary.unknown],
          ].map(([label, value]) => (
            <div className={styles.metric} key={label}>
              <p>{label}</p>
              <strong>{value}</strong>
            </div>
          ))}
        </div>
      )}
      <Card>
        <DataTable
          headers={[
            '',
            t('native.time'),
            t('native.requestId'),
            t('native.endpoint'),
            'HTTP',
            t('native.outcome'),
            t('native.downstream_duration'),
          ]}
          rows={(query.data?.requests ?? []).map((r) => ({
            key: r.id,
            cells: [
              <Button type="button" size="sm" variant="ghost" onClick={() => setSelected(r)}>
                {t('native.details')}
              </Button>,
              date(r.started),
              r.requestId || '—',
              r.endpoint,
              r.httpStatus,
              <span
                className={styles.statusPill}
                data-tone={
                  r.outcome === 'http_success'
                    ? 'success'
                    : r.outcome === 'stream_unknown'
                      ? 'muted'
                      : 'danger'
                }
              >
                {t('native.outcome_' + r.outcome)}
              </span>,
              r.duration + ' ms',
            ],
          }))}
        />
        <div className={styles.pagination}>
          <Button
            type="button"
            variant="secondary"
            disabled={query.loading || !cursors.length}
            onClick={() => setCursors(cursors.slice(0, -1))}
          >
            {t('native.previous')}
          </Button>
          <span>{cursors.length + 1}</span>
          <Button
            type="button"
            variant="secondary"
            disabled={query.loading || !query.data?.next}
            onClick={() => setCursors([...cursors, query.data!.next])}
          >
            {t('native.next')}
          </Button>
        </div>
      </Card>
      {selected && (
        <Card
          title={t('native.details')}
          extra={
            <Button type="button" variant="ghost" onClick={() => setSelected(null)}>
              {t('native.close')}
            </Button>
          }
        >
          <TracePanel key={selected.id} id={selected.id} observation={selected} />
        </Card>
      )}
    </Page>
  );
}

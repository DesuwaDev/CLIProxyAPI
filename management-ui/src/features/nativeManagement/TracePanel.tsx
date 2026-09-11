import { useCallback, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { nativeManagementApi } from '@/services/api/nativeManagement';
import { nativeInsightsApi, type RequestObservation } from '@/services/api/nativeInsights';
import { useNativeManagement } from './context';
import { useNativeQuery } from './useNativeQuery';
import { DataTable, QueryState } from './components';
import { date, short } from './format';
import styles from './NativeManagement.module.scss';

export function CopyValue({ value }: { value: string }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  const [error, setError] = useState('');
  async function copy() {
    setError('');
    try {
      if (navigator.clipboard) await navigator.clipboard.writeText(value);
      else {
        const input = document.createElement('textarea');
        input.value = value;
        input.style.position = 'fixed';
        input.style.opacity = '0';
        document.body.appendChild(input);
        input.select();
        const ok = document.execCommand('copy');
        input.remove();
        if (!ok) throw new Error('copy');
      }
      setCopied(true);
    } catch {
      setError(t('native.copy_failed'));
    }
  }
  return value ? (
    <span className={styles.copyValue}>
      <code>{value}</code>
      <Button type="button" size="sm" variant="ghost" onClick={() => void copy()}>
        {t(copied ? 'native.copied' : 'native.copy')}
      </Button>
      {error && <small role="status">{error}</small>}
    </span>
  ) : (
    <>—</>
  );
}
export function ObservationDetails({ value }: { value: RequestObservation }) {
  const { t } = useTranslation();
  return (
    <>
      <DataTable
        headers={[t('native.field'), t('native.value')]}
        rows={[
          { key: 'id', cells: [t('native.traceId'), <CopyValue value={value.id} />] },
          {
            key: 'request',
            cells: [t('native.requestId'), <CopyValue value={value.requestId} />],
          },
          { key: 'outcome', cells: [t('native.outcome'), t('native.outcome_' + value.outcome)] },
          { key: 'status', cells: ['HTTP', value.httpStatus] },
          { key: 'duration', cells: [t('native.downstream_duration'), value.duration + ' ms'] },
          { key: 'endpoint', cells: [t('native.endpoint'), value.endpoint] },
          {
            key: 'inspection',
            cells: [
              t('native.stream_inspection'),
              !value.stream
                ? '—'
                : t(
                    value.inspectionComplete
                      ? 'native.inspection_complete'
                      : 'native.inspection_partial'
                  ),
            ],
          },
        ]}
      />
      <p className={styles.hint}>{t('native.diagnostics_caveat')}</p>
    </>
  );
}
export function TracePanel({ id, observation }: { id: string; observation?: RequestObservation }) {
  const { t } = useTranslation();
  const { enabled } = useNativeManagement();
  const history = enabled('history'),
    diagnostics = enabled('diagnostics');
  const loader = useCallback(
    async (signal: AbortSignal) => {
      const [attempts, request] = await Promise.allSettled([
        history ? nativeManagementApi.trace(id, signal) : Promise.resolve(null),
        observation
          ? Promise.resolve(observation)
          : diagnostics
            ? nativeInsightsApi.request(id, signal)
            : Promise.resolve(null),
      ]);
      if (signal.aborted) signal.throwIfAborted();
      const requestError =
        request.status === 'rejected' && request.reason?.status !== 404
          ? String(request.reason)
          : '';
      return {
        attempts: attempts.status === 'fulfilled' ? attempts.value : null,
        request: request.status === 'fulfilled' ? request.value : null,
        error: [attempts.status === 'rejected' ? String(attempts.reason) : '', requestError]
          .filter(Boolean)
          .join(' · '),
      };
    },
    [id, observation, history, diagnostics]
  );
  const query = useNativeQuery(loader);
  return (
    <section className={styles.page}>
      <h3>{t('native.trace')}</h3>
      <QueryState loading={query.loading} error={query.error || query.data?.error || ''} />
      {query.data?.request ? (
        <ObservationDetails value={query.data.request} />
      ) : (
        !query.loading && <p className={styles.hint}>{t('native.observation_unavailable')}</p>
      )}
      <h3>{t('native.attempt_timeline')}</h3>
      {query.data?.attempts?.truncated && <p>{t('native.truncated')}</p>}
      <DataTable
        headers={[
          t('native.time'),
          t('native.provider'),
          t('native.model'),
          t('native.account'),
          t('native.status'),
          t('native.duration'),
          t('native.upstreamRequestId'),
        ]}
        rows={(query.data?.attempts?.events ?? []).map((e) => ({
          key: e.id,
          cells: [
            date(e.timestamp),
            e.provider,
            e.model,
            short(e.account),
            e.failed ? e.failureCode || e.statusCode : t('native.success'),
            e.latencyObserved ? e.latency + ' ms' : t('native.no_measurement'),
            <CopyValue value={e.upstreamRequestId} />,
          ],
        }))}
      />
      <p className={styles.hint}>{t('native.timeline_hint')}</p>
    </section>
  );
}

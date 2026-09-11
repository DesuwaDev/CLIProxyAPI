import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import type { Analytics } from '@/services/api/nativeInsights';
import { DataTable } from './components';
import { number } from './format';
import styles from './NativeManagement.module.scss';

export function AnalyticsCards({ data }: { data?: Analytics }) {
  const { t } = useTranslation();
  if (!data) return null;
  const ms = (value: number | null) =>
    value === null ? t('native.no_measurement') : number(value) + ' ms';
  const max = Math.max(1, ...data.histogram.map((v) => v.count));
  return (
    <>
      <Card title={t('native.performance')}>
        <DataTable
          headers={['', t('native.samples'), 'P50', 'P95', 'P99', t('native.maximum')]}
          rows={(['duration', 'ttft'] as const).map((k) => ({
            key: k,
            cells: [
              t('native.' + k),
              number(data[k].samples),
              ms(data[k].p50),
              ms(data[k].p95),
              ms(data[k].p99),
              ms(data[k].maximum),
            ],
          }))}
        />
        <p className={styles.hint}>{t('native.percentile_hint')}</p>
        <div className={styles.metrics}>
          {[
            [
              t('native.window_rpm'),
              new Intl.NumberFormat(undefined, { maximumSignificantDigits: 3 }).format(data.rpm),
            ],
            [t('native.window_tpm'), data.tpm.toFixed(2)],
            [
              t('native.cache_rate'),
              data.cacheReadRate === null
                ? t('native.no_measurement')
                : (data.cacheReadRate * 100).toFixed(2) + '%',
            ],
          ].map(([label, value]) => (
            <div className={styles.metric} key={label}>
              <p>{label}</p>
              <strong>{value}</strong>
            </div>
          ))}
        </div>
        <p className={styles.hint}>{t('native.rate_hint')}</p>
      </Card>
      <Card title={t('native.latency_distribution')}>
        <div className={styles.histogram}>
          {data.histogram.map((v) => (
            <div key={v.label}>
              <span>{v.label}</span>
              <meter min={0} max={max} value={v.count} aria-label={v.label} />
              <strong>{number(v.count)}</strong>
            </div>
          ))}
        </div>
      </Card>
    </>
  );
}

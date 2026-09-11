import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { nativeInsightsApi } from '@/services/api/nativeInsights';
import { AnalyticsCards } from './AnalyticsCards';
import { number, money, short } from './format';
import { useCallback, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { Card } from '@/components/ui/Card';
import { nativeManagementApi as api, type UsageAggregate } from '@/services/api/nativeManagement';
import { useNativeManagement } from './context';
import { useNativeQuery } from './useNativeQuery';
import { useInventory } from './inventoryContext';
import { DataTable, FeatureGate, Page, QueryState, RangeControl } from './components';
import styles from './NativeManagement.module.scss';

function percentageChange(current: number, previous: number): number | null {
  return previous > 0 ? ((current - previous) / previous) * 100 : null;
}
const compact = (n: number) =>
  Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 }).format(n);
function Metrics({ value, previous }: { value?: UsageAggregate; previous?: UsageAggregate }) {
  const { t } = useTranslation();
  if (!value) return <p className={styles.empty}>{t('native.empty')}</p>;
  const change = (now: number, old: number | undefined) => {
    const delta = old === undefined ? null : percentageChange(now, old);
    return delta === null
      ? t('native.insight_no_comparison')
      : t('native.insight_change', { value: (delta >= 0 ? '+' : '') + delta.toFixed(1) + '%' });
  };
  const successRate = value.attempts ? (value.attempts - value.failures) / value.attempts : null;
  return (
    <div className={styles.metrics}>
      {[
        {
          label: t('native.attempts'),
          value: compact(value.attempts),
          full: number(value.attempts),
          hint: change(value.attempts, previous?.attempts),
          tone: 'blue',
        },
        {
          label: t('native.insight_success_rate'),
          value: successRate === null ? '—' : (successRate * 100).toFixed(1) + '%',
          full: '',
          hint: t('native.failures_count', { count: value.failures }),
          tone: value.failures ? 'danger' : 'success',
        },
        {
          label: t('native.tokens'),
          value: compact(value.total),
          full: number(value.total),
          hint: change(value.total, previous?.total),
          tone: 'purple',
        },
        {
          label: t('native.estimated_cost'),
          value: money(value.unpriced === value.attempts && value.attempts > 0 ? null : value.cost),
          full: money(value.cost),
          hint: t('native.unpriced_count', { count: value.unpriced }),
          tone: 'amber',
        },
      ].map((v) => (
        <div key={v.label} className={styles.metric} data-accent={v.tone}>
          <p>{v.label}</p>
          <strong title={v.full}>{v.value}</strong>
          <small>{v.hint}</small>
        </div>
      ))}
    </div>
  );
}
export function NativeUsageSummary() {
  const { enabled } = useNativeManagement();
  return enabled('history') ? <SummaryContent /> : null;
}
function SummaryContent() {
  const { t } = useTranslation();
  const loader = useCallback(
    (signal: AbortSignal) =>
      api.summary({ from: Date.now() - 86400000, to: Date.now() + 1 }, '', signal),
    []
  );
  const query = useNativeQuery(loader);
  return (
    <section className={styles.compact}>
      <div className={styles.compactHeader}>
        <h2>{t('native.dashboard_title')}</h2>
        <Link to="/usage">{t('native.view_usage')}</Link>
      </div>
      <QueryState {...query} />
      <Metrics value={query.data?.groups[0]} />
    </section>
  );
}
export function UsagePage() {
  return (
    <FeatureGate module="history">
      <UsageContent />
    </FeatureGate>
  );
}
type TrendMetric = 'attempts' | 'total' | 'cost';
function UsageContent() {
  const { t } = useTranslation(),
    inventory = useInventory();
  const [hours, setHours] = useState(24),
    [group, setGroup] = useState('model'),
    [metric, setMetric] = useState<TrendMetric>('attempts');
  const loader = useCallback(
    async (signal: AbortSignal) => {
      const to = Date.now() + 1,
        from = to - hours * 3600000,
        filter = { from, to };
      const [summary, previous, groups, trend, aliases, analytics, models, keys, recent] =
        await Promise.all([
          api.summary(filter, '', signal),
          api.summary({ from: from - hours * 3600000, to: from }, '', signal),
          api.summary(filter, group, signal),
          api.summary(filter, hours > 168 ? 'day' : 'hour', signal),
          api.aliases(signal),
          nativeInsightsApi.analytics(filter, signal),
          api.summary(filter, 'model', signal),
          api.summary(filter, 'key', signal),
          api.events({ ...filter, sort: 'recent', limit: 8 }, signal),
        ]);
      return {
        summary,
        previous,
        groups,
        trend,
        aliases,
        analytics,
        models,
        keys,
        recent,
        from,
        to,
        step: hours > 168 ? 86400000 : 3600000,
      };
    },
    [hours, group]
  );
  const query = useNativeQuery(loader);
  useHeaderRefresh(query.refresh);
  const data = query.data,
    total = data?.summary.groups[0],
    analysis = data?.analytics;
  const observed = new Map((data?.trend.groups ?? []).map((v) => [v.group, v]));
  const trend = data
    ? Array.from(
        { length: Math.ceil(data.to / data.step) - Math.floor(data.from / data.step) },
        (_, i) => {
          const group = String((Math.floor(data.from / data.step) + i) * data.step);
          return (
            observed.get(group) ?? {
              group,
              attempts: 0,
              failures: 0,
              total: 0,
              cost: 0,
              unpriced: 0,
            }
          );
        }
      )
    : [];
  const max = Math.max(metric === 'cost' ? 0.000001 : 1, ...trend.map((v) => v[metric]));
  const barWidth = 850 / Math.max(trend.length, 1);
  const labelFor = (scope: string, value: string) => {
    if (scope === 'key') return data?.aliases[value] || short(value);
    if (scope === 'account') {
      const item = inventory.items.find((v) => v.scope === 'credential' && v.target === value);
      return item?.profile.name || item?.account?.fileName || item?.account?.label || short(value);
    }
    return value || '—';
  };
  const metricLabel = t(
    'native.' + { attempts: 'attempts', total: 'tokens', cost: 'estimated_cost' }[metric]
  );
  const metricValue = (v: number) => (metric === 'cost' ? money(v) : compact(v));
  const historyURL = (kind: string, value: string) =>
    '/request-history?' +
    new URLSearchParams({
      [kind === 'key' ? 'keyHash' : kind]: value,
      from: String(data?.from ?? 0),
      to: String(data?.to ?? 0),
    }).toString();
  function ranking(kind: string, groups: UsageAggregate[]) {
    const rows = [...groups].sort((a, b) => b[metric] - a[metric]).slice(0, 6),
      maximum = Math.max(1e-9, ...rows.map((v) => v[metric]));
    return (
      <Card
        title={t(kind === 'model' ? 'native.insight_model_ranking' : 'native.insight_key_ranking')}
        extra={<Link to="/credential-usage">{t('native.insight_view_all')}</Link>}
      >
        <div className={styles.rankList}>
          {rows.map((v, i) => (
            <Link to={historyURL(kind, v.group)} className={styles.rankRow} key={v.group}>
              <span className={styles.rankNumber}>{i + 1}</span>
              <div>
                <div className={styles.accountFoot}>
                  <strong title={v.group}>{labelFor(kind, v.group)}</strong>
                  <span>{metricValue(v[metric])}</span>
                </div>
                <meter
                  min={0}
                  max={maximum}
                  value={v[metric]}
                  aria-label={labelFor(kind, v.group)}
                />
                <small>
                  {number(v.attempts)} {t('native.attempts')} ·{' '}
                  {v.attempts ? (((v.attempts - v.failures) / v.attempts) * 100).toFixed(1) : '—'}%{' '}
                  {t('native.insight_success_rate')}
                </small>
              </div>
            </Link>
          ))}
        </div>
        {!rows.length && <p className={styles.empty}>{t('native.empty')}</p>}
      </Card>
    );
  }
  return (
    <Page title={t('native.usage')} description={t('native.insight_intro')}>
      <RangeControl hours={hours} onChange={setHours} onRefresh={() => void query.refresh()} />
      <QueryState {...query} />
      <Metrics value={total} previous={data?.previous.groups[0]} />
      {!!total?.unpriced && (
        <div className={styles.notice}>
          <strong>{t('native.insight_pricing_gap', { count: total.unpriced })}</strong>
          <Link to="/price-rules">{t('native.insight_fix_prices')}</Link>
        </div>
      )}
      {analysis && (
        <div className={styles.quickMetrics}>
          {[
            [t('native.window_rpm'), compact(analysis.rpm)],
            [t('native.window_tpm'), compact(analysis.tpm)],
            [
              'P95 ' + t('native.duration'),
              analysis.duration.p95 === null ? '—' : number(analysis.duration.p95) + ' ms',
            ],
            [
              'P50 ' + t('native.ttft'),
              analysis.ttft.p50 === null ? '—' : number(analysis.ttft.p50) + ' ms',
            ],
            [
              t('native.cache_rate'),
              analysis.cacheReadRate === null
                ? '—'
                : (analysis.cacheReadRate * 100).toFixed(1) + '%',
            ],
          ].map(([label, value]) => (
            <div key={label}>
              <span>{label}</span>
              <strong>{value}</strong>
            </div>
          ))}
        </div>
      )}
      <div className={styles.insightGrid}>
        <Card
          title={t('native.trend')}
          extra={
            <div className={styles.segmented}>
              {(['attempts', 'total', 'cost'] as const).map((v) => (
                <button
                  type="button"
                  aria-pressed={metric === v}
                  key={v}
                  onClick={() => setMetric(v)}
                >
                  {t(
                    'native.' + { attempts: 'attempts', total: 'tokens', cost: 'estimated_cost' }[v]
                  )}
                </button>
              ))}
            </div>
          }
        >
          <div className={styles.chartLegend}>
            <span>
              <i className={styles.legendSuccess} />
              {metricLabel}
            </span>
            {metric === 'attempts' && (
              <span>
                <i className={styles.legendFailure} />
                {t('native.failures')}
              </span>
            )}
          </div>
          <svg viewBox="0 0 960 260" className={styles.chart} role="img" aria-label={metricLabel}>
            {[0, 0.25, 0.5, 0.75, 1].map((p) => (
              <g key={p}>
                <line
                  x1="90"
                  x2="940"
                  y1={220 - p * 190}
                  y2={220 - p * 190}
                  className={styles.gridLine}
                />
                <text x="0" y={224 - p * 190} className={styles.chartLabel}>
                  {metric === 'cost' ? '$' + compact(max * p) : compact(max * p)}
                </text>
              </g>
            ))}
            {trend.map((v, i) => {
              const height = (v[metric] / max) * 190,
                failed = metric === 'attempts' ? (v.failures / max) * 190 : 0;
              return (
                <g key={v.group}>
                  <title>
                    {new Date(Number(v.group)).toLocaleString()}: {metricValue(v[metric])} ·{' '}
                    {t('native.failures')} {v.failures} · {t('native.unpriced')} {v.unpriced}
                  </title>
                  <rect
                    x={90 + i * barWidth + 1}
                    y={220 - height}
                    width={Math.max(1, barWidth - 2)}
                    height={Math.max(0, height - failed)}
                    className={styles.success}
                    rx={2}
                  />
                  {metric === 'attempts' && (
                    <rect
                      x={90 + i * barWidth + 1}
                      y={220 - failed}
                      width={Math.max(1, barWidth - 2)}
                      height={failed}
                      className={styles.failure}
                    />
                  )}
                  {(i === 0 || i === Math.floor(trend.length / 2) || i === trend.length - 1) && (
                    <text
                      x={90 + i * barWidth}
                      y="250"
                      textAnchor={i === trend.length - 1 ? 'end' : 'start'}
                      className={styles.chartLabel}
                    >
                      {new Date(Number(v.group)).toLocaleString(undefined, {
                        month: 'numeric',
                        day: 'numeric',
                        hour: '2-digit',
                      })}
                    </text>
                  )}
                </g>
              );
            })}
          </svg>
          <p className={styles.hint}>{t('native.insight_trend_hint')}</p>
        </Card>
        <Card title={t('native.insight_token_mix')}>
          {total && (
            <>
              <strong className={styles.heroNumber} title={number(total.total)}>
                {compact(total.total)} <small>Token</small>
              </strong>
              <div className={styles.tokenComposition}>
                <span style={{ flex: total.input || 0.001 }} />
                <span style={{ flex: total.output || 0.001 }} />
              </div>
              <dl className={styles.tokenBreakdown}>
                {[
                  [t('native.input'), total.input],
                  [t('native.output'), total.output],
                  [t('native.cache_read'), total.cacheRead],
                  [t('native.cacheWrite'), total.cacheWrite],
                  [t('native.reasoning'), total.reasoning],
                ].map(([label, value]) => (
                  <div key={label}>
                    <dt>{label}</dt>
                    <dd>{number(Number(value))}</dd>
                  </div>
                ))}
              </dl>
              <p className={styles.hint}>{t('native.insight_token_hint')}</p>
            </>
          )}
        </Card>
      </div>
      <div className={styles.twoColumns}>
        {ranking('model', data?.models.groups ?? [])}
        {ranking('key', data?.keys.groups ?? [])}
      </div>
      <Card
        title={t('native.insight_recent')}
        extra={<Link to="/request-history">{t('native.insight_view_all')}</Link>}
      >
        <DataTable
          headers={[
            t('native.time'),
            t('native.model'),
            t('native.status'),
            t('native.tokens'),
            t('native.estimated_cost'),
            t('native.duration'),
          ]}
          rows={(data?.recent.events ?? []).map((e) => ({
            key: e.id,
            cells: [
              <Link to={'/request-history?trace=' + encodeURIComponent(e.traceId)}>
                {new Date(e.timestamp).toLocaleString(undefined, {
                  month: '2-digit',
                  day: '2-digit',
                  hour: '2-digit',
                  minute: '2-digit',
                  second: '2-digit',
                })}
              </Link>,
              e.alias || e.model,
              <span
                className={styles.statusPill}
                title={e.failed ? e.failureCode : undefined}
                data-tone={e.failed ? 'danger' : 'success'}
              >
                {e.failed
                  ? t('common.failure') + (e.statusCode ? ' · ' + e.statusCode : '')
                  : t('native.success')}
              </span>,
              compact(e.total),
              money(e.cost),
              e.latencyObserved ? number(e.latency) + ' ms' : '—',
            ],
          }))}
        />
      </Card>
      <Card
        title={t('native.breakdown')}
        extra={
          <select
            className="input"
            aria-label={t('native.breakdown')}
            value={group}
            onChange={(e) => setGroup(e.target.value)}
          >
            {['model', 'provider', 'account', 'key', 'status'].map((v) => (
              <option key={v} value={v}>
                {t('native.' + v)}
              </option>
            ))}
          </select>
        }
      >
        {data?.groups.truncated && <p className={styles.hint}>{t('native.truncated')}</p>}
        <DataTable
          headers={[
            t('native.' + group),
            t('native.attempts'),
            t('native.insight_success_rate'),
            t('native.tokens'),
            t('native.estimated_cost'),
            t('native.unpriced'),
          ]}
          rows={(data?.groups.groups ?? []).map((v) => ({
            key: v.group,
            cells: [
              group === 'status' ? (
                v.group
              ) : (
                <Link to={historyURL(group, v.group)}>{labelFor(group, v.group)}</Link>
              ),
              number(v.attempts),
              v.attempts ? (((v.attempts - v.failures) / v.attempts) * 100).toFixed(1) + '%' : '—',
              number(v.total),
              money(v.unpriced === v.attempts && v.attempts > 0 ? null : v.cost),
              number(v.unpriced),
            ],
          }))}
        />
      </Card>
      <details className={styles.performanceDetails}>
        <summary>{t('native.insight_performance_details')}</summary>
        <AnalyticsCards data={analysis} />
      </details>
    </Page>
  );
}

import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { nativeControlsApi } from '@/services/api/nativeControls';
import {
  nativeInventoryApi as api,
  type InventoryItem,
  type InventoryForecast,
} from '@/services/api/nativeInventory';
import { useQuotaStore } from '@/stores/useQuotaStore';
import { useInventory } from './inventoryContext';
import { useNativeManagement } from './context';
import { useNativeQuery } from './useNativeQuery';
import { number, money, date } from './format';
import styles from './NativeManagement.module.scss';

function Totals({ item }: { item?: InventoryItem }) {
  const { t } = useTranslation();
  const total = item?.totals;
  const allUnknown = !!total?.requests && total.unpriced === total.requests;
  return (
    <div className={styles.accountMetrics}>
      <div>
        <span>{t('native.inventory_requests')}</span>
        <strong>{number(total?.requests ?? 0)}</strong>
      </div>
      <div>
        <span>{t('native.tokens')}</span>
        <strong>{number(total?.tokens ?? 0)}</strong>
      </div>
      <div>
        <span>{t('native.inventory_spent')}</span>
        <strong>{money(allUnknown ? null : (total?.cost ?? 0))}</strong>
      </div>
    </div>
  );
}
export function KeyUsageBadge({ apiKey }: { apiKey: string }) {
  const { enabled } = useNativeManagement();
  return enabled('inventory') ? <KeySummary apiKey={apiKey} /> : null;
}
function KeySummary({ apiKey }: { apiKey: string }) {
  const { t } = useTranslation(),
    inventory = useInventory();
  const load = useCallback(
    (signal: AbortSignal) => nativeControlsApi.identity('client', apiKey, signal),
    [apiKey]
  );
  const query = useNativeQuery(load);
  const item = inventory.items.find((v) => v.scope === 'client' && v.target === query.data?.id);
  if (query.error || inventory.error)
    return <p className={styles.hint}>{t('native.inventory_load_failed')}</p>;
  return (
    <div className={styles.keySummary}>
      {item?.profile.name && <strong>{item.profile.name}</strong>}
      {item?.profile.notes && <small title={item.profile.notes}>{item.profile.notes}</small>}
      {item?.profile.tags.length ? (
        <span className={styles.tags}>
          {item.profile.tags.map((v) => (
            <span key={v}>{v}</span>
          ))}
        </span>
      ) : null}
      <Totals item={item} />
      {!!item?.totals.unpriced && (
        <small>{t('native.unpriced_count', { count: item.totals.unpriced })}</small>
      )}
      {item?.profile.disabled && (
        <span className={styles.statusPill} data-tone="danger">
          {t('native.profile_disabled')}
        </span>
      )}
      {!!item?.profile.expires && (
        <small>
          {t('native.profile_expiry')}: {date(item.profile.expires)}
        </small>
      )}
    </div>
  );
}
export function AccountUsageSummary({ target, fileName }: { target?: string; fileName: string }) {
  const { enabled } = useNativeManagement(),
    inventory = useInventory();
  if (!enabled('inventory')) return null;
  const item = inventory.items.find(
    (v) =>
      v.scope === 'credential' && (target ? v.target === target : v.account?.fileName === fileName)
  );
  if (inventory.error) return <p className={styles.hint}>{inventory.error}</p>;
  return item ? <AccountSummary key={item.target} item={item} fileName={fileName} /> : null;
}
function AccountSummary({ item, fileName }: { item: InventoryItem; fileName: string }) {
  const { t } = useTranslation();
  const quota = useQuotaStore((s) => s.codexQuota[fileName]);
  const [quotaError, setQuotaError] = useState('');
  const load = useCallback(
    (signal: AbortSignal) => api.get('credential', item.target, signal),
    [item.target]
  );
  const query = useNativeQuery(load);
  const refreshForecast = query.refresh;
  useEffect(() => {
    if (quota?.status !== 'success' || !quota.windows.length || !quota.observedAt) return;
    const windows = quota.windows
      .filter(
        (w) =>
          w.usedPercent !== null &&
          w.resetAtMs &&
          w.periodHours &&
          ['five-hour', 'weekly', 'monthly'].includes(w.id)
      )
      .map((w) => ({
        id: w.id,
        usedPercent: w.usedPercent!,
        start: w.resetAtMs! - w.periodHours! * 3600000,
        reset: w.resetAtMs!,
        observed: quota.observedAt!,
      }));
    if (!windows.length) return;
    const controller = new AbortController();
    void api
      .windows(item.target, windows, controller.signal)
      .then(() => {
        if (!controller.signal.aborted) {
          void refreshForecast();
          setQuotaError('');
        }
      })
      .catch((e) => {
        if (!controller.signal.aborted) setQuotaError(e instanceof Error ? e.message : String(e));
      });
    return () => controller.abort();
  }, [item.target, quota, refreshForecast]);
  const cached = query.data?.item;
  const live = {
    ...(cached ?? item),
    profile:
      cached && cached.profile.version > item.profile.version ? cached.profile : item.profile,
    totals: cached && cached.totals.requests >= item.totals.requests ? cached.totals : item.totals,
  };
  return (
    <section className={styles.accountUsage}>
      {live.profile.name && <strong>{live.profile.name}</strong>}
      {live.profile.notes && (
        <p className={styles.annotation} title={live.profile.notes}>
          {live.profile.notes}
        </p>
      )}
      <Totals item={live} />
      <div className={styles.accountFoot}>
        <span>
          {t('native.inventory_since')}: {date(live.totals.first)}
        </span>
        <Link to={'/request-history?account=' + encodeURIComponent(item.target)}>
          {t('native.inventory_details')}
        </Link>
      </div>
      {!!live.totals.unpriced && (
        <p className={styles.hint}>{t('native.unpriced_count', { count: live.totals.unpriced })}</p>
      )}
      {live.profile.budget !== null && (
        <div className={styles.budgetBar}>
          <span>
            {t('native.profile_budget')}: {money(live.profile.budget)}
          </span>
          <span>
            {t('native.inventory_budget_remaining')}:{' '}
            {money(
              live.totals.unpriced ? null : Math.max(0, live.profile.budget - live.totals.cost)
            )}
          </span>
          <meter
            aria-label={t('native.profile_budget')}
            min={0}
            max={Math.max(1, live.profile.budget)}
            value={live.totals.cost}
          />
        </div>
      )}
      {(query.error || quotaError) && <p className={styles.hint}>{query.error || quotaError}</p>}
      {query.data?.forecasts.length ? (
        <div className={styles.forecastGrid}>
          {query.data.forecasts.map((f) => (
            <QuotaForecast key={f.window.id} value={f} />
          ))}
        </div>
      ) : (
        <p className={styles.hint}>{t('native.inventory_quota_hint')}</p>
      )}
    </section>
  );
}
export function QuotaForecast({ value: f }: { value: InventoryForecast }) {
  const { t } = useTranslation(),
    w = f.window;
  const hours = Math.round((w.reset - w.start) / 3600000);
  const expired = f.reason === 'expired_snapshot';
  return (
    <div className={styles.forecast}>
      <div className={styles.accountFoot}>
        <strong>{t('native.inventory_window', { hours })}</strong>
        <span>
          {expired
            ? t('native.inventory_expired')
            : w.usedPercent.toFixed(1) + '% ' + t('native.inventory_used')}
        </span>
      </div>
      <meter min={0} max={100} value={w.usedPercent} aria-label={t('native.inventory_used')} />
      <div className={styles.forecastValues}>
        <span>
          {t('native.inventory_window_spent')}
          <strong>{money(f.usage.unpriced ? null : f.usage.cost)}</strong>
        </span>
        <span>
          {t('native.inventory_total_est')}
          <strong>{money(f.estimatedTotal)}</strong>
        </span>
        <span>
          {t('native.inventory_remaining_est')}
          <strong>{money(f.estimatedRemaining)}</strong>
        </span>
      </div>
      <small>
        {t('native.inventory_reset')}: {date(w.reset)}
      </small>
      <small>
        {t('native.inventory_observed')}: {date(w.observed)}
      </small>
      <small>
        {f.reason === 'proportional_estimate'
          ? t('native.inventory_estimate_hint')
          : t('native.inventory_reason_' + f.reason)}
        {f.coverage === 'partial' ? ' · ' + t('native.inventory_partial') : ''}
      </small>
    </div>
  );
}

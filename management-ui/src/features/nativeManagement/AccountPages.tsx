import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { date, short, number } from './format';
import { useCallback, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { nativeManagementApi as api, type AccountAction } from '@/services/api/nativeManagement';
import { useNativeQuery } from './useNativeQuery';
import { DataTable, FeatureGate, Page, QueryState } from './components';
import styles from './NativeManagement.module.scss';

export function AccountHealthPage() {
  return (
    <FeatureGate module="accounts">
      <HealthContent />
    </FeatureGate>
  );
}
function HealthContent() {
  const { t } = useTranslation();
  const [account, setAccount] = useState(''),
    [busy, setBusy] = useState(false),
    [error, setError] = useState('');
  const loader = useCallback((signal: AbortSignal) => api.accounts(signal), []),
    query = useNativeQuery(loader);
  const historyLoader = useCallback(
      (signal: AbortSignal) =>
        account ? api.accountHistory(account, signal) : Promise.resolve([]),
      [account]
    ),
    history = useNativeQuery(historyLoader);
  useHeaderRefresh(async () => {
    await Promise.all([query.refresh(), history.refresh()]);
  });
  async function inspect() {
    setBusy(true);
    setError('');
    try {
      await api.inspect();
      await Promise.all([query.refresh(), history.refresh()]);
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Page title={t('native.health')} description={t('native.health_hint')}>
      <div className={styles.toolbar}>
        <Link to="/auth-files">{t('native.manage_auth')}</Link>
        <Link to="/quota">{t('native.live_quota')}</Link>
        <Button type="button" variant="secondary" disabled={busy} onClick={() => void inspect()}>
          {t('native.inspect')}
        </Button>
      </div>
      <QueryState {...query} />
      {error && <p role="alert">{error}</p>}
      <Card>
        <DataTable
          headers={[
            t('native.account'),
            t('native.provider'),
            t('native.state'),
            t('native.next_retry'),
            t('native.quota_observed'),
            '',
          ]}
          rows={(query.data ?? []).map((a) => ({
            key: a.account,
            cells: [
              a.label || short(a.account),
              a.provider,
              t('native.state_' + a.state, { defaultValue: a.state }),
              date(a.nextRetry),
              date(a.quotaObserved),
              <Button type="button" size="sm" variant="ghost" onClick={() => setAccount(a.account)}>
                {t('native.history')}
              </Button>,
            ],
          }))}
        />
      </Card>
      {account && (
        <Card
          title={`${t('native.history')} · ${short(account)}`}
          extra={
            <Button type="button" variant="ghost" onClick={() => setAccount('')}>
              {t('native.close')}
            </Button>
          }
        >
          <p className={styles.hint}>{t('native.snapshots_hint')}</p>
          <QueryState {...history} />
          <DataTable
            headers={[t('native.time'), t('native.state'), t('native.next_retry')]}
            rows={(history.data ?? []).map((a, i) => ({
              key: `${a.checkedAt}-${i}`,
              cells: [
                date(a.checkedAt),
                t('native.state_' + a.state, { defaultValue: a.state }),
                date(a.nextRetry),
              ],
            }))}
          />
        </Card>
      )}
    </Page>
  );
}
export function AccountActionsPage() {
  return (
    <FeatureGate module="accounts">
      <ActionsContent />
    </FeatureGate>
  );
}
function ActionsContent() {
  const { t } = useTranslation();
  const [busy, setBusy] = useState(false),
    [error, setError] = useState('');
  const loader = useCallback((signal: AbortSignal) => api.actions(signal), []),
    query = useNativeQuery(loader);
  useHeaderRefresh(query.refresh);
  async function update(a: AccountAction, status: AccountAction['status']) {
    setBusy(true);
    setError('');
    try {
      await api.setAction(a, status);
      await query.refresh();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Page title={t('native.actions')} description={t('native.actions_hint')}>
      <div className={styles.toolbar}>
        <Link to="/auth-files">{t('native.manage_auth')}</Link>
        <Button type="button" variant="secondary" onClick={() => void query.refresh()}>
          {t('common.refresh')}
        </Button>
      </div>
      <QueryState {...query} />
      {error && <p role="alert">{error}</p>}
      {query.data?.truncated && <p>{t('native.truncated')}</p>}
      <Card>
        <DataTable
          headers={[
            t('native.account'),
            t('native.failureCode'),
            t('native.hits'),
            t('native.first_seen'),
            t('native.last_seen'),
            t('native.status'),
          ]}
          rows={(query.data?.actions ?? []).map((a) => ({
            key: a.account + ':' + a.code,
            cells: [
              short(a.account),
              a.code,
              number(a.hits),
              date(a.firstSeen),
              date(a.lastSeen),
              <select
                className="input"
                aria-label={`${a.account} ${a.code}`}
                value={a.status}
                disabled={busy}
                onChange={(e) => void update(a, e.target.value as AccountAction['status'])}
              >
                {(['pending', 'ignored', 'resolved'] as const).map((s) => (
                  <option key={s} value={s}>
                    {t('native.' + s)}
                  </option>
                ))}
              </select>,
            ],
          }))}
        />
      </Card>
    </Page>
  );
}

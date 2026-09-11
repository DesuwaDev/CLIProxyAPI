import { Link } from 'react-router-dom';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { date, number } from './format';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { nativeManagementApi as api, type NativeModuleName } from '@/services/api/nativeManagement';
import { useNativeManagement } from './context';
import { Page, QueryState } from './components';
import { CodexVersionEditor } from './CodexVersionEditor';
import styles from './NativeManagement.module.scss';

export function ModuleSettingsPage() {
  const { t } = useTranslation(),
    query = useNativeManagement(),
    [busy, setBusy] = useState(false),
    [error, setError] = useState('');
  useHeaderRefresh(query.refresh);
  async function toggle(name: NativeModuleName, value: boolean) {
    setBusy(true);
    setError('');
    try {
      await api.setModule(name, value);
      await query.refresh();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Page title={t('native.modules')} description={t('native.modules_hint')}>
      <QueryState loading={query.loading} error={query.error || error} />
      {!query.status && !query.loading && !query.error && <Card>{t('native.unavailable')}</Card>}
      <div className={styles.moduleGrid}>
        {query.status?.modules.map((m) => (
          <Card
            key={m.name}
            title={t('native.module_' + m.name)}
            extra={
              <ToggleSwitch
                checked={m.enabled}
                disabled={busy || query.loading}
                ariaLabel={t('native.module_' + m.name)}
                onChange={(v) => void toggle(m.name, v)}
              />
            }
          >
            <p className={styles.hint}>{t('native.module_' + m.name + '_hint')}</p>
          </Card>
        ))}
      </div>
      {query.enabled('headers') && <CodexVersionEditor />}
      {query.enabled('wire') && (
        <Card title={t('native.disguise_title')}>
          <p className={styles.hint}>{t('native.disguise_modules_link_hint')}</p>
          <Link to="/codex-disguise">{t('native.disguise_open')}</Link>
        </Card>
      )}
      {query.status && (
        <Card
          title={t('native.storage')}
          extra={
            <Button type="button" variant="secondary" onClick={() => void query.refresh()}>
              {t('common.refresh')}
            </Button>
          }
        >
          <div className={styles.metrics}>
            {[
              ['written', number(query.status.written)],
              ['failed_writes', number(query.status.failedWrites)],
              ['maintenance_failures', number(query.status.maintenanceFailures)],
              ['diagnostic_dropped', number(query.status.diagnosticDropped)],
              ['diagnostic_failed', number(query.status.diagnosticFailed)],
              ['retention', String(query.status.retentionDays)],
            ].map(([k, v]) => (
              <div className={styles.metric} key={k}>
                <p>{t('native.' + k)}</p>
                <strong>{v}</strong>
              </div>
            ))}
          </div>
          <p className={styles.hint}>
            {t('native.last_write')}: {date(query.status.lastWrite)}
          </p>
          <p className={styles.hint}>{t('native.automation_off')}</p>
        </Card>
      )}
    </Page>
  );
}

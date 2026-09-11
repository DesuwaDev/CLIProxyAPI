import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { nativeControlsApi as api, type CodexVersionSettings } from '@/services/api/nativeControls';
import { QueryState } from './components';
import { date } from './format';
import { useNativeQuery } from './useNativeQuery';
import styles from './NativeManagement.module.scss';

export function CodexVersionEditor() {
  const { t } = useTranslation();
  const load = useCallback((signal: AbortSignal) => api.codexVersion(signal), []);
  const query = useNativeQuery(load);
  const { refresh, loading } = query;
  const syncing = query.data?.syncing;
  useEffect(() => {
    if (!syncing || loading) return;
    const timer = window.setTimeout(() => void refresh(), 2000);
    return () => window.clearTimeout(timer);
  }, [syncing, refresh, loading]);
  const [draft, setDraft] = useState<CodexVersionSettings | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState('');
  const controller = useRef<AbortController | null>(null);
  useEffect(() => () => controller.current?.abort(), []);
  const settings = draft ?? query.data?.settings;
  const locked = busy || query.loading;

  async function run(action: 'save' | 'sync') {
    if (locked || !settings) return;
    const current = new AbortController();
    controller.current = current;
    setBusy(true);
    setMessage('');
    try {
      if (action === 'save') {
        await api.setCodexVersion(settings, current.signal);
      } else {
        await api.syncCodexVersion(current.signal);
      }
      if (current.signal.aborted) return;
      setDraft(null);
      setMessage(t(action === 'save' ? 'native.saved' : 'native.codex_version_synced'));
      await query.refresh();
    } catch (error) {
      if (!current.signal.aborted) {
        setMessage(error instanceof Error ? error.message : String(error));
        if (action === 'sync') await query.refresh();
      }
    } finally {
      if (!current.signal.aborted) setBusy(false);
    }
  }

  return (
    <Card title={t('native.codex_version_title')}>
      <div className={styles.page}>
        <p className={styles.hint}>{t('native.codex_version_hint')}</p>
        <QueryState {...query} />
        {query.data && settings && (
          <>
            <div className={styles.metrics}>
              <div className={styles.metric}>
                <p>{t('native.codex_version_effective')}</p>
                <strong>{query.data.effective_version}</strong>
                <p>{t(`native.codex_version_source_${query.data.effective_source}`)}</p>
              </div>
              <div className={styles.metric}>
                <p>{t('native.codex_version_latest')}</p>
                <strong>{query.data.synced_version || '—'}</strong>
                <p>
                  {t('native.codex_version_checked')}: {date(query.data.last_checked_ms)}
                </p>
              </div>
            </div>
            <Input
              label={t('native.codex_version_manual')}
              placeholder={query.data.builtin_version}
              value={settings.manual_version}
              maxLength={64}
              disabled={locked}
              hint={t('native.codex_version_manual_hint')}
              onChange={(event) => setDraft({ ...settings, manual_version: event.target.value })}
            />
            <ToggleSwitch
              label={t('native.codex_version_auto')}
              checked={settings.automatic}
              disabled={locked}
              onChange={(automatic) => setDraft({ ...settings, automatic })}
            />
            <p className={styles.hint}>{t('native.codex_version_auto_hint')}</p>
            <div className={styles.actions}>
              <Button type="button" disabled={locked || !draft} onClick={() => void run('save')}>
                {t('native.codex_version_save')}
              </Button>
              <Button
                type="button"
                variant="secondary"
                disabled={locked || query.data.syncing || !!draft}
                onClick={() => void run('sync')}
              >
                {t(
                  query.data.syncing ? 'native.codex_version_syncing' : 'native.codex_version_sync'
                )}
              </Button>
            </div>
            {draft && <p className={styles.hint}>{t('native.codex_version_save_first')}</p>}
            {query.data.last_error && <p role="alert">{query.data.last_error}</p>}
          </>
        )}
        <Button
          type="button"
          variant="ghost"
          disabled={busy || query.loading}
          onClick={() => {
            setDraft(null);
            setMessage('');
            void query.refresh();
          }}
        >
          {t('native.codex_version_reload')}
        </Button>
        {message && <p role="status">{message}</p>}
      </div>
    </Card>
  );
}

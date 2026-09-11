import { CatalogModelPrices } from './CatalogModelPrices';
import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { nativeInsightsApi as api, type CatalogSettings } from '@/services/api/nativeInsights';
import { useNativeQuery } from './useNativeQuery';
import { QueryState } from './components';
import { date, number } from './format';
import styles from './NativeManagement.module.scss';

export function PriceCatalogCard() {
  const { t } = useTranslation();
  const loader = useCallback((signal: AbortSignal) => api.catalog(signal), []);
  const query = useNativeQuery(loader);
  const [draft, setDraft] = useState<CatalogSettings | null>(null),
    [busy, setBusy] = useState(false),
    [syncing, setSyncing] = useState(false),
    [message, setMessage] = useState('');
  const controller = useRef<AbortController | null>(null);
  useEffect(() => () => controller.current?.abort(), []);
  const settings = draft ?? query.data?.settings;
  async function save() {
    if (!settings) return;
    setBusy(true);
    setMessage('');
    try {
      await api.setCatalog(settings);
      setDraft(null);
      await query.refresh();
      setMessage(t('native.saved'));
    } catch (e) {
      setMessage(String(e));
    } finally {
      setBusy(false);
    }
  }
  async function sync() {
    const current = new AbortController();
    controller.current = current;
    setSyncing(true);
    setMessage('');
    try {
      await api.syncCatalog(current.signal);
      setMessage(t('native.catalog_synced'));
    } catch (e) {
      setMessage(current.signal.aborted ? t('native.cancelled') : String(e));
    } finally {
      setSyncing(false);
      controller.current = null;
      await query.refresh();
    }
  }
  return (
    <Card title={t('native.catalog')}>
      <p className={styles.hint}>{t('native.catalog_hint')}</p>
      <QueryState {...query} />
      {settings && (
        <form
          className={styles.page}
          onSubmit={(e) => {
            e.preventDefault();
            void save();
          }}
        >
          <label>
            {t('native.catalog_preset')}
            <select
              className="input"
              disabled={busy || syncing}
              value={
                settings.source ===
                'https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json'
                  ? 'litellm'
                  : settings.source ===
                      'https://raw.githubusercontent.com/Wei-Shaw/model-price-repo/main/model_prices_and_context_window.json'
                    ? 'sub2api'
                    : 'custom'
              }
              onChange={(e) => {
                if (e.target.value === 'custom') setDraft({ ...settings, source: '' });
                else
                  setDraft({
                    ...settings,
                    source:
                      e.target.value === 'litellm'
                        ? 'https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json'
                        : 'https://raw.githubusercontent.com/Wei-Shaw/model-price-repo/main/model_prices_and_context_window.json',
                  });
              }}
            >
              <option value="litellm">LiteLLM</option>
              <option value="sub2api">sub2api</option>
              <option value="custom">{t('native.catalog_custom')}</option>
            </select>
          </label>
          <Input
            label={t('native.catalog_source')}
            type="url"
            required
            value={settings.source}
            disabled={busy || syncing}
            onChange={(e) => setDraft({ ...settings, source: e.target.value })}
          />
          <div className={styles.toolbar}>
            <ToggleSwitch
              checked={settings.automatic}
              disabled={busy || syncing}
              ariaLabel={t('native.catalog_auto')}
              onChange={(v) => setDraft({ ...settings, automatic: v })}
            />
            <span>{t('native.catalog_auto')}</span>
            <Input
              label={t('native.catalog_interval')}
              type="number"
              min={1}
              max={168}
              step={1}
              required
              value={Number.isNaN(settings.intervalHours) ? '' : settings.intervalHours}
              disabled={busy || syncing}
              onChange={(e) => setDraft({ ...settings, intervalHours: e.target.valueAsNumber })}
            />
          </div>
          <div className={styles.actions}>
            <Button type="submit" disabled={busy || syncing || !draft}>
              {t('native.save')}
            </Button>
            <Button
              type="button"
              variant="ghost"
              disabled={busy || syncing || !draft}
              onClick={() => setDraft(null)}
            >
              {t('native.discard')}
            </Button>
            <Button
              type="button"
              variant="secondary"
              disabled={busy || syncing || !!draft || query.loading || query.data?.syncing}
              onClick={() => void sync()}
            >
              {t('native.catalog_sync')}
            </Button>
            {syncing && (
              <Button type="button" variant="secondary" onClick={() => controller.current?.abort()}>
                {t('native.cancel')}
              </Button>
            )}
            <Button
              type="button"
              variant="ghost"
              disabled={query.loading}
              onClick={() => void query.refresh()}
            >
              {t('common.refresh')}
            </Button>
          </div>
          {draft && <p className={styles.hint}>{t('native.catalog_save_first')}</p>}
        </form>
      )}
      {query.data && (
        <p className={styles.hint}>
          {t('native.catalog_status', {
            count: number(query.data.models),
            skipped: number(query.data.skipped),
          })}{' '}
          · {t('native.catalog_updated')}: {date(query.data.updated)} ·{' '}
          {t('native.catalog_attempt')}: {date(query.data.lastAttempt)}
        </p>
      )}
      {(syncing || query.data?.syncing) && <p role="status">{t('native.catalog_syncing')}</p>}
      {query.data?.lastError && (
        <p role="alert" className="error-box">
          {query.data.lastError}
        </p>
      )}
      {message && <p role="status">{message}</p>}
      <CatalogModelPrices revision={query.data?.hash ?? ''} />
    </Card>
  );
}

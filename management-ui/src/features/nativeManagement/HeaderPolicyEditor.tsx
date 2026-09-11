import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { nativeControlsApi as api, type HeaderMode } from '@/services/api/nativeControls';
import { QueryState } from './components';
import { useNativeQuery } from './useNativeQuery';
import styles from './NativeManagement.module.scss';

export function HeaderPolicyEditor({ target, disabled }: { target: string; disabled?: boolean }) {
  const { t } = useTranslation();
  const load = useCallback((signal: AbortSignal) => api.headers(target, signal), [target]);
  const query = useNativeQuery(load);
  const [draft, setDraft] = useState<HeaderMode | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState('');
  const controller = useRef<AbortController | null>(null);
  useEffect(() => () => controller.current?.abort(), []);
  const mode = draft ?? query.data;

  async function save() {
    if (!draft || busy) return;
    const current = new AbortController();
    controller.current = current;
    setBusy(true);
    setMessage('');
    try {
      await api.setHeaders(target, draft, current.signal);
      if (current.signal.aborted) return;
      setDraft(null);
      setMessage(t('native.saved'));
      await query.refresh();
    } catch (error) {
      if (!current.signal.aborted) setMessage(String(error));
    } finally {
      if (!current.signal.aborted) setBusy(false);
    }
  }

  return (
    <section className={styles.page}>
      <h3>{t('native.module_headers')}</h3>
      <QueryState {...query} />
      {mode && (
        <>
          <label>
            {t('native.headers_mode')}
            <select
              className="input"
              value={mode}
              disabled={disabled || busy || query.loading}
              onChange={(event) => setDraft(event.target.value as HeaderMode)}
            >
              {(['off', 'clean', 'client'] as const).map((value) => (
                <option key={value} value={value}>
                  {t(`native.headers_${value}`)}
                </option>
              ))}
            </select>
          </label>
          <p className={styles.hint}>{t(`native.headers_${mode}_hint`)}</p>
          <p className={styles.hint}>{t('native.headers_identity_hint')}</p>
          <div className={styles.actions}>
            <Button
              type="button"
              disabled={disabled || busy || query.loading || !draft || !!query.error}
              onClick={() => void save()}
            >
              {t('native.save_headers')}
            </Button>
          </div>
        </>
      )}
      <div className={styles.actions}>
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
          {t('native.reload_headers')}
        </Button>
      </div>
      {message && <p role="status">{message}</p>}
    </section>
  );
}

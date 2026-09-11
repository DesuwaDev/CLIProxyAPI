import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import {
  nativeControlsApi as api,
  type WireMode,
  type WirePolicy,
} from '@/services/api/nativeControls';
import { QueryState } from './components';
import { useNativeQuery } from './useNativeQuery';
import styles from './NativeManagement.module.scss';

export function WirePolicyEditor({ target, disabled }: { target: string; disabled?: boolean }) {
  const { t } = useTranslation();
  const load = useCallback((signal: AbortSignal) => api.wire(target, signal), [target]);
  const query = useNativeQuery(load);
  const [draft, setDraft] = useState<WirePolicy | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState('');
  const controller = useRef<AbortController | null>(null);
  useEffect(() => () => controller.current?.abort(), []);
  const policy = draft ?? query.data?.policy;
  const locked = disabled || busy || query.loading;

  async function save() {
    if (!draft || busy) return;
    const current = new AbortController();
    controller.current = current;
    setBusy(true);
    setMessage('');
    try {
      await api.setWire(target, draft, current.signal);
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
      <h3>{t('native.module_wire')}</h3>
      <QueryState {...query} />
      {policy && (
        <>
          <label>
            {t('native.wire_mode')}
            <select
              className="input"
              value={policy.mode}
              disabled={locked}
              onChange={(event) =>
                setDraft({
                  ...policy,
                  mode: event.target.value as WireMode,
                  ...(event.target.value === 'codex' && policy.mode === 'off'
                    ? { compress: true, cookies: true, routing_hint: true }
                    : {}),
                })
              }
            >
              {(['off', 'codex'] as const).map((value) => (
                <option key={value} value={value}>
                  {t(`native.wire_${value}`)}
                </option>
              ))}
            </select>
          </label>
          <p className={styles.hint}>{t(`native.wire_${policy.mode}_hint`)}</p>
          {policy.mode === 'codex' && (
            <>
              <ToggleSwitch
                label={t('native.wire_compress')}
                checked={policy.compress}
                disabled={locked}
                onChange={(compress) => setDraft({ ...policy, compress })}
              />
              <ToggleSwitch
                label={t('native.wire_cookies')}
                checked={policy.cookies}
                disabled={locked}
                onChange={(cookies) => setDraft({ ...policy, cookies })}
              />
              <ToggleSwitch
                label={t('native.wire_routing_hint')}
                checked={policy.routing_hint}
                disabled={locked}
                onChange={(routing_hint) => setDraft({ ...policy, routing_hint })}
              />
            </>
          )}
          <p className={styles.hint}>{t('native.wire_scope_hint')}</p>
          <p className={styles.hint}>{t('native.wire_recommend_hint')}</p>
          {query.data && (
            <p className={styles.hint}>
              {t('native.wire_stats', {
                open: query.data.connections.open,
                reused: query.data.connections.reused,
                dials: query.data.connections.dials,
                compressed: query.data.connections.compressed,
                cookies: query.data.connections.cookies,
              })}
            </p>
          )}
          {query.data?.connections.last_error && (
            <p role="alert">
              {t('native.wire_last_error')}: {query.data.connections.last_error}
            </p>
          )}
          <div className={styles.actions}>
            <Button
              type="button"
              disabled={locked || !draft || !!query.error}
              onClick={() => void save()}
            >
              {t('native.save_wire')}
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
          {t('native.reload_wire')}
        </Button>
      </div>
      {message && <p role="status">{message}</p>}
    </section>
  );
}

// Keys returned by the wire profile endpoint, in display order. Unknown keys
// fall back to the raw key name so a newer backend never hides a value.
const PROFILE_KEYS = [
  'client',
  'captured',
  'tls_ja4_like',
  'http2_settings',
  'body',
  'cookies',
  'websocket',
] as const;
const PROFILE_LABELS: Record<string, string> = {
  client: 'native.wire_profile_client',
  captured: 'native.wire_profile_captured',
  tls_ja4_like: 'native.wire_profile_tls',
  http2_settings: 'native.wire_profile_http2',
  body: 'native.wire_profile_body',
  cookies: 'native.wire_profile_cookies',
  websocket: 'native.wire_profile_websocket',
};

export function WireSetupGuideCard() {
  const { t } = useTranslation();
  const steps = ['affinity', 'headers', 'fingerprint', 'wire'] as const;
  return (
    <Card title={t('native.wire_setup_title')}>
      <div className={styles.page}>
        <p className={styles.hint}>{t('native.wire_setup_hint')}</p>
        <ol className={styles.hint} style={{ margin: 0, paddingLeft: '1.4em' }}>
          {steps.map((step) => (
            <li key={step}>{t(`native.wire_setup_step_${step}`)}</li>
          ))}
        </ol>
        <p className={styles.hint}>{t('native.wire_setup_safety')}</p>
      </div>
    </Card>
  );
}

export function WireProfileCard() {
  const { t } = useTranslation();
  const load = useCallback((signal: AbortSignal) => api.wireProfile(signal), []);
  const query = useNativeQuery(load);
  const ordered = query.data
    ? [
        ...PROFILE_KEYS.filter((k) => k in query.data!).map((k) => [k, query.data![k]] as const),
        ...Object.entries(query.data).filter(
          ([k]) => !(PROFILE_KEYS as readonly string[]).includes(k)
        ),
      ]
    : [];
  return (
    <Card title={t('native.wire_profile_title')}>
      <div className={styles.page}>
        <p className={styles.hint}>{t('native.wire_profile_hint')}</p>
        <QueryState {...query} />
        {query.data && (
          <div className={styles.metrics}>
            {ordered.map(([key, value]) => (
              <div className={styles.metric} key={key}>
                <p>{PROFILE_LABELS[key] ? t(PROFILE_LABELS[key]) : key}</p>
                <strong style={{ fontSize: '0.85em', wordBreak: 'break-word' }}>{value}</strong>
              </div>
            ))}
            <div className={styles.metric}>
              <p>{t('native.wire_profile_verified')}</p>
              <strong style={{ fontSize: '0.85em', wordBreak: 'break-word' }}>
                {t('native.wire_profile_verified_value')}
              </strong>
            </div>
          </div>
        )}
      </div>
    </Card>
  );
}

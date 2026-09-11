import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import {
  nativeControlsApi as api,
  type ControlScope,
  type FingerprintMode,
  type LimitPolicy,
} from '@/services/api/nativeControls';
import { useNativeManagement } from './context';
import { useNativeQuery } from './useNativeQuery';
import { ProfileEditor } from './ProfileEditor';
import { HeaderPolicyEditor } from './HeaderPolicyEditor';
import { WirePolicyEditor } from './WirePolicyEditor';
import { QueryState } from './components';
import styles from './NativeManagement.module.scss';

type Props = { scope?: ControlScope; target?: string; disabled?: boolean };

// The original editors remain responsible for credentials; these optional policies
// are saved independently and never rewrite or export credential secrets.
export function CredentialControls({ scope = 'credential', target = '', disabled }: Props) {
  const { t } = useTranslation(),
    { enabled } = useNativeManagement();
  const [open, setOpen] = useState(false);
  if (
    !enabled('inventory') &&
    !enabled('limits') &&
    !(
      scope === 'credential' &&
      (enabled('fingerprint') || enabled('headers') || enabled('wire'))
    )
  )
    return null;
  return (
    <details className={styles.controls} onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>{t('native.credential_controls')}</summary>
      {!target ? (
        <p className={styles.hint}>{t('native.controls_save_first')}</p>
      ) : (
        open && (
          <ControlEditor
            key={`${scope}:${target}`}
            scope={scope}
            target={target}
            disabled={disabled}
          />
        )
      )}
    </details>
  );
}

function ControlEditor({ scope = 'credential', target = '', disabled }: Props) {
  const { t } = useTranslation(),
    { enabled } = useNativeManagement();
  const limitsEnabled = enabled('limits'),
    fingerprintEnabled = enabled('fingerprint');
  const load = useCallback(
    async (signal: AbortSignal) => {
      const identity = await api.identity(scope, target, signal);
      const [limits, mode] = await Promise.all([
        limitsEnabled ? api.limits(scope, identity.id, signal) : null,
        fingerprintEnabled && identity.fingerprint ? api.fingerprint(identity.id, signal) : null,
      ]);
      return { identity, limits, mode };
    },
    [scope, target, limitsEnabled, fingerprintEnabled]
  );
  const query = useNativeQuery(load);
  const [draft, setDraft] = useState<LimitPolicy | null>(null),
    [modeDraft, setModeDraft] = useState<FingerprintMode | null>(null);
  const [busy, setBusy] = useState(false),
    [message, setMessage] = useState('');
  const controller = useRef<AbortController | null>(null);
  useEffect(() => () => controller.current?.abort(), []);
  const policy = draft ?? query.data?.limits?.policy,
    mode = modeDraft ?? query.data?.mode;
  const valid =
    policy &&
    Number.isInteger(policy.rpm) &&
    policy.rpm >= 0 &&
    policy.rpm <= 100000 &&
    Number.isInteger(policy.concurrency) &&
    policy.concurrency >= 0 &&
    policy.concurrency <= 10000;
  async function save(kind: 'limits' | 'fingerprint') {
    if (!query.data) return;
    const current = new AbortController();
    controller.current = current;
    setBusy(true);
    setMessage('');
    try {
      if (kind === 'limits' && policy && valid) {
        await api.setLimits(scope, query.data.identity.id, policy, current.signal);
        setDraft(null);
      }
      if (kind === 'fingerprint' && mode) {
        await api.setFingerprint(query.data.identity.id, mode, current.signal);
        setModeDraft(null);
      }
      if (!current.signal.aborted) {
        setMessage(t('native.saved'));
        await query.refresh();
      }
    } catch (e) {
      if (!current.signal.aborted) setMessage(String(e));
    } finally {
      if (!current.signal.aborted) setBusy(false);
    }
  }
  return (
    <div className={styles.page}>
      <p className={styles.hint}>{t('native.controls_independent')}</p>
      <QueryState {...query} />
      {limitsEnabled && policy && (
        <>
          <p className={styles.hint}>
            {t(scope === 'client' ? 'native.client_limits_hint' : 'native.credential_limits_hint')}
          </p>
          <div className={styles.controlsGrid}>
            <Input
              label={t('native.limit_rpm')}
              type="number"
              min={0}
              max={100000}
              step={1}
              value={Number.isNaN(policy.rpm) ? '' : policy.rpm}
              disabled={disabled || busy}
              onChange={(e) => setDraft({ ...policy, rpm: e.target.valueAsNumber })}
            />
            <Input
              label={t('native.limit_concurrency')}
              type="number"
              min={0}
              max={10000}
              step={1}
              value={Number.isNaN(policy.concurrency) ? '' : policy.concurrency}
              disabled={disabled || busy}
              onChange={(e) => setDraft({ ...policy, concurrency: e.target.valueAsNumber })}
            />
          </div>
          <p className={styles.hint}>
            {t('native.limit_state', {
              active: query.data?.limits?.active,
              rejected: query.data?.limits?.rejected,
            })}
          </p>
          <Button
            type="button"
            disabled={disabled || busy || query.loading || !draft || !valid}
            onClick={() => void save('limits')}
          >
            {t('native.save_limits')}
          </Button>
        </>
      )}
      {fingerprintEnabled && mode && (
        <>
          <label>
            {t('native.fingerprint_mode')}
            <select
              className="input"
              value={mode}
              disabled={disabled || busy}
              onChange={(e) => setModeDraft(e.target.value as FingerprintMode)}
            >
              {(['off', 'device', 'session', 'full'] as const).map((v) => (
                <option key={v} value={v}>
                  {t(`native.fingerprint_${v}`)}
                </option>
              ))}
            </select>
          </label>
          <p className={styles.hint}>{t(`native.fingerprint_${mode}_hint`)}</p>
          <Button
            type="button"
            disabled={disabled || busy || query.loading || !modeDraft}
            onClick={() => void save('fingerprint')}
          >
            {t('native.save_fingerprint')}
          </Button>
        </>
      )}
      {scope === 'credential' && enabled('headers') && query.data?.identity.headers && (
        <HeaderPolicyEditor
          key={query.data.identity.id}
          target={query.data.identity.id}
          disabled={disabled || busy}
        />
      )}
      {scope === 'credential' && enabled('wire') && query.data?.identity.wire && (
        <WirePolicyEditor
          key={'wire' + query.data.identity.id}
          target={query.data.identity.id}
          disabled={disabled || busy}
        />
      )}
      {enabled('inventory') && query.data && (
        <ProfileEditor
          key={scope + query.data.identity.id}
          scope={scope}
          target={query.data.identity.id}
          disabled={disabled}
        />
      )}
      <div className={styles.actions}>
        <Button
          type="button"
          variant="ghost"
          disabled={busy || query.loading}
          onClick={() => {
            setDraft(null);
            setModeDraft(null);
            void query.refresh();
          }}
        >
          {t('native.reload_controls')}
        </Button>
      </div>
      {message && <p role="status">{message}</p>}
    </div>
  );
}

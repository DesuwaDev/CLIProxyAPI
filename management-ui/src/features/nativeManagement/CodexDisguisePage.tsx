import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { parseDocument } from 'yaml';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { authFilesApi } from '@/services/api/authFiles';
import { configFileApi } from '@/services/api/configFile';
import {
  nativeControlsApi as api,
  type FingerprintMode,
  type HeaderMode,
  type WireMode,
  type WirePolicy,
} from '@/services/api/nativeControls';
import { nativeManagementApi, type NativeModuleName } from '@/services/api/nativeManagement';
import type { AuthFileItem } from '@/types/authFile';
import { useNativeManagement } from './context';
import { Page, QueryState } from './components';
import { useNativeQuery } from './useNativeQuery';
import { WireProfileCard, WireSetupGuideCard } from './WirePolicyEditor';
import styles from './NativeManagement.module.scss';

// One row per Codex OAuth credential with the three per-account policies that
// together decide how the account looks on the wire. Nothing here touches the
// request body, model or account material.
type Row = {
  file: string;
  label: string;
  id: string;
  headers: HeaderMode;
  fingerprint: FingerprintMode;
  wire: WirePolicy;
  lastError: string;
};

const RECOMMENDED = {
  headers: 'client' as HeaderMode,
  fingerprint: 'device' as FingerprintMode,
  wire: { mode: 'codex' as WireMode, compress: true, cookies: true, routing_hint: true },
};
const MODULES: NativeModuleName[] = ['headers', 'fingerprint', 'wire'];

// Provider pre-filter only; the server's identity lookup decides whether the
// credential is a Codex OAuth account (API-key Codex entries report wire=false).
const isCodex = (f: AuthFileItem) => String(f.type ?? f.provider ?? '').toLowerCase() === 'codex';

const rowMatches = (r: Row) =>
  r.headers === RECOMMENDED.headers &&
  r.fingerprint === RECOMMENDED.fingerprint &&
  r.wire.mode === RECOMMENDED.wire.mode &&
  r.wire.compress &&
  r.wire.cookies &&
  r.wire.routing_hint;

async function readAffinity(signal?: AbortSignal): Promise<boolean> {
  const yaml = await configFileApi.fetchConfigYaml();
  if (signal?.aborted) throw new DOMException('aborted', 'AbortError');
  const doc = parseDocument(yaml);
  const v = doc.getIn(['routing', 'session-affinity']);
  return v === true;
}

async function writeAffinity(value: boolean) {
  // Re-read right before writing so we never clobber concurrent edits with a
  // stale document; only the one key is changed.
  const yaml = await configFileApi.fetchConfigYaml();
  const doc = parseDocument(yaml);
  if (value) doc.setIn(['routing', 'session-affinity'], true);
  else if (doc.hasIn(['routing', 'session-affinity']))
    doc.setIn(['routing', 'session-affinity'], false);
  await configFileApi.saveConfigYaml(doc.toString());
}

export function CodexDisguisePage() {
  const { t } = useTranslation();
  const native = useNativeManagement();
  const enabled = (m: NativeModuleName) => native.enabled(m);
  const allModulesOn = MODULES.every(enabled);

  const load = useCallback(
    async (signal: AbortSignal) => {
      const [listing, affinity] = await Promise.all([authFilesApi.list(), readAffinity(signal)]);
      const codex = listing.files.filter(isCodex);
      const rows: Row[] = [];
      for (const f of codex) {
        let identity: Awaited<ReturnType<typeof api.identity>>;
        try {
          identity = await api.identity('credential', f.name, signal);
        } catch {
          continue; // not loaded by the server yet
        }
        if (!identity.wire) continue;
        const [headers, fingerprint, wire] = await Promise.all([
          enabled('headers') ? api.headers(identity.id, signal) : ('off' as HeaderMode),
          enabled('fingerprint')
            ? api.fingerprint(identity.id, signal)
            : ('off' as FingerprintMode),
          enabled('wire')
            ? api.wire(identity.id, signal)
            : {
                policy: { ...RECOMMENDED.wire, mode: 'off' as WireMode },
                connections: { last_error: '' },
              },
        ]);
        rows.push({
          file: f.name,
          label: f.email || f.name,
          id: identity.id,
          headers,
          fingerprint,
          wire: wire.policy,
          lastError: wire.connections.last_error,
        });
      }
      return { rows, affinity, total: codex.length };
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [native.status]
  );
  const query = useNativeQuery(load);
  useHeaderRefresh(query.refresh);

  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState('');
  const controller = useRef<AbortController | null>(null);
  useEffect(() => () => controller.current?.abort(), []);

  const rows = useMemo(() => query.data?.rows ?? [], [query.data]);
  const matching = useMemo(() => rows.filter(rowMatches).length, [rows]);
  const affinity = query.data?.affinity ?? false;
  const allGood = allModulesOn && affinity && rows.length > 0 && matching === rows.length;

  async function run(work: (signal: AbortSignal) => Promise<void>, done?: string) {
    if (busy) return;
    const current = new AbortController();
    controller.current = current;
    setBusy(true);
    setMessage('');
    try {
      await work(current.signal);
      if (current.signal.aborted) return;
      setMessage(done ?? t('native.saved'));
      await native.refresh();
      await query.refresh();
    } catch (error) {
      if (!current.signal.aborted) setMessage(String(error));
    } finally {
      if (!current.signal.aborted) setBusy(false);
    }
  }

  const applyRow = (r: Row, signal: AbortSignal) =>
    Promise.all([
      r.headers === RECOMMENDED.headers ? null : api.setHeaders(r.id, RECOMMENDED.headers, signal),
      r.fingerprint === RECOMMENDED.fingerprint
        ? null
        : api.setFingerprint(r.id, RECOMMENDED.fingerprint, signal),
      rowMatches({ ...r, headers: RECOMMENDED.headers, fingerprint: RECOMMENDED.fingerprint })
        ? null
        : api.setWire(r.id, RECOMMENDED.wire, signal),
    ]);

  const applyAll = () =>
    run(async (signal) => {
      for (const m of MODULES) if (!enabled(m)) await nativeManagementApi.setModule(m, true);
      if (!affinity) await writeAffinity(true);
      // Modules may have just been enabled; policies can be written now.
      for (const r of rows) await applyRow(r, signal);
    }, t('native.disguise_applied'));

  const resetAll = () =>
    run(async (signal) => {
      for (const r of rows) {
        await api.setWire(r.id, { ...r.wire, mode: 'off' }, signal);
        await api.setFingerprint(r.id, 'off', signal);
        await api.setHeaders(r.id, 'off', signal);
      }
    }, t('native.disguise_reset_done'));

  const setRow = (
    r: Row,
    patch: Partial<Pick<Row, 'headers' | 'fingerprint'>> & { wire?: WirePolicy }
  ) =>
    run(async (signal) => {
      if (patch.headers !== undefined) await api.setHeaders(r.id, patch.headers, signal);
      if (patch.fingerprint !== undefined)
        await api.setFingerprint(r.id, patch.fingerprint, signal);
      if (patch.wire !== undefined) await api.setWire(r.id, patch.wire, signal);
    });

  const locked = busy || query.loading;

  return (
    <Page title={t('native.disguise_title')} description={t('native.disguise_hint')}>
      <QueryState loading={query.loading} error={query.error} />

      <Card
        title={t('native.disguise_status_title')}
        extra={
          <div className={styles.actions}>
            <Button type="button" disabled={locked || allGood} onClick={() => void applyAll()}>
              {t('native.disguise_apply_all')}
            </Button>
            <Button
              type="button"
              variant="ghost"
              disabled={locked || rows.length === 0}
              onClick={() => void resetAll()}
            >
              {t('native.disguise_reset_all')}
            </Button>
          </div>
        }
      >
        <div className={styles.page}>
          <p className={allGood ? styles.stateOk : styles.hint} role="status">
            {allGood
              ? t('native.disguise_all_good')
              : t('native.disguise_not_ready', { matching, total: rows.length })}
          </p>
          <div className={styles.metrics}>
            {MODULES.map((m) => (
              <div className={styles.metric} key={m}>
                <p>{t('native.module_' + m)}</p>
                <ToggleSwitch
                  checked={enabled(m)}
                  disabled={locked}
                  ariaLabel={t('native.module_' + m)}
                  label={enabled(m) ? t('native.disguise_on') : t('native.disguise_off')}
                  onChange={(v) =>
                    void run(async () => {
                      await nativeManagementApi.setModule(m, v);
                    })
                  }
                />
              </div>
            ))}
            <div className={styles.metric}>
              <p>{t('native.disguise_affinity')}</p>
              <ToggleSwitch
                checked={affinity}
                disabled={locked}
                ariaLabel={t('native.disguise_affinity')}
                label={affinity ? t('native.disguise_on') : t('native.disguise_off')}
                onChange={(v) => void run(() => writeAffinity(v))}
              />
              <small>{t('native.disguise_affinity_hint')}</small>
            </div>
          </div>
          {message && <p role="status">{message}</p>}
        </div>
      </Card>

      <Card title={t('native.disguise_accounts_title', { count: rows.length })}>
        <p className={styles.hint}>{t('native.disguise_accounts_hint')}</p>
        {!query.loading && rows.length === 0 && (
          <p className={styles.empty}>
            {query.data && query.data.total > 0
              ? t('native.disguise_accounts_not_loaded')
              : t('native.disguise_accounts_empty')}
          </p>
        )}
        {rows.length > 0 && (
          <div className={styles.table}>
            <table>
              <thead>
                <tr>
                  <th>{t('native.disguise_col_account')}</th>
                  <th>{t('native.module_headers')}</th>
                  <th>{t('native.module_fingerprint')}</th>
                  <th>{t('native.module_wire')}</th>
                  <th>{t('native.disguise_col_state')}</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => {
                  const ok = rowMatches(r);
                  return (
                    <tr key={r.id}>
                      <td title={r.file}>{r.label}</td>
                      <td>
                        <select
                          className="input"
                          value={r.headers}
                          disabled={locked || !enabled('headers')}
                          onChange={(e) =>
                            void setRow(r, { headers: e.target.value as HeaderMode })
                          }
                        >
                          {(['off', 'clean', 'client'] as const).map((v) => (
                            <option key={v} value={v}>
                              {t(`native.headers_${v}`)}
                            </option>
                          ))}
                        </select>
                      </td>
                      <td>
                        <select
                          className="input"
                          value={r.fingerprint}
                          disabled={locked || !enabled('fingerprint')}
                          onChange={(e) =>
                            void setRow(r, { fingerprint: e.target.value as FingerprintMode })
                          }
                        >
                          {(['off', 'device', 'session', 'full'] as const).map((v) => (
                            <option key={v} value={v}>
                              {t(`native.fingerprint_${v}`)}
                            </option>
                          ))}
                        </select>
                      </td>
                      <td>
                        <select
                          className="input"
                          value={r.wire.mode}
                          disabled={locked || !enabled('wire')}
                          onChange={(e) =>
                            void setRow(r, {
                              wire:
                                e.target.value === 'codex'
                                  ? RECOMMENDED.wire
                                  : { ...r.wire, mode: 'off' },
                            })
                          }
                        >
                          {(['off', 'codex'] as const).map((v) => (
                            <option key={v} value={v}>
                              {t(`native.wire_${v}`)}
                            </option>
                          ))}
                        </select>
                      </td>
                      <td className={ok ? styles.stateOk : styles.stateBad} title={r.lastError}>
                        {ok ? t('native.disguise_row_ok') : t('native.disguise_row_partial')}
                        {r.lastError ? ' ⚠' : ''}
                      </td>
                      <td>
                        <Button
                          type="button"
                          variant="secondary"
                          disabled={locked || ok}
                          onClick={() => void run((s) => applyRow(r, s).then(() => undefined))}
                        >
                          {t('native.disguise_apply_row')}
                        </Button>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <WireSetupGuideCard />
      {enabled('wire') && <WireProfileCard />}
    </Page>
  );
}

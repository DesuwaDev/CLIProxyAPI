import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import {
  nativeRiskApi as api,
  type RiskConfig,
  type RiskResult,
  type RiskEvent,
  type RiskBlock,
} from '@/services/api/nativeRisk';
import { useNativeQuery } from './useNativeQuery';
import { DataTable, FeatureGate, Page, QueryState } from './components';
import { RiskPolicyEditor, RiskEndpointsEditor } from './RiskEditors';
import { number } from './format';
import styles from './NativeManagement.module.scss';

export function RiskControlPage() {
  return (
    <FeatureGate module="risk">
      <RiskWorkspace />
    </FeatureGate>
  );
}
function RiskWorkspace() {
  const { t } = useTranslation();
  const [tab, setTab] = useState('overview');
  const [filter, setFilter] = useState(''),
    [before, setBefore] = useState(0);
  const load = useCallback(
    async (signal: AbortSignal) => {
      const [config, status, logs, blocks] = await Promise.all([
        api.config(signal),
        api.status(signal),
        api.events(filter, before, signal),
        api.blocks(signal),
      ]);
      return { config, status, logs, blocks };
    },
    [filter, before]
  );
  const query = useNativeQuery(load);
  useHeaderRefresh(query.refresh);
  const [draft, setDraft] = useState<RiskConfig | null>(null),
    [selected, setSelected] = useState<RiskEvent | null>(null);
  const [busy, setBusy] = useState(false),
    [message, setMessage] = useState(''),
    [error, setError] = useState('');
  const [testText, setTestText] = useState(''),
    [testEndpoint, setTestEndpoint] = useState('');
  const [testResult, setTestResult] = useState<RiskResult | null>(null);
  const controller = useRef<AbortController | null>(null);
  useEffect(() => () => controller.current?.abort(), []);
  const c = draft ?? query.data?.config,
    status = query.data?.status;
  async function act(work: (signal: AbortSignal) => Promise<void>) {
    const current = new AbortController();
    controller.current = current;
    setBusy(true);
    setError('');
    setMessage('');
    try {
      await work(current.signal);
      if (!current.signal.aborted) setMessage(t('native.saved'));
    } catch (e) {
      if (!current.signal.aborted) setError(e instanceof Error ? e.message : String(e));
    } finally {
      if (!current.signal.aborted) setBusy(false);
    }
  }
  function save() {
    if (!c) return;
    void act(async (signal) => {
      await api.save(
        {
          ...c,
          providers: c.providers.map((v) => v.trim()).filter(Boolean),
          models: c.models.map((v) => v.trim()).filter(Boolean),
        },
        signal
      );
      if (!signal.aborted) {
        setDraft(null);
        await query.refresh();
      }
    });
  }
  function unblock(block: RiskBlock) {
    void act(async (signal) => {
      await api.unblock(block, signal);
      await query.refresh();
    });
  }
  return (
    <Page title={t('native.risk_title')} description={t('native.risk_hint')}>
      <div className={styles.workspaceBar}>
        <span
          className={styles.statusPill}
          data-tone={status?.mode === 'block' ? 'success' : 'muted'}
        >
          {t('native.risk_effective')}: {t('native.risk_' + (status?.mode || 'off'))}
        </span>
        <span className={styles.hint}>
          {t('native.risk_version', { version: query.data?.config.version ?? 0 })}
        </span>
        <Button
          type="button"
          variant="secondary"
          onClick={() => void query.refresh()}
          disabled={busy}
        >
          {t('common.refresh')}
        </Button>
        {draft && <span className={styles.hint}>{t('native.risk_unsaved')}</span>}
        <Button type="button" onClick={save} disabled={!draft || busy} loading={busy}>
          {t('native.risk_save')}
        </Button>
      </div>
      <nav className={styles.tabs} aria-label={t('native.risk_title')}>
        {['overview', 'policy', 'endpoints', 'events', 'blocks', 'test'].map((v) => (
          <button
            type="button"
            key={v}
            aria-current={tab === v ? 'page' : undefined}
            className={tab === v ? styles.tabActive : ''}
            onClick={() => setTab(v)}
          >
            {t('native.risk_' + v)}
          </button>
        ))}
      </nav>
      <QueryState loading={query.loading} error={error || query.error} />
      {message && (
        <p role="status" className={styles.hint}>
          {message}
        </p>
      )}
      {tab === 'overview' && (
        <>
          <div className={styles.metrics}>
            {[
              [t('native.risk_blocked_24h'), number(status?.blocked ?? 0)],
              [
                t('native.risk_flagged_24h'),
                number((status?.counts.flag ?? 0) + (status?.counts.block ?? 0)),
              ],
              [t('native.risk_upstream_24h'), number(status?.counts.upstream_block ?? 0)],
              [t('native.risk_errors_24h'), number(status?.counts.error ?? 0)],
            ].map(([label, value]) => (
              <div className={styles.metric} key={label}>
                <p>{label}</p>
                <strong>{value}</strong>
              </div>
            ))}
          </div>
          <Card title={t('native.risk_flow')}>
            <div className={styles.flow}>
              {['request', 'local_rules', 'audit_model', 'decision', 'upstream'].map((v, i) => (
                <span key={v}>
                  {i > 0 && <span aria-hidden="true">→ </span>}
                  {t('native.risk_flow_' + v)}
                </span>
              ))}
            </div>
            <p className={styles.hint}>{t('native.risk_mode_hint')}</p>
            <Button type="button" variant="secondary" onClick={() => setTab('policy')}>
              {t('native.risk_configure')}
            </Button>
          </Card>
          <Card title={t('native.risk_runtime')}>
            <div className={styles.inlineStats}>
              <span>
                {t('native.risk_active')}: <strong>{status?.active ?? 0}</strong>
              </span>
              <span>
                {t('native.risk_queue')}:{' '}
                <strong>
                  {status?.queued ?? 0} / {status?.capacity ?? 64}
                </strong>
              </span>
              <span>
                {t('native.risk_dropped')}: <strong>{status?.dropped ?? 0}</strong>
              </span>
              <span>
                {t('native.risk_write_errors')}: <strong>{status?.failedWrites ?? 0}</strong>
              </span>
            </div>
            <DataTable
              headers={[
                t('native.risk_endpoint'),
                t('native.attempts'),
                t('native.failures'),
                t('native.risk_active'),
                t('native.duration'),
                t('native.risk_last_status'),
              ]}
              rows={(query.data?.config.endpoints ?? []).map((e) => {
                const s = status?.endpoints[e.id];
                return {
                  key: e.id,
                  cells: [
                    e.name || e.id,
                    s?.calls ?? 0,
                    s?.errors ?? 0,
                    s?.active ?? 0,
                    s ? s.lastLatency + ' ms' : '—',
                    s?.lastError || (s?.lastStatus ? String(s.lastStatus) : '—'),
                  ],
                };
              })}
            />
          </Card>
        </>
      )}
      {tab === 'policy' && c && <RiskPolicyEditor value={c} onChange={setDraft} disabled={busy} />}
      {tab === 'endpoints' && c && (
        <RiskEndpointsEditor value={c} onChange={setDraft} disabled={busy} />
      )}
      {tab === 'events' && (
        <Card
          title={t('native.risk_events')}
          extra={
            <select
              className="input"
              value={filter}
              aria-label={t('native.risk_events')}
              onChange={(e) => {
                setFilter(e.target.value);
                setBefore(0);
                setSelected(null);
              }}
            >
              <option value="">{t('native.risk_all')}</option>
              {['allow', 'flag', 'block', 'error', 'upstream_block'].map((v) => (
                <option key={v} value={v}>
                  {t('native.risk_result_' + v)}
                </option>
              ))}
            </select>
          }
        >
          <p className={styles.hint}>{t('native.risk_event_privacy')}</p>
          <DataTable
            headers={[
              t('native.time'),
              t('native.model'),
              t('native.risk_decision'),
              t('native.risk_reason'),
              t('native.duration'),
              '',
            ]}
            rows={(query.data?.logs.events ?? []).map((e) => ({
              key: e.id,
              cells: [
                new Date(e.timestamp).toLocaleString(),
                e.model,
                <span
                  className={styles.statusPill}
                  data-tone={
                    e.blocked ? 'danger' : e.result.decision === 'allow' ? 'success' : 'muted'
                  }
                >
                  {t('native.risk_result_' + e.result.decision)}
                  {e.blocked ? ' · 403/503' : ''}
                </span>,
                e.result.errorCode || e.result.category || '—',
                e.result.latency + ' ms',
                <Button type="button" size="sm" variant="secondary" onClick={() => setSelected(e)}>
                  {t('native.risk_details')}
                </Button>,
              ],
            }))}
          />
          <div className={styles.toolbar}>
            <Button
              type="button"
              variant="secondary"
              disabled={!before}
              onClick={() => setBefore(0)}
            >
              {t('native.risk_first_page')}
            </Button>
            <Button
              type="button"
              variant="secondary"
              disabled={!query.data?.logs.next}
              onClick={() => setBefore(query.data?.logs.next ?? 0)}
            >
              {t('native.risk_next_page')}
            </Button>
          </div>
          {selected && (
            <section className={styles.detailPanel}>
              <h3>{t('native.risk_details')}</h3>
              <dl className={styles.detailGrid}>
                {[
                  ['ID', selected.id],
                  ['Trace ID', selected.traceId],
                  [t('native.account'), selected.account],
                  [t('native.key'), selected.keyHash],
                  [t('native.risk_input_hash'), selected.inputHash],
                  [t('native.risk_session_hash'), selected.sessionHash],
                  [t('native.risk_input_chars'), selected.inputChars],
                  [t('native.risk_mode'), t('native.risk_' + selected.mode)],
                  [t('native.risk_endpoint'), selected.result.endpointId],
                  [t('native.risk_rules'), selected.result.ruleId],
                  [t('native.risk_chunks'), selected.result.chunks],
                ].map(([label, value]) => (
                  <div key={label}>
                    <dt>{label}</dt>
                    <dd>{value || '—'}</dd>
                  </div>
                ))}
              </dl>
              <DataTable
                headers={[t('native.risk_category'), t('native.risk_score')]}
                rows={Object.entries(selected.result.scores).map(([k, v]) => ({
                  key: k,
                  cells: [k, v.toFixed(3)],
                }))}
              />
              {selected.traceId && (
                <Link to={'/request-history?trace=' + encodeURIComponent(selected.traceId)}>
                  {t('native.requests')}
                </Link>
              )}
            </section>
          )}
        </Card>
      )}
      {tab === 'blocks' && (
        <Card title={t('native.risk_blocks')}>
          <p className={styles.hint}>{t('native.risk_blocks_hint')}</p>
          {query.data?.blocks.truncated && <p>{t('native.truncated')}</p>}
          <DataTable
            headers={[
              t('native.risk_kind'),
              t('native.risk_input_hash'),
              t('native.risk_reason'),
              t('native.profile_expiry'),
              '',
            ]}
            rows={(query.data?.blocks.blocks ?? []).map((b) => ({
              key: b.kind + b.target,
              cells: [
                b.kind,
                <code>{b.target}</code>,
                b.reason,
                new Date(b.expires).toLocaleString(),
                <Button
                  type="button"
                  variant="secondary"
                  size="sm"
                  disabled={busy}
                  onClick={() => unblock(b)}
                >
                  {t('native.risk_unblock')}
                </Button>,
              ],
            }))}
          />
        </Card>
      )}
      {tab === 'test' && (
        <Card title={t('native.risk_test')}>
          <p className={styles.hint}>{t('native.risk_test_hint')}</p>
          <label>
            {t('native.risk_endpoint')}
            <select
              className="input"
              value={testEndpoint}
              onChange={(e) => setTestEndpoint(e.target.value)}
            >
              <option value="">{t('native.risk_saved_policy')}</option>
              {(query.data?.config.endpoints ?? []).map((e) => (
                <option key={e.id} value={e.id}>
                  {e.name || e.id}
                </option>
              ))}
            </select>
          </label>
          <label>
            {t('native.risk_test_input')}
            <textarea
              className="input"
              rows={10}
              value={testText}
              onChange={(e) => setTestText(e.target.value)}
            />
          </label>
          <div className={styles.toolbar}>
            <Button
              type="button"
              disabled={busy || !testText}
              loading={busy}
              onClick={() =>
                void act(async (signal) => {
                  const r = await api.test(testText, testEndpoint, signal);
                  if (!signal.aborted) setTestResult(r);
                  await query.refresh();
                })
              }
            >
              {t('native.risk_run_test')}
            </Button>
            <Button
              type="button"
              variant="secondary"
              disabled={!busy}
              onClick={() => {
                controller.current?.abort();
                setBusy(false);
              }}
            >
              {t('common.cancel')}
            </Button>
          </div>
          {testResult && (
            <div className={styles.detailPanel}>
              <strong>{t('native.risk_result_' + testResult.decision)}</strong> ·{' '}
              {testResult.errorCode || testResult.category || '—'} · {testResult.latency} ms
              <DataTable
                headers={[t('native.risk_category'), t('native.risk_score')]}
                rows={Object.entries(testResult.scores).map(([k, v]) => ({
                  key: k,
                  cells: [k, v.toFixed(3)],
                }))}
              />
            </div>
          )}
        </Card>
      )}
    </Page>
  );
}

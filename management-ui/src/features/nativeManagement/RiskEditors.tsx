import { makeClientId } from '@/types/visualConfig';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import type { RiskConfig, RiskEndpoint } from '@/services/api/nativeRisk';
import styles from './NativeManagement.module.scss';

type Props = { value: RiskConfig; onChange: (c: RiskConfig) => void; disabled: boolean };
const split = (s: string) => s.split(',');
export function RiskPolicyEditor({ value: c, onChange, disabled }: Props) {
  const { t } = useTranslation();
  const patch = (value: Partial<RiskConfig>) => onChange({ ...c, ...value });
  return (
    <fieldset disabled={disabled} className={styles.fieldset}>
      <Card title={t('native.risk_policy')}>
        <div className={styles.formGrid}>
          <label>
            {t('native.risk_mode')}
            <select
              className="input"
              value={c.mode}
              onChange={(e) => patch({ mode: e.target.value as RiskConfig['mode'] })}
            >
              {(['off', 'observe', 'block'] as const).map((v) => (
                <option value={v} key={v}>
                  {t('native.risk_' + v)}
                </option>
              ))}
            </select>
          </label>
          <label>
            {t('native.risk_strategy')}
            <select
              className="input"
              value={c.strategy}
              onChange={(e) => patch({ strategy: e.target.value as RiskConfig['strategy'] })}
            >
              {['keywords', 'api', 'both'].map((v) => (
                <option value={v} key={v}>
                  {t('native.risk_' + v)}
                </option>
              ))}
            </select>
          </label>
          <label>
            {t('native.risk_providers')}
            <input
              className="input"
              value={c.providers.join(',')}
              onChange={(e) => patch({ providers: split(e.target.value) })}
            />
          </label>
          <label>
            {t('native.risk_model_filter')}
            <select
              className="input"
              value={c.modelFilter}
              onChange={(e) => patch({ modelFilter: e.target.value as RiskConfig['modelFilter'] })}
            >
              {['all', 'include', 'exclude'].map((v) => (
                <option value={v} key={v}>
                  {t('native.risk_' + v)}
                </option>
              ))}
            </select>
          </label>
        </div>
        <label>
          {t('native.risk_models')}
          <textarea
            className="input"
            rows={3}
            value={c.models.join('\n')}
            onChange={(e) => patch({ models: e.target.value.split('\n') })}
          />
        </label>
        <div className={styles.checkGrid}>
          {(
            ['latestTurnOnly', 'failClosed', 'recordPass', 'hashBlock', 'sessionBlock'] as const
          ).map((key) => (
            <label key={key}>
              <input
                type="checkbox"
                checked={c[key]}
                onChange={(e) => patch({ [key]: e.target.checked })}
              />
              {t('native.risk_' + key)}
            </label>
          ))}
        </div>
        <p className={styles.hint}>{t('native.risk_policy_hint')}</p>
        <div className={styles.formGrid}>
          {(['concurrency', 'chunkSize', 'maxInputChars', 'hashTTL', 'sessionTTL'] as const).map(
            (key) => (
              <label key={key}>
                {t('native.risk_' + key)}
                <input
                  className="input"
                  type="number"
                  min={1}
                  value={c[key]}
                  onChange={(e) => patch({ [key]: Number(e.target.value) })}
                />
              </label>
            )
          )}
        </div>
      </Card>
      <Card
        title={t('native.risk_rules')}
        extra={
          <Button
            type="button"
            size="sm"
            variant="secondary"
            onClick={() =>
              patch({
                rules: [
                  ...c.rules,
                  { id: makeClientId(), pattern: '', enabled: true, regex: false },
                ],
              })
            }
          >
            {t('common.add')}
          </Button>
        }
      >
        {c.rules.map((r, i) => (
          <div key={r.id} className={styles.ruleRow}>
            <input
              type="checkbox"
              checked={r.enabled}
              aria-label={t('native.risk_rule_enabled')}
              onChange={(e) =>
                patch({
                  rules: c.rules.map((v, j) => (j === i ? { ...v, enabled: e.target.checked } : v)),
                })
              }
            />
            <input
              className="input"
              value={r.pattern}
              aria-label={t('native.risk_pattern')}
              placeholder={t('native.risk_pattern')}
              onChange={(e) =>
                patch({
                  rules: c.rules.map((v, j) => (j === i ? { ...v, pattern: e.target.value } : v)),
                })
              }
            />
            <label>
              <input
                type="checkbox"
                checked={r.regex}
                onChange={(e) =>
                  patch({
                    rules: c.rules.map((v, j) => (j === i ? { ...v, regex: e.target.checked } : v)),
                  })
                }
              />
              RE2
            </label>
            <Button
              type="button"
              size="sm"
              variant="danger"
              onClick={() => patch({ rules: c.rules.filter((_, j) => j !== i) })}
            >
              {t('common.delete')}
            </Button>
          </div>
        ))}
        {c.rules.length === 0 && <p className={styles.hint}>{t('native.risk_no_rules')}</p>}
      </Card>
      <Card title={t('native.risk_thresholds')}>
        <p className={styles.hint}>{t('native.risk_threshold_hint')}</p>
        {Object.entries(c.thresholds).map(([category, threshold]) => (
          <div className={styles.ruleRow} key={category}>
            <code>{category}</code>
            <input
              className="input"
              type="number"
              min={0}
              max={1}
              step={0.05}
              value={threshold}
              aria-label={category}
              onChange={(e) =>
                patch({ thresholds: { ...c.thresholds, [category]: Number(e.target.value) } })
              }
            />
            <Button
              type="button"
              variant="secondary"
              size="sm"
              onClick={() => {
                const next = { ...c.thresholds };
                delete next[category];
                patch({ thresholds: next });
              }}
            >
              {t('common.delete')}
            </Button>
          </div>
        ))}
        <label>
          {t('native.risk_add_category')}
          <input
            className="input"
            placeholder="violence"
            onKeyDown={(e) => {
              if (e.key === 'Enter' && e.currentTarget.value.trim()) {
                e.preventDefault();
                patch({ thresholds: { ...c.thresholds, [e.currentTarget.value.trim()]: 0.8 } });
                e.currentTarget.value = '';
              }
            }}
          />
        </label>
      </Card>
    </fieldset>
  );
}
export function RiskEndpointsEditor({ value: c, onChange, disabled }: Props) {
  const { t } = useTranslation();
  const update = (id: string, patch: Partial<RiskEndpoint>) =>
    onChange({ ...c, endpoints: c.endpoints.map((e) => (e.id === id ? { ...e, ...patch } : e)) });
  return (
    <fieldset disabled={disabled} className={styles.fieldset}>
      <Card
        title={t('native.risk_endpoints')}
        extra={
          <Button
            type="button"
            variant="secondary"
            onClick={() =>
              onChange({
                ...c,
                endpoints: [
                  ...c.endpoints,
                  {
                    id: makeClientId(),
                    name: '',
                    url: 'http://127.0.0.1:11434/v1',
                    model: 'sileader/qwen3guard:0.6b',
                    protocol: 'qwen3guard',
                    enabled: true,
                    hasToken: false,
                  },
                ],
              })
            }
          >
            {t('common.add')}
          </Button>
        }
      >
        <p className={styles.hint}>{t('native.risk_endpoint_hint')}</p>
        {c.endpoints.map((e, i) => (
          <section className={styles.endpointEditor} key={e.id}>
            <div className={styles.toolbar}>
              <strong>
                #{i + 1} · {e.name || t('native.risk_endpoint')}
              </strong>
              <label>
                <input
                  type="checkbox"
                  checked={e.enabled}
                  onChange={(v) => update(e.id, { enabled: v.target.checked })}
                />
                {t('native.risk_rule_enabled')}
              </label>
              <Button
                type="button"
                size="sm"
                variant="secondary"
                disabled={i === 0}
                onClick={() => {
                  const next = [...c.endpoints];
                  [next[i - 1], next[i]] = [next[i], next[i - 1]];
                  onChange({ ...c, endpoints: next });
                }}
              >
                {t('native.risk_move_up')}
              </Button>
              <Button
                type="button"
                size="sm"
                variant="danger"
                onClick={() =>
                  onChange({ ...c, endpoints: c.endpoints.filter((v) => v.id !== e.id) })
                }
              >
                {t('common.delete')}
              </Button>
            </div>
            <div className={styles.formGrid}>
              <label>
                {t('native.profile_name')}
                <input
                  className="input"
                  value={e.name}
                  onChange={(v) => update(e.id, { name: v.target.value })}
                />
              </label>
              <label>
                {t('native.risk_protocol')}
                <select
                  className="input"
                  value={e.protocol}
                  onChange={(v) =>
                    update(e.id, { protocol: v.target.value as RiskEndpoint['protocol'] })
                  }
                >
                  <option value="qwen3guard">Qwen3Guard</option>
                  <option value="guard_json">{t('native.risk_guard_json')}</option>
                  <option value="moderations">OpenAI Moderations</option>
                </select>
              </label>
              <label>
                Base URL
                <input
                  className="input"
                  value={e.url}
                  onChange={(v) => update(e.id, { url: v.target.value })}
                />
              </label>
              <label>
                {t('native.model')}
                <input
                  className="input"
                  value={e.model}
                  onChange={(v) => update(e.id, { model: v.target.value })}
                />
              </label>
              <label>
                {t('native.risk_token')}
                <input
                  className="input"
                  type="password"
                  autoComplete="new-password"
                  value={e.token || ''}
                  placeholder={
                    e.hasToken ? t('native.risk_token_saved') : t('native.risk_token_optional')
                  }
                  onChange={(v) => update(e.id, { token: v.target.value, clearToken: false })}
                />
              </label>
              <label>
                <input
                  type="checkbox"
                  checked={e.clearToken || false}
                  onChange={(v) => update(e.id, { clearToken: v.target.checked, token: '' })}
                />
                {t('native.risk_clear_token')}
              </label>
            </div>
          </section>
        ))}
      </Card>
    </fieldset>
  );
}

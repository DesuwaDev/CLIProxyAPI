import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { PriceCatalogCard } from './PriceCatalogCard';
import { useCallback, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { nativeManagementApi as api, type PriceRule } from '@/services/api/nativeManagement';
import { useNativeQuery } from './useNativeQuery';
import { FeatureGate, Page, QueryState } from './components';
import styles from './NativeManagement.module.scss';

export function PriceRulesPage() {
  return (
    <FeatureGate module="pricing">
      <PricesContent />
    </FeatureGate>
  );
}
function PricesContent() {
  const { t } = useTranslation(),
    [draft, setDraft] = useState<PriceRule[] | null>(null),
    [busy, setBusy] = useState(false),
    [message, setMessage] = useState('');
  const loader = useCallback((signal: AbortSignal) => api.prices(signal), []),
    query = useNativeQuery(loader);
  useHeaderRefresh(query.refresh, draft === null);
  const rules = draft ?? query.data ?? [];
  function change(index: number, key: keyof PriceRule, value: string | number) {
    setDraft(rules.map((r, i) => (i === index ? { ...r, [key]: value } : r)));
    setMessage('');
  }
  async function save() {
    setBusy(true);
    setMessage('');
    try {
      await api.setPrices(rules);
      setDraft(null);
      await query.refresh();
      setMessage(t('native.saved'));
    } catch (e) {
      setMessage(String(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Page title={t('native.prices')} description={t('native.prices_hint')}>
      <PriceCatalogCard />
      <QueryState {...query} />
      {message && <p role="status">{message}</p>}
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
        className={styles.page}
      >
        <div className={styles.toolbar}>
          <Button
            type="button"
            variant="secondary"
            disabled={busy || query.loading || !!query.error}
            onClick={() =>
              setDraft([
                ...rules,
                {
                  id: 'rule-' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2),
                  provider: '*',
                  model: '',
                  tier: '*',
                  minContext: 0,
                  input: Number.NaN,
                  output: Number.NaN,
                  cacheRead: Number.NaN,
                  cacheWrite: Number.NaN,
                },
              ])
            }
          >
            {t('native.add_rule')}
          </Button>
          <Button
            type="button"
            variant="ghost"
            disabled={busy || draft === null}
            onClick={() => {
              setDraft(null);
              setMessage('');
            }}
          >
            {t('native.discard')}
          </Button>
          <Button type="submit" disabled={busy || draft === null || query.loading || !!query.error}>
            {t('native.save')}
          </Button>
        </div>
        {rules.length === 0 && (
          <Card>
            <p className={styles.empty}>{t('native.no_prices')}</p>
          </Card>
        )}
        {rules.map((r, i) => (
          <Card
            key={r.id}
            title={r.model || t('native.new_rule')}
            extra={
              <Button
                type="button"
                variant="danger"
                size="sm"
                disabled={busy}
                onClick={() => setDraft(rules.filter((_, index) => index !== i))}
              >
                {t('native.remove')}
              </Button>
            }
          >
            <div className={styles.filters}>
              {(['provider', 'model', 'tier'] as const).map((k) => (
                <Input
                  key={k}
                  label={t('native.' + k)}
                  required
                  value={r[k]}
                  disabled={busy}
                  onChange={(e) => change(i, k, e.target.value)}
                  placeholder={k === 'model' ? 'gpt-4.1' : '*'}
                />
              ))}
              {(['minContext', 'input', 'output', 'cacheRead', 'cacheWrite'] as const).map((k) => (
                <Input
                  key={k}
                  label={t('native.price_' + k)}
                  required
                  type="number"
                  min="0"
                  max={k === 'minContext' ? Number.MAX_SAFE_INTEGER : 1000000}
                  step={k === 'minContext' ? '1' : 'any'}
                  value={Number.isNaN(r[k]) ? '' : r[k]}
                  disabled={busy}
                  onChange={(e) => change(i, k, e.target.valueAsNumber)}
                />
              ))}
            </div>
          </Card>
        ))}
      </form>
    </Page>
  );
}

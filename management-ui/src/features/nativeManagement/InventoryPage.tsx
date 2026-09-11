import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useInventory } from './inventoryContext';
import { DataTable, FeatureGate, Page, QueryState } from './components';
import { ProfileEditor } from './ProfileEditor';
import { number, money, date, short } from './format';
import type { InventoryItem } from '@/services/api/nativeInventory';
import styles from './NativeManagement.module.scss';

export function InventoryPage() {
  return (
    <FeatureGate module="inventory">
      <InventoryWorkspace />
    </FeatureGate>
  );
}
function InventoryWorkspace() {
  const { t } = useTranslation(),
    inventory = useInventory();
  const [scope, setScope] = useState('client'),
    [search, setSearch] = useState(''),
    [sort, setSort] = useState('cost');
  const [selected, setSelected] = useState<InventoryItem | null>(null);
  useHeaderRefresh(inventory.refresh);
  const rows = useMemo(
    () =>
      inventory.items
        .filter(
          (v) =>
            v.scope === scope &&
            [
              v.target,
              v.profile.name,
              v.profile.notes,
              ...v.profile.tags,
              v.account?.fileName ?? '',
              v.account?.label ?? '',
            ]
              .join(' ')
              .toLowerCase()
              .includes(search.toLowerCase())
        )
        .sort((a, b) => {
          if (sort === 'requests') return b.totals.requests - a.totals.requests;
          if (sort === 'tokens') return b.totals.tokens - a.totals.tokens;
          if (sort === 'last') return b.totals.last - a.totals.last;
          return b.totals.cost - a.totals.cost;
        }),
    [inventory.items, scope, search, sort]
  );
  return (
    <Page title={t('native.inventory_title')} description={t('native.inventory_hint')}>
      <div className={styles.toolbar}>
        <label>
          {t('native.inventory_scope')}
          <select
            className="input"
            value={scope}
            onChange={(e) => {
              setScope(e.target.value);
              setSelected(null);
            }}
          >
            <option value="client">{t('native.key')}</option>
            <option value="credential">{t('native.account')}</option>
          </select>
        </label>
        <input
          className="input"
          value={search}
          aria-label={t('native.inventory_search')}
          placeholder={t('native.inventory_search')}
          onChange={(e) => setSearch(e.target.value)}
        />
        <select
          className="input"
          value={sort}
          aria-label={t('native.inventory_sort')}
          onChange={(e) => setSort(e.target.value)}
        >
          {['cost', 'requests', 'tokens', 'last'].map((v) => (
            <option key={v} value={v}>
              {t('native.inventory_sort_' + v)}
            </option>
          ))}
        </select>
        <Button type="button" variant="secondary" onClick={() => void inventory.refresh()}>
          {t('common.refresh')}
        </Button>
      </div>
      <QueryState loading={inventory.loading} error={inventory.error} />
      {inventory.truncated && <p className={styles.hint}>{t('native.truncated')}</p>}
      <Card title={t('native.inventory_ranking')}>
        <DataTable
          headers={[
            t('native.profile_name'),
            t('native.inventory_requests'),
            t('native.tokens'),
            t('native.inventory_spent'),
            t('native.profile_budget'),
            t('native.inventory_last'),
            '',
          ]}
          rows={rows.map((v) => ({
            key: v.scope + v.target,
            cells: [
              <div>
                <strong>
                  {v.profile.name || v.account?.fileName || v.account?.label || short(v.target)}
                </strong>
                <small className={styles.rowSub}>
                  {v.profile.notes || v.profile.tags.join(' · ')}
                </small>
              </div>,
              number(v.totals.requests),
              number(v.totals.tokens),
              <div>
                {money(
                  v.totals.unpriced === v.totals.requests && v.totals.requests > 0
                    ? null
                    : v.totals.cost
                )}
                {!!v.totals.unpriced && (
                  <small className={styles.rowSub}>
                    {t('native.unpriced_count', { count: v.totals.unpriced })}
                  </small>
                )}
              </div>,
              v.profile.budget === null ? (
                '—'
              ) : (
                <span>
                  {money(v.profile.budget)} /{' '}
                  {money(v.totals.unpriced ? null : Math.max(0, v.profile.budget - v.totals.cost))}
                </span>
              ),
              date(v.totals.last),
              <div className={styles.inlineStats}>
                <Button type="button" size="sm" variant="secondary" onClick={() => setSelected(v)}>
                  {t('native.profile_edit')}
                </Button>
                <Link
                  to={
                    '/request-history?' +
                    (v.scope === 'client' ? 'keyHash=' : 'account=') +
                    encodeURIComponent(v.target)
                  }
                >
                  {t('native.inventory_details')}
                </Link>
              </div>,
            ],
          }))}
        />
      </Card>
      {selected && (
        <Card
          title={
            selected.profile.name ||
            selected.account?.fileName ||
            selected.account?.label ||
            short(selected.target)
          }
          extra={
            <Button type="button" variant="ghost" onClick={() => setSelected(null)}>
              {t('common.close')}
            </Button>
          }
        >
          <ProfileEditor
            key={selected.scope + selected.target}
            scope={selected.scope}
            target={selected.target}
          />
        </Card>
      )}
    </Page>
  );
}

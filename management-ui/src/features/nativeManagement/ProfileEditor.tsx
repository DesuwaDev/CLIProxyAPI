import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { nativeInventoryApi as api, type InventoryProfile } from '@/services/api/nativeInventory';
import type { ControlScope } from '@/services/api/nativeControls';
import { useNativeQuery } from './useNativeQuery';
import { QueryState } from './components';
import { useInventory } from './inventoryContext';
import { money, number, date } from './format';
import styles from './NativeManagement.module.scss';

export function ProfileEditor({
  scope,
  target,
  disabled,
}: {
  scope: ControlScope;
  target: string;
  disabled?: boolean;
}) {
  const { t } = useTranslation(),
    inventory = useInventory();
  const load = useCallback(
    (signal: AbortSignal) => api.get(scope, target, signal),
    [scope, target]
  );
  const query = useNativeQuery(load);
  const [draft, setDraft] = useState<InventoryProfile | null>(null),
    [busy, setBusy] = useState(false),
    [message, setMessage] = useState('');
  const controller = useRef<AbortController | null>(null);
  useEffect(() => () => controller.current?.abort(), []);
  const p = draft ?? query.data?.item.profile,
    total = query.data?.item.totals;
  const patch = (v: Partial<InventoryProfile>) => {
    if (p) setDraft({ ...p, ...v });
  };
  async function save() {
    if (!p) return;
    const current = new AbortController();
    controller.current = current;
    setBusy(true);
    setMessage('');
    try {
      await api.save(scope, target, p, current.signal);
      if (!current.signal.aborted) {
        setDraft(null);
        await query.refresh();
        await inventory.refresh();
        setMessage(t('native.saved'));
      }
    } catch (e) {
      if (!current.signal.aborted) setMessage(e instanceof Error ? e.message : String(e));
    } finally {
      if (!current.signal.aborted) setBusy(false);
    }
  }
  const localDate = (value: number) =>
    value
      ? new Date(value - new Date(value).getTimezoneOffset() * 60000).toISOString().slice(0, 16)
      : '';
  return (
    <section className={styles.profileEditor}>
      <h3>{t('native.profile_title')}</h3>
      <QueryState {...query} />
      {p && (
        <fieldset disabled={disabled || busy} className={styles.fieldset}>
          <div className={styles.formGrid}>
            <label>
              {t('native.profile_name')}
              <input
                className="input"
                maxLength={160}
                value={p.name}
                onChange={(e) => patch({ name: e.target.value })}
              />
            </label>
            <label>
              {t('native.profile_tags')}
              <input
                className="input"
                value={p.tags.join(',')}
                onChange={(e) => patch({ tags: e.target.value.split(',') })}
              />
            </label>
            <label>
              {t('native.profile_expiry')}
              <input
                className="input"
                type="datetime-local"
                value={localDate(p.expires)}
                onChange={(e) =>
                  patch({ expires: e.target.value ? new Date(e.target.value).getTime() : 0 })
                }
              />
            </label>
            <label>
              {t('native.profile_budget')}
              <input
                className="input"
                type="number"
                min={0}
                step={0.01}
                value={p.budget ?? ''}
                onChange={(e) =>
                  patch({ budget: e.target.value === '' ? null : Number(e.target.value) })
                }
              />
            </label>
          </div>
          <label>
            {t('native.profile_notes')}
            <textarea
              className="input"
              rows={3}
              maxLength={4000}
              value={p.notes}
              onChange={(e) => patch({ notes: e.target.value })}
            />
          </label>
          <label>
            {t('native.profile_models')}
            <textarea
              className="input"
              rows={3}
              value={p.models.join('\n')}
              onChange={(e) => patch({ models: e.target.value.split('\n') })}
            />
          </label>
          <label>
            <input
              type="checkbox"
              checked={p.disabled}
              onChange={(e) => patch({ disabled: e.target.checked })}
            />
            {t('native.profile_disabled')}
          </label>
          <p className={styles.hint}>{t('native.profile_hint')}</p>
          {total && (
            <div className={styles.inlineStats}>
              <span>
                {t('native.inventory_requests')}: <strong>{number(total.requests)}</strong>
              </span>
              <span>
                Token: <strong>{number(total.tokens)}</strong>
              </span>
              <span>
                {t('native.inventory_spent')}:{' '}
                <strong>
                  {money(
                    total.unpriced === total.requests && total.requests > 0 ? null : total.cost
                  )}
                </strong>
              </span>
              <span>
                {t('native.inventory_last')}: <strong>{date(total.last)}</strong>
              </span>
            </div>
          )}
          <Button
            type="button"
            disabled={!draft || query.loading || busy || disabled}
            onClick={() => void save()}
            loading={busy}
          >
            {t('native.profile_save')}
          </Button>
        </fieldset>
      )}
      {message && <p role="status">{message}</p>}
    </section>
  );
}

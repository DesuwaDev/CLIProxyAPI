import { useCallback, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { nativeControlsApi } from '@/services/api/nativeControls';
import { useNativeQuery } from './useNativeQuery';
import { QueryState } from './components';
import { date, number } from './format';
import styles from './NativeManagement.module.scss';

export function CatalogModelPrices({ revision }: { revision: string }) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState(''),
    [model, setModel] = useState('');
  return (
    <section className={styles.page}>
      <h3>{t('native.catalog_lookup')}</h3>
      <p className={styles.hint}>{t('native.catalog_lookup_hint')}</p>
      <form
        className={styles.toolbar}
        onSubmit={(e) => {
          e.preventDefault();
          setModel(draft.trim());
        }}
      >
        <Input
          label={t('native.catalog_model')}
          value={draft}
          required
          maxLength={256}
          onChange={(e) => setDraft(e.target.value)}
        />
        <Button type="submit" disabled={!draft.trim()}>
          {t('native.catalog_search')}
        </Button>
      </form>
      {model && <ModelPrices key={`${model}:${revision}`} model={model} />}
    </section>
  );
}
function ModelPrices({ model }: { model: string }) {
  const { t } = useTranslation();
  const load = useCallback(
    (signal: AbortSignal) => nativeControlsApi.modelPrices(model, signal),
    [model]
  );
  const query = useNativeQuery(load);
  const price = (v: number | null) =>
    v === null ? '—' : v.toLocaleString(undefined, { maximumFractionDigits: 8 });
  return (
    <>
      <QueryState {...query} />
      {query.data && (
        <>
          <p className={styles.hint}>
            {t('native.catalog_loaded_source')}: {query.data.source || '—'} ·{' '}
            {date(query.data.updated)}
          </p>
          {!query.data.rates.length ? (
            <p>{t('native.catalog_empty')}</p>
          ) : (
            <div className={styles.table}>
              <table>
                <thead>
                  <tr>
                    {[
                      'catalog_tier',
                      'catalog_threshold',
                      'input',
                      'output',
                      'cache_read',
                      'cache_write',
                      'cache_write_1h',
                    ].map((k) => (
                      <th key={k}>{t(`native.${k}`)}</th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {query.data.rates.map((r) => (
                    <tr key={`${r.tier}:${r.minContext}`}>
                      <td>{r.tier}</td>
                      <td>{number(r.minContext)}</td>
                      <td>{price(r.input)}</td>
                      <td>{price(r.output)}</td>
                      <td>{price(r.cacheRead)}</td>
                      <td>{price(r.cacheWrite)}</td>
                      <td>{price(r.cacheWrite1h)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </>
  );
}

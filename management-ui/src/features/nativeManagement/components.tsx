import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import type { NativeModuleName } from '@/services/api/nativeManagement';
import { useNativeManagement } from './context';
import styles from './NativeManagement.module.scss';

export function FeatureGate({
  module,
  children,
}: {
  module: NativeModuleName;
  children: ReactNode;
}) {
  const { t } = useTranslation();
  const { loading, error, enabled } = useNativeManagement();
  if (loading) return <p>{t('common.loading')}</p>;
  if (error) return <p className="error-box">{error}</p>;
  if (!enabled(module))
    return (
      <Card title={t('native.disabled')}>
        <p>{t('native.disabled_hint')}</p>
        <Link to="/native-modules">{t('native.modules')}</Link>
      </Card>
    );
  return children;
}
export function Page({
  title,
  description,
  children,
}: {
  title: string;
  description: string;
  children: ReactNode;
}) {
  return (
    <div className={styles.page}>
      <header>
        <h1>{title}</h1>
        <p className={styles.hint}>{description}</p>
      </header>
      {children}
    </div>
  );
}
export function QueryState({ loading, error }: { loading: boolean; error: string }) {
  const { t } = useTranslation();
  return (
    <>
      {error && (
        <div className="error-box" role="alert">
          {error}
        </div>
      )}
      {loading && <p className={styles.hint}>{t('common.loading')}</p>}
    </>
  );
}
export function DataTable({
  headers,
  rows,
}: {
  headers: string[];
  rows: { key: string; cells: ReactNode[] }[];
}) {
  const { t } = useTranslation();
  return (
    <div className={styles.table}>
      <table>
        <thead>
          <tr>
            {headers.map((h, i) => (
              <th key={i}>{h}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.key}>
              {r.cells.map((cell, i) => (
                <td key={i}>{cell}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      {rows.length === 0 && <p className={styles.empty}>{t('native.empty')}</p>}
    </div>
  );
}
export function RangeControl({
  hours,
  onChange,
  onRefresh,
  hintKey = 'native.attempt_note',
}: {
  hours: number;
  onChange: (v: number) => void;
  onRefresh: () => void;
  hintKey?: string;
}) {
  const { t } = useTranslation();
  return (
    <div className={styles.toolbar}>
      <label>
        {t('native.range')}{' '}
        <select className="input" value={hours} onChange={(e) => onChange(Number(e.target.value))}>
          {[1, 24, 168, 720].map((h) => (
            <option key={h} value={h}>
              {t('native.last_hours', { count: h })}
            </option>
          ))}
        </select>
      </label>
      <span className={styles.hint}>{t(hintKey)}</span>
      <Button type="button" variant="secondary" onClick={onRefresh}>
        {t('common.refresh')}
      </Button>
    </div>
  );
}

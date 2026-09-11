import { useCallback, type PropsWithChildren } from 'react';
import { nativeInventoryApi } from '@/services/api/nativeInventory';
import { InventoryContext } from './inventoryContext';
import { useNativeManagement } from './context';
import { useNativeQuery } from './useNativeQuery';

export function InventoryProvider({ children }: PropsWithChildren) {
  const { enabled } = useNativeManagement();
  const active = enabled('inventory');
  const load = useCallback(
    (signal: AbortSignal) =>
      active ? nativeInventoryApi.list(signal) : Promise.resolve({ items: [], truncated: false }),
    [active]
  );
  const query = useNativeQuery(load);
  return (
    <InventoryContext.Provider
      value={{
        items: active ? (query.data?.items ?? []) : [],
        error: query.error,
        loading: query.loading,
        truncated: query.data?.truncated ?? false,
        refresh: query.refresh,
      }}
    >
      {children}
    </InventoryContext.Provider>
  );
}

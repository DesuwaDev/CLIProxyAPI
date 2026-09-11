import { useCallback, type PropsWithChildren } from 'react';
import { useAuthStore } from '@/stores';
import { nativeManagementApi, type NativeModuleName } from '@/services/api/nativeManagement';
import { InventoryProvider } from './InventoryProvider';
import { NativeContext } from './context';
import { useNativeQuery } from './useNativeQuery';

function SessionProvider({ children }: PropsWithChildren) {
  const loader = useCallback(async (signal: AbortSignal) => {
    try {
      return await nativeManagementApi.status(signal);
    } catch (error) {
      if ((error as { status?: number })?.status === 404) return null;
      throw error;
    }
  }, []);
  const { data: status, loading, error, refresh } = useNativeQuery(loader);
  const enabled = useCallback(
    (name: NativeModuleName) => status?.modules.some((m) => m.name === name && m.enabled) === true,
    [status]
  );
  return (
    <NativeContext.Provider value={{ status, loading, error, refresh, enabled }}>
      <InventoryProvider>{children}</InventoryProvider>
    </NativeContext.Provider>
  );
}

// Remount the feature subtree on connection changes; old data never crosses sessions.
export function NativeManagementProvider({ children }: PropsWithChildren) {
  const apiBase = useAuthStore((s) => s.apiBase),
    key = useAuthStore((s) => s.managementKey);
  return <SessionProvider key={JSON.stringify([apiBase, key])}>{children}</SessionProvider>;
}

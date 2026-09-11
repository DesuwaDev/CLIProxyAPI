import { createContext, useContext } from 'react';
import type { NativeModuleName, NativeStatus } from '@/services/api/nativeManagement';

export const NativeContext = createContext<{
  status: NativeStatus | null;
  loading: boolean;
  error: string;
  refresh: () => Promise<void>;
  enabled: (name: NativeModuleName) => boolean;
}>({ status: null, loading: true, error: '', refresh: async () => {}, enabled: () => false });
export const useNativeManagement = () => useContext(NativeContext);

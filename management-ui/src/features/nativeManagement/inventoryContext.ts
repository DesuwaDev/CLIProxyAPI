import { createContext, useContext } from 'react';
import type { InventoryItem } from '@/services/api/nativeInventory';
export const InventoryContext = createContext<{
  items: InventoryItem[];
  error: string;
  loading: boolean;
  truncated: boolean;
  refresh: () => Promise<void>;
}>({ items: [], error: '', loading: false, truncated: false, refresh: async () => {} });
export const useInventory = () => useContext(InventoryContext);

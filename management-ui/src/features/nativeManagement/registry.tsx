import type { NativeModuleName } from '@/services/api/nativeManagement';
import { UsagePage } from './UsagePage';
import { RequestHistoryPage } from './RequestHistoryPage';
import { AccountHealthPage, AccountActionsPage } from './AccountPages';
import { PriceRulesPage } from './PriceRulesPage';
import { ModuleSettingsPage } from './ModuleSettingsPage';
import { RiskControlPage } from './RiskControlPage';
import { InventoryPage } from './InventoryPage';
import { DiagnosticsPage } from './DiagnosticsPage';

export const nativeManagementRoutes = [
  { path: '/risk-control', element: <RiskControlPage /> },
  { path: '/credential-usage', element: <InventoryPage /> },
  { path: '/usage', element: <UsagePage /> },
  { path: '/request-history', element: <RequestHistoryPage /> },
  { path: '/request-diagnostics', element: <DiagnosticsPage /> },
  { path: '/account-health', element: <AccountHealthPage /> },
  { path: '/account-actions', element: <AccountActionsPage /> },
  { path: '/price-rules', element: <PriceRulesPage /> },
  { path: '/native-modules', element: <ModuleSettingsPage /> },
];
export const nativeNavigation: {
  path: string;
  labelKey: string;
  icon: string;
  module: NativeModuleName;
}[] = [
  { path: '/risk-control', labelKey: 'native.risk_title', icon: 'config', module: 'risk' },
  {
    path: '/credential-usage',
    labelKey: 'native.inventory_title',
    icon: 'authFiles',
    module: 'inventory',
  },
  { path: '/usage', labelKey: 'native.usage', icon: 'dashboard', module: 'history' },
  { path: '/request-history', labelKey: 'native.requests', icon: 'logs', module: 'history' },
  {
    path: '/request-diagnostics',
    labelKey: 'native.diagnostics',
    icon: 'logs',
    module: 'diagnostics',
  },
  { path: '/account-health', labelKey: 'native.health', icon: 'quota', module: 'accounts' },
  { path: '/account-actions', labelKey: 'native.actions', icon: 'authFiles', module: 'accounts' },
  { path: '/price-rules', labelKey: 'native.prices', icon: 'config', module: 'pricing' },
];

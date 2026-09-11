export const number = (value: number) => value.toLocaleString();
export const date = (value: number) => (value > 0 ? new Date(value).toLocaleString() : '—');
export const short = (value: string) =>
  value.length > 22 ? value.slice(0, 10) + '…' + value.slice(-7) : value || '—';
export const money = (value: number | null, unknown = '—') =>
  value === null ? unknown : '$' + value.toFixed(6);

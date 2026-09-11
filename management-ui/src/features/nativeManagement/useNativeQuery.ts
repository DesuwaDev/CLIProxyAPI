import { useCallback, useEffect, useRef, useState } from 'react';

export function useNativeQuery<T>(loader: (signal: AbortSignal) => Promise<T>) {
  const [state, setState] = useState<{ data: T | null; loading: boolean; error: string }>({
    data: null,
    loading: true,
    error: '',
  });
  const sequence = useRef(0),
    controller = useRef<AbortController | null>(null);
  const refresh = useCallback(async () => {
    const current = ++sequence.current;
    controller.current?.abort();
    const next = new AbortController();
    controller.current = next;
    setState((s) => ({ ...s, loading: true, error: '' }));
    try {
      const data = await loader(next.signal);
      if (current === sequence.current && !next.signal.aborted)
        setState({ data, loading: false, error: '' });
    } catch (error) {
      if (current === sequence.current && !next.signal.aborted)
        setState({
          data: null,
          loading: false,
          error: error instanceof Error ? error.message : String(error),
        });
    }
  }, [loader]);
  useEffect(() => {
    void refresh();
    return () => {
      controller.current?.abort();
    };
  }, [refresh]);
  return { ...state, refresh };
}

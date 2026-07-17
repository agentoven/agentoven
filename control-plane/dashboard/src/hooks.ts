import { useEffect, useState, useCallback, useRef } from 'react';

const LOCAL_STORAGE_SYNC_EVENT = 'ao-local-storage-sync';

/**
 * State backed by localStorage, so it survives page reloads. Also broadcasts changes to every
 * other mounted instance of this hook using the same key (within the same tab), so sibling
 * components (e.g. a sidebar and a modal) stay in sync without needing a shared context.
 */
export function useLocalStorageState<T>(key: string, initialValue: T) {
  const [value, setValue] = useState<T>(() => {
    try {
      const raw = window.localStorage.getItem(key);
      return raw != null ? (JSON.parse(raw) as T) : initialValue;
    } catch {
      return initialValue;
    }
  });

  useEffect(() => {
    function handleSync(e: Event) {
      const detail = (e as CustomEvent<{ key: string; value: T }>).detail;
      if (detail && detail.key === key) setValue(detail.value);
    }
    window.addEventListener(LOCAL_STORAGE_SYNC_EVENT, handleSync);
    return () => window.removeEventListener(LOCAL_STORAGE_SYNC_EVENT, handleSync);
  }, [key]);

  const setAndPersist = useCallback((next: T | ((prev: T) => T)) => {
    setValue((prev) => {
      const resolved = typeof next === 'function' ? (next as (prev: T) => T)(prev) : next;
      try {
        window.localStorage.setItem(key, JSON.stringify(resolved));
      } catch {
        // ignore quota/serialization errors
      }
      window.dispatchEvent(new CustomEvent(LOCAL_STORAGE_SYNC_EVENT, { detail: { key, value: resolved } }));
      return resolved;
    });
  }, [key]);

  return [value, setAndPersist] as const;
}

export function useAPI<T>(fetcher: () => Promise<T>) {
  const [data, setData] = useState<T | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // Keep a stable ref to the latest fetcher so the callback identity never changes.
  const fetcherRef = useRef(fetcher);
  fetcherRef.current = fetcher;

  const refetch = useCallback(() => {
    setLoading(true);
    setError(null);
    fetcherRef.current()
      .then(setData)
      .catch((e) => setError(e.message))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    refetch();
  }, [refetch]);

  return { data, loading, error, refetch };
}

import { useCallback, useEffect, useRef, useState, type DependencyList } from 'react';

export interface AsyncState<T> {
    data: T | null;
    error: unknown;
    /** True while the first load (or a reload with `reset`) is in flight. */
    loading: boolean;
    /** True while any load is in flight, including background reloads. */
    refreshing: boolean;
    reload: () => Promise<void>;
    setData: (updater: T | ((prev: T | null) => T | null)) => void;
}

/**
 * Runs an async loader on mount and whenever `deps` change. Results of a
 * superseded call are ignored, so quick navigation never shows stale data.
 * `reload()` refreshes in the background (keeps the current data visible).
 */
export function useAsync<T>(loader: () => Promise<T>, deps: DependencyList): AsyncState<T> {
    const [data, setDataState] = useState<T | null>(null);
    const [error, setError] = useState<unknown>(null);
    const [loading, setLoading] = useState(true);
    const [refreshing, setRefreshing] = useState(false);
    const seq = useRef(0);
    const loaderRef = useRef(loader);
    loaderRef.current = loader;

    const run = useCallback(async (background: boolean) => {
        const id = ++seq.current;
        if (background) setRefreshing(true);
        else {
            setLoading(true);
            setError(null);
        }
        try {
            const result = await loaderRef.current();
            if (id !== seq.current) return;
            setDataState(result);
            setError(null);
        } catch (e) {
            if (id !== seq.current) return;
            setError(e);
        } finally {
            if (id === seq.current) {
                setLoading(false);
                setRefreshing(false);
            }
        }
    }, []);

    useEffect(() => {
        void run(false);
        return () => {
            seq.current++;
        };
    }, deps);

    const reload = useCallback(() => run(true), [run]);
    const setData = useCallback((updater: T | ((prev: T | null) => T | null)) => {
        setDataState(prev => (typeof updater === 'function' ? (updater as (p: T | null) => T | null)(prev) : updater));
    }, []);

    return { data, error, loading, refreshing, reload, setData };
}

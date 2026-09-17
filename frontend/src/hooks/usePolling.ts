import { useEffect, useRef } from 'react';

/**
 * Calls `fn` every `intervalMs` while `active` is true. The call is skipped
 * when the tab is hidden, and never overlaps a previous call still running.
 */
export function usePolling(fn: () => Promise<unknown> | void, active: boolean, intervalMs: number): void {
    const fnRef = useRef(fn);
    fnRef.current = fn;

    useEffect(() => {
        if (!active) return;
        let busy = false;
        let disposed = false;
        const tick = async () => {
            if (busy || disposed || document.visibilityState === 'hidden') return;
            busy = true;
            try {
                await fnRef.current();
            } finally {
                busy = false;
            }
        };
        const timer = window.setInterval(() => void tick(), intervalMs);
        return () => {
            disposed = true;
            window.clearInterval(timer);
        };
    }, [active, intervalMs]);
}

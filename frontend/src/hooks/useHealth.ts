import { useEffect, useState } from 'react';
import { getHealth } from '../api/client';
import type { Health } from '../types';

/**
 * Fetches the server health once on mount. `health` stays null until the
 * response arrives (or forever when the request failed, see `error`).
 */
export function useHealth(): { health: Health | null; error: string | null } {
    const [health, setHealth] = useState<Health | null>(null);
    const [error, setError] = useState<string | null>(null);

    useEffect(() => {
        let active = true;
        getHealth()
            .then(h => {
                if (active) setHealth(h);
            })
            .catch((e: unknown) => {
                if (active) setError(e instanceof Error ? e.message : String(e));
            });
        return () => {
            active = false;
        };
    }, []);

    return { health, error };
}

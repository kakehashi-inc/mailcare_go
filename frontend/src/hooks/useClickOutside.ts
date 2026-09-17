import { useEffect, type RefObject } from 'react';

/** Calls `onOutside` when a pointerdown/keydown(Escape) happens outside `ref`. */
export function useClickOutside<T extends HTMLElement>(
    ref: RefObject<T | null>,
    active: boolean,
    onOutside: () => void
): void {
    useEffect(() => {
        if (!active) return;
        function handlePointer(e: MouseEvent) {
            if (ref.current && !ref.current.contains(e.target as Node)) {
                onOutside();
            }
        }
        function handleKey(e: KeyboardEvent) {
            if (e.key === 'Escape') onOutside();
        }
        document.addEventListener('mousedown', handlePointer);
        document.addEventListener('keydown', handleKey);
        return () => {
            document.removeEventListener('mousedown', handlePointer);
            document.removeEventListener('keydown', handleKey);
        };
    }, [ref, active, onOutside]);
}

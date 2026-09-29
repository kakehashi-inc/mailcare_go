import { useEffect, useRef, type RefObject } from 'react';

/** Elements that can take the keyboard focus. */
const FOCUSABLE =
    'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

/**
 * Keeps the keyboard focus inside a modal dialog while it is open: the focus goes to `initialFocus` (else the
 * first focusable element) on opening and back to the element that had it on closing; Tab and Shift+Tab wrap
 * around the first and last focusable elements, and a focus that still leaves the dialog (Shift+Tab out of a
 * radio group, for example) is brought back. Escape calls `onEscape`. Every focusable element counts, not only
 * the buttons, so check boxes, radio buttons and text fields in the dialog stay reachable.
 */
export function useFocusTrap<T extends HTMLElement>(
    ref: RefObject<T | null>,
    active: boolean,
    onEscape: () => void,
    initialFocus?: RefObject<HTMLElement | null>
): void {
    // The latest callback, so that a new one (a busy state changed) does not reset the focus.
    const escape = useRef(onEscape);
    escape.current = onEscape;

    useEffect(() => {
        if (!active) return;
        const previous = document.activeElement as HTMLElement | null;
        const focusable = () => Array.from(ref.current?.querySelectorAll<HTMLElement>(FOCUSABLE) ?? []);
        // A radio group is entered on its checked button, as the browser does.
        const focusOn = (el: HTMLElement) => {
            if (el instanceof HTMLInputElement && el.type === 'radio' && el.name) {
                const checked = ref.current?.querySelector<HTMLElement>(
                    `input[type="radio"][name="${CSS.escape(el.name)}"]:checked`
                );
                (checked ?? el).focus();
                return;
            }
            el.focus();
        };
        const start = initialFocus?.current ?? focusable()[0];
        if (start) focusOn(start);
        let backward = false;
        function onKey(e: KeyboardEvent) {
            if (e.key === 'Escape') {
                escape.current();
                return;
            }
            if (e.key !== 'Tab') return;
            backward = e.shiftKey;
            const items = focusable();
            if (items.length === 0) return;
            const first = items[0];
            const last = items[items.length - 1];
            if (e.shiftKey && document.activeElement === first) {
                e.preventDefault();
                focusOn(last);
            } else if (!e.shiftKey && document.activeElement === last) {
                e.preventDefault();
                focusOn(first);
            }
        }
        function onFocusIn(e: FocusEvent) {
            const root = ref.current;
            if (!root || root.contains(e.target as Node)) return;
            const items = focusable();
            if (items.length > 0) focusOn(backward ? items[items.length - 1] : items[0]);
        }
        document.addEventListener('keydown', onKey);
        document.addEventListener('focusin', onFocusIn);
        return () => {
            document.removeEventListener('keydown', onKey);
            document.removeEventListener('focusin', onFocusIn);
            previous?.focus();
        };
    }, [ref, active, initialFocus]);
}

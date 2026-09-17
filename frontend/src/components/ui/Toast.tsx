import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { TOAST_DURATION_MS } from '../../constants';
import { Icon } from './Icon';

type ToastKind = 'info' | 'error' | 'success';

interface ToastItem {
    id: number;
    kind: ToastKind;
    message: string;
}

interface ToastApi {
    info: (message: string) => void;
    error: (message: string) => void;
    success: (message: string) => void;
}

const ToastContext = createContext<ToastApi | null>(null);

const KIND: Record<ToastKind, { className: string; icon: string }> = {
    info: { className: 'border-info/40 bg-info-soft text-ink', icon: 'info' },
    error: { className: 'border-danger/40 bg-danger-soft text-ink', icon: 'error_outline' },
    success: { className: 'border-success/40 bg-success-soft text-ink', icon: 'check_circle' },
};

export function ToastProvider({ children }: { children: ReactNode }) {
    const { t } = useTranslation();
    const [toasts, setToasts] = useState<ToastItem[]>([]);
    const nextId = useRef(1);

    const remove = useCallback((id: number) => {
        setToasts(prev => prev.filter(x => x.id !== id));
    }, []);

    const push = useCallback(
        (kind: ToastKind, message: string) => {
            const id = nextId.current++;
            setToasts(prev => [...prev, { id, kind, message }]);
            window.setTimeout(() => remove(id), kind === 'error' ? TOAST_DURATION_MS * 2 : TOAST_DURATION_MS);
        },
        [remove]
    );

    const api = useMemo<ToastApi>(
        () => ({
            info: m => push('info', m),
            error: m => push('error', m),
            success: m => push('success', m),
        }),
        [push]
    );

    return (
        <ToastContext.Provider value={api}>
            {children}
            <div
                className='pointer-events-none fixed inset-x-4 bottom-4 z-50 flex flex-col items-center gap-2 sm:inset-x-auto sm:right-4 sm:items-end'
                aria-live='polite'
                aria-atomic='true'
            >
                {toasts.map(item => (
                    <div
                        key={item.id}
                        role={item.kind === 'error' ? 'alert' : 'status'}
                        className={`pointer-events-auto flex w-full max-w-sm items-start gap-3 rounded-lg border px-4 py-3 text-base shadow-lg ${KIND[item.kind].className}`}
                    >
                        <Icon name={KIND[item.kind].icon} className='mt-0.5 text-[20px]' />
                        <span className='min-w-0 flex-1 break-words'>{item.message}</span>
                        <button
                            type='button'
                            onClick={() => remove(item.id)}
                            aria-label={t('common.close')}
                            className='-m-2 flex min-h-tap min-w-tap items-center justify-center rounded-md text-muted hover:text-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-accent'
                        >
                            <Icon name='close' className='text-[20px]' />
                        </button>
                    </div>
                ))}
            </div>
        </ToastContext.Provider>
    );
}

export function useToast(): ToastApi {
    const ctx = useContext(ToastContext);
    if (!ctx) throw new Error('useToast must be used within ToastProvider');
    return ctx;
}

import { useTranslation } from 'react-i18next';

interface SpinnerProps {
    size?: number;
    className?: string;
    label?: string;
}

export function Spinner({ size = 18, className = '', label }: SpinnerProps) {
    const { t } = useTranslation();
    return (
        <span
            className={`inline-block animate-spin rounded-full border-2 border-current border-t-transparent ${className}`}
            style={{ width: size, height: size }}
            role='status'
            aria-label={label ?? t('common.loading')}
        />
    );
}

/** Centered spinner with text, for a page or a card that is still loading. */
export function LoadingBlock({ label }: { label?: string }) {
    const { t } = useTranslation();
    return (
        <div className='flex items-center justify-center gap-3 py-12 text-muted' role='status'>
            <Spinner size={22} label={label ?? t('common.loading')} />
            <span className='text-sm'>{label ?? t('common.loading')}</span>
        </div>
    );
}

export function FullPageSpinner() {
    const { t } = useTranslation();
    return (
        <div className='flex min-h-screen items-center justify-center bg-canvas text-muted'>
            <Spinner size={28} label={t('common.loading')} />
        </div>
    );
}

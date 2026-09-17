import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from './Button';
import { Icon } from './Icon';

interface ErrorStateProps {
    title?: ReactNode;
    message: ReactNode;
    icon?: string;
    onRetry?: () => void;
}

export function ErrorState({ title, message, icon = 'error_outline', onRetry }: ErrorStateProps) {
    const { t } = useTranslation();
    return (
        <div
            role='alert'
            className='flex flex-col items-center rounded-xl border border-danger/40 bg-danger-soft px-4 py-8 text-center'
        >
            <Icon name={icon} className='text-[40px] text-danger' />
            <p className='mt-3 text-base font-semibold text-ink'>{title ?? t('error.title')}</p>
            <p className='mt-1 max-w-md break-words text-sm text-ink'>{message}</p>
            {onRetry && (
                <Button className='mt-4' icon='refresh' onClick={onRetry}>
                    {t('common.retry')}
                </Button>
            )}
        </div>
    );
}

/** Inline (non-blocking) error banner for forms and cards. */
export function InlineError({ message }: { message: ReactNode }) {
    return (
        <div
            role='alert'
            className='flex items-start gap-2 rounded-md border border-danger/40 bg-danger-soft px-3 py-2 text-sm text-ink'
        >
            <Icon name='error_outline' className='mt-0.5 text-[18px] text-danger' />
            <span className='min-w-0 break-words'>{message}</span>
        </div>
    );
}

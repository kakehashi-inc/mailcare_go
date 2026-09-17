import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Icon } from '../components/ui/Icon';

/** Centered card layout shared by the login and setup pages. */
export function AuthShell({
    title,
    description,
    children,
}: {
    title: string;
    description?: string;
    children: ReactNode;
}) {
    const { t } = useTranslation();
    return (
        <div className='flex min-h-screen flex-col bg-canvas'>
            <main id='main' className='flex flex-1 items-start justify-center px-4 pb-12 pt-4 sm:items-center'>
                <div className='w-full max-w-md rounded-2xl border border-line bg-surface p-6 shadow-sm sm:p-8'>
                    <div className='flex items-center gap-2'>
                        <Icon name='mark_email_read' className='text-[28px] text-accent' />
                        <span className='text-xl font-bold text-ink'>{t('app.title')}</span>
                    </div>
                    <h1 className='mt-4 text-2xl font-bold tracking-tight text-ink'>{title}</h1>
                    {description && <p className='mt-1 text-base text-muted'>{description}</p>}
                    <div className='mt-6'>{children}</div>
                </div>
            </main>
        </div>
    );
}

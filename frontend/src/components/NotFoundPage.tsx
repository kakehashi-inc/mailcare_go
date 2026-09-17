import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';

export function NotFoundPage() {
    const { t } = useTranslation();

    return (
        <section className='flex flex-1 items-center justify-center px-4 py-12'>
            <div className='w-full max-w-md rounded-2xl border border-line bg-surface p-8 text-center shadow-sm'>
                <p className='text-sm font-semibold uppercase tracking-widest text-accent'>404</p>
                <h1 className='mt-2 text-2xl font-bold tracking-tight text-ink'>{t('notFound.title')}</h1>
                <p className='mt-3 text-sm leading-relaxed text-muted'>{t('notFound.description')}</p>
                <Link
                    to='/'
                    className='mt-6 inline-flex items-center rounded-md bg-accent px-4 py-2 text-sm font-medium text-accent-contrast transition-colors hover:bg-accent-hover focus:outline-none focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-offset-2 focus-visible:ring-offset-surface'
                >
                    {t('notFound.back')}
                </Link>
            </div>
        </section>
    );
}

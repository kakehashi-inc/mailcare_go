import { useTranslation } from 'react-i18next';

export function HomePage() {
    const { t } = useTranslation();

    return (
        <section className='flex flex-1 items-center justify-center px-4 py-12'>
            <div className='w-full max-w-md rounded-2xl border border-line bg-surface p-8 text-center shadow-sm'>
                <h1 className='text-3xl font-bold tracking-tight text-ink'>{t('home.greeting')}</h1>
                <p className='mt-3 text-sm leading-relaxed text-muted'>{t('home.description')}</p>
            </div>
        </section>
    );
}

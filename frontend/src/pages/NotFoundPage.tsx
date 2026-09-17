import { useTranslation } from 'react-i18next';
import { LinkButton } from '../components/ui/Button';
import { useDocumentTitle } from '../hooks/useDocumentTitle';

export function NotFoundPage() {
    const { t } = useTranslation();
    useDocumentTitle(t('notFound.title'));
    return (
        <section className='flex flex-1 items-center justify-center px-4 py-12'>
            <div className='w-full max-w-md rounded-2xl border border-line bg-surface p-8 text-center shadow-sm'>
                <p className='text-sm font-semibold uppercase tracking-widest text-accent'>404</p>
                <h1 className='mt-2 text-2xl font-bold tracking-tight text-ink'>{t('notFound.title')}</h1>
                <p className='mt-3 text-base leading-relaxed text-muted'>{t('notFound.description')}</p>
                <LinkButton to='/' variant='primary' className='mt-6' icon='home'>
                    {t('notFound.back')}
                </LinkButton>
            </div>
        </section>
    );
}

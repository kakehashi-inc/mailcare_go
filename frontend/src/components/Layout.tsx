import { useTranslation } from 'react-i18next';
import { Outlet } from 'react-router-dom';
import { RouteErrorBoundary } from './ErrorBoundary';
import { Header } from './Header';

/** App shell for signed-in pages: header + routed content. */
export function Layout() {
    const { t } = useTranslation();
    return (
        <div className='flex min-h-screen flex-col'>
            <a
                href='#main'
                className='sr-only focus:not-sr-only focus:fixed focus:left-4 focus:top-4 focus:z-50 focus:rounded-md focus:bg-accent focus:px-4 focus:py-2 focus:text-accent-contrast'
            >
                {t('header.skipToContent')}
            </a>
            <Header />
            <main id='main' className='flex flex-1 flex-col'>
                <RouteErrorBoundary>
                    <Outlet />
                </RouteErrorBoundary>
            </main>
        </div>
    );
}

import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Navigate, useLocation } from 'react-router-dom';
import { FullPageSpinner } from '../components/ui/Spinner';
import { ErrorState } from '../components/ui/ErrorState';
import { safeNextPath } from '../utils/next';
import { useAuth } from './AuthProvider';

/** Renders children only for a signed-in user; otherwise goes to /setup or /login. */
export function RequireAuth({ children }: { children: ReactNode }) {
    const { me, loading, needsSetup } = useAuth();
    const location = useLocation();
    if (loading) return <FullPageSpinner />;
    if (needsSetup) return <Navigate to='/setup' replace />;
    if (!me) {
        const next = location.pathname + location.search;
        return <Navigate to={`/login?next=${encodeURIComponent(next)}`} replace />;
    }
    return <>{children}</>;
}

/** Renders children only for administrators; others see an explanation. */
export function RequireAdmin({ children }: { children: ReactNode }) {
    const { isAdmin } = useAuth();
    const { t } = useTranslation();
    if (!isAdmin) {
        return (
            <div className='mx-auto w-full max-w-3xl px-4 py-8'>
                <ErrorState title={t('error.forbidden')} message={t('error.adminOnly')} icon='lock' />
            </div>
        );
    }
    return <>{children}</>;
}

/** For /login and /setup: sends an already signed-in user to the app. */
export function RedirectIfAuthed({ children }: { children: ReactNode }) {
    const { me, loading, needsSetup } = useAuth();
    const location = useLocation();
    if (loading) return <FullPageSpinner />;
    if (needsSetup && location.pathname !== '/setup') return <Navigate to='/setup' replace />;
    if (!needsSetup && location.pathname === '/setup') return <Navigate to={me ? '/' : '/login'} replace />;
    if (me) {
        return <Navigate to={safeNextPath(new URLSearchParams(location.search).get('next'))} replace />;
    }
    return <>{children}</>;
}

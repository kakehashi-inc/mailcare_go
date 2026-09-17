import { Component, type ErrorInfo, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { useLocation } from 'react-router-dom';
import { Icon } from './ui/Icon';

interface Props {
    children: ReactNode;
    /** Changing this value (e.g. the route) clears a caught error. */
    resetKey?: string;
}

interface State {
    error: Error | null;
}

/** Catches render errors below it and shows a card with a reload link instead of a blank page. */
export class ErrorBoundary extends Component<Props, State> {
    state: State = { error: null };

    static getDerivedStateFromError(error: Error): State {
        return { error };
    }

    componentDidCatch(error: Error, info: ErrorInfo): void {
        console.error('Unhandled render error', error, info.componentStack);
    }

    componentDidUpdate(prev: Props): void {
        if (this.state.error && prev.resetKey !== this.props.resetKey) {
            this.setState({ error: null });
        }
    }

    render(): ReactNode {
        if (this.state.error) return <ErrorCard error={this.state.error} />;
        return this.props.children;
    }
}

function ErrorCard({ error }: { error: Error }) {
    const { t } = useTranslation();
    return (
        <div className='mx-auto w-full max-w-3xl px-4 py-8'>
            <div
                role='alert'
                className='flex flex-col items-center rounded-xl border border-danger/40 bg-danger-soft px-4 py-8 text-center'
            >
                <Icon name='error_outline' className='text-[40px] text-danger' />
                <p className='mt-3 text-base font-semibold text-ink'>{t('error.renderTitle')}</p>
                <p className='mt-1 max-w-md break-words text-sm text-ink'>{t('error.renderHint')}</p>
                <code className='mt-2 max-w-full break-words rounded-md bg-surface px-2 py-1 font-mono text-xs text-muted'>
                    {error.message}
                </code>
                <a
                    href={window.location.href}
                    className='mt-4 inline-flex min-h-tap items-center gap-2 rounded-md bg-accent px-4 py-2 text-base font-medium text-accent-contrast hover:bg-accent-hover focus:outline-none focus-visible:ring-2 focus-visible:ring-accent'
                >
                    <Icon name='refresh' className='text-[20px]' />
                    {t('error.reload')}
                </a>
            </div>
        </div>
    );
}

/** ErrorBoundary that resets whenever the route changes. */
export function RouteErrorBoundary({ children }: { children: ReactNode }) {
    const location = useLocation();
    return <ErrorBoundary resetKey={location.pathname + location.search}>{children}</ErrorBoundary>;
}

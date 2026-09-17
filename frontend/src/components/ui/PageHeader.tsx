import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { Icon } from './Icon';

export interface Crumb {
    label: string;
    to?: string;
}

interface PageHeaderProps {
    title: ReactNode;
    description?: ReactNode;
    crumbs?: Crumb[];
    actions?: ReactNode;
}

export function PageHeader({ title, description, crumbs, actions }: PageHeaderProps) {
    return (
        <div className='mb-6'>
            {crumbs && crumbs.length > 0 && (
                <nav aria-label='breadcrumb' className='mb-2'>
                    <ol className='flex flex-wrap items-center gap-1 text-sm text-muted'>
                        {crumbs.map((c, i) => (
                            <li key={i} className='flex items-center gap-1'>
                                {i > 0 && <Icon name='chevron_right' className='text-[18px]' />}
                                {c.to ? (
                                    <Link
                                        to={c.to}
                                        className='inline-flex min-h-tap items-center rounded px-1 hover:text-ink hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-accent'
                                    >
                                        {c.label}
                                    </Link>
                                ) : (
                                    <span
                                        className='inline-flex min-h-tap items-center px-1 text-ink'
                                        aria-current='page'
                                    >
                                        {c.label}
                                    </span>
                                )}
                            </li>
                        ))}
                    </ol>
                </nav>
            )}
            <div className='flex flex-wrap items-start justify-between gap-3'>
                <div className='min-w-0'>
                    <h1 className='break-words text-2xl font-bold tracking-tight text-ink'>{title}</h1>
                    {description && <p className='mt-1 text-base text-muted'>{description}</p>}
                </div>
                {actions && <div className='flex flex-wrap items-center gap-2'>{actions}</div>}
            </div>
        </div>
    );
}

/** Standard page width and phone gutters. */
export function PageContainer({ children, wide = false }: { children: ReactNode; wide?: boolean }) {
    return (
        <div className={`mx-auto w-full ${wide ? 'max-w-7xl' : 'max-w-5xl'} px-4 py-6 sm:px-6 lg:px-8`}>{children}</div>
    );
}

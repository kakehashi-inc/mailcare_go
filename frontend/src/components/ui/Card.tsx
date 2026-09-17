import type { HTMLAttributes, ReactNode } from 'react';

interface CardProps extends HTMLAttributes<HTMLDivElement> {
    children: ReactNode;
    padded?: boolean;
}

export function Card({ children, padded = true, className = '', ...rest }: CardProps) {
    return (
        <div
            {...rest}
            className={`rounded-xl border border-line bg-surface shadow-sm ${padded ? 'p-4 sm:p-5' : ''} ${className}`}
        >
            {children}
        </div>
    );
}

interface CardHeaderProps {
    title: ReactNode;
    description?: ReactNode;
    actions?: ReactNode;
    icon?: string;
    as?: 'h2' | 'h3';
}

export function CardHeader({ title, description, actions, as: Tag = 'h2' }: CardHeaderProps) {
    return (
        <div className='mb-4 flex flex-wrap items-start justify-between gap-3'>
            <div className='min-w-0'>
                <Tag className='text-lg font-semibold text-ink'>{title}</Tag>
                {description && <p className='mt-1 text-sm text-muted'>{description}</p>}
            </div>
            {actions && <div className='flex flex-wrap items-center gap-2'>{actions}</div>}
        </div>
    );
}

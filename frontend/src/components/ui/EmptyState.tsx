import type { ReactNode } from 'react';

interface EmptyStateProps {
    title: ReactNode;
    description?: ReactNode;
    action?: ReactNode;
}

/** A plain text placeholder for lists and cards that have nothing to show (no decorative icon). */
export function EmptyState({ title, description, action }: EmptyStateProps) {
    return (
        <div className='flex flex-col items-center justify-center rounded-xl border border-dashed border-line px-4 py-8 text-center'>
            <p className='text-base text-ink'>{title}</p>
            {description && <p className='mt-1 max-w-md text-sm text-muted'>{description}</p>}
            {action && <div className='mt-4'>{action}</div>}
        </div>
    );
}

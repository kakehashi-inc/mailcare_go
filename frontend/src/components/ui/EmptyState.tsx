import type { ReactNode } from 'react';
import { Icon } from './Icon';

interface EmptyStateProps {
    icon?: string;
    title: ReactNode;
    description?: ReactNode;
    action?: ReactNode;
}

export function EmptyState({ icon = 'inbox', title, description, action }: EmptyStateProps) {
    return (
        <div className='flex flex-col items-center justify-center rounded-xl border border-dashed border-line px-4 py-10 text-center'>
            <Icon name={icon} className='text-[40px] text-muted' />
            <p className='mt-3 text-base font-medium text-ink'>{title}</p>
            {description && <p className='mt-1 max-w-md text-sm text-muted'>{description}</p>}
            {action && <div className='mt-4'>{action}</div>}
        </div>
    );
}

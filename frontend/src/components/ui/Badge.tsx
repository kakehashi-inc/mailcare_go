import type { ReactNode } from 'react';
import { Icon } from './Icon';

export type BadgeTone = 'neutral' | 'success' | 'warning' | 'danger' | 'info' | 'accent';

const TONE: Record<BadgeTone, string> = {
    neutral: 'bg-well text-ink border-line',
    success: 'bg-success-soft text-success border-success/30',
    warning: 'bg-warning-soft text-warning border-warning/30',
    danger: 'bg-danger-soft text-danger border-danger/30',
    info: 'bg-info-soft text-info border-info/30',
    accent: 'bg-accent-soft text-accent border-accent/30',
};

interface BadgeProps {
    tone?: BadgeTone;
    icon?: string;
    children: ReactNode;
    className?: string;
    title?: string;
}

/** Status chip that always pairs color with an icon and text. */
export function Badge({ tone = 'neutral', icon, children, className = '', title }: BadgeProps) {
    return (
        <span
            title={title}
            className={`inline-flex max-w-full min-w-0 items-center gap-1 whitespace-nowrap rounded-full border px-2.5 py-0.5 text-sm font-medium ${TONE[tone]} ${className}`}
        >
            {icon && <Icon name={icon} className='shrink-0 text-[16px]' />}
            <span className='min-w-0 truncate'>{children}</span>
        </span>
    );
}

import type { ReactNode } from 'react';
import { Icon } from './Icon';

type Tone = 'info' | 'success' | 'warning' | 'danger';

const TONE: Record<Tone, { className: string; icon: string }> = {
    info: { className: 'border-info/40 bg-info-soft', icon: 'info' },
    success: { className: 'border-success/40 bg-success-soft', icon: 'check_circle' },
    warning: { className: 'border-warning/40 bg-warning-soft', icon: 'warning' },
    danger: { className: 'border-danger/40 bg-danger-soft', icon: 'error_outline' },
};

const ICON_TONE: Record<Tone, string> = {
    info: 'text-info',
    success: 'text-success',
    warning: 'text-warning',
    danger: 'text-danger',
};

interface AlertProps {
    tone?: Tone;
    title?: ReactNode;
    children?: ReactNode;
    actions?: ReactNode;
    className?: string;
}

/** Static banner with icon + text (never color alone). */
export function Alert({ tone = 'info', title, children, actions, className = '' }: AlertProps) {
    return (
        <div
            role={tone === 'danger' ? 'alert' : 'status'}
            className={`flex items-start gap-3 rounded-lg border px-4 py-3 ${TONE[tone].className} ${className}`}
        >
            <Icon name={TONE[tone].icon} className={`mt-0.5 text-[22px] ${ICON_TONE[tone]}`} />
            <div className='min-w-0 flex-1 text-base text-ink'>
                {title && <p className='font-semibold'>{title}</p>}
                {children && <div className={title ? 'mt-1 text-sm' : ''}>{children}</div>}
            </div>
            {actions && <div className='shrink-0'>{actions}</div>}
        </div>
    );
}

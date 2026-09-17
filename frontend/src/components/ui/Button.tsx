import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { Icon } from './Icon';
import { Spinner } from './Spinner';

export type ButtonVariant = 'primary' | 'secondary' | 'danger' | 'ghost' | 'link';
export type ButtonSize = 'md' | 'sm';

const BASE =
    'inline-flex items-center justify-center gap-2 rounded-md font-medium transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-offset-2 focus-visible:ring-offset-surface disabled:cursor-not-allowed disabled:opacity-50';

const VARIANT: Record<ButtonVariant, string> = {
    primary: 'bg-accent text-accent-contrast hover:bg-accent-hover',
    secondary: 'border border-line bg-surface text-ink hover:bg-well',
    danger: 'bg-danger text-danger-contrast hover:bg-danger-hover',
    ghost: 'text-ink hover:bg-well',
    link: 'text-accent underline-offset-2 hover:underline',
};

const SIZE: Record<ButtonSize, string> = {
    // 44px minimum tap target in both sizes; "sm" only tightens the padding.
    md: 'min-h-tap px-4 py-2 text-base',
    sm: 'min-h-tap px-3 py-1.5 text-sm',
};

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
    variant?: ButtonVariant;
    size?: ButtonSize;
    icon?: string;
    loading?: boolean;
    block?: boolean;
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
    { variant = 'secondary', size = 'md', icon, loading = false, block = false, className = '', children, ...rest },
    ref
) {
    return (
        <button
            ref={ref}
            type='button'
            {...rest}
            disabled={rest.disabled || loading}
            aria-busy={loading || undefined}
            className={`${BASE} ${VARIANT[variant]} ${SIZE[size]} ${block ? 'w-full' : ''} ${className}`}
        >
            {loading ? <Spinner size={16} /> : icon ? <Icon name={icon} className='text-[20px]' /> : null}
            {children}
        </button>
    );
});

interface LinkButtonProps {
    to: string;
    variant?: ButtonVariant;
    size?: ButtonSize;
    icon?: string;
    className?: string;
    children: ReactNode;
    'aria-label'?: string;
}

/** A router link styled as a button. */
export function LinkButton({
    to,
    variant = 'secondary',
    size = 'md',
    icon,
    className = '',
    children,
    ...rest
}: LinkButtonProps) {
    return (
        <Link to={to} className={`${BASE} ${VARIANT[variant]} ${SIZE[size]} ${className}`} {...rest}>
            {icon && <Icon name={icon} className='text-[20px]' />}
            {children}
        </Link>
    );
}

interface IconButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
    icon: string;
    /** Required: the icon alone carries no text. */
    label: string;
    loading?: boolean;
}

/** Square 44px button with only an icon; the label is exposed to assistive tech and as a tooltip. */
export function IconButton({ icon, label, loading = false, className = '', ...rest }: IconButtonProps) {
    return (
        <button
            type='button'
            {...rest}
            disabled={rest.disabled || loading}
            aria-label={label}
            title={label}
            className={`inline-flex min-h-tap min-w-tap items-center justify-center rounded-md text-muted transition-colors hover:bg-well hover:text-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-accent disabled:cursor-not-allowed disabled:opacity-50 ${className}`}
        >
            {loading ? <Spinner size={16} /> : <Icon name={icon} className='text-[22px]' />}
        </button>
    );
}

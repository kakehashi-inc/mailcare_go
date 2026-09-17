import {
    forwardRef,
    useId,
    type InputHTMLAttributes,
    type ReactNode,
    type SelectHTMLAttributes,
    type TextareaHTMLAttributes,
} from 'react';

const CONTROL =
    'w-full min-h-tap rounded-md border border-line bg-surface px-3 py-2 text-base text-ink placeholder:text-muted focus:border-accent focus:outline-none focus:ring-2 focus:ring-accent/40 disabled:cursor-not-allowed disabled:opacity-60';

interface ShellProps {
    id: string;
    label: ReactNode;
    hint?: ReactNode;
    error?: ReactNode;
    required?: boolean;
    children: ReactNode;
    className?: string;
}

function Shell({ id, label, hint, error, required, children, className = '' }: ShellProps) {
    return (
        <div className={className}>
            <label htmlFor={id} className='mb-1 block text-sm font-medium text-ink'>
                {label}
                {required && (
                    <span className='ml-1 text-danger' aria-hidden='true'>
                        *
                    </span>
                )}
            </label>
            {children}
            {hint && !error && (
                <p id={`${id}-hint`} className='mt-1 text-sm text-muted'>
                    {hint}
                </p>
            )}
            {error && (
                <p id={`${id}-error`} className='mt-1 text-sm text-danger' role='alert'>
                    {error}
                </p>
            )}
        </div>
    );
}

interface InputFieldProps extends Omit<InputHTMLAttributes<HTMLInputElement>, 'id'> {
    label: ReactNode;
    hint?: ReactNode;
    error?: ReactNode;
    wrapperClassName?: string;
}

export const InputField = forwardRef<HTMLInputElement, InputFieldProps>(function InputField(
    { label, hint, error, required, wrapperClassName, className = '', ...rest },
    ref
) {
    const id = useId();
    return (
        <Shell id={id} label={label} hint={hint} error={error} required={required} className={wrapperClassName}>
            <input
                ref={ref}
                id={id}
                required={required}
                aria-invalid={error ? true : undefined}
                aria-describedby={error ? `${id}-error` : hint ? `${id}-hint` : undefined}
                className={`${CONTROL} ${className}`}
                {...rest}
            />
        </Shell>
    );
});

interface SelectFieldProps extends Omit<SelectHTMLAttributes<HTMLSelectElement>, 'id'> {
    label: ReactNode;
    hint?: ReactNode;
    error?: ReactNode;
    wrapperClassName?: string;
}

export function SelectField({
    label,
    hint,
    error,
    required,
    wrapperClassName,
    className = '',
    children,
    ...rest
}: SelectFieldProps) {
    const id = useId();
    return (
        <Shell id={id} label={label} hint={hint} error={error} required={required} className={wrapperClassName}>
            <select
                id={id}
                required={required}
                aria-invalid={error ? true : undefined}
                aria-describedby={error ? `${id}-error` : hint ? `${id}-hint` : undefined}
                className={`${CONTROL} ${className}`}
                {...rest}
            >
                {children}
            </select>
        </Shell>
    );
}

interface TextareaFieldProps extends Omit<TextareaHTMLAttributes<HTMLTextAreaElement>, 'id'> {
    label: ReactNode;
    hint?: ReactNode;
    error?: ReactNode;
    wrapperClassName?: string;
}

export function TextareaField({
    label,
    hint,
    error,
    required,
    wrapperClassName,
    className = '',
    ...rest
}: TextareaFieldProps) {
    const id = useId();
    return (
        <Shell id={id} label={label} hint={hint} error={error} required={required} className={wrapperClassName}>
            <textarea
                id={id}
                required={required}
                aria-invalid={error ? true : undefined}
                aria-describedby={error ? `${id}-error` : hint ? `${id}-hint` : undefined}
                className={`${CONTROL} ${className}`}
                {...rest}
            />
        </Shell>
    );
}

interface CheckboxFieldProps extends Omit<InputHTMLAttributes<HTMLInputElement>, 'id' | 'type'> {
    label: ReactNode;
    hint?: ReactNode;
}

export function CheckboxField({ label, hint, className = '', ...rest }: CheckboxFieldProps) {
    const id = useId();
    return (
        <div className={`flex items-start gap-3 ${className}`}>
            <span className='flex min-h-tap items-center'>
                <input
                    id={id}
                    type='checkbox'
                    className='h-5 w-5 rounded border-line accent-accent focus:outline-none focus-visible:ring-2 focus-visible:ring-accent'
                    aria-describedby={hint ? `${id}-hint` : undefined}
                    {...rest}
                />
            </span>
            <span className='flex min-h-tap flex-col justify-center'>
                <label htmlFor={id} className='text-base text-ink'>
                    {label}
                </label>
                {hint && (
                    <span id={`${id}-hint`} className='text-sm text-muted'>
                        {hint}
                    </span>
                )}
            </span>
        </div>
    );
}

interface ToggleFieldProps {
    label: ReactNode;
    hint?: ReactNode;
    checked: boolean;
    onChange: (checked: boolean) => void;
    disabled?: boolean;
}

/** A switch that also reads as on/off text for people who cannot see the color. */
export function ToggleField({ label, hint, checked, onChange, disabled }: ToggleFieldProps) {
    const id = useId();
    return (
        <div className='flex items-start justify-between gap-4'>
            <span className='flex min-h-tap flex-col justify-center'>
                <span id={`${id}-label`} className='text-base font-medium text-ink'>
                    {label}
                </span>
                {hint && <span className='text-sm text-muted'>{hint}</span>}
            </span>
            <button
                id={id}
                type='button'
                role='switch'
                aria-checked={checked}
                aria-labelledby={`${id}-label`}
                disabled={disabled}
                onClick={() => onChange(!checked)}
                className={`relative inline-flex h-11 w-20 shrink-0 items-center rounded-full border transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-offset-2 focus-visible:ring-offset-surface disabled:cursor-not-allowed disabled:opacity-50 ${
                    checked ? 'border-accent bg-accent' : 'border-line bg-well'
                }`}
            >
                <span
                    className={`absolute top-1 h-8 w-8 rounded-full bg-surface shadow transition-transform ${
                        checked ? 'translate-x-11' : 'translate-x-1'
                    }`}
                />
                <span className='sr-only'>{checked ? 'on' : 'off'}</span>
            </button>
        </div>
    );
}

export const controlClass = CONTROL;

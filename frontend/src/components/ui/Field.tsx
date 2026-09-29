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

/**
 * Width of a control whose value is short, applied to the control only (the label, hint and error keep the
 * full width of the container, so a hint never wraps at the width of a narrow input). "short" suits numbers
 * and times, "medium" names and choices; without it the control fills its container.
 */
export type ControlWidth = 'short' | 'medium';

export const CONTROL_WIDTH: Record<ControlWidth, string> = {
    short: 'max-w-xs',
    medium: 'max-w-md',
};

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

interface InputFieldProps extends Omit<InputHTMLAttributes<HTMLInputElement>, 'id' | 'width'> {
    label: ReactNode;
    hint?: ReactNode;
    error?: ReactNode;
    width?: ControlWidth;
    wrapperClassName?: string;
}

export const InputField = forwardRef<HTMLInputElement, InputFieldProps>(function InputField(
    { label, hint, error, required, width, wrapperClassName, className = '', ...rest },
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
                className={`${CONTROL} ${width ? CONTROL_WIDTH[width] : ''} ${className}`}
                {...rest}
            />
        </Shell>
    );
});

interface SelectFieldProps extends Omit<SelectHTMLAttributes<HTMLSelectElement>, 'id'> {
    label: ReactNode;
    hint?: ReactNode;
    error?: ReactNode;
    width?: ControlWidth;
    wrapperClassName?: string;
}

export function SelectField({
    label,
    hint,
    error,
    required,
    width,
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
                className={`${CONTROL} ${width ? CONTROL_WIDTH[width] : ''} ${className}`}
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

interface RadioGroupFieldProps<T extends string> {
    label: ReactNode;
    /** The choices in display order. */
    options: readonly { value: T; label: ReactNode }[];
    /** The chosen value; '' = none chosen yet. */
    value: T | '';
    onChange: (value: T) => void;
    required?: boolean;
    hint?: ReactNode;
    error?: ReactNode;
    disabled?: boolean;
    className?: string;
}

/**
 * A group of radio buttons with its label as the legend. Each choice sits on its own row, the button left of its
 * label (like CheckboxField), and the whole row is the tap target.
 */
export function RadioGroupField<T extends string>({
    label,
    options,
    value,
    onChange,
    required,
    hint,
    error,
    disabled,
    className = '',
}: RadioGroupFieldProps<T>) {
    const id = useId();
    return (
        <fieldset
            className={className}
            aria-describedby={error ? `${id}-error` : hint ? `${id}-hint` : undefined}
            aria-invalid={error ? true : undefined}
            disabled={disabled}
        >
            <legend className='mb-1 block text-sm font-medium text-ink'>
                {label}
                {required && (
                    <span className='ml-1 text-danger' aria-hidden='true'>
                        *
                    </span>
                )}
            </legend>
            <div className='flex flex-col'>
                {options.map(o => (
                    <label
                        key={o.value}
                        className='flex min-h-tap cursor-pointer items-center gap-3 rounded-md px-1 text-base text-ink hover:bg-well'
                    >
                        <input
                            type='radio'
                            name={id}
                            value={o.value}
                            checked={value === o.value}
                            required={required}
                            onChange={() => onChange(o.value)}
                            className='h-5 w-5 shrink-0 accent-accent focus:outline-none focus-visible:ring-2 focus-visible:ring-accent'
                        />
                        <span>{o.label}</span>
                    </label>
                ))}
            </div>
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
        </fieldset>
    );
}

interface ToggleFieldProps {
    label: ReactNode;
    hint?: ReactNode;
    checked: boolean;
    onChange: (checked: boolean) => void;
    disabled?: boolean;
}

/**
 * A switch that also reads as on/off text for people who cannot see the color. The switch sits on the left,
 * right next to its label (like CheckboxField), so it is always clear which setting it belongs to; clicking
 * the label toggles it too.
 */
export function ToggleField({ label, hint, checked, onChange, disabled }: ToggleFieldProps) {
    const id = useId();
    return (
        <div className='flex items-start gap-3'>
            <button
                id={id}
                type='button'
                role='switch'
                aria-checked={checked}
                aria-labelledby={`${id}-label`}
                aria-describedby={hint ? `${id}-hint` : undefined}
                disabled={disabled}
                onClick={() => onChange(!checked)}
                className='group inline-flex min-h-tap shrink-0 items-center focus:outline-none disabled:cursor-not-allowed disabled:opacity-50'
            >
                <span
                    aria-hidden='true'
                    className={`relative inline-flex h-6 w-11 items-center rounded-full border transition-colors group-focus-visible:ring-2 group-focus-visible:ring-accent group-focus-visible:ring-offset-2 group-focus-visible:ring-offset-surface ${
                        checked ? 'border-accent bg-accent' : 'border-line bg-well'
                    }`}
                >
                    <span
                        className={`absolute h-5 w-5 rounded-full bg-surface shadow transition-transform ${
                            checked ? 'translate-x-[1.25rem]' : 'translate-x-0.5'
                        }`}
                    />
                </span>
                <span className='sr-only'>{checked ? 'on' : 'off'}</span>
            </button>
            <span className='flex min-h-tap flex-col justify-center'>
                <label
                    id={`${id}-label`}
                    htmlFor={id}
                    className={`text-base font-medium text-ink ${disabled ? 'cursor-not-allowed' : 'cursor-pointer'}`}
                >
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

export const controlClass = CONTROL;

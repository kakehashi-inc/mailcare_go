interface IconProps {
    /** Material Icons ligature name, e.g. "check_circle". */
    name: string;
    className?: string;
    /** Accessible label; omitted icons are decorative (aria-hidden). */
    label?: string;
}

/** Material Icons (outlined) glyph. Decorative unless a label is given. */
export function Icon({ name, className = '', label }: IconProps) {
    return (
        <span
            className={`material-icons-outlined select-none align-middle ${className}`}
            aria-hidden={label ? undefined : 'true'}
            role={label ? 'img' : undefined}
            aria-label={label}
        >
            {name}
        </span>
    );
}

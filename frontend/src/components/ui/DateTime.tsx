import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { formatDateTime, formatRelative } from '../../utils/format';

interface DateTimeProps {
    value: string | null | undefined;
    /** Show "3 minutes ago" with the exact time as a tooltip. */
    relative?: boolean;
    className?: string;
    /** Text shown when the value is empty. Defaults to a dash. */
    empty?: string;
}

/** Date/time rendered in the active language. Relative mode refreshes every minute. */
export function DateTime({ value, relative = false, className = '', empty }: DateTimeProps) {
    const { i18n } = useTranslation();
    void i18n.language; // re-render on language change
    const [now, setNow] = useState(() => Date.now());
    useEffect(() => {
        if (!relative) return;
        const timer = window.setInterval(() => setNow(Date.now()), 60000);
        return () => window.clearInterval(timer);
    }, [relative]);

    if (!value) return <span className={`text-muted ${className}`}>{empty ?? '-'}</span>;
    const absolute = formatDateTime(value);
    if (relative) {
        return (
            <time dateTime={value} title={absolute} className={className}>
                {formatRelative(value, now)}
            </time>
        );
    }
    return (
        <time dateTime={value} className={className}>
            {absolute}
        </time>
    );
}

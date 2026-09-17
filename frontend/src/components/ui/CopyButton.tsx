import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Icon } from './Icon';

interface CopyButtonProps {
    text: string;
    /** Accessible label describing what is copied. */
    label?: string;
    size?: 'sm' | 'md';
    className?: string;
}

/** Copies text to the clipboard and confirms with an icon + text change. */
export function CopyButton({ text, label, size = 'sm', className = '' }: CopyButtonProps) {
    const { t } = useTranslation();
    const [copied, setCopied] = useState(false);
    const [failed, setFailed] = useState(false);

    async function copy() {
        try {
            await navigator.clipboard.writeText(text);
            setCopied(true);
            setFailed(false);
            window.setTimeout(() => setCopied(false), 2000);
        } catch {
            setFailed(true);
            window.setTimeout(() => setFailed(false), 2000);
        }
    }

    const title = label ?? t('common.copy');
    return (
        <button
            type='button'
            onClick={copy}
            aria-label={copied ? t('common.copied') : title}
            title={title}
            className={`inline-flex min-h-tap items-center gap-1 rounded-md px-2 text-muted hover:bg-well hover:text-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-accent ${
                size === 'sm' ? 'text-sm' : 'text-base'
            } ${className}`}
        >
            <Icon name={copied ? 'check' : failed ? 'error_outline' : 'content_copy'} className='text-[18px]' />
            <span aria-live='polite'>
                {copied ? t('common.copied') : failed ? t('common.copyFailed') : t('common.copy')}
            </span>
        </button>
    );
}

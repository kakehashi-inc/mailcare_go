import { useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { SUPPORTED_LANGS } from '../constants';
import { useClickOutside } from '../hooks/useClickOutside';
import { changeLanguage, currentLang } from '../i18n/i18n';

/** Globe icon button that opens a dropdown to pick the UI language. */
export function LanguageSwitch() {
    const { t, i18n } = useTranslation();
    // Reading i18n.language subscribes this component to language changes.
    void i18n.language;
    const current = currentLang();
    const [open, setOpen] = useState(false);
    const ref = useRef<HTMLDivElement>(null);
    useClickOutside(ref, open, () => setOpen(false));

    return (
        <div className='relative' ref={ref}>
            <button
                type='button'
                aria-label={t('header.language')}
                title={t('header.language')}
                aria-haspopup='menu'
                aria-expanded={open}
                onClick={() => setOpen(v => !v)}
                className='flex min-h-tap min-w-tap items-center justify-center rounded-md text-muted hover:bg-well hover:text-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-accent'
            >
                <span className='material-icons-outlined text-[22px]' aria-hidden='true'>
                    language
                </span>
            </button>
            {open && (
                <div
                    role='menu'
                    className='absolute right-0 z-20 mt-1 min-w-40 overflow-hidden rounded-md border border-line bg-surface py-1 shadow-xl'
                >
                    {SUPPORTED_LANGS.map(code => (
                        <button
                            key={code}
                            type='button'
                            role='menuitemradio'
                            aria-checked={current === code}
                            onClick={() => {
                                setOpen(false);
                                void changeLanguage(code);
                            }}
                            className={`flex min-h-tap w-full items-center justify-between gap-3 px-4 py-2 text-left text-base hover:bg-well focus:bg-well focus:outline-none ${
                                current === code ? 'text-ink' : 'text-muted'
                            }`}
                        >
                            {t(`lang.${code}`)}
                            {current === code && (
                                <span className='material-icons-outlined text-base text-accent' aria-hidden='true'>
                                    check
                                </span>
                            )}
                        </button>
                    ))}
                </div>
            )}
        </div>
    );
}

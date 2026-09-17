import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { useHealth } from '../hooks/useHealth';
import { LanguageSwitch } from './LanguageSwitch';

/** Formats a build version for display: "1.2.3" -> "v1.2.3", "dev" stays. */
function formatVersion(version: string): string {
    return /^\d/.test(version) ? `v${version}` : version;
}

export function Header() {
    const { t } = useTranslation();
    const { health } = useHealth();

    return (
        <header className='sticky top-0 z-30 border-b border-line bg-surface/80 backdrop-blur'>
            <div className='mx-auto flex max-w-5xl items-center justify-between gap-4 px-4 py-3'>
                <Link
                    to='/'
                    className='flex items-baseline gap-2 rounded text-base font-semibold text-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-accent'
                >
                    {t('app.title')}
                    {health && <span className='text-xs font-normal text-muted'>{formatVersion(health.version)}</span>}
                </Link>
                <LanguageSwitch />
            </div>
        </header>
    );
}

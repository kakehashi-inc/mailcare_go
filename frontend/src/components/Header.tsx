import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, NavLink, useLocation, useNavigate } from 'react-router-dom';
import { useAuth } from '../auth/AuthProvider';
import { BREAKPOINT_LG } from '../constants';
import { useClickOutside } from '../hooks/useClickOutside';
import { useMediaQuery } from '../hooks/useMediaQuery';
import { formatVersion } from '../utils/format';
import { LanguageSwitch } from './LanguageSwitch';
import { Icon } from './ui/Icon';

interface NavEntry {
    to: string;
    label: string;
    icon: string;
    end?: boolean;
}

const NAV_CLASS =
    'inline-flex min-h-tap items-center gap-1.5 rounded-md px-3 text-base font-medium text-muted transition-colors hover:bg-well hover:text-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-accent';
const NAV_ACTIVE = 'bg-accent-soft text-accent';

/**
 * Site header: brand + version, primary navigation with a settings dropdown,
 * user name, language switch and logout. Below the lg breakpoint the
 * navigation folds into a hamburger drawer.
 */
export function Header() {
    const { t } = useTranslation();
    const { me, isAdmin, logout } = useAuth();
    const navigate = useNavigate();
    const location = useLocation();
    const wide = useMediaQuery(`(min-width: ${BREAKPOINT_LG}px)`);
    const [drawerOpen, setDrawerOpen] = useState(false);
    const [settingsOpen, setSettingsOpen] = useState(false);
    const settingsRef = useRef<HTMLDivElement>(null);
    useClickOutside(settingsRef, settingsOpen, () => setSettingsOpen(false));

    // Close the drawer on navigation.
    useEffect(() => {
        setDrawerOpen(false);
        setSettingsOpen(false);
    }, [location.pathname]);

    // Lock body scroll while the drawer is open.
    useEffect(() => {
        if (!drawerOpen) return;
        const prev = document.body.style.overflow;
        document.body.style.overflow = 'hidden';
        function onKey(e: KeyboardEvent) {
            if (e.key === 'Escape') setDrawerOpen(false);
        }
        document.addEventListener('keydown', onKey);
        return () => {
            document.body.style.overflow = prev;
            document.removeEventListener('keydown', onKey);
        };
    }, [drawerOpen]);

    const primary: NavEntry[] = [
        { to: '/alerts', label: t('nav.alerts'), icon: 'notifications' },
        { to: '/mails', label: t('nav.mails'), icon: 'mail' },
        { to: '/tools', label: t('nav.tools'), icon: 'build' },
    ];
    // Admin-only entries are hidden for members (direct URLs still show the "admins only" screen).
    const settingsEntries: NavEntry[] = [
        ...(isAdmin
            ? [
                  { to: '/settings/general', label: t('nav.settingsGeneral'), icon: 'tune' },
                  { to: '/settings/mailboxes', label: t('nav.settingsMailboxes'), icon: 'alternate_email' },
                  { to: '/settings/users', label: t('nav.settingsUsers'), icon: 'group' },
              ]
            : []),
        { to: '/settings/tokens', label: t('nav.settingsTokens'), icon: 'key' },
        { to: '/settings/account', label: t('nav.settingsAccount'), icon: 'manage_accounts' },
    ];
    const settingsActive = location.pathname.startsWith('/settings');

    async function handleLogout() {
        await logout();
        navigate('/login', { replace: true });
    }

    const userBlock = me && (
        <span className='inline-flex items-center gap-1.5 text-sm text-muted' title={me.user.username}>
            <Icon name={isAdmin ? 'admin_panel_settings' : 'person'} className='text-[20px]' />
            <span className='max-w-40 truncate'>{me.user.display_name || me.user.username}</span>
        </span>
    );

    return (
        <header className='sticky top-0 z-30'>
            {/* The blur lives on the bar, not on <header>: backdrop-filter would
                turn the header into the containing block of the fixed drawer. */}
            <div className='border-b border-line bg-surface/90 backdrop-blur'>
                <div className='mx-auto flex max-w-7xl items-center gap-2 px-4 py-2 sm:px-6 lg:px-8'>
                    {!wide && (
                        <button
                            type='button'
                            aria-label={drawerOpen ? t('header.closeMenu') : t('header.openMenu')}
                            aria-expanded={drawerOpen}
                            aria-controls='mobile-nav'
                            onClick={() => setDrawerOpen(v => !v)}
                            className='-ml-2 flex min-h-tap min-w-tap items-center justify-center rounded-md text-ink hover:bg-well focus:outline-none focus-visible:ring-2 focus-visible:ring-accent'
                        >
                            <Icon name={drawerOpen ? 'close' : 'menu'} className='text-[26px]' />
                        </button>
                    )}
                    <Link
                        to='/'
                        className='flex min-h-tap items-baseline gap-2 rounded px-1 text-lg font-bold text-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-accent'
                    >
                        {t('app.title')}
                        {me && <span className='text-xs font-normal text-muted'>{formatVersion(me.version)}</span>}
                    </Link>

                    {wide && (
                        <nav aria-label={t('header.mainNav')} className='ml-6 flex items-center gap-1'>
                            {primary.map(entry => (
                                <NavLink
                                    key={entry.to}
                                    to={entry.to}
                                    className={({ isActive }) => `${NAV_CLASS} ${isActive ? NAV_ACTIVE : ''}`}
                                >
                                    <Icon name={entry.icon} className='text-[20px]' />
                                    {entry.label}
                                </NavLink>
                            ))}
                            <div className='relative flex items-center' ref={settingsRef}>
                                <NavLink
                                    to='/settings'
                                    end
                                    className={`${NAV_CLASS} rounded-r-none pr-2 ${settingsActive ? NAV_ACTIVE : ''}`}
                                >
                                    <Icon name='settings' className='text-[20px]' />
                                    {t('nav.settings')}
                                </NavLink>
                                <button
                                    type='button'
                                    aria-label={t('header.settingsMenu')}
                                    aria-haspopup='menu'
                                    aria-expanded={settingsOpen}
                                    onClick={() => setSettingsOpen(v => !v)}
                                    className={`inline-flex min-h-tap items-center rounded-r-md px-1 text-muted hover:bg-well hover:text-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-accent ${
                                        settingsActive ? NAV_ACTIVE : ''
                                    }`}
                                >
                                    <Icon name={settingsOpen ? 'expand_less' : 'expand_more'} className='text-[22px]' />
                                </button>
                                {settingsOpen && (
                                    <div
                                        role='menu'
                                        className='absolute left-0 top-full z-20 mt-1 min-w-56 overflow-hidden rounded-md border border-line bg-surface py-1 shadow-xl'
                                    >
                                        {settingsEntries.map(entry => (
                                            <NavLink
                                                key={entry.to}
                                                to={entry.to}
                                                role='menuitem'
                                                onClick={() => setSettingsOpen(false)}
                                                className={({ isActive }) =>
                                                    `flex min-h-tap items-center gap-2 px-4 text-base hover:bg-well focus:bg-well focus:outline-none ${
                                                        isActive ? 'text-accent' : 'text-ink'
                                                    }`
                                                }
                                            >
                                                <Icon name={entry.icon} className='text-[20px]' />
                                                {entry.label}
                                            </NavLink>
                                        ))}
                                    </div>
                                )}
                            </div>
                        </nav>
                    )}

                    <div className='ml-auto flex items-center gap-1'>
                        {wide && userBlock}
                        <LanguageSwitch />
                        {me && (
                            <button
                                type='button'
                                onClick={handleLogout}
                                aria-label={t('header.logout')}
                                title={t('header.logout')}
                                className='inline-flex min-h-tap min-w-tap items-center justify-center gap-1 rounded-md px-2 text-muted hover:bg-well hover:text-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-accent'
                            >
                                <Icon name='logout' className='text-[22px]' />
                                {wide && <span className='text-sm'>{t('header.logout')}</span>}
                            </button>
                        )}
                    </div>
                </div>
            </div>

            {!wide && drawerOpen && (
                <div
                    className='fixed inset-0 top-[57px] z-20 bg-black/50'
                    onClick={() => setDrawerOpen(false)}
                    role='presentation'
                >
                    <nav
                        id='mobile-nav'
                        aria-label={t('header.mainNav')}
                        className='max-h-full overflow-y-auto border-b border-line bg-surface px-4 pb-4 pt-2 shadow-xl'
                        onClick={e => e.stopPropagation()}
                    >
                        {me && <div className='border-b border-line py-3'>{userBlock}</div>}
                        <ul className='mt-2 flex flex-col gap-1'>
                            <li>
                                <NavLink
                                    to='/'
                                    end
                                    className={({ isActive }) => `${NAV_CLASS} w-full ${isActive ? NAV_ACTIVE : ''}`}
                                >
                                    <Icon name='dashboard' className='text-[20px]' />
                                    {t('nav.dashboard')}
                                </NavLink>
                            </li>
                            {primary.map(entry => (
                                <li key={entry.to}>
                                    <NavLink
                                        to={entry.to}
                                        className={({ isActive }) =>
                                            `${NAV_CLASS} w-full ${isActive ? NAV_ACTIVE : ''}`
                                        }
                                    >
                                        <Icon name={entry.icon} className='text-[20px]' />
                                        {entry.label}
                                    </NavLink>
                                </li>
                            ))}
                            <li>
                                <NavLink
                                    to='/settings'
                                    end
                                    className={({ isActive }) => `${NAV_CLASS} w-full ${isActive ? NAV_ACTIVE : ''}`}
                                >
                                    <Icon name='settings' className='text-[20px]' />
                                    {t('nav.settings')}
                                </NavLink>
                                <ul className='ml-6 mt-1 flex flex-col gap-1 border-l border-line pl-2'>
                                    {settingsEntries.map(entry => (
                                        <li key={entry.to}>
                                            <NavLink
                                                to={entry.to}
                                                className={({ isActive }) =>
                                                    `${NAV_CLASS} w-full ${isActive ? NAV_ACTIVE : ''}`
                                                }
                                            >
                                                <Icon name={entry.icon} className='text-[20px]' />
                                                {entry.label}
                                            </NavLink>
                                        </li>
                                    ))}
                                </ul>
                            </li>
                        </ul>
                    </nav>
                </div>
            )}
        </header>
    );
}

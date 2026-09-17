import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, NavLink, useLocation, useNavigate } from 'react-router-dom';
import { useAuth } from '../auth/AuthProvider';
import { BREAKPOINT_LG } from '../constants';
import { useClickOutside } from '../hooks/useClickOutside';
import { useMediaQuery } from '../hooks/useMediaQuery';
import { formatVersion } from '../utils/format';
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
const MENU_ITEM_CLASS =
    'flex min-h-tap w-full items-center gap-2 px-4 text-left text-base hover:bg-well focus:bg-well focus:outline-none';

/**
 * Site header: brand + version, primary navigation with an admin-only
 * settings dropdown, language switch and a user menu (profile, logout) opened
 * from the user name. Below the lg breakpoint the navigation folds into a
 * hamburger drawer that also carries the user menu entries.
 */
export function Header() {
    const { t } = useTranslation();
    const { me, isAdmin, logout } = useAuth();
    const navigate = useNavigate();
    const location = useLocation();
    const wide = useMediaQuery(`(min-width: ${BREAKPOINT_LG}px)`);
    const [drawerOpen, setDrawerOpen] = useState(false);
    const [settingsOpen, setSettingsOpen] = useState(false);
    const [userOpen, setUserOpen] = useState(false);
    const settingsRef = useRef<HTMLDivElement>(null);
    const userRef = useRef<HTMLDivElement>(null);
    useClickOutside(settingsRef, settingsOpen, () => setSettingsOpen(false));
    useClickOutside(userRef, userOpen, () => setUserOpen(false));

    // Close the drawer and the menus on navigation.
    useEffect(() => {
        setDrawerOpen(false);
        setSettingsOpen(false);
        setUserOpen(false);
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
    // The whole settings menu is for administrators (direct URLs show the "admins only" screen).
    const settingsEntries: NavEntry[] = [
        { to: '/settings/general', label: t('nav.settingsGeneral'), icon: 'tune' },
        { to: '/settings/notifications', label: t('nav.settingsNotifications'), icon: 'mark_email_unread' },
        { to: '/settings/mailboxes', label: t('nav.settingsMailboxes'), icon: 'alternate_email' },
        { to: '/settings/users', label: t('nav.settingsUsers'), icon: 'group' },
        { to: '/settings/tokens', label: t('nav.settingsTokens'), icon: 'key' },
    ];
    const settingsActive = location.pathname.startsWith('/settings') && location.pathname !== '/settings/profile';
    const userName = me ? me.user.display_name || me.user.username : '';

    async function handleLogout() {
        setUserOpen(false);
        await logout();
        navigate('/login', { replace: true });
    }

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
                    {/* The brand and the version share a baseline inside a box that is itself centered in the bar. */}
                    <Link
                        to='/'
                        className='flex min-h-tap items-center rounded px-1 focus:outline-none focus-visible:ring-2 focus-visible:ring-accent'
                    >
                        <span className='flex items-baseline gap-2 text-lg font-bold leading-none text-ink'>
                            {t('app.title')}
                            {me && <span className='text-xs font-normal text-muted'>{formatVersion(me.version)}</span>}
                        </span>
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
                            {isAdmin && (
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
                                        <Icon
                                            name={settingsOpen ? 'expand_less' : 'expand_more'}
                                            className='text-[22px]'
                                        />
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
                                                        `${MENU_ITEM_CLASS} ${isActive ? 'text-accent' : 'text-ink'}`
                                                    }
                                                >
                                                    <Icon name={entry.icon} className='text-[20px]' />
                                                    {entry.label}
                                                </NavLink>
                                            ))}
                                        </div>
                                    )}
                                </div>
                            )}
                        </nav>
                    )}

                    <div className='ml-auto flex items-center gap-1'>
                        {wide && me && (
                            <div className='relative' ref={userRef}>
                                <button
                                    type='button'
                                    aria-label={t('header.userMenu', { name: userName })}
                                    aria-haspopup='menu'
                                    aria-expanded={userOpen}
                                    title={me.user.username}
                                    onClick={() => setUserOpen(v => !v)}
                                    className='inline-flex min-h-tap items-center gap-1.5 rounded-md px-2 text-sm text-muted hover:bg-well hover:text-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-accent'
                                >
                                    <span className='max-w-40 truncate'>{userName}</span>
                                </button>
                                {userOpen && (
                                    <div
                                        role='menu'
                                        className='absolute right-0 top-full z-20 mt-1 min-w-48 overflow-hidden rounded-md border border-line bg-surface py-1 shadow-xl'
                                    >
                                        <NavLink
                                            to='/settings/profile'
                                            role='menuitem'
                                            onClick={() => setUserOpen(false)}
                                            className={({ isActive }) =>
                                                `${MENU_ITEM_CLASS} ${isActive ? 'text-accent' : 'text-ink'}`
                                            }
                                        >
                                            <Icon name='manage_accounts' className='text-[20px]' />
                                            {t('nav.settingsProfile')}
                                        </NavLink>
                                        <button
                                            type='button'
                                            role='menuitem'
                                            onClick={() => void handleLogout()}
                                            className={`${MENU_ITEM_CLASS} text-ink`}
                                        >
                                            <Icon name='logout' className='text-[20px]' />
                                            {t('header.logout')}
                                        </button>
                                    </div>
                                )}
                            </div>
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
                            {isAdmin && (
                                <li>
                                    <NavLink
                                        to='/settings'
                                        end
                                        className={({ isActive }) =>
                                            `${NAV_CLASS} w-full ${isActive ? NAV_ACTIVE : ''}`
                                        }
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
                            )}
                        </ul>
                        {me && (
                            <div className='mt-3 border-t border-line pt-3'>
                                <p
                                    className='inline-flex min-h-tap items-center gap-1.5 px-3 text-sm text-muted'
                                    title={me.user.username}
                                >
                                    <span className='max-w-60 truncate'>{userName}</span>
                                </p>
                                <ul className='ml-6 mt-1 flex flex-col gap-1 border-l border-line pl-2'>
                                    <li>
                                        <NavLink
                                            to='/settings/profile'
                                            className={({ isActive }) =>
                                                `${NAV_CLASS} w-full ${isActive ? NAV_ACTIVE : ''}`
                                            }
                                        >
                                            <Icon name='manage_accounts' className='text-[20px]' />
                                            {t('nav.settingsProfile')}
                                        </NavLink>
                                    </li>
                                    <li>
                                        <button
                                            type='button'
                                            onClick={() => void handleLogout()}
                                            className={`${NAV_CLASS} w-full`}
                                        >
                                            <Icon name='logout' className='text-[20px]' />
                                            {t('header.logout')}
                                        </button>
                                    </li>
                                </ul>
                            </div>
                        )}
                    </nav>
                </div>
            )}
        </header>
    );
}

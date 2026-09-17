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
 * Site header: brand + version, primary navigation (settings is a plain
 * admin-only link to the menu page) and a user menu (profile, logout) opened
 * from the user name on every width. Below the lg breakpoint the navigation
 * folds into a hamburger drawer.
 */
export function Header() {
    const { t } = useTranslation();
    const { me, isAdmin, logout } = useAuth();
    const navigate = useNavigate();
    const location = useLocation();
    const wide = useMediaQuery(`(min-width: ${BREAKPOINT_LG}px)`);
    const [drawerOpen, setDrawerOpen] = useState(false);
    const [userOpen, setUserOpen] = useState(false);
    const userRef = useRef<HTMLDivElement>(null);
    useClickOutside(userRef, userOpen, () => setUserOpen(false));

    // Close the drawer and the menus on navigation.
    useEffect(() => {
        setDrawerOpen(false);
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
    // Settings is a plain link to the menu page (administrators only; direct URLs show the "admins only" screen).
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
                    {/* The brand and the version are vertically centered with each other and with the bar. */}
                    <Link
                        to='/'
                        className='flex min-h-tap shrink-0 items-center rounded px-1 focus:outline-none focus-visible:ring-2 focus-visible:ring-accent'
                    >
                        <span className='flex items-center gap-2 text-lg font-bold leading-none text-ink'>
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
                                <NavLink to='/settings' className={`${NAV_CLASS} ${settingsActive ? NAV_ACTIVE : ''}`}>
                                    <Icon name='settings' className='text-[20px]' />
                                    {t('nav.settings')}
                                </NavLink>
                            )}
                        </nav>
                    )}

                    {/* The user menu stays at the right on every width; long names are cut with an ellipsis
                        (about 40% of the bar on phones) and the title carries the full name. */}
                    <div className='ml-auto flex min-w-0 items-center gap-1'>
                        {me && (
                            <div className='relative min-w-0' ref={userRef}>
                                <button
                                    type='button'
                                    aria-label={t('header.userMenu', { name: userName })}
                                    aria-haspopup='menu'
                                    aria-expanded={userOpen}
                                    title={userName}
                                    onClick={() => setUserOpen(v => !v)}
                                    className='inline-flex min-h-tap max-w-full items-center rounded-md px-2 text-sm text-muted hover:bg-well hover:text-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-accent'
                                >
                                    <span className='block max-w-[9rem] truncate sm:max-w-[14rem]'>{userName}</span>
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
                                        className={`${NAV_CLASS} w-full ${settingsActive ? NAV_ACTIVE : ''}`}
                                    >
                                        <Icon name='settings' className='text-[20px]' />
                                        {t('nav.settings')}
                                    </NavLink>
                                </li>
                            )}
                        </ul>
                    </nav>
                </div>
            )}
        </header>
    );
}

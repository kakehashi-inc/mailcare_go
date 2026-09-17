import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import { useLocation, useNavigate } from 'react-router-dom';
import * as apiClient from '../api/client';
import type { LoginInput, Me, SetupInput } from '../types';
import { applyLanguage } from '../i18n/i18n';
import { applyTheme } from '../utils/theme';
import { setDisplayTimeZone } from '../utils/timezone';

export interface AuthState {
    /** The signed-in user and the server version, or null when signed out. */
    me: Me | null;
    /** True until the initial setup/session probe finished. */
    loading: boolean;
    /** True when the server has no users yet and /setup must be completed. */
    needsSetup: boolean;
    isAdmin: boolean;
    login: (input: LoginInput) => Promise<void>;
    setup: (input: SetupInput) => Promise<void>;
    logout: () => Promise<void>;
    refresh: () => Promise<void>;
}

const AuthContext = createContext<AuthState | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
    const [me, setMe] = useState<Me | null>(null);
    const [loading, setLoading] = useState(true);
    const [needsSetup, setNeedsSetup] = useState(false);
    const navigate = useNavigate();
    const location = useLocation();

    const probe = useCallback(async () => {
        try {
            const s = await apiClient.getSetupStatus();
            if (s.needs_setup) {
                setNeedsSetup(true);
                setMe(null);
                return;
            }
            setNeedsSetup(false);
        } catch {
            // Health of the setup endpoint is not critical: fall through to /me.
            setNeedsSetup(false);
        }
        try {
            setMe(await apiClient.getMe());
        } catch {
            setMe(null);
        }
    }, []);

    useEffect(() => {
        let active = true;
        void probe().finally(() => {
            if (active) setLoading(false);
        });
        return () => {
            active = false;
        };
    }, [probe]);

    // A 401 from any API call means the session ended: forget the user and
    // go to the login page, remembering where the user wanted to be.
    useEffect(() => {
        apiClient.setUnauthorizedHandler(() => {
            setMe(null);
            const here = location.pathname + location.search;
            if (!here.startsWith('/login') && !here.startsWith('/setup')) {
                navigate(`/login?next=${encodeURIComponent(here)}`, { replace: true });
            }
        });
    }, [navigate, location.pathname, location.search]);

    const login = useCallback(async (input: LoginInput) => {
        await apiClient.login(input);
        setMe(await apiClient.getMe());
        setNeedsSetup(false);
    }, []);

    const setup = useCallback(async (input: SetupInput) => {
        await apiClient.setup(input);
        setNeedsSetup(false);
        setMe(await apiClient.getMe());
    }, []);

    // Language, theme and time zone follow the signed-in user; signed out they
    // are the defaults (ja, auto, Asia/Tokyo). The setup page may change the
    // language and theme before a user exists; me is null then, so this does
    // not fight it.
    useEffect(() => {
        setDisplayTimeZone(me?.user.timezone);
        applyTheme(me?.user.theme);
        void applyLanguage(me?.user.language);
    }, [me]);

    const logout = useCallback(async () => {
        try {
            await apiClient.logout();
        } finally {
            setMe(null);
        }
    }, []);

    const refresh = useCallback(async () => {
        try {
            setMe(await apiClient.getMe());
        } catch {
            setMe(null);
        }
    }, []);

    const value = useMemo<AuthState>(
        () => ({
            me,
            loading,
            needsSetup,
            isAdmin: me?.user.role === 'admin',
            login,
            setup,
            logout,
            refresh,
        }),
        [me, loading, needsSetup, login, setup, logout, refresh]
    );

    return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthState {
    const ctx = useContext(AuthContext);
    if (!ctx) throw new Error('useAuth must be used within AuthProvider');
    return ctx;
}

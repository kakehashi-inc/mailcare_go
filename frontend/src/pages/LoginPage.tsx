import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { ApiError } from '../api/client';
import { useAuth } from '../auth/AuthProvider';
import { Button } from '../components/ui/Button';
import { InlineError } from '../components/ui/ErrorState';
import { InputField } from '../components/ui/Field';
import { TabPanel, Tabs } from '../components/ui/Tabs';
import { useDocumentTitle } from '../hooks/useDocumentTitle';
import { errorMessage } from '../utils/errors';
import { AuthShell } from './AuthShell';

type Mode = 'password' | 'token';

export function LoginPage() {
    const { t } = useTranslation();
    useDocumentTitle(t('login.title'));
    const { login } = useAuth();
    const navigate = useNavigate();
    const [params] = useSearchParams();
    const [mode, setMode] = useState<Mode>('password');
    const [username, setUsername] = useState('');
    const [password, setPassword] = useState('');
    const [token, setToken] = useState('');
    const [submitting, setSubmitting] = useState(false);
    const [error, setError] = useState<string | null>(null);

    const canSubmit = mode === 'password' ? username.trim() !== '' && password !== '' : token.trim() !== '';

    async function handleSubmit(e: FormEvent) {
        e.preventDefault();
        if (!canSubmit || submitting) return;
        setSubmitting(true);
        setError(null);
        try {
            await login(mode === 'password' ? { username: username.trim(), password } : { token: token.trim() });
            const next = params.get('next');
            navigate(next && next.startsWith('/') ? next : '/', { replace: true });
        } catch (err) {
            if (err instanceof ApiError && (err.status === 401 || err.status === 400)) {
                setError(t('login.errorInvalid'));
            } else {
                setError(errorMessage(err, t));
            }
        } finally {
            setSubmitting(false);
        }
    }

    return (
        <AuthShell title={t('login.title')} description={t('login.description')}>
            <Tabs<Mode>
                label={t('login.methodLabel')}
                value={mode}
                onChange={m => {
                    setMode(m);
                    setError(null);
                }}
                tabs={[
                    { key: 'password', label: t('login.tabPassword'), icon: 'password' },
                    { key: 'token', label: t('login.tabToken'), icon: 'key' },
                ]}
            />
            <form onSubmit={handleSubmit} noValidate>
                <TabPanel id='password' active={mode === 'password'}>
                    <div className='space-y-4'>
                        <InputField
                            label={t('login.username')}
                            autoComplete='username'
                            value={username}
                            onChange={e => setUsername(e.target.value)}
                            autoFocus
                            required
                        />
                        <InputField
                            label={t('login.password')}
                            type='password'
                            autoComplete='current-password'
                            value={password}
                            onChange={e => setPassword(e.target.value)}
                            required
                        />
                    </div>
                </TabPanel>
                <TabPanel id='token' active={mode === 'token'}>
                    <InputField
                        label={t('login.token')}
                        type='password'
                        autoComplete='off'
                        value={token}
                        onChange={e => setToken(e.target.value)}
                        placeholder='mlc_...'
                        hint={t('login.tokenHint')}
                        autoFocus
                        required
                    />
                </TabPanel>
                {error && (
                    <div className='mt-4'>
                        <InlineError message={error} />
                    </div>
                )}
                <Button
                    type='submit'
                    variant='primary'
                    block
                    className='mt-6'
                    loading={submitting}
                    disabled={!canSubmit}
                    icon='login'
                >
                    {t('login.submit')}
                </Button>
            </form>
        </AuthShell>
    );
}

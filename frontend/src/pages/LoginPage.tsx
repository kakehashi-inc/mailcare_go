import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { ApiError } from '../api/client';
import { useAuth } from '../auth/AuthProvider';
import { Button } from '../components/ui/Button';
import { InlineError } from '../components/ui/ErrorState';
import { CheckboxField, InputField } from '../components/ui/Field';
import { useDocumentTitle } from '../hooks/useDocumentTitle';
import { errorMessage } from '../utils/errors';
import { safeNextPath } from '../utils/next';
import { AuthShell } from './AuthShell';

/** Username + password login. "Keep me signed in" asks the server for a long-lived session. */
export function LoginPage() {
    const { t } = useTranslation();
    useDocumentTitle(t('login.title'));
    const { login } = useAuth();
    const navigate = useNavigate();
    const [params] = useSearchParams();
    const [username, setUsername] = useState('');
    const [password, setPassword] = useState('');
    const [remember, setRemember] = useState(false);
    const [submitting, setSubmitting] = useState(false);
    const [error, setError] = useState<string | null>(null);

    const canSubmit = username.trim() !== '' && password !== '';

    async function handleSubmit(e: FormEvent) {
        e.preventDefault();
        if (!canSubmit || submitting) return;
        setSubmitting(true);
        setError(null);
        try {
            await login({ username: username.trim(), password, remember });
            navigate(safeNextPath(params.get('next')), { replace: true });
        } catch (err) {
            if (err instanceof ApiError && (err.status === 401 || err.status === 400)) {
                setError(t('login.errorInvalid'));
            } else if (err instanceof ApiError && err.status === 429) {
                setError(t('login.errorTooMany'));
            } else {
                setError(errorMessage(err, t));
            }
        } finally {
            setSubmitting(false);
        }
    }

    return (
        <AuthShell title={t('login.title')} description={t('login.description')}>
            <form onSubmit={handleSubmit} noValidate>
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
                    <CheckboxField
                        label={t('login.remember')}
                        hint={t('login.rememberHint')}
                        checked={remember}
                        onChange={e => setRemember(e.target.checked)}
                    />
                </div>
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

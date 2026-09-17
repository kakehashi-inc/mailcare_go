import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { useAuth } from '../auth/AuthProvider';
import { Alert } from '../components/ui/Alert';
import { Button } from '../components/ui/Button';
import { InlineError } from '../components/ui/ErrorState';
import { InputField } from '../components/ui/Field';
import { MIN_PASSWORD_LENGTH } from '../constants';
import { useDocumentTitle } from '../hooks/useDocumentTitle';
import { errorMessage } from '../utils/errors';
import { AuthShell } from './AuthShell';

export function SetupPage() {
    const { t } = useTranslation();
    useDocumentTitle(t('setup.title'));
    const { setup } = useAuth();
    const navigate = useNavigate();
    const [username, setUsername] = useState('');
    const [displayName, setDisplayName] = useState('');
    const [password, setPassword] = useState('');
    const [confirm, setConfirm] = useState('');
    const [submitting, setSubmitting] = useState(false);
    const [error, setError] = useState<string | null>(null);

    const tooShort = password !== '' && password.length < MIN_PASSWORD_LENGTH;
    const mismatch = confirm !== '' && confirm !== password;
    const canSubmit = username.trim() !== '' && password.length >= MIN_PASSWORD_LENGTH && confirm === password;

    async function handleSubmit(e: FormEvent) {
        e.preventDefault();
        if (!canSubmit || submitting) return;
        setSubmitting(true);
        setError(null);
        try {
            await setup({ username: username.trim(), display_name: displayName.trim(), password });
            navigate('/', { replace: true });
        } catch (err) {
            setError(errorMessage(err, t));
        } finally {
            setSubmitting(false);
        }
    }

    return (
        <AuthShell title={t('setup.title')} description={t('setup.description')}>
            <Alert tone='info' className='mb-4'>
                {t('setup.hint')}
            </Alert>
            <form onSubmit={handleSubmit} noValidate className='space-y-4'>
                <InputField
                    label={t('user.username')}
                    autoComplete='username'
                    value={username}
                    onChange={e => setUsername(e.target.value)}
                    hint={t('user.usernameHint')}
                    autoFocus
                    required
                />
                <InputField
                    label={t('user.displayName')}
                    autoComplete='name'
                    value={displayName}
                    onChange={e => setDisplayName(e.target.value)}
                />
                <InputField
                    label={t('user.password')}
                    type='password'
                    autoComplete='new-password'
                    value={password}
                    onChange={e => setPassword(e.target.value)}
                    hint={t('user.passwordHint', { min: MIN_PASSWORD_LENGTH })}
                    error={tooShort ? t('user.passwordTooShort', { min: MIN_PASSWORD_LENGTH }) : undefined}
                    required
                />
                <InputField
                    label={t('user.passwordConfirm')}
                    type='password'
                    autoComplete='new-password'
                    value={confirm}
                    onChange={e => setConfirm(e.target.value)}
                    error={mismatch ? t('user.passwordMismatch') : undefined}
                    required
                />
                {error && <InlineError message={error} />}
                <Button
                    type='submit'
                    variant='primary'
                    block
                    loading={submitting}
                    disabled={!canSubmit}
                    icon='person_add'
                >
                    {t('setup.submit')}
                </Button>
            </form>
        </AuthShell>
    );
}

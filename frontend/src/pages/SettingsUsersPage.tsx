import { useEffect, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { createUser, deleteUser, listUsers, setUserPassword, updateUser } from '../api/client';
import { useAuth } from '../auth/AuthProvider';
import { RoleBadge } from '../components/domain/StatusBadges';
import { UserPreferenceFields } from '../components/domain/UserPreferenceFields';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card, CardHeader } from '../components/ui/Card';
import { ConfirmDialog } from '../components/ui/ConfirmDialog';
import { DateTime } from '../components/ui/DateTime';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState, InlineError } from '../components/ui/ErrorState';
import { InputField, SelectField } from '../components/ui/Field';
import { PageContainer, PageHeader } from '../components/ui/PageHeader';
import { LoadingBlock } from '../components/ui/Spinner';
import { Table, type Column } from '../components/ui/Table';
import { useToast } from '../components/ui/Toast';
import { DEFAULT_LANG, MIN_PASSWORD_LENGTH, type Lang } from '../constants';
import { isLang } from '../i18n/i18n';
import { useAsync } from '../hooks/useAsync';
import { useDocumentTitle } from '../hooks/useDocumentTitle';
import type { Role, UserDTO, UserUpdateInput } from '../types';
import { errorMessage } from '../utils/errors';
import { DEFAULT_THEME, isTheme, type Theme } from '../utils/theme';
import { DEFAULT_TIME_ZONE } from '../utils/timezone';
import { isEmailAddress } from '../utils/validate';

type Panel = { mode: 'create' } | { mode: 'edit'; user: UserDTO } | { mode: 'password'; user: UserDTO } | null;

export function SettingsUsersPage() {
    const { t } = useTranslation();
    useDocumentTitle(t('nav.settingsUsers'));
    const { me } = useAuth();
    const toast = useToast();
    const users = useAsync(listUsers, []);
    const [panel, setPanel] = useState<Panel>(null);
    const [deleting, setDeleting] = useState<UserDTO | null>(null);
    const [busy, setBusy] = useState(false);

    const adminCount = (users.data ?? []).filter(u => u.role === 'admin').length;

    async function remove() {
        if (!deleting) return;
        setBusy(true);
        try {
            await deleteUser(deleting.id);
            toast.success(t('user.deleted', { username: deleting.username }));
            setDeleting(null);
            await users.reload();
        } catch (err) {
            toast.error(errorMessage(err, t));
        } finally {
            setBusy(false);
        }
    }

    const columns: Column<UserDTO>[] = [
        {
            key: 'username',
            header: t('user.username'),
            primary: true,
            cell: u => (
                <span className='inline-flex flex-wrap items-center gap-2 break-all'>
                    <span className='font-medium text-ink'>{u.username}</span>
                    {me?.user.id === u.id && (
                        <Badge tone='info' icon='person_pin'>
                            {t('user.you')}
                        </Badge>
                    )}
                </span>
            ),
        },
        {
            key: 'display_name',
            header: t('user.displayName'),
            cell: u =>
                u.display_name ? (
                    <span className='break-words'>{u.display_name}</span>
                ) : (
                    <span className='text-muted'>-</span>
                ),
        },
        { key: 'role', header: t('user.role'), cell: u => <RoleBadge role={u.role} /> },
        {
            key: 'email',
            header: t('user.email'),
            cell: u =>
                u.email ? (
                    <span className='break-all'>{u.email}</span>
                ) : (
                    <span className='text-muted'>{t('user.emailNone')}</span>
                ),
        },
        {
            key: 'last_login',
            header: t('user.lastLogin'),
            cell: u => <DateTime value={u.last_login_at} relative empty={t('user.neverLoggedIn')} />,
        },
        { key: 'created', header: t('common.createdAt'), cell: u => <DateTime value={u.created_at} /> },
        {
            key: 'actions',
            header: t('common.actions'),
            hideLabelInCard: true,
            className: 'whitespace-nowrap',
            cell: u => {
                const isSelf = me?.user.id === u.id;
                const lastAdmin = u.role === 'admin' && adminCount <= 1;
                return (
                    <span className='flex flex-wrap gap-1'>
                        <Button
                            size='sm'
                            icon='edit'
                            aria-label={t('user.editAria', { username: u.username })}
                            onClick={() => setPanel({ mode: 'edit', user: u })}
                        >
                            {t('common.edit')}
                        </Button>
                        <Button
                            size='sm'
                            icon='lock_reset'
                            aria-label={t('user.resetPasswordAria', { username: u.username })}
                            onClick={() => setPanel({ mode: 'password', user: u })}
                        >
                            {t('user.resetPassword')}
                        </Button>
                        <Button
                            size='sm'
                            icon='delete'
                            className='text-danger'
                            disabled={isSelf || lastAdmin}
                            aria-label={t('user.deleteAria', { username: u.username })}
                            title={
                                isSelf
                                    ? t('user.cannotDeleteSelf')
                                    : lastAdmin
                                      ? t('user.cannotDeleteLastAdmin')
                                      : undefined
                            }
                            onClick={() => setDeleting(u)}
                        >
                            {t('common.delete')}
                        </Button>
                    </span>
                );
            },
        },
    ];

    return (
        <PageContainer wide>
            <PageHeader
                title={t('nav.settingsUsers')}
                description={t('settings.menu.users')}
                crumbs={[{ label: t('nav.settings'), to: '/settings' }, { label: t('nav.settingsUsers') }]}
                actions={
                    <Button variant='primary' icon='person_add' onClick={() => setPanel({ mode: 'create' })}>
                        {t('user.add')}
                    </Button>
                }
            />

            {panel && (
                <div className='mb-6'>
                    <UserForm
                        // Remount per target so the fields are always prefilled from the row
                        // (switching from one inline form to another must never keep stale state).
                        key={`${panel.mode}-${panel.mode === 'create' ? 'new' : panel.user.id}`}
                        panel={panel}
                        adminCount={adminCount}
                        onClose={() => setPanel(null)}
                        onSaved={async () => {
                            setPanel(null);
                            await users.reload();
                        }}
                    />
                </div>
            )}

            {users.loading ? (
                <LoadingBlock />
            ) : users.error || !users.data ? (
                <ErrorState message={errorMessage(users.error, t)} onRetry={() => void users.reload()} />
            ) : (
                <Table
                    columns={columns}
                    rows={users.data}
                    rowKey={u => u.id}
                    caption={t('nav.settingsUsers')}
                    emptyState={<EmptyState title={t('user.empty')} />}
                />
            )}

            <ConfirmDialog
                open={deleting !== null}
                title={t('user.deleteTitle')}
                message={deleting ? t('user.deleteMessage', { username: deleting.username }) : ''}
                confirmLabel={t('common.delete')}
                danger
                busy={busy}
                onConfirm={() => void remove()}
                onCancel={() => setDeleting(null)}
            />
        </PageContainer>
    );
}

interface UserFormProps {
    panel: NonNullable<Panel>;
    adminCount: number;
    onClose: () => void;
    onSaved: () => Promise<void>;
}

function UserForm({ panel, adminCount, onClose, onSaved }: UserFormProps) {
    const { t } = useTranslation();
    const toast = useToast();
    const editing = panel.mode === 'edit' ? panel.user : null;
    const [username, setUsername] = useState('');
    const [displayName, setDisplayName] = useState(editing?.display_name ?? '');
    const [email, setEmail] = useState(editing?.email ?? '');
    const [language, setLanguage] = useState<Lang>(isLang(editing?.language) ? editing.language : DEFAULT_LANG);
    const [timezone, setTimezone] = useState(editing?.timezone || DEFAULT_TIME_ZONE);
    const [theme, setTheme] = useState<Theme>(isTheme(editing?.theme) ? editing.theme : DEFAULT_THEME);
    const [role, setRole] = useState<Role>(editing?.role ?? 'user');
    const [password, setPassword] = useState('');
    const [confirm, setConfirm] = useState('');
    const [submitting, setSubmitting] = useState(false);
    const [error, setError] = useState<string | null>(null);

    // Escape closes the inline form, like a dialog.
    useEffect(() => {
        function onKey(e: KeyboardEvent) {
            if (e.key === 'Escape') onClose();
        }
        document.addEventListener('keydown', onKey);
        return () => document.removeEventListener('keydown', onKey);
    }, [onClose]);

    // Edit sends only the fields that differ from the row, so nothing is wiped unintentionally.
    const changes: UserUpdateInput = {};
    if (editing) {
        if (displayName.trim() !== editing.display_name) changes.display_name = displayName.trim();
        if (email.trim() !== editing.email) changes.email = email.trim();
        if (language !== (isLang(editing.language) ? editing.language : DEFAULT_LANG)) changes.language = language;
        if (timezone !== (editing.timezone || DEFAULT_TIME_ZONE)) changes.timezone = timezone;
        if (theme !== (isTheme(editing.theme) ? editing.theme : DEFAULT_THEME)) changes.theme = theme;
        if (role !== editing.role) changes.role = role;
    }
    const editDirty = Object.keys(changes).length > 0;

    const needsPassword = panel.mode === 'create' || panel.mode === 'password';
    const tooShort = password !== '' && password.length < MIN_PASSWORD_LENGTH;
    const mismatch = confirm !== '' && confirm !== password;
    const passwordOk = !needsPassword || (password.length >= MIN_PASSWORD_LENGTH && confirm === password);
    const demotingLastAdmin =
        panel.mode === 'edit' && panel.user.role === 'admin' && role !== 'admin' && adminCount <= 1;
    const emailError = email.trim() !== '' && !isEmailAddress(email) ? t('user.emailInvalid') : undefined;
    const canSubmit =
        (panel.mode !== 'create' || username.trim() !== '') &&
        (panel.mode !== 'edit' || editDirty) &&
        passwordOk &&
        !demotingLastAdmin &&
        !emailError &&
        !submitting;

    const title =
        panel.mode === 'create'
            ? t('user.add')
            : panel.mode === 'edit'
              ? t('user.editTitle', { username: panel.user.username })
              : t('user.resetPasswordTitle', { username: panel.user.username });

    async function handleSubmit(e: FormEvent) {
        e.preventDefault();
        if (!canSubmit) return;
        setSubmitting(true);
        setError(null);
        try {
            if (panel.mode === 'create') {
                await createUser({
                    username: username.trim(),
                    display_name: displayName.trim(),
                    email: email.trim(),
                    language,
                    timezone,
                    theme,
                    password,
                    role,
                });
                toast.success(t('user.created', { username: username.trim() }));
            } else if (panel.mode === 'edit') {
                await updateUser(panel.user.id, changes);
                toast.success(t('user.updated', { username: panel.user.username }));
            } else {
                await setUserPassword(panel.user.id, password);
                toast.success(t('user.passwordReset', { username: panel.user.username }));
            }
            await onSaved();
        } catch (err) {
            setError(errorMessage(err, t));
        } finally {
            setSubmitting(false);
        }
    }

    return (
        <Card>
            <CardHeader
                title={title}
                actions={
                    <Button size='sm' variant='ghost' icon='close' onClick={onClose}>
                        {t('common.close')}
                    </Button>
                }
            />
            <form onSubmit={handleSubmit} noValidate className='grid grid-cols-1 gap-4 md:grid-cols-2'>
                {panel.mode === 'create' && (
                    <InputField
                        label={t('user.username')}
                        autoComplete='off'
                        value={username}
                        onChange={e => setUsername(e.target.value)}
                        hint={t('user.usernameHint')}
                        autoFocus
                        required
                    />
                )}
                {panel.mode !== 'password' && (
                    <>
                        <InputField
                            label={t('user.displayName')}
                            autoComplete='off'
                            value={displayName}
                            onChange={e => setDisplayName(e.target.value)}
                            autoFocus={panel.mode === 'edit'}
                        />
                        <InputField
                            label={t('user.email')}
                            type='email'
                            autoComplete='off'
                            inputMode='email'
                            value={email}
                            onChange={e => setEmail(e.target.value)}
                            hint={t('user.emailHint')}
                            error={emailError}
                        />
                        <UserPreferenceFields
                            language={language}
                            timezone={timezone}
                            theme={theme}
                            onLanguage={setLanguage}
                            onTimezone={setTimezone}
                            onTheme={setTheme}
                        />
                        <SelectField
                            label={t('user.role')}
                            value={role}
                            onChange={e => setRole(e.target.value as Role)}
                            hint={t('user.roleHint')}
                            error={demotingLastAdmin ? t('user.cannotDemoteLastAdmin') : undefined}
                        >
                            <option value='user'>{t('role.user')}</option>
                            <option value='admin'>{t('role.admin')}</option>
                        </SelectField>
                    </>
                )}
                {needsPassword && (
                    <>
                        <InputField
                            label={panel.mode === 'password' ? t('profile.newPassword') : t('user.password')}
                            type='password'
                            autoComplete='new-password'
                            value={password}
                            onChange={e => setPassword(e.target.value)}
                            hint={t('user.passwordHint', { min: MIN_PASSWORD_LENGTH })}
                            error={tooShort ? t('user.passwordTooShort', { min: MIN_PASSWORD_LENGTH }) : undefined}
                            autoFocus={panel.mode === 'password'}
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
                    </>
                )}
                {error && (
                    <div className='md:col-span-2'>
                        <InlineError message={error} />
                    </div>
                )}
                <div className='flex flex-col-reverse gap-2 sm:flex-row sm:justify-end md:col-span-2'>
                    <Button onClick={onClose}>{t('common.cancel')}</Button>
                    <Button type='submit' variant='primary' icon='save' loading={submitting} disabled={!canSubmit}>
                        {panel.mode === 'create' ? t('common.create') : t('common.save')}
                    </Button>
                </div>
            </form>
        </Card>
    );
}

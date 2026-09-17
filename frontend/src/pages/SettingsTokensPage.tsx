import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { createToken, deleteToken, listTokens, listUsers } from '../api/client';
import { useAuth } from '../auth/AuthProvider';
import { Alert } from '../components/ui/Alert';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card, CardHeader } from '../components/ui/Card';
import { ConfirmDialog } from '../components/ui/ConfirmDialog';
import { CopyButton } from '../components/ui/CopyButton';
import { DateTime } from '../components/ui/DateTime';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState, InlineError } from '../components/ui/ErrorState';
import { InputField, SelectField, controlClass } from '../components/ui/Field';
import { PageContainer, PageHeader } from '../components/ui/PageHeader';
import { LoadingBlock } from '../components/ui/Spinner';
import { Table, type Column } from '../components/ui/Table';
import { useToast } from '../components/ui/Toast';
import { useAsync } from '../hooks/useAsync';
import { useDocumentTitle } from '../hooks/useDocumentTitle';
import type { TokenDTO } from '../types';
import { errorMessage } from '../utils/errors';

type Expiry = 'never' | '7' | '30' | '90' | '365' | 'custom';

function expiresAt(choice: Expiry, custom: string): string | undefined {
    if (choice === 'never') return undefined;
    if (choice === 'custom') {
        const d = new Date(custom);
        return Number.isNaN(d.getTime()) ? undefined : d.toISOString();
    }
    const d = new Date();
    d.setDate(d.getDate() + Number(choice));
    return d.toISOString();
}

export function SettingsTokensPage() {
    const { t } = useTranslation();
    useDocumentTitle(t('nav.settingsTokens'));
    const { me, isAdmin } = useAuth();
    const toast = useToast();
    const [filterUser, setFilterUser] = useState('');
    const users = useAsync(() => (isAdmin ? listUsers() : Promise.resolve([])), [isAdmin]);
    const tokens = useAsync(() => listTokens(filterUser ? Number(filterUser) : undefined), [filterUser]);
    const [showForm, setShowForm] = useState(false);
    const [name, setName] = useState('');
    const [owner, setOwner] = useState('');
    const [expiry, setExpiry] = useState<Expiry>('never');
    const [custom, setCustom] = useState('');
    const [submitting, setSubmitting] = useState(false);
    const [error, setError] = useState<string | null>(null);
    const [created, setCreated] = useState<{ token: TokenDTO; value: string } | null>(null);
    const [deleting, setDeleting] = useState<TokenDTO | null>(null);
    const [busy, setBusy] = useState(false);

    const customInvalid =
        expiry === 'custom' &&
        (custom === '' || Number.isNaN(new Date(custom).getTime()) || new Date(custom).getTime() <= Date.now());
    const canSubmit = name.trim() !== '' && !customInvalid && !submitting;

    async function handleCreate(e: FormEvent) {
        e.preventDefault();
        if (!canSubmit) return;
        setSubmitting(true);
        setError(null);
        try {
            const ownerId = isAdmin && owner ? Number(owner) : undefined;
            const result = await createToken({
                user_id: ownerId,
                name: name.trim(),
                expires: expiresAt(expiry, custom),
            });
            setCreated(result);
            setName('');
            setExpiry('never');
            setCustom('');
            setShowForm(false);
            await tokens.reload();
        } catch (err) {
            setError(errorMessage(err, t));
        } finally {
            setSubmitting(false);
        }
    }

    async function remove() {
        if (!deleting) return;
        setBusy(true);
        try {
            await deleteToken(deleting.identifier);
            toast.success(t('token.deleted', { name: deleting.name || deleting.identifier }));
            setDeleting(null);
            await tokens.reload();
        } catch (err) {
            toast.error(errorMessage(err, t));
        } finally {
            setBusy(false);
        }
    }

    const now = Date.now();
    const columns: Column<TokenDTO>[] = [
        {
            key: 'name',
            header: t('token.name'),
            primary: true,
            cell: tk => (
                <span className='break-all'>
                    <span className='font-medium text-ink'>{tk.name || t('token.unnamed')}</span>
                    <code className='block font-mono text-sm text-muted'>{tk.identifier}</code>
                </span>
            ),
        },
        ...(isAdmin
            ? [
                  {
                      key: 'user',
                      header: t('token.owner'),
                      cell: (tk: TokenDTO) => tk.username,
                  } satisfies Column<TokenDTO>,
              ]
            : []),
        {
            key: 'expires',
            header: t('token.expires'),
            cell: tk => {
                if (!tk.expires_at) return <span className='text-muted'>{t('token.neverExpires')}</span>;
                const expired = new Date(tk.expires_at).getTime() < now;
                return (
                    <span className='flex flex-wrap items-center gap-1'>
                        <DateTime value={tk.expires_at} />
                        {expired && (
                            <Badge tone='danger' icon='event_busy'>
                                {t('token.expired')}
                            </Badge>
                        )}
                    </span>
                );
            },
        },
        {
            key: 'last_used',
            header: t('token.lastUsed'),
            cell: tk => <DateTime value={tk.last_used_at} relative empty={t('token.neverUsed')} />,
        },
        { key: 'created', header: t('common.createdAt'), cell: tk => <DateTime value={tk.created_at} /> },
        {
            key: 'actions',
            header: t('common.actions'),
            hideLabelInCard: true,
            className: 'whitespace-nowrap',
            cell: tk => (
                <Button
                    size='sm'
                    icon='delete'
                    className='text-danger'
                    aria-label={t('token.deleteAria', { name: tk.name || tk.identifier })}
                    onClick={() => setDeleting(tk)}
                >
                    {t('common.delete')}
                </Button>
            ),
        },
    ];

    return (
        <PageContainer wide>
            <PageHeader
                title={t('nav.settingsTokens')}
                description={t('token.description')}
                crumbs={[{ label: t('nav.settings'), to: '/settings' }, { label: t('nav.settingsTokens') }]}
                actions={
                    <Button variant='primary' icon='add' onClick={() => setShowForm(v => !v)} aria-expanded={showForm}>
                        {t('token.create')}
                    </Button>
                }
            />

            {created && (
                <Alert
                    tone='success'
                    className='mb-6'
                    title={t('token.createdTitle', { name: created.token.name || created.token.identifier })}
                    actions={
                        <Button size='sm' variant='ghost' icon='close' onClick={() => setCreated(null)}>
                            {t('common.close')}
                        </Button>
                    }
                >
                    <p>{t('token.createdOnce')}</p>
                    <div className='mt-2 flex flex-wrap items-center gap-2'>
                        <code className='break-all rounded-md bg-surface px-2 py-1 font-mono text-sm text-ink'>
                            {created.value}
                        </code>
                        <CopyButton text={created.value} label={t('token.copy')} size='md' />
                    </div>
                </Alert>
            )}

            {showForm && (
                <Card className='mb-6'>
                    <CardHeader title={t('token.create')} />
                    <form onSubmit={handleCreate} noValidate className='grid grid-cols-1 gap-4 md:grid-cols-2'>
                        <InputField
                            label={t('token.name')}
                            value={name}
                            onChange={e => setName(e.target.value)}
                            hint={t('token.nameHint')}
                            autoFocus
                            required
                        />
                        {isAdmin && (
                            <SelectField
                                label={t('token.owner')}
                                value={owner}
                                onChange={e => setOwner(e.target.value)}
                            >
                                <option value=''>{me ? `${me.user.username} (${t('user.you')})` : '-'}</option>
                                {(users.data ?? [])
                                    .filter(u => u.id !== me?.user.id)
                                    .map(u => (
                                        <option key={u.id} value={String(u.id)}>
                                            {u.username}
                                        </option>
                                    ))}
                            </SelectField>
                        )}
                        <SelectField
                            label={t('token.expires')}
                            value={expiry}
                            onChange={e => setExpiry(e.target.value as Expiry)}
                        >
                            <option value='never'>{t('token.neverExpires')}</option>
                            <option value='7'>{t('token.expiresDays', { count: 7 })}</option>
                            <option value='30'>{t('token.expiresDays', { count: 30 })}</option>
                            <option value='90'>{t('token.expiresDays', { count: 90 })}</option>
                            <option value='365'>{t('token.expiresDays', { count: 365 })}</option>
                            <option value='custom'>{t('token.expiresCustom')}</option>
                        </SelectField>
                        {expiry === 'custom' && (
                            <div>
                                <label
                                    htmlFor='token-expires-custom'
                                    className='mb-1 block text-sm font-medium text-ink'
                                >
                                    {t('token.expiresAt')}
                                </label>
                                <input
                                    id='token-expires-custom'
                                    type='datetime-local'
                                    value={custom}
                                    onChange={e => setCustom(e.target.value)}
                                    aria-invalid={customInvalid || undefined}
                                    className={controlClass}
                                />
                                {customInvalid && custom !== '' && (
                                    <p className='mt-1 text-sm text-danger' role='alert'>
                                        {t('token.expiresInvalid')}
                                    </p>
                                )}
                            </div>
                        )}
                        {error && (
                            <div className='md:col-span-2'>
                                <InlineError message={error} />
                            </div>
                        )}
                        <div className='flex flex-col-reverse gap-2 sm:flex-row sm:justify-end md:col-span-2'>
                            <Button onClick={() => setShowForm(false)}>{t('common.cancel')}</Button>
                            <Button
                                type='submit'
                                variant='primary'
                                icon='key'
                                loading={submitting}
                                disabled={!canSubmit}
                            >
                                {t('token.create')}
                            </Button>
                        </div>
                    </form>
                </Card>
            )}

            {isAdmin && (
                <div className='mb-4 max-w-sm'>
                    <SelectField
                        label={t('token.filterUser')}
                        value={filterUser}
                        onChange={e => setFilterUser(e.target.value)}
                    >
                        <option value=''>{t('common.all')}</option>
                        {(users.data ?? []).map(u => (
                            <option key={u.id} value={String(u.id)}>
                                {u.username}
                            </option>
                        ))}
                    </SelectField>
                </div>
            )}

            {tokens.loading ? (
                <LoadingBlock />
            ) : tokens.error || !tokens.data ? (
                <ErrorState message={errorMessage(tokens.error, t)} onRetry={() => void tokens.reload()} />
            ) : (
                <Table
                    columns={columns}
                    rows={tokens.data}
                    rowKey={tk => tk.identifier}
                    caption={t('nav.settingsTokens')}
                    emptyState={<EmptyState icon='key' title={t('token.empty')} description={t('token.emptyHint')} />}
                />
            )}

            <ConfirmDialog
                open={deleting !== null}
                title={t('token.deleteTitle')}
                message={deleting ? t('token.deleteMessage', { name: deleting.name || deleting.identifier }) : ''}
                confirmLabel={t('common.delete')}
                danger
                busy={busy}
                onConfirm={() => void remove()}
                onCancel={() => setDeleting(null)}
            />
        </PageContainer>
    );
}

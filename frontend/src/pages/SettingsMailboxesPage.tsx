import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { deleteMailbox, listMailboxes, testMailbox } from '../api/client';
import { EnabledBadge, CheckStatusBadge, fetchStatus } from '../components/domain/StatusBadges';
import { Button, LinkButton } from '../components/ui/Button';
import { ConfirmDialog } from '../components/ui/ConfirmDialog';
import { DateTime } from '../components/ui/DateTime';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { CheckboxField } from '../components/ui/Field';
import { PageContainer, PageHeader } from '../components/ui/PageHeader';
import { LoadingBlock } from '../components/ui/Spinner';
import { Table, type Column } from '../components/ui/Table';
import { useToast } from '../components/ui/Toast';
import { useAsync } from '../hooks/useAsync';
import { useDocumentTitle } from '../hooks/useDocumentTitle';
import type { MailboxDTO } from '../types';
import { errorMessage } from '../utils/errors';

export function SettingsMailboxesPage() {
    const { t } = useTranslation();
    useDocumentTitle(t('nav.settingsMailboxes'));
    const toast = useToast();
    const navigate = useNavigate();
    const list = useAsync(listMailboxes, []);
    const [testing, setTesting] = useState<number | null>(null);
    const [deleting, setDeleting] = useState<MailboxDTO | null>(null);
    const [keepData, setKeepData] = useState(false);
    const [busy, setBusy] = useState(false);

    async function test(mb: MailboxDTO) {
        setTesting(mb.id);
        try {
            await testMailbox({
                id: mb.id,
                address: mb.address,
                imap_host: mb.imap_host,
                imap_port: mb.imap_port,
                imap_security: mb.imap_security,
                imap_username: mb.imap_username,
                folder: mb.folder,
            });
            toast.success(t('mailbox.testOk', { address: mb.address }));
        } catch (err) {
            toast.error(t('mailbox.testFailed', { message: errorMessage(err, t) }));
        } finally {
            setTesting(null);
        }
    }

    async function remove() {
        if (!deleting) return;
        setBusy(true);
        try {
            await deleteMailbox(deleting.id, keepData);
            toast.success(t('mailbox.deleted', { address: deleting.address }));
            setDeleting(null);
            setKeepData(false);
            await list.reload();
        } catch (err) {
            toast.error(errorMessage(err, t));
        } finally {
            setBusy(false);
        }
    }

    const columns: Column<MailboxDTO>[] = [
        {
            key: 'address',
            header: t('mailbox.address'),
            primary: true,
            cell: mb => (
                <span className='break-all'>
                    <span className='font-medium text-ink'>{mb.address}</span>
                    {mb.display_name && <span className='block text-sm text-muted'>{mb.display_name}</span>}
                </span>
            ),
        },
        {
            key: 'server',
            header: t('mailbox.server'),
            cell: mb => (
                <span className='break-all font-mono text-sm'>
                    {mb.imap_host}:{mb.imap_port} ({t(`imapSecurity.${mb.imap_security}`)})
                </span>
            ),
        },
        { key: 'enabled', header: t('common.status'), cell: mb => <EnabledBadge enabled={mb.enabled} /> },
        {
            key: 'check',
            header: t('mailbox.lastChecked'),
            cell: mb => (
                <span className='flex flex-col gap-1'>
                    <CheckStatusBadge status={fetchStatus(mb)} />
                    <DateTime
                        value={mb.last_fetched_at}
                        relative
                        empty={t('checkStatus.never')}
                        className='text-sm text-muted'
                    />
                </span>
            ),
        },
        {
            key: 'actions',
            header: t('common.actions'),
            hideLabelInCard: true,
            className: 'whitespace-nowrap',
            cell: mb => (
                <span className='flex flex-wrap gap-1' onClick={e => e.stopPropagation()}>
                    <Button
                        size='sm'
                        icon='network_check'
                        loading={testing === mb.id}
                        aria-label={t('mailbox.testAria', { address: mb.address })}
                        onClick={() => void test(mb)}
                    >
                        {t('mailbox.test')}
                    </Button>
                    <LinkButton
                        size='sm'
                        icon='edit'
                        to={`/settings/mailboxes/${mb.id}`}
                        aria-label={t('mailbox.editAria', { address: mb.address })}
                    >
                        {t('common.edit')}
                    </LinkButton>
                    <Button
                        size='sm'
                        icon='delete'
                        className='text-danger'
                        aria-label={t('mailbox.deleteAria', { address: mb.address })}
                        onClick={() => setDeleting(mb)}
                    >
                        {t('common.delete')}
                    </Button>
                </span>
            ),
        },
    ];

    return (
        <PageContainer wide>
            <PageHeader
                title={t('nav.settingsMailboxes')}
                description={t('settings.menu.mailboxes')}
                crumbs={[{ label: t('nav.settings'), to: '/settings' }, { label: t('nav.settingsMailboxes') }]}
                actions={
                    <LinkButton to='/settings/mailboxes/new' variant='primary' icon='add'>
                        {t('mailbox.add')}
                    </LinkButton>
                }
            />
            {list.loading ? (
                <LoadingBlock />
            ) : list.error || !list.data ? (
                <ErrorState message={errorMessage(list.error, t)} onRetry={() => void list.reload()} />
            ) : (
                <Table
                    columns={columns}
                    rows={list.data}
                    rowKey={mb => mb.id}
                    caption={t('nav.settingsMailboxes')}
                    onRowClick={mb => navigate(`/settings/mailboxes/${mb.id}`)}
                    rowAriaLabel={mb => t('mailbox.editAria', { address: mb.address })}
                    emptyState={
                        <EmptyState
                            title={t('mailbox.emptyTitle')}
                            action={
                                <LinkButton to='/settings/mailboxes/new' variant='primary' icon='add'>
                                    {t('mailbox.add')}
                                </LinkButton>
                            }
                        />
                    }
                />
            )}
            <ConfirmDialog
                open={deleting !== null}
                title={t('mailbox.deleteTitle')}
                message={
                    deleting && (
                        <div className='flex flex-col gap-3'>
                            <p>{t('mailbox.deleteMessage', { address: deleting.address })}</p>
                            <CheckboxField
                                label={t('mailbox.keepData')}
                                hint={t('mailbox.keepDataHint')}
                                checked={keepData}
                                onChange={e => setKeepData(e.target.checked)}
                            />
                        </div>
                    )
                }
                confirmLabel={t('common.delete')}
                danger
                busy={busy}
                onConfirm={() => void remove()}
                onCancel={() => setDeleting(null)}
            />
        </PageContainer>
    );
}

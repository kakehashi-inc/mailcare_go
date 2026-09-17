import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { getMailbox, listMessages } from '../api/client';
import { MailboxSelect } from '../components/domain/MailboxSelect';
import { BounceKindBadge } from '../components/domain/StatusBadges';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { DateTime } from '../components/ui/DateTime';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { CheckboxField, InputField } from '../components/ui/Field';
import { PageContainer, PageHeader } from '../components/ui/PageHeader';
import { Pagination } from '../components/ui/Pagination';
import { LoadingBlock } from '../components/ui/Spinner';
import { Table, type Column } from '../components/ui/Table';
import { MAIL_PAGE_SIZE } from '../constants';
import { useAsync } from '../hooks/useAsync';
import { useDocumentTitle } from '../hooks/useDocumentTitle';
import { useMailboxes } from '../hooks/useMailboxes';
import type { MessageDTO } from '../types';
import { errorMessage } from '../utils/errors';
import { formatBytes, mailboxLabel } from '../utils/format';

export function MailsListPage() {
    const { t } = useTranslation();
    const { mailboxId = '' } = useParams();
    const id = Number(mailboxId);
    const navigate = useNavigate();
    const [params, setParams] = useSearchParams();
    const q = params.get('q') ?? '';
    const onlyBounce = params.get('only_bounce') === '1';
    const group = params.get('group') ?? '';
    const page = Math.max(1, Number(params.get('page') ?? '1') || 1);
    const [search, setSearch] = useState(q);

    const { mailboxes } = useMailboxes();
    const mailbox = useAsync(() => getMailbox(id), [id]);
    useDocumentTitle(mailbox.data ? `${t('nav.mails')} - ${mailbox.data.address}` : t('nav.mails'));
    const list = useAsync(
        () => listMessages(id, { q, only_bounce: onlyBounce, group, page, per_page: MAIL_PAGE_SIZE }),
        [id, q, onlyBounce, group, page]
    );

    function update(patch: Record<string, string>) {
        const next = new URLSearchParams(params);
        for (const [k, v] of Object.entries(patch)) {
            if (v) next.set(k, v);
            else next.delete(k);
        }
        if (!('page' in patch)) next.delete('page');
        setParams(next, { replace: true });
    }

    const columns: Column<MessageDTO>[] = [
        {
            key: 'date',
            header: t('mail.date'),
            cell: m => <DateTime value={m.date ?? m.received_at} />,
            className: 'whitespace-nowrap',
        },
        {
            key: 'subject',
            header: t('mail.subject'),
            cell: m => <span className='break-words font-medium text-ink'>{m.subject || t('mail.noSubject')}</span>,
            primary: true,
        },
        {
            key: 'from',
            header: t('mail.from'),
            cell: m => (
                <span className='break-all'>{m.from_name ? `${m.from_name} <${m.from_address}>` : m.from_address}</span>
            ),
        },
        {
            key: 'kind',
            header: t('mail.kind'),
            cell: m => <BounceKindBadge kind={m.bounce_kind} isBounce={m.is_bounce} />,
        },
        {
            key: 'size',
            header: t('mail.size'),
            cell: m => formatBytes(m.size),
            className: 'whitespace-nowrap text-muted',
        },
    ];

    const data = list.data;

    // A page beyond the last one (e.g. after messages were removed) shows nothing: go to the last page.
    useEffect(() => {
        if (!data || data.total === 0) return;
        const perPage = data.per_page || MAIL_PAGE_SIZE;
        const last = Math.max(1, Math.ceil(data.total / perPage));
        if (page > last) update({ page: last > 1 ? String(last) : '' });
    }, [data, page]);

    return (
        <PageContainer wide>
            <PageHeader
                title={mailbox.data ? mailboxLabel(mailbox.data) : t('nav.mails')}
                crumbs={[{ label: t('nav.mails'), to: '/mails' }, { label: mailbox.data?.address ?? '...' }]}
            />

            <div className='grid grid-cols-1 gap-3 md:grid-cols-3'>
                <MailboxSelect
                    mailboxes={mailboxes}
                    value={mailboxId}
                    onChange={v => {
                        if (v) navigate(`/mails/${v}`);
                    }}
                />
                <form
                    role='search'
                    onSubmit={e => {
                        e.preventDefault();
                        update({ q: search.trim() });
                    }}
                >
                    <InputField
                        label={t('common.search')}
                        type='search'
                        value={search}
                        onChange={e => setSearch(e.target.value)}
                        onBlur={() => search.trim() !== q && update({ q: search.trim() })}
                        placeholder={t('mails.searchPlaceholder')}
                        enterKeyHint='search'
                    />
                </form>
                <div className='flex items-end'>
                    <CheckboxField
                        label={t('mails.onlyBounce')}
                        checked={onlyBounce}
                        onChange={e => update({ only_bounce: e.target.checked ? '1' : '' })}
                    />
                </div>
            </div>

            {group && (
                <div className='mt-3 flex flex-wrap items-center gap-2'>
                    <Badge tone='accent' icon='filter_alt'>
                        {t('mails.groupFilter')}: <code className='font-mono'>{group}</code>
                    </Badge>
                    <Button size='sm' variant='ghost' icon='close' onClick={() => update({ group: '' })}>
                        {t('common.clearFilter')}
                    </Button>
                </div>
            )}

            <div className='mt-4 flex flex-col gap-4'>
                {list.loading ? (
                    <LoadingBlock />
                ) : list.error || !data ? (
                    <ErrorState message={errorMessage(list.error, t)} onRetry={() => void list.reload()} />
                ) : (
                    <>
                        <Table
                            columns={columns}
                            rows={data.messages}
                            rowKey={m => m.message_key}
                            caption={t('nav.mails')}
                            onRowClick={m => navigate(`/mails/${id}/${encodeURIComponent(m.message_key)}`)}
                            rowAriaLabel={m => t('mail.openAria', { subject: m.subject || t('mail.noSubject') })}
                            emptyState={
                                <EmptyState
                                    title={t('mails.empty')}
                                    description={q || onlyBounce || group ? t('mails.emptyFiltered') : undefined}
                                />
                            }
                            dense
                        />
                        {data.total > 0 && (
                            <Pagination
                                page={data.page || page}
                                perPage={data.per_page || MAIL_PAGE_SIZE}
                                total={data.total}
                                onChange={p => update({ page: String(p) })}
                            />
                        )}
                    </>
                )}
            </div>
        </PageContainer>
    );
}

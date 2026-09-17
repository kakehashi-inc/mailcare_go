import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useParams, useSearchParams } from 'react-router-dom';
import { getMailbox, listGroups } from '../api/client';
import { GroupRow } from '../components/domain/GroupRow';
import { MailboxSelect } from '../components/domain/MailboxSelect';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { InputField, SelectField } from '../components/ui/Field';
import { PageContainer, PageHeader } from '../components/ui/PageHeader';
import { LoadingBlock } from '../components/ui/Spinner';
import { TabPanel, Tabs } from '../components/ui/Tabs';
import { useAsync } from '../hooks/useAsync';
import { useDocumentTitle } from '../hooks/useDocumentTitle';
import { useMailboxes } from '../hooks/useMailboxes';
import type { GroupState } from '../types';
import { errorMessage } from '../utils/errors';
import { mailboxLabel } from '../utils/format';
import { useNavigate } from 'react-router-dom';

const STATES: GroupState[] = ['open', 'resolved', 'ignored'];
const RESPONSIBLES = ['sender', 'recipient', 'domain', 'unknown'] as const;
type Sort = 'last_seen_desc' | 'last_seen_asc' | 'count_desc' | 'severity';

const SEVERITY_ORDER: Record<string, number> = { high: 0, medium: 1, low: 2 };

export function AlertsGroupsPage() {
    const { t } = useTranslation();
    const { mailboxId = '' } = useParams();
    const id = Number(mailboxId);
    const navigate = useNavigate();
    const [params, setParams] = useSearchParams();
    const state = (STATES.includes(params.get('state') as GroupState) ? params.get('state') : 'open') as GroupState;
    const responsible = params.get('responsible') ?? '';
    const q = params.get('q') ?? '';
    const [search, setSearch] = useState(q);
    const [sort, setSort] = useState<Sort>('last_seen_desc');

    const { mailboxes } = useMailboxes();
    const mailbox = useAsync(() => getMailbox(id), [id]);
    useDocumentTitle(mailbox.data ? `${t('nav.alerts')} - ${mailbox.data.address}` : t('nav.alerts'));
    const groups = useAsync(() => listGroups(id, { state, responsible, q }), [id, state, responsible, q]);

    function update(patch: Record<string, string>) {
        const next = new URLSearchParams(params);
        for (const [k, v] of Object.entries(patch)) {
            if (v) next.set(k, v);
            else next.delete(k);
        }
        setParams(next, { replace: true });
    }

    const rows = useMemo(() => {
        const list = [...(groups.data?.groups ?? [])];
        const time = (s: string | null) => (s ? new Date(s).getTime() : 0);
        switch (sort) {
            case 'last_seen_asc':
                list.sort((a, b) => time(a.last_seen) - time(b.last_seen));
                break;
            case 'count_desc':
                list.sort((a, b) => b.message_count - a.message_count);
                break;
            case 'severity':
                list.sort(
                    (a, b) =>
                        (SEVERITY_ORDER[a.report_severity] ?? 3) - (SEVERITY_ORDER[b.report_severity] ?? 3) ||
                        time(b.last_seen) - time(a.last_seen)
                );
                break;
            default:
                list.sort((a, b) => time(b.last_seen) - time(a.last_seen));
        }
        return list;
    }, [groups.data, sort]);

    const counts = groups.data?.counts;

    return (
        <PageContainer wide>
            <PageHeader
                title={mailbox.data ? mailboxLabel(mailbox.data) : t('nav.alerts')}
                crumbs={[{ label: t('nav.alerts'), to: '/alerts' }, { label: mailbox.data?.address ?? '...' }]}
            />

            <div className='grid grid-cols-1 gap-3 md:grid-cols-3'>
                <MailboxSelect
                    mailboxes={mailboxes}
                    value={mailboxId}
                    onChange={v => {
                        if (v) navigate(`/alerts/${v}?${params.toString()}`);
                    }}
                />
                <form
                    className='md:col-span-1'
                    onSubmit={e => {
                        e.preventDefault();
                        update({ q: search.trim() });
                    }}
                    role='search'
                >
                    <InputField
                        label={t('common.search')}
                        type='search'
                        value={search}
                        onChange={e => setSearch(e.target.value)}
                        onBlur={() => search.trim() !== q && update({ q: search.trim() })}
                        placeholder={t('alerts.searchPlaceholder')}
                        enterKeyHint='search'
                    />
                </form>
                <div className='grid grid-cols-2 gap-3'>
                    <SelectField
                        label={t('alerts.responsibleFilter')}
                        value={responsible}
                        onChange={e => update({ responsible: e.target.value })}
                    >
                        <option value=''>{t('common.all')}</option>
                        {RESPONSIBLES.map(r => (
                            <option key={r} value={r}>
                                {t(`responsible.${r}`)}
                            </option>
                        ))}
                    </SelectField>
                    <SelectField label={t('common.sort')} value={sort} onChange={e => setSort(e.target.value as Sort)}>
                        <option value='last_seen_desc'>{t('alerts.sortLastSeenDesc')}</option>
                        <option value='last_seen_asc'>{t('alerts.sortLastSeenAsc')}</option>
                        <option value='count_desc'>{t('alerts.sortCountDesc')}</option>
                        <option value='severity'>{t('alerts.sortSeverity')}</option>
                    </SelectField>
                </div>
            </div>

            <div className='mt-4'>
                <Tabs<GroupState>
                    label={t('alerts.stateTabs')}
                    value={state}
                    onChange={s => update({ state: s })}
                    tabs={STATES.map(s => ({
                        key: s,
                        label: t(`groupState.${s}`),
                        icon:
                            s === 'open'
                                ? 'notifications_active'
                                : s === 'resolved'
                                  ? 'check_circle'
                                  : 'visibility_off',
                        count: counts ? counts[s] : undefined,
                    }))}
                />
                {STATES.map(s => (
                    <TabPanel key={s} id={s} active={state === s}>
                        {groups.loading ? (
                            <LoadingBlock />
                        ) : groups.error ? (
                            <ErrorState message={errorMessage(groups.error, t)} onRetry={() => void groups.reload()} />
                        ) : rows.length === 0 ? (
                            <EmptyState
                                icon={s === 'open' ? 'task_alt' : 'inbox'}
                                title={t(`alerts.empty.${s}`)}
                                description={q || responsible ? t('alerts.emptyFiltered') : undefined}
                            />
                        ) : (
                            <ul className='flex flex-col gap-3' aria-live='polite'>
                                {rows.map(g => (
                                    <GroupRow
                                        key={g.group_key}
                                        group={g}
                                        to={`/alerts/${id}/groups/${encodeURIComponent(g.group_key)}`}
                                    />
                                ))}
                            </ul>
                        )}
                    </TabPanel>
                ))}
            </div>
        </PageContainer>
    );
}

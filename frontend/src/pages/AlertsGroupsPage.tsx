import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { getMailbox, listGroups } from '../api/client';
import { GroupRow } from '../components/domain/GroupRow';
import { MailboxSelect } from '../components/domain/MailboxSelect';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { InputField, SelectField } from '../components/ui/Field';
import { Icon } from '../components/ui/Icon';
import { PageContainer, PageHeader } from '../components/ui/PageHeader';
import { LoadingBlock } from '../components/ui/Spinner';
import { TabPanel, Tabs } from '../components/ui/Tabs';
import { useAsync } from '../hooks/useAsync';
import { useDocumentTitle } from '../hooks/useDocumentTitle';
import { useMailboxes } from '../hooks/useMailboxes';
import { BOUNCE_CATEGORIES, type GroupScope, type GroupState } from '../types';
import { categoryLabel } from '../utils/category';
import { mailboxLabel } from '../utils/format';

const STATES: GroupState[] = ['open', 'resolved', 'ignored'];
const SCOPES: { key: Exclude<GroupScope, 'all'>; icon: string }[] = [
    { key: 'actionable', icon: 'build' },
    { key: 'excluded', icon: 'do_not_disturb_on' },
];
const RESPONSIBLES = ['sender', 'recipient', 'domain', 'unknown'] as const;
type Sort = 'last_seen_desc' | 'last_seen_asc' | 'count_desc' | 'severity';

const SEVERITY_ORDER: Record<string, number> = { high: 0, medium: 1, low: 2 };

export function AlertsGroupsPage() {
    const { t } = useTranslation();
    const { mailboxId = '' } = useParams();
    const id = Number(mailboxId);
    const navigate = useNavigate();
    const [params, setParams] = useSearchParams();
    const scope: Exclude<GroupScope, 'all'> = params.get('scope') === 'excluded' ? 'excluded' : 'actionable';
    const state = (STATES.includes(params.get('state') as GroupState) ? params.get('state') : 'open') as GroupState;
    const category = params.get('category') ?? '';
    const responsible = params.get('responsible') ?? '';
    const q = params.get('q') ?? '';
    const [search, setSearch] = useState(q);
    const [sort, setSort] = useState<Sort>('last_seen_desc');

    const { mailboxes } = useMailboxes();
    const mailbox = useAsync(() => getMailbox(id), [id]);
    useDocumentTitle(mailbox.data ? `${t('layout.nav.alerts')} - ${mailbox.data.address}` : t('layout.nav.alerts'));
    // Excluded groups have no states: their list is requested without one.
    const listState = scope === 'excluded' ? '' : state;
    const groups = useAsync(
        () => listGroups(id, { scope, state: listState, category, responsible, q }),
        [id, scope, listState, category, responsible, q]
    );

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
    const filtered = Boolean(q || responsible || category);

    function renderList(emptyTitle: string) {
        if (groups.loading) return <LoadingBlock />;
        if (groups.error) {
            return (
                <ErrorState
                    message={t(groups.error?.key ?? 'system.internal', groups.error?.params)}
                    onRetry={() => void groups.reload()}
                />
            );
        }
        if (rows.length === 0) {
            return <EmptyState title={emptyTitle} description={filtered ? t('common.emptyFiltered') : undefined} />;
        }
        return (
            <ul className='flex flex-col gap-3' aria-live='polite'>
                {rows.map(g => (
                    <GroupRow
                        key={g.group_key}
                        group={g}
                        to={`/alerts/${id}/groups/${encodeURIComponent(g.group_key)}`}
                    />
                ))}
            </ul>
        );
    }

    return (
        <PageContainer wide>
            <PageHeader
                title={mailbox.data ? mailboxLabel(mailbox.data) : t('layout.nav.alerts')}
                crumbs={[{ label: t('layout.nav.alerts'), to: '/alerts' }, { label: mailbox.data?.address ?? '...' }]}
            />

            <div className='grid grid-cols-1 gap-3 md:grid-cols-2'>
                <MailboxSelect
                    mailboxes={mailboxes}
                    value={mailboxId}
                    onChange={v => {
                        if (v) navigate(`/alerts/${v}?${params.toString()}`);
                    }}
                />
                <form
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
                        placeholder={t('page.alerts.searchPlaceholder')}
                        enterKeyHint='search'
                    />
                </form>
            </div>
            <div className='mt-3 grid grid-cols-1 gap-3 sm:grid-cols-3'>
                <SelectField
                    label={t('field.group.category')}
                    value={category}
                    onChange={e => update({ category: e.target.value })}
                >
                    <option value=''>{t('common.all')}</option>
                    {BOUNCE_CATEGORIES.map(c => (
                        <option key={c} value={c}>
                            {categoryLabel(c, t)}
                        </option>
                    ))}
                </SelectField>
                <SelectField
                    label={t('field.group.responsible')}
                    value={responsible}
                    onChange={e => update({ responsible: e.target.value })}
                >
                    <option value=''>{t('common.all')}</option>
                    {RESPONSIBLES.map(r => (
                        <option key={r} value={r}>
                            {t(`value.responsible.${r}`)}
                        </option>
                    ))}
                </SelectField>
                <SelectField label={t('common.sort')} value={sort} onChange={e => setSort(e.target.value as Sort)}>
                    <option value='last_seen_desc'>{t('page.alerts.sortLastSeenDesc')}</option>
                    <option value='last_seen_asc'>{t('page.alerts.sortLastSeenAsc')}</option>
                    <option value='count_desc'>{t('page.alerts.sortCountDesc')}</option>
                    <option value='severity'>{t('field.group.severity')}</option>
                </SelectField>
            </div>

            <div className='mt-5'>
                <div
                    role='group'
                    aria-label={t('page.alerts.scope')}
                    className='inline-flex max-w-full flex-wrap gap-1 rounded-lg border border-line bg-well p-1'
                >
                    {SCOPES.map(s => {
                        const active = scope === s.key;
                        return (
                            <button
                                key={s.key}
                                type='button'
                                aria-pressed={active}
                                onClick={() => update({ scope: s.key === 'actionable' ? '' : s.key, state: '' })}
                                className={`inline-flex min-h-tap items-center gap-1.5 rounded-md px-3 py-1 text-base font-medium transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-accent ${
                                    active ? 'bg-surface text-accent shadow-sm' : 'text-muted hover:text-ink'
                                }`}
                            >
                                <Icon name={s.icon} className='text-[18px]' />
                                {t(
                                    s.key === 'actionable' ? 'value.groupScope.actionable' : 'value.groupScope.excluded'
                                )}
                            </button>
                        );
                    })}
                </div>
                {scope === 'excluded' && <p className='mt-2 text-sm text-muted'>{t('page.alerts.excludedHint')}</p>}
            </div>

            {scope === 'excluded' ? (
                <div className='mt-4'>{renderList(t('page.alerts.empty.excluded'))}</div>
            ) : (
                <div className='mt-4'>
                    <Tabs<GroupState>
                        label={t('field.group.state')}
                        value={state}
                        onChange={s => update({ state: s })}
                        tabs={STATES.map(s => ({
                            key: s,
                            label: t(`value.groupState.${s}`),
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
                            {renderList(t(`page.alerts.empty.${s}`))}
                        </TabPanel>
                    ))}
                </div>
            )}
        </PageContainer>
    );
}

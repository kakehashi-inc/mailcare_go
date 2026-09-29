import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { getMailbox, listGroups, setGroupState, ApiError } from '../api/client';
import { GroupRow } from '../components/domain/GroupRow';
import { MailboxSelect } from '../components/domain/MailboxSelect';
import { Button } from '../components/ui/Button';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { InputField, SelectField } from '../components/ui/Field';
import { Icon } from '../components/ui/Icon';
import { PageContainer, PageHeader } from '../components/ui/PageHeader';
import { Pagination } from '../components/ui/Pagination';
import { LoadingBlock } from '../components/ui/Spinner';
import { TabPanel, Tabs } from '../components/ui/Tabs';
import { useToast } from '../components/ui/Toast';
import { GROUP_PAGE_SIZE } from '../constants';
import { useAsync } from '../hooks/useAsync';
import { useDocumentTitle } from '../hooks/useDocumentTitle';
import { useMailboxes } from '../hooks/useMailboxes';
import { BOUNCE_CATEGORIES, type GroupDTO, type GroupScope, type GroupSort, type GroupState } from '../types';
import { categoryLabel, groupHeadline } from '../utils/category';
import { mailboxLabel } from '../utils/format';

const STATES: GroupState[] = ['open', 'resolved', 'ignored'];
const SCOPES: { key: Exclude<GroupScope, 'all'>; icon: string }[] = [
    { key: 'actionable', icon: 'build' },
    { key: 'excluded', icon: 'do_not_disturb_on' },
];
const RESPONSIBLES = ['sender', 'recipient', 'domain', 'unknown'] as const;
/**
 * The state changes offered on each row of an actionable-list tab (every signed-in user), labeled
 * with the target state alone and the icon of the detail page's button (the aria-label names the
 * action and the group).
 */
const ROW_ACTIONS: Record<GroupState, GroupState[]> = {
    open: ['resolved', 'ignored'],
    resolved: ['open'],
    ignored: ['open'],
};
const STATE_ICONS: Record<GroupState, string> = { open: 'undo', resolved: 'check_circle', ignored: 'visibility_off' };
/** Orders offered by the sort select; '' (newest first) is the server default and is left out of the URL. */
const SORTS: readonly (GroupSort | '')[] = ['', 'last_seen_asc', 'count_desc', 'severity'];

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
    const sort = SORTS.find(s => s !== '' && s === params.get('sort')) ?? '';
    const page = Math.max(1, Number(params.get('page') ?? '1') || 1);
    const [search, setSearch] = useState(q);
    const toast = useToast();
    // The row whose state is being changed, and to which state.
    const [changing, setChanging] = useState<{ key: string; state: GroupState } | null>(null);

    const { mailboxes } = useMailboxes();
    const mailbox = useAsync(() => getMailbox(id), [id]);
    useDocumentTitle(mailbox.data ? `${t('layout.nav.alerts')} - ${mailbox.data.address}` : t('layout.nav.alerts'));
    // Excluded groups have no states: their list is requested without one.
    const listState = scope === 'excluded' ? '' : state;
    // The server sorts and pages the list, so that the order holds across pages.
    const groups = useAsync(
        () =>
            listGroups(id, {
                scope,
                state: listState,
                category,
                responsible,
                q,
                sort: sort || undefined,
                page,
                per_page: GROUP_PAGE_SIZE,
            }),
        [id, scope, listState, category, responsible, q, sort, page]
    );

    // Every change of the filters, the order or the tab starts again at the first page.
    function update(patch: Record<string, string>) {
        const next = new URLSearchParams(params);
        for (const [k, v] of Object.entries(patch)) {
            if (v) next.set(k, v);
            else next.delete(k);
        }
        if (!('page' in patch)) next.delete('page');
        setParams(next, { replace: true });
    }

    // A page past the end (its last group changed state, or an old URL) moves to the last page.
    const data = groups.data;
    useEffect(() => {
        if (data && data.groups.length === 0 && data.total > 0 && page > 1) {
            update({ page: String(Math.ceil(data.total / (data.per_page || GROUP_PAGE_SIZE))) });
        }
        // Runs only when a new page of data arrives (update reads the current URL parameters).
    }, [data]);

    // Changes the state of one group from the list; it then moves to another tab, so the list and the
    // tab counts are loaded again.
    async function changeState(group: GroupDTO, next: GroupState) {
        setChanging({ key: group.group_key, state: next });
        try {
            await setGroupState(id, group.group_key, next);
            toast.success(t('result.group.stateChanged', { state: t(`value.groupState.${next}`) }));
            await groups.reload();
        } catch (err) {
            toast.error(t((err as ApiError).key, (err as ApiError).params));
        } finally {
            setChanging(null);
        }
    }

    function rowActions(group: GroupDTO) {
        if (scope !== 'actionable') return undefined;
        const headline = groupHeadline(group, t);
        return ROW_ACTIONS[group.state].map(next => (
            <Button
                key={next}
                size='xs'
                variant={next === 'resolved' ? 'primary' : 'secondary'}
                icon={STATE_ICONS[next]}
                loading={changing?.key === group.group_key && changing.state === next}
                disabled={changing !== null}
                onClick={() => void changeState(group, next)}
                aria-label={t(`action.group.markAsFor.${next}`, { group: headline })}
            >
                {t(`value.groupState.${next}`)}
            </Button>
        ));
    }

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
        if (!data || data.groups.length === 0) {
            return <EmptyState title={emptyTitle} description={filtered ? t('common.emptyFiltered') : undefined} />;
        }
        const pager = (
            <Pagination
                page={data.page || page}
                perPage={data.per_page || GROUP_PAGE_SIZE}
                total={data.total}
                onChange={p => update({ page: String(p) })}
            />
        );
        return (
            <div className='flex flex-col gap-4'>
                {pager}
                <ul className='flex flex-col gap-3' aria-live='polite'>
                    {data.groups.map(g => (
                        <GroupRow
                            key={g.group_key}
                            group={g}
                            to={`/alerts/${id}/groups/${encodeURIComponent(g.group_key)}`}
                            actions={rowActions(g)}
                        />
                    ))}
                </ul>
                {pager}
            </div>
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
                        if (!v) return;
                        // Another mailbox keeps the filters and the order, and starts at the first page.
                        const next = new URLSearchParams(params);
                        next.delete('page');
                        navigate(`/alerts/${v}?${next.toString()}`);
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
                <SelectField label={t('common.sort')} value={sort} onChange={e => update({ sort: e.target.value })}>
                    <option value=''>{t('page.alerts.sortLastSeenDesc')}</option>
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

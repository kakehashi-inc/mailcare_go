import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { checkMailbox, createJob, getDashboard } from '../api/client';
import { useAuth } from '../auth/AuthProvider';
import { GroupRow } from '../components/domain/GroupRow';
import { JobList } from '../components/domain/JobList';
import { CheckStatusBadge } from '../components/domain/StatusBadges';
import { Alert } from '../components/ui/Alert';
import { Badge } from '../components/ui/Badge';
import { Button, LinkButton } from '../components/ui/Button';
import { Card, CardHeader } from '../components/ui/Card';
import { DateTime } from '../components/ui/DateTime';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Icon } from '../components/ui/Icon';
import { PageContainer, PageHeader } from '../components/ui/PageHeader';
import { LoadingBlock } from '../components/ui/Spinner';
import { useToast } from '../components/ui/Toast';
import { DASHBOARD_REFRESH_MS, JOB_POLL_INTERVAL_MS } from '../constants';
import { useAsync } from '../hooks/useAsync';
import { useDocumentTitle } from '../hooks/useDocumentTitle';
import { usePolling } from '../hooks/usePolling';
import type { MailboxDTO } from '../types';
import { errorMessage } from '../utils/errors';
import { formatNumber } from '../utils/format';

export function DashboardPage() {
    const { t } = useTranslation();
    useDocumentTitle(t('nav.dashboard'));
    const { isAdmin } = useAuth();
    const toast = useToast();
    const { data, error, loading, reload } = useAsync(getDashboard, []);
    const [checking, setChecking] = useState<number | 'all' | null>(null);

    const hasActive = (data?.active_jobs.length ?? 0) > 0;
    // A mailbox is being checked when a check job for it (or for all mailboxes) is queued or running.
    const checkActive = (mailboxId: number) =>
        (data?.active_jobs ?? []).some(
            j => j.kind === 'check' && (j.mailbox_id === null || j.mailbox_id === mailboxId)
        );
    usePolling(reload, !loading, hasActive ? JOB_POLL_INTERVAL_MS : DASHBOARD_REFRESH_MS);

    async function runCheck(mb: MailboxDTO | null) {
        setChecking(mb ? mb.id : 'all');
        try {
            const result = mb ? await checkMailbox(mb.id) : await createJob({ kind: 'check' });
            if (!result.created) toast.info(t('jobs.alreadyActive'));
            else
                toast.success(mb ? t('dashboard.checkQueued', { address: mb.address }) : t('dashboard.checkAllQueued'));
            await reload();
        } catch (err) {
            toast.error(errorMessage(err, t));
        } finally {
            setChecking(null);
        }
    }

    if (loading) return <LoadingBlock />;
    if (error || !data) {
        return (
            <PageContainer>
                <ErrorState message={errorMessage(error, t)} onRetry={() => void reload()} />
            </PageContainer>
        );
    }

    const totals = [
        {
            label: t('dashboard.totalMailboxes'),
            value: data.totals.mailboxes,
            icon: 'alternate_email',
            tone: 'text-ink',
        },
        {
            label: t('dashboard.totalOpenGroups'),
            value: data.totals.open_groups,
            icon: 'notifications_active',
            tone: data.totals.open_groups > 0 ? 'text-danger' : 'text-ink',
        },
        { label: t('dashboard.totalBounces'), value: data.totals.bounces, icon: 'report', tone: 'text-ink' },
        { label: t('dashboard.totalMessages'), value: data.totals.messages, icon: 'mail', tone: 'text-ink' },
    ];

    return (
        <PageContainer wide>
            <PageHeader
                title={t('nav.dashboard')}
                description={t('dashboard.description')}
                actions={
                    <Button
                        variant='primary'
                        icon='sync'
                        loading={checking === 'all'}
                        disabled={data.mailboxes.length === 0 || checking !== null}
                        onClick={() => void runCheck(null)}
                    >
                        {t('dashboard.checkAll')}
                    </Button>
                }
            />

            {!data.agent.enabled && (
                <Alert tone='info' className='mb-4' title={t('dashboard.agentDisabledTitle')}>
                    {t('dashboard.agentDisabled')}
                    {isAdmin && (
                        <>
                            {' '}
                            <Link
                                to='/settings/general'
                                className='font-medium text-accent underline underline-offset-2'
                            >
                                {t('nav.settingsGeneral')}
                            </Link>
                        </>
                    )}
                </Alert>
            )}
            {data.agent.enabled && !data.agent.available && (
                <Alert tone='warning' className='mb-4' title={t('dashboard.agentUnavailableTitle')}>
                    {t('dashboard.agentUnavailable', { provider: data.agent.provider })}
                </Alert>
            )}

            <section aria-label={t('dashboard.totals')} className='grid grid-cols-2 gap-3 md:grid-cols-4'>
                {totals.map(item => (
                    <Card key={item.label} className='flex items-center gap-3'>
                        <Icon name={item.icon} className='text-[28px] text-muted' />
                        <div className='min-w-0'>
                            <p className='break-words text-sm leading-tight text-muted'>{item.label}</p>
                            <p className={`text-2xl font-bold ${item.tone}`}>{formatNumber(item.value)}</p>
                        </div>
                    </Card>
                ))}
            </section>

            <div className='mt-6 grid grid-cols-1 gap-6 lg:grid-cols-3'>
                <div className='flex flex-col gap-6 lg:col-span-2'>
                    <section>
                        <CardHeader title={t('dashboard.mailboxes')} />
                        {data.mailboxes.length === 0 ? (
                            <EmptyState
                                icon='mail_outline'
                                title={t('mailbox.emptyTitle')}
                                description={isAdmin ? t('mailbox.emptyAdminHint') : t('mailbox.emptyUserHint')}
                                action={
                                    isAdmin ? (
                                        <LinkButton to='/settings/mailboxes/new' variant='primary' icon='add'>
                                            {t('mailbox.add')}
                                        </LinkButton>
                                    ) : undefined
                                }
                            />
                        ) : (
                            <ul className='grid grid-cols-1 gap-4 md:grid-cols-2'>
                                {data.mailboxes.map(mb => (
                                    <li key={mb.id}>
                                        <Card className='flex h-full flex-col gap-3'>
                                            <div className='flex items-start justify-between gap-2'>
                                                <div className='min-w-0'>
                                                    <Link
                                                        to={`/alerts/${mb.id}`}
                                                        className='flex min-h-tap items-center truncate rounded text-base font-semibold text-ink hover:text-accent hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-accent'
                                                    >
                                                        {mb.display_name || mb.address}
                                                    </Link>
                                                    {mb.display_name && (
                                                        <p className='truncate text-sm text-muted'>{mb.address}</p>
                                                    )}
                                                </div>
                                                {checkActive(mb.id) ? (
                                                    <Badge tone='info' icon='autorenew'>
                                                        {t('checkStatus.running')}
                                                    </Badge>
                                                ) : (
                                                    <CheckStatusBadge status={mb.last_check_status} />
                                                )}
                                            </div>
                                            <dl className='grid grid-cols-2 gap-2 text-sm'>
                                                <div>
                                                    <dt className='text-muted'>{t('mailbox.openGroups')}</dt>
                                                    <dd
                                                        className={`text-xl font-bold ${mb.stats?.groups.open ? 'text-danger' : 'text-ink'}`}
                                                    >
                                                        {mb.stats ? formatNumber(mb.stats.groups.open) : '-'}
                                                    </dd>
                                                </div>
                                                <div>
                                                    <dt className='text-muted'>{t('mailbox.bounces')}</dt>
                                                    <dd className='text-xl font-bold text-ink'>
                                                        {mb.stats ? formatNumber(mb.stats.bounces) : '-'}
                                                    </dd>
                                                </div>
                                                <div className='col-span-2'>
                                                    <dt className='text-muted'>{t('mailbox.lastChecked')}</dt>
                                                    <dd className='text-ink'>
                                                        <DateTime
                                                            value={mb.last_checked_at}
                                                            relative
                                                            empty={t('checkStatus.never')}
                                                        />
                                                    </dd>
                                                </div>
                                            </dl>
                                            {mb.last_check_status === 'error' && mb.last_check_error && (
                                                <p className='break-words text-sm text-danger' role='alert'>
                                                    {mb.last_check_error}
                                                </p>
                                            )}
                                            {!mb.enabled && (
                                                <p className='text-sm text-muted'>{t('mailbox.disabledNote')}</p>
                                            )}
                                            <div className='mt-auto flex flex-wrap gap-2'>
                                                <Button
                                                    size='sm'
                                                    variant='primary'
                                                    icon='sync'
                                                    loading={checking === mb.id}
                                                    disabled={checking !== null || checkActive(mb.id)}
                                                    onClick={() => void runCheck(mb)}
                                                >
                                                    {t('dashboard.checkNow')}
                                                </Button>
                                                <LinkButton size='sm' to={`/alerts/${mb.id}`} icon='notifications'>
                                                    {t('nav.alerts')}
                                                </LinkButton>
                                                <LinkButton size='sm' to={`/mails/${mb.id}`} icon='mail'>
                                                    {t('nav.mails')}
                                                </LinkButton>
                                            </div>
                                        </Card>
                                    </li>
                                ))}
                            </ul>
                        )}
                    </section>

                    <section>
                        <CardHeader title={t('dashboard.recentGroups')} description={t('dashboard.recentGroupsHint')} />
                        {data.recent_groups.length === 0 ? (
                            <EmptyState icon='task_alt' title={t('dashboard.noRecentGroups')} />
                        ) : (
                            <ul className='flex flex-col gap-3'>
                                {data.recent_groups.map(g => (
                                    <GroupRow
                                        key={`${g.mailbox_id}-${g.group_key}`}
                                        group={g}
                                        mailboxAddress={g.mailbox_address}
                                        showState
                                        to={`/alerts/${g.mailbox_id}/groups/${encodeURIComponent(g.group_key)}`}
                                    />
                                ))}
                            </ul>
                        )}
                    </section>
                </div>

                <aside className='flex flex-col gap-6'>
                    <Card>
                        <CardHeader title={t('dashboard.schedule')} as='h2' />
                        <dl className='space-y-3 text-base'>
                            <div>
                                <dt className='text-sm text-muted'>{t('dashboard.nextCheck')}</dt>
                                <dd className='text-ink'>
                                    {data.next_check_at ? (
                                        <>
                                            <DateTime value={data.next_check_at} />{' '}
                                            <span className='text-sm text-muted'>
                                                (<DateTime value={data.next_check_at} relative />)
                                            </span>
                                        </>
                                    ) : (
                                        t('dashboard.noSchedule')
                                    )}
                                </dd>
                            </div>
                            <div>
                                <dt className='text-sm text-muted'>{t('dashboard.checkTimes')}</dt>
                                <dd className='flex flex-wrap gap-2'>
                                    {data.check_times.length === 0 ? (
                                        <span className='text-muted'>{t('dashboard.noSchedule')}</span>
                                    ) : (
                                        data.check_times.map(time => (
                                            <span
                                                key={time}
                                                className='rounded-md bg-well px-2 py-0.5 font-mono text-sm text-ink'
                                            >
                                                {time}
                                            </span>
                                        ))
                                    )}
                                </dd>
                            </div>
                            <div>
                                <dt className='text-sm text-muted'>{t('dashboard.agent')}</dt>
                                <dd className='flex items-center gap-1 text-ink'>
                                    <Icon
                                        name={
                                            data.agent.enabled && data.agent.available
                                                ? 'check_circle'
                                                : 'remove_circle_outline'
                                        }
                                        className={`text-[18px] ${data.agent.enabled && data.agent.available ? 'text-success' : 'text-muted'}`}
                                    />
                                    {data.agent.provider || '-'}
                                    <span className='text-sm text-muted'>
                                        ({data.agent.enabled ? t('common.enabled') : t('common.disabled')})
                                    </span>
                                </dd>
                            </div>
                        </dl>
                        {isAdmin && (
                            <LinkButton to='/settings/general' size='sm' icon='tune' className='mt-4'>
                                {t('nav.settingsGeneral')}
                            </LinkButton>
                        )}
                    </Card>

                    <Card>
                        <CardHeader
                            title={t('dashboard.activeJobs')}
                            actions={
                                <LinkButton to='/tools' size='sm' icon='list'>
                                    {t('dashboard.allJobs')}
                                </LinkButton>
                            }
                        />
                        <JobList jobs={data.active_jobs} emptyTitle={t('dashboard.noActiveJobs')} />
                        {data.recent_jobs.length > 0 && (
                            <details className='mt-4'>
                                <summary className='cursor-pointer text-sm font-medium text-muted hover:text-ink'>
                                    {t('dashboard.recentJobs')}
                                </summary>
                                <div className='mt-2'>
                                    <JobList jobs={data.recent_jobs} />
                                </div>
                            </details>
                        )}
                    </Card>
                </aside>
            </div>
        </PageContainer>
    );
}

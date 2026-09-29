import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useSearchParams } from 'react-router-dom';
import { cancelJob, listActiveJobs, listFinishedJobs, ApiError } from '../api/client';
import { JobList } from '../components/domain/JobList';
import { Button } from '../components/ui/Button';
import { Card, CardHeader } from '../components/ui/Card';
import { ErrorState } from '../components/ui/ErrorState';
import { PageContainer, PageHeader } from '../components/ui/PageHeader';
import { Pagination } from '../components/ui/Pagination';
import { LoadingBlock } from '../components/ui/Spinner';
import { Tabs } from '../components/ui/Tabs';
import { useToast } from '../components/ui/Toast';
import { JOB_PAGE_SIZE, JOB_POLL_INTERVAL_MS } from '../constants';
import { useAsync } from '../hooks/useAsync';
import { useDocumentTitle } from '../hooks/useDocumentTitle';
import { usePolling } from '../hooks/usePolling';
import { JOB_HISTORY_FILTERS, type JobDTO, type JobHistoryFilter } from '../types';

const FILTER_ICONS: Record<JobHistoryFilter, string> = {
    all: 'history',
    done: 'check_circle',
    error: 'error',
    canceled: 'block',
};

/**
 * Jobs (administrators only). "Active" lists every queued and running job
 * and refreshes itself; a queued job can be canceled. "History" lists the
 * finished jobs, the most recently finished first, filtered by all / done /
 * error / canceled (URL `status`) and paged (URL `page`, JOB_PAGE_SIZE per page); it is
 * reloaded whenever an active job leaves the active list.
 */
export function JobsPage() {
    const { t } = useTranslation();
    useDocumentTitle(t('layout.nav.jobs'));
    const toast = useToast();
    const [params, setParams] = useSearchParams();
    const statusParam = params.get('status');
    const filter: JobHistoryFilter = JOB_HISTORY_FILTERS.find(f => f !== 'all' && f === statusParam) ?? 'all';
    const page = Math.max(1, Number(params.get('page') ?? '1') || 1);
    const [canceling, setCanceling] = useState<number | null>(null);

    const active = useAsync(() => listActiveJobs(), []);
    const history = useAsync(
        () => listFinishedJobs({ status: filter === 'all' ? undefined : filter, page, per_page: JOB_PAGE_SIZE }),
        [filter, page]
    );

    const activeCount = active.data?.length ?? 0;
    usePolling(active.reload, !active.loading, activeCount > 0 ? JOB_POLL_INTERVAL_MS : JOB_POLL_INTERVAL_MS * 5);

    // A job that left the active list has finished (or was canceled): show it in the history.
    const activeIds = (active.data ?? []).map(j => j.id).join(',');
    const previousIds = useRef<string | null>(null);
    const reloadHistory = history.reload;
    useEffect(() => {
        const before = previousIds.current;
        previousIds.current = activeIds;
        if (before === null || before === activeIds) return;
        const now = new Set(activeIds.split(','));
        if (before.split(',').some(id => id && !now.has(id))) void reloadHistory();
    }, [activeIds, reloadHistory]);

    function update(patch: Record<string, string>) {
        const next = new URLSearchParams(params);
        for (const [k, v] of Object.entries(patch)) {
            if (v) next.set(k, v);
            else next.delete(k);
        }
        if (!('page' in patch)) next.delete('page');
        setParams(next, { replace: true });
    }

    async function cancel(job: JobDTO) {
        setCanceling(job.id);
        try {
            await cancelJob(job.id);
            toast.success(t('result.job.canceled'));
            await active.reload();
        } catch (err) {
            toast.error(t((err as ApiError).key, (err as ApiError).params));
        } finally {
            setCanceling(null);
        }
    }

    const refresh = () => Promise.all([active.reload(), history.reload()]);
    const data = history.data;

    return (
        <PageContainer wide>
            <PageHeader
                title={t('layout.nav.jobs')}
                description={t('page.jobs.description')}
                actions={
                    <Button
                        size='sm'
                        icon='refresh'
                        loading={active.refreshing || history.refreshing}
                        onClick={() => void refresh()}
                    >
                        {t('common.refresh')}
                    </Button>
                }
            />

            <div className='flex flex-col gap-6'>
                <Card>
                    <CardHeader title={t('page.jobs.active')} />
                    {active.loading ? (
                        <LoadingBlock />
                    ) : active.error || !active.data ? (
                        <ErrorState
                            message={t(active.error?.key ?? 'system.internal', active.error?.params)}
                            onRetry={() => void active.reload()}
                        />
                    ) : (
                        <JobList
                            jobs={active.data}
                            onCancel={cancel}
                            cancelingId={canceling}
                            emptyTitle={t('page.jobs.activeEmpty')}
                        />
                    )}
                </Card>

                <Card>
                    <CardHeader title={t('page.jobs.history')} />
                    <Tabs<JobHistoryFilter>
                        label={t('page.jobs.historyTabs')}
                        value={filter}
                        onChange={f => update({ status: f === 'all' ? '' : f })}
                        tabs={JOB_HISTORY_FILTERS.map(f => ({
                            key: f,
                            label: t(`page.jobs.filter.${f}`),
                            icon: FILTER_ICONS[f],
                            count: data?.counts?.[f],
                        }))}
                    />
                    <div
                        role='tabpanel'
                        id={`tabpanel-${filter}`}
                        aria-labelledby={`tab-${filter}`}
                        className='mt-4 flex flex-col gap-4'
                    >
                        {history.loading ? (
                            <LoadingBlock />
                        ) : history.error || !data ? (
                            <ErrorState
                                message={t(history.error?.key ?? 'system.internal', history.error?.params)}
                                onRetry={() => void history.reload()}
                            />
                        ) : (
                            <>
                                {data.total > 0 && (
                                    <Pagination
                                        page={data.page || page}
                                        perPage={data.per_page || JOB_PAGE_SIZE}
                                        total={data.total}
                                        onChange={p => update({ page: String(p) })}
                                    />
                                )}
                                <JobList jobs={data.jobs} emptyTitle={t('page.jobs.historyEmpty')} />
                                {data.total > 0 && (
                                    <Pagination
                                        page={data.page || page}
                                        perPage={data.per_page || JOB_PAGE_SIZE}
                                        total={data.total}
                                        onChange={p => update({ page: String(p) })}
                                    />
                                )}
                            </>
                        )}
                    </div>
                </Card>
            </div>
        </PageContainer>
    );
}

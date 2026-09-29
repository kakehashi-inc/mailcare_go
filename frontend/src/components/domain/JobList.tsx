import { useTranslation } from 'react-i18next';
import type { JobDTO } from '../../types';
import { formatDuration } from '../../utils/format';
import { IconButton } from '../ui/Button';
import { DateTime } from '../ui/DateTime';
import { EmptyState } from '../ui/EmptyState';
import { Icon } from '../ui/Icon';
import { JobProgressLog } from './JobProgressLog';
import { JobStatusBadge } from './StatusBadges';

interface JobListProps {
    jobs: JobDTO[];
    onCancel?: (job: JobDTO) => void;
    cancelingId?: number | null;
    emptyTitle?: string;
}

/** Job rows with live progress text. Same layout on every width. */
export function JobList({ jobs, onCancel, cancelingId, emptyTitle }: JobListProps) {
    const { t } = useTranslation();
    if (jobs.length === 0) {
        return <EmptyState title={emptyTitle ?? t('component.jobList.empty')} />;
    }
    return (
        <ul className='flex flex-col gap-2'>
            {jobs.map(job => {
                const active = job.status === 'queued' || job.status === 'running';
                const targetLabel =
                    job.kind === 'analyze'
                        ? job.target === '*'
                            ? t('component.jobList.targetAll')
                            : job.target || t('component.jobList.targetNeeds')
                        : '';
                return (
                    <li key={job.id} className='rounded-lg border border-line bg-surface p-3'>
                        <div className='flex flex-wrap items-start justify-between gap-2'>
                            <div className='min-w-0 flex-1'>
                                <div className='flex flex-wrap items-center gap-2'>
                                    <span className='font-medium text-ink'>{t(`value.jobKind.${job.kind}`)}</span>
                                    <JobStatusBadge status={job.status} />
                                    <span className='text-sm text-muted'>#{job.id}</span>
                                </div>
                                <p className='mt-1 break-all text-sm text-muted'>
                                    {job.mailbox_address ||
                                        (job.mailbox_deleted
                                            ? t('component.jobList.deletedMailbox')
                                            : t('component.mailboxSelect.all'))}
                                    {job.mailbox_id === null &&
                                        !job.mailbox_deleted &&
                                        ` / ${t('component.jobList.expanded')}`}
                                    {targetLabel && ` / ${targetLabel}`}
                                    {job.requested_by &&
                                        ` / ${t('component.jobList.requestedBy', { name: job.requested_by })}`}
                                </p>
                            </div>
                            <div className='flex items-center gap-2 text-sm text-muted'>
                                <DateTime value={job.created_at} relative />
                                {onCancel && job.status === 'queued' && (
                                    <IconButton
                                        icon='cancel'
                                        label={t('action.job.cancel')}
                                        loading={cancelingId === job.id}
                                        onClick={() => onCancel(job)}
                                    />
                                )}
                            </div>
                        </div>
                        {active && (
                            <div className='mt-2 flex items-start gap-2 text-sm text-ink' aria-live='polite'>
                                <Icon name='autorenew' className='mt-0.5 animate-spin text-[18px] text-info' />
                                {job.progress ? (
                                    <JobProgressLog text={job.progress} />
                                ) : (
                                    <span className='min-w-0 flex-1'>{t('component.jobList.waiting')}</span>
                                )}
                            </div>
                        )}
                        {job.status === 'done' && (job.result || job.started_at) && (
                            <p className='mt-2 break-words text-sm text-muted'>
                                {job.result}
                                {job.started_at && (
                                    <span className='ml-2'>({formatDuration(job.started_at, job.finished_at, t)})</span>
                                )}
                            </p>
                        )}
                        {job.status === 'error' && job.error_message && (
                            <p className='mt-2 break-words text-sm text-danger' role='alert'>
                                {job.error_message}
                            </p>
                        )}
                    </li>
                );
            })}
        </ul>
    );
}

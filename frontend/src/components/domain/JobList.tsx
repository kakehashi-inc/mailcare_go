import type { TFunction } from 'i18next';
import { useTranslation } from 'react-i18next';
import { ALL_JOB_KINDS, type JobDTO, type JobKind } from '../../types';
import { formatDuration } from '../../utils/format';
import { IconButton } from '../ui/Button';
import { DateTime } from '../ui/DateTime';
import { EmptyState } from '../ui/EmptyState';
import { Icon } from '../ui/Icon';
import { JobStatusBadge } from './StatusBadges';

/** Label of a job kind; kinds this build does not know (e.g. old "check" jobs) are shown as "Other (kind)". */
export function jobKindLabel(kind: string, t: TFunction): string {
    return (ALL_JOB_KINDS as readonly string[]).includes(kind)
        ? t(`jobKind.${kind as JobKind}`)
        : t('jobKind.other', { kind });
}

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
        return <EmptyState title={emptyTitle ?? t('jobs.empty')} />;
    }
    return (
        <ul className='flex flex-col gap-2'>
            {jobs.map(job => {
                const active = job.status === 'queued' || job.status === 'running';
                const targetLabel =
                    job.kind === 'analyze'
                        ? job.target === '*'
                            ? t('jobs.targetAll')
                            : job.target || t('jobs.targetNeeds')
                        : '';
                return (
                    <li key={job.id} className='rounded-lg border border-line bg-surface p-3'>
                        <div className='flex flex-wrap items-start justify-between gap-2'>
                            <div className='min-w-0 flex-1'>
                                <div className='flex flex-wrap items-center gap-2'>
                                    <span className='font-medium text-ink'>{jobKindLabel(job.kind, t)}</span>
                                    <JobStatusBadge status={job.status} />
                                    <span className='text-sm text-muted'>#{job.id}</span>
                                </div>
                                <p className='mt-1 break-all text-sm text-muted'>
                                    {job.mailbox_address || t('mailbox.all')}
                                    {job.mailbox_id === null && ` / ${t('jobs.expanded')}`}
                                    {targetLabel && ` / ${targetLabel}`}
                                    {job.requested_by && ` / ${t('jobs.requestedBy', { name: job.requested_by })}`}
                                </p>
                            </div>
                            <div className='flex items-center gap-2 text-sm text-muted'>
                                <DateTime value={job.created_at} relative />
                                {onCancel && job.status === 'queued' && (
                                    <IconButton
                                        icon='cancel'
                                        label={t('jobs.cancel')}
                                        loading={cancelingId === job.id}
                                        onClick={() => onCancel(job)}
                                    />
                                )}
                            </div>
                        </div>
                        {active && (
                            <div className='mt-2 flex items-start gap-2 text-sm text-ink' aria-live='polite'>
                                <Icon name='autorenew' className='mt-0.5 animate-spin text-[18px] text-info' />
                                <span className='max-h-40 min-w-0 flex-1 overflow-y-auto whitespace-pre-line break-words font-mono text-sm'>
                                    {job.progress || t('jobs.waiting')}
                                </span>
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

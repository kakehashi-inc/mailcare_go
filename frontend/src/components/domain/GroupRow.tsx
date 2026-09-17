import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import type { GroupDTO } from '../../types';
import { Badge } from '../ui/Badge';
import { DateTime } from '../ui/DateTime';
import { Icon } from '../ui/Icon';
import { GroupStateBadge, ResponsibleBadge, SeverityBadge } from './StatusBadges';

interface GroupRowProps {
    group: GroupDTO;
    to: string;
    /** Shown above the title on cross-mailbox lists. */
    mailboxAddress?: string;
    showState?: boolean;
}

/** One bounce group as a clickable card; used by the alerts list and the dashboard. */
export function GroupRow({ group, to, mailboxAddress, showState = false }: GroupRowProps) {
    const { t } = useTranslation();
    return (
        <li>
            <Link
                to={to}
                className='block rounded-xl border border-line bg-surface p-4 shadow-sm transition-colors hover:border-accent focus:outline-none focus-visible:ring-2 focus-visible:ring-accent'
            >
                {mailboxAddress && <p className='mb-1 truncate text-sm text-muted'>{mailboxAddress}</p>}
                <div className='flex flex-wrap items-center gap-2'>
                    <SeverityBadge severity={group.report_severity} />
                    {showState && <GroupStateBadge state={group.state} />}
                    {group.needs_analysis && group.report_status !== 'running' && (
                        <Badge tone='warning' icon='pending_actions'>
                            {t('group.needsAnalysis')}
                        </Badge>
                    )}
                    {group.report_status === 'running' && (
                        <Badge tone='info' icon='autorenew'>
                            {t('reportStatus.running')}
                        </Badge>
                    )}
                </div>
                <p className='mt-2 break-words text-base font-semibold text-ink'>{group.title}</p>
                <div className='mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-sm text-muted'>
                    <span className='inline-flex items-center gap-1'>
                        <Icon name='dns' className='text-[16px]' />
                        {group.recipient_domain || '-'}
                    </span>
                    <span className='inline-flex items-center gap-1'>
                        <Icon name='code' className='text-[16px]' />
                        {group.status_code || group.smtp_code || '-'}
                    </span>
                    <ResponsibleBadge responsible={group.responsible} />
                </div>
                {group.report_summary && <p className='mt-2 line-clamp-2 text-sm text-ink'>{group.report_summary}</p>}
                <div className='mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-sm text-muted'>
                    <span>{t('group.messages', { count: group.message_count })}</span>
                    <span>{t('group.recipients', { count: group.recipient_count })}</span>
                    <span>{t('group.ips', { count: group.remote_ip_count })}</span>
                    <span className='inline-flex items-center gap-1'>
                        <Icon name='schedule' className='text-[16px]' />
                        {t('group.lastSeen')}: <DateTime value={group.last_seen} relative />
                    </span>
                </div>
            </Link>
        </li>
    );
}

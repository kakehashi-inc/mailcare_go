import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import type { GroupDTO } from '../../types';
import { groupHeadline } from '../../utils/category';
import { Badge } from '../ui/Badge';
import { DateTime } from '../ui/DateTime';
import { Icon } from '../ui/Icon';
import { CategoryBadge, GroupStateBadge, ResponsibleBadge, SeverityBadge, UnanalyzableBadge } from './StatusBadges';

interface GroupRowProps {
    group: GroupDTO;
    to: string;
    /** Shown above the title on cross-mailbox lists. */
    mailboxAddress?: string;
    showState?: boolean;
    /** Compact buttons at the right of the badge row, above the link to the detail (e.g. the state changes of the alerts list). */
    actions?: ReactNode;
}

/**
 * One bounce group as a card; used by the alerts list and the dashboard. The headline is the link to the
 * group detail and covers the whole card (stretched link), so actions, when given, can sit at the right
 * of the badge row above it without being nested in the link.
 */
export function GroupRow({ group, to, mailboxAddress, showState = false, actions }: GroupRowProps) {
    const { t } = useTranslation();
    const headline = groupHeadline(group, t);
    return (
        <li className='relative rounded-xl border border-line bg-surface p-4 shadow-sm transition-colors hover:border-accent'>
            {mailboxAddress && <p className='mb-1 truncate text-sm text-muted'>{mailboxAddress}</p>}
            <div className='flex items-start justify-between gap-2'>
                <div className='flex min-w-0 flex-wrap items-center gap-2'>
                    <CategoryBadge category={group.category} />
                    {group.actionable && <SeverityBadge severity={group.report_severity} />}
                    {showState && <GroupStateBadge state={group.state} />}
                    {group.actionable && group.needs_analysis && group.report_status !== 'running' && (
                        <Badge tone='warning' icon='pending_actions'>
                            {t('field.group.needsAnalysis')}
                        </Badge>
                    )}
                    {group.report_unanalyzable && group.report_status !== 'running' && <UnanalyzableBadge />}
                    {group.report_status === 'running' && (
                        <Badge tone='info' icon='autorenew'>
                            {t('value.reportStatus.running')}
                        </Badge>
                    )}
                </div>
                {actions && <div className='relative z-10 flex shrink-0 flex-wrap justify-end gap-2'>{actions}</div>}
            </div>
            <p className='mt-2 break-words text-base font-semibold text-ink'>
                <Link
                    to={to}
                    className='after:absolute after:inset-0 after:rounded-xl focus:outline-none focus-visible:after:ring-2 focus-visible:after:ring-accent'
                >
                    {headline}
                </Link>
            </p>
            <div className='mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-sm text-muted'>
                <span className='inline-flex items-center gap-1'>
                    <Icon name='code' className='text-[16px]' />
                    {group.status_code || '-'}
                </span>
                <span className='inline-flex items-center gap-1'>
                    <Icon name='dns' className='text-[16px]' />
                    {group.recipient_domain || '-'}
                </span>
                <ResponsibleBadge responsible={group.responsible} />
            </div>
            {group.diagnostic_template && (
                <p className='mt-1 truncate font-mono text-sm text-muted'>{group.diagnostic_template}</p>
            )}
            {group.report_summary && <p className='mt-2 line-clamp-2 text-sm text-ink'>{group.report_summary}</p>}
            <div className='mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-sm text-muted'>
                <span>{t('field.group.messageCountValue', { count: group.message_count })}</span>
                {group.recipient_count > 0 && (
                    <span>{t('field.group.recipientCountValue', { count: group.recipient_count })}</span>
                )}
                <span>{t('field.group.ipCountValue', { count: group.remote_ip_count })}</span>
                <span className='inline-flex items-center gap-1'>
                    <Icon name='schedule' className='text-[16px]' />
                    {t('field.group.lastSeen')}: <DateTime value={group.last_seen} relative />
                </span>
            </div>
        </li>
    );
}

import { useTranslation } from 'react-i18next';
import type { ProbeLevel, ProbeMailDTO, ProbeStatus } from '../../types';
import { formatMinutes, formatSeconds } from '../../utils/format';
import { Badge, type BadgeTone } from '../ui/Badge';

const PROBE_STATUS: Record<ProbeStatus, { tone: BadgeTone; icon: string }> = {
    pending: { tone: 'neutral', icon: 'schedule_send' },
    delayed: { tone: 'warning', icon: 'hourglass_bottom' },
    failure: { tone: 'danger', icon: 'error' },
    send_failed: { tone: 'danger', icon: 'cancel_schedule_send' },
    arrived: { tone: 'success', icon: 'mark_email_read' },
    recovered: { tone: 'info', icon: 'restore' },
    resolved: { tone: 'accent', icon: 'task_alt' },
};

/** Status of a probe mail. */
export function ProbeStatusBadge({ status }: { status: ProbeStatus }) {
    const { t } = useTranslation();
    const s = PROBE_STATUS[status] ?? PROBE_STATUS.pending;
    return (
        <Badge tone={s.tone} icon={s.icon}>
            {t(`value.probeStatus.${status}`)}
        </Badge>
    );
}

/**
 * The worst level a closed probe mail reached (recovered or resolved): it stays visible that the mail was delayed
 * or failed. Nothing for open mails (their status says it) and for mails that never reached a level.
 */
export function ProbeLevelBadge({ status, level }: { status: ProbeStatus; level: ProbeLevel }) {
    const { t } = useTranslation();
    if (!level || (status !== 'recovered' && status !== 'resolved')) return null;
    return (
        <Badge tone={level === 'failure' ? 'danger' : 'warning'} icon='history'>
            {t(`value.probeLevel.${level}`)}
        </Badge>
    );
}

/** Both badges of a probe mail, side by side. */
export function ProbeBadges({ probe }: { probe: Pick<ProbeMailDTO, 'status' | 'level'> }) {
    return (
        <span className='inline-flex flex-wrap items-center gap-1'>
            <ProbeStatusBadge status={probe.status} />
            <ProbeLevelBadge status={probe.status} level={probe.level} />
        </span>
    );
}

/** The delay of a probe mail: how long it took to arrive, or how long it has been missing at the latest check. */
export function ProbeDelay({ probe }: { probe: Pick<ProbeMailDTO, 'status' | 'delay_seconds' | 'arrived_at'> }) {
    const { t } = useTranslation();
    if (probe.status === 'send_failed') return <span className='text-muted'>-</span>;
    // Minutes are precise enough for a delay (the arrival time has whole seconds only).
    const duration =
        probe.delay_seconds < 60
            ? formatSeconds(probe.delay_seconds, t)
            : formatMinutes(Math.floor(probe.delay_seconds / 60), t);
    if (probe.arrived_at) return <span>{t('page.probes.arrivedAfter', { duration })}</span>;
    if (probe.delay_seconds === 0) return <span className='text-muted'>-</span>;
    return (
        <span className={probe.status === 'failure' ? 'text-danger' : probe.status === 'delayed' ? 'text-warning' : ''}>
            {t('page.probes.missingFor', { duration })}
        </span>
    );
}

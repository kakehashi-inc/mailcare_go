import { useTranslation } from 'react-i18next';
import type { CheckStatus, GroupState, JobStatus, ReportStatus } from '../../types';
import { Badge, type BadgeTone } from '../ui/Badge';

const GROUP_STATE: Record<GroupState, { tone: BadgeTone; icon: string }> = {
    open: { tone: 'danger', icon: 'notifications_active' },
    resolved: { tone: 'success', icon: 'check_circle' },
    ignored: { tone: 'neutral', icon: 'visibility_off' },
};

export function GroupStateBadge({ state }: { state: GroupState }) {
    const { t } = useTranslation();
    const s = GROUP_STATE[state] ?? GROUP_STATE.open;
    return (
        <Badge tone={s.tone} icon={s.icon}>
            {t(`groupState.${state}`)}
        </Badge>
    );
}

const SEVERITY: Record<string, { tone: BadgeTone; icon: string }> = {
    high: { tone: 'danger', icon: 'priority_high' },
    medium: { tone: 'warning', icon: 'remove' },
    low: { tone: 'info', icon: 'arrow_downward' },
};

export function SeverityBadge({ severity }: { severity: string }) {
    const { t } = useTranslation();
    const s = SEVERITY[severity];
    if (!s) {
        return (
            <Badge tone='neutral' icon='help_outline'>
                {t('severity.unknown')}
            </Badge>
        );
    }
    return (
        <Badge tone={s.tone} icon={s.icon}>
            {t(`severity.${severity as 'high' | 'medium' | 'low'}`)}
        </Badge>
    );
}

const RESPONSIBLE_ICON: Record<string, string> = {
    sender: 'send',
    recipient: 'person',
    domain: 'dns',
    unknown: 'help_outline',
};

export function ResponsibleBadge({ responsible }: { responsible: string }) {
    const { t } = useTranslation();
    const key = (['sender', 'recipient', 'domain'].includes(responsible) ? responsible : 'unknown') as
        'sender' | 'recipient' | 'domain' | 'unknown';
    return (
        <Badge tone={key === 'unknown' ? 'neutral' : 'accent'} icon={RESPONSIBLE_ICON[key]}>
            {t(`responsible.${key}`)}
        </Badge>
    );
}

const JOB_STATUS: Record<JobStatus, { tone: BadgeTone; icon: string }> = {
    queued: { tone: 'neutral', icon: 'schedule' },
    running: { tone: 'info', icon: 'autorenew' },
    done: { tone: 'success', icon: 'check_circle' },
    error: { tone: 'danger', icon: 'error_outline' },
    canceled: { tone: 'warning', icon: 'cancel' },
};

export function JobStatusBadge({ status }: { status: JobStatus }) {
    const { t } = useTranslation();
    const s = JOB_STATUS[status] ?? JOB_STATUS.queued;
    return (
        <Badge tone={s.tone} icon={s.icon}>
            {t(`jobStatus.${status}`)}
        </Badge>
    );
}

const CHECK_STATUS: Record<CheckStatus, { tone: BadgeTone; icon: string }> = {
    '': { tone: 'neutral', icon: 'remove_circle_outline' },
    ok: { tone: 'success', icon: 'check_circle' },
    error: { tone: 'danger', icon: 'error_outline' },
};

export function CheckStatusBadge({ status }: { status: CheckStatus }) {
    const { t } = useTranslation();
    const s = CHECK_STATUS[status] ?? CHECK_STATUS[''];
    const key = status === '' ? 'never' : status;
    return (
        <Badge tone={s.tone} icon={s.icon}>
            {t(`checkStatus.${key}`)}
        </Badge>
    );
}

const REPORT_STATUS: Record<ReportStatus, { tone: BadgeTone; icon: string }> = {
    '': { tone: 'neutral', icon: 'pending' },
    running: { tone: 'info', icon: 'autorenew' },
    completed: { tone: 'success', icon: 'task_alt' },
    error: { tone: 'danger', icon: 'error_outline' },
};

export function ReportStatusBadge({ status }: { status: ReportStatus | string }) {
    const { t } = useTranslation();
    const key = (status in REPORT_STATUS ? status : '') as ReportStatus;
    const s = REPORT_STATUS[key];
    return (
        <Badge tone={s.tone} icon={s.icon}>
            {t(`reportStatus.${key === '' ? 'none' : key}`)}
        </Badge>
    );
}

export function BounceKindBadge({ kind, isBounce }: { kind: string; isBounce: boolean }) {
    const { t } = useTranslation();
    if (!isBounce) {
        return (
            <Badge tone='neutral' icon='mail'>
                {t('bounceKind.none')}
            </Badge>
        );
    }
    const known = ['failed', 'delayed', 'auto_reply', 'other'] as const;
    const key = (known as readonly string[]).includes(kind) ? (kind as (typeof known)[number]) : 'other';
    const tone: BadgeTone =
        key === 'failed' ? 'danger' : key === 'delayed' ? 'warning' : key === 'auto_reply' ? 'info' : 'neutral';
    const icon =
        key === 'failed'
            ? 'report'
            : key === 'delayed'
              ? 'hourglass_top'
              : key === 'auto_reply'
                ? 'reply'
                : 'help_outline';
    return (
        <Badge tone={tone} icon={icon}>
            {t(`bounceKind.${key}`)}
        </Badge>
    );
}

export function RoleBadge({ role }: { role: string }) {
    const { t } = useTranslation();
    const admin = role === 'admin';
    return (
        <Badge tone={admin ? 'accent' : 'neutral'} icon={admin ? 'admin_panel_settings' : 'person'}>
            {t(admin ? 'role.admin' : 'role.user')}
        </Badge>
    );
}

export function EnabledBadge({ enabled }: { enabled: boolean }) {
    const { t } = useTranslation();
    return (
        <Badge tone={enabled ? 'success' : 'neutral'} icon={enabled ? 'toggle_on' : 'toggle_off'}>
            {t(enabled ? 'common.enabled' : 'common.disabled')}
        </Badge>
    );
}

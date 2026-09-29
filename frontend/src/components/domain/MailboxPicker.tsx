import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { useAuth } from '../../auth/AuthProvider';
import type { MailboxDTO } from '../../types';
import { LinkButton } from '../ui/Button';
import { EmptyState } from '../ui/EmptyState';
import { Icon } from '../ui/Icon';
import { CheckStatusBadge, fetchStatus } from './StatusBadges';
import { DateTime } from '../ui/DateTime';

interface MailboxPickerProps {
    mailboxes: MailboxDTO[];
    /** Builds the destination of a card. */
    linkTo: (mb: MailboxDTO) => string;
    /** Chooses which counter to highlight on the card. */
    highlight: 'open' | 'messages';
}

/** Card grid used by the alerts and mails index pages to choose a mailbox. */
export function MailboxPicker({ mailboxes, linkTo, highlight }: MailboxPickerProps) {
    const { t } = useTranslation();
    const { isAdmin } = useAuth();

    if (mailboxes.length === 0) {
        return (
            <EmptyState
                title={t('component.mailboxPicker.empty')}
                action={
                    isAdmin ? (
                        <LinkButton to='/settings/mailboxes/new' variant='primary' icon='add'>
                            {t('action.mailbox.add')}
                        </LinkButton>
                    ) : undefined
                }
            />
        );
    }

    return (
        <ul className='grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3'>
            {mailboxes.map(mb => {
                const open = mb.stats?.groups.open;
                const recheck = mb.stats
                    ? mb.stats.groups.resolved_recheck + mb.stats.groups.ignored_recheck
                    : undefined;
                const messages = mb.stats?.messages;
                const targetMessages = mb.stats?.target_messages;
                const junkMessages = mb.stats?.junk_messages;
                const value = highlight === 'open' ? open : messages;
                return (
                    <li key={mb.id}>
                        <Link
                            to={linkTo(mb)}
                            className='flex h-full flex-col gap-3 rounded-xl border border-line bg-surface p-4 shadow-sm transition-colors hover:border-accent focus:outline-none focus-visible:ring-2 focus-visible:ring-accent'
                            aria-label={t('action.mailbox.openFor', { address: mb.address })}
                        >
                            <div className='flex items-start justify-between gap-3'>
                                <div className='min-w-0'>
                                    <p className='truncate text-base font-semibold text-ink'>
                                        {mb.display_name || mb.address}
                                    </p>
                                    {mb.display_name && <p className='truncate text-sm text-muted'>{mb.address}</p>}
                                </div>
                                <Icon name='chevron_right' className='shrink-0 text-muted' />
                            </div>
                            <div className='flex flex-wrap items-center gap-2'>
                                <CheckStatusBadge status={fetchStatus(mb)} />
                                {!mb.enabled && <span className='text-sm text-muted'>{t('common.disabled')}</span>}
                            </div>
                            <div className='mt-auto flex items-end justify-between gap-3'>
                                <div className='flex min-w-0 flex-wrap gap-x-6 gap-y-2'>
                                    <div>
                                        <p className='text-sm text-muted'>
                                            {highlight === 'open'
                                                ? t('field.mailbox.openGroups')
                                                : t('field.mailbox.messages')}
                                        </p>
                                        <p
                                            className={`text-2xl font-bold ${highlight === 'open' && value ? 'text-danger' : 'text-ink'}`}
                                        >
                                            {value ?? '-'}
                                        </p>
                                    </div>
                                    {highlight === 'open' && (
                                        <div>
                                            <p className='text-sm text-muted'>{t('field.mailbox.recheckGroups')}</p>
                                            <p
                                                className={`text-2xl font-bold ${recheck ? 'text-warning' : 'text-ink'}`}
                                            >
                                                {recheck ?? '-'}
                                            </p>
                                        </div>
                                    )}
                                    {highlight === 'messages' && (
                                        <>
                                            <div>
                                                <p className='text-sm text-muted'>
                                                    {t('field.mailbox.targetMessages')}
                                                </p>
                                                <p className='text-2xl font-bold text-ink'>{targetMessages ?? '-'}</p>
                                            </div>
                                            <div>
                                                <p className='text-sm text-muted'>{t('field.mailbox.junkMessages')}</p>
                                                <p className='text-2xl font-bold text-ink'>{junkMessages ?? '-'}</p>
                                            </div>
                                        </>
                                    )}
                                </div>
                                <p className='shrink-0 whitespace-nowrap text-right text-sm text-muted'>
                                    {t('field.mailbox.lastChecked')}
                                    <br />
                                    <DateTime
                                        value={mb.last_fetched_at}
                                        relative
                                        empty={t('value.checkStatus.never')}
                                    />
                                </p>
                            </div>
                        </Link>
                    </li>
                );
            })}
        </ul>
    );
}

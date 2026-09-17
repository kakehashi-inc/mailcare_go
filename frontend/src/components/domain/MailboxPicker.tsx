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
                title={t('mailbox.emptyTitle')}
                action={
                    isAdmin ? (
                        <LinkButton to='/settings/mailboxes/new' variant='primary' icon='add'>
                            {t('mailbox.add')}
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
                const messages = mb.stats?.messages;
                const value = highlight === 'open' ? open : messages;
                return (
                    <li key={mb.id}>
                        <Link
                            to={linkTo(mb)}
                            className='flex h-full flex-col gap-3 rounded-xl border border-line bg-surface p-4 shadow-sm transition-colors hover:border-accent focus:outline-none focus-visible:ring-2 focus-visible:ring-accent'
                            aria-label={t('mailbox.openAria', { address: mb.address })}
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
                                <div>
                                    <p className='text-sm text-muted'>
                                        {highlight === 'open' ? t('mailbox.openGroups') : t('mailbox.messages')}
                                    </p>
                                    <p
                                        className={`text-2xl font-bold ${highlight === 'open' && value ? 'text-danger' : 'text-ink'}`}
                                    >
                                        {value ?? '-'}
                                    </p>
                                </div>
                                <p className='text-right text-sm text-muted'>
                                    {t('mailbox.lastChecked')}
                                    <br />
                                    <DateTime value={mb.last_fetched_at} relative empty={t('checkStatus.never')} />
                                </p>
                            </div>
                        </Link>
                    </li>
                );
            })}
        </ul>
    );
}

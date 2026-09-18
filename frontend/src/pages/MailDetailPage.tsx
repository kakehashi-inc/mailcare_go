import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useParams } from 'react-router-dom';
import { getMailbox, getMessage, messageHtmlUrl, messageRawUrl } from '../api/client';
import { BounceKindBadge, ResponsibleBadge } from '../components/domain/StatusBadges';
import { Alert } from '../components/ui/Alert';
import { Card, CardHeader } from '../components/ui/Card';
import { CopyButton } from '../components/ui/CopyButton';
import { DateTime } from '../components/ui/DateTime';
import { DescriptionList } from '../components/ui/DescriptionList';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Icon } from '../components/ui/Icon';
import { PageContainer, PageHeader } from '../components/ui/PageHeader';
import { LoadingBlock } from '../components/ui/Spinner';
import { TabPanel, Tabs } from '../components/ui/Tabs';
import { useAsync } from '../hooks/useAsync';
import { useDocumentTitle } from '../hooks/useDocumentTitle';
import { errorMessage } from '../utils/errors';
import { formatBytes } from '../utils/format';

/** Tab keys: the headers, then one tab per text section and per HTML section (text-1, html-2, ...). */
type BodyTab = 'headers' | `text-${number}` | `html-${number}`;

/** Renders one value of the parsed-message JSON: scalars inline, arrays as lines, objects as a nested list. */
function HeaderValue({ value }: { value: unknown }) {
    if (value === null || value === undefined || value === '') return <span className='text-muted'>-</span>;
    if (Array.isArray(value)) {
        if (value.length === 0) return <span className='text-muted'>-</span>;
        return (
            <ul className='flex flex-col gap-1'>
                {value.map((v, i) => (
                    <li key={i}>
                        <HeaderValue value={v} />
                    </li>
                ))}
            </ul>
        );
    }
    if (typeof value === 'object') {
        const entries = Object.entries(value as Record<string, unknown>).filter(
            ([, v]) => v !== null && v !== undefined && v !== ''
        );
        if (entries.length === 0) return <span className='text-muted'>-</span>;
        return (
            <dl className='rounded-md border border-line p-2'>
                {entries.map(([k, v]) => (
                    <div key={k} className='grid grid-cols-1 gap-x-3 py-0.5 sm:grid-cols-[10rem_1fr]'>
                        <dt className='text-muted'>{k}</dt>
                        <dd className='min-w-0'>
                            <HeaderValue value={v} />
                        </dd>
                    </div>
                ))}
            </dl>
        );
    }
    return <>{String(value)}</>;
}

const BUTTON_LINK =
    'inline-flex min-h-tap items-center justify-center gap-2 rounded-md border border-line bg-surface px-4 py-2 text-base font-medium text-ink transition-colors hover:bg-well focus:outline-none focus-visible:ring-2 focus-visible:ring-accent';

const TEXT_BODY =
    'max-h-[70vh] overflow-auto whitespace-pre-wrap break-words rounded-md bg-well p-3 font-mono text-sm leading-relaxed text-ink';

export function MailDetailPage() {
    const { t } = useTranslation();
    const { mailboxId = '', messageKey = '' } = useParams();
    const id = Number(mailboxId);
    const mailbox = useAsync(() => getMailbox(id), [id]);
    const detail = useAsync(() => getMessage(id, messageKey), [id, messageKey]);
    // The chosen tab is remembered per message so that moving to another
    // mail starts from its default tab again.
    const [chosen, setChosen] = useState<{ key: string; tab: BodyTab } | null>(null);
    const tab = chosen?.key === messageKey ? chosen.tab : null;
    const setTab = (next: BodyTab) => setChosen({ key: messageKey, tab: next });
    useDocumentTitle(detail.data?.message.subject || t('mail.detail'));

    if (detail.loading) return <LoadingBlock />;
    if (detail.error || !detail.data) {
        return (
            <PageContainer>
                <ErrorState message={errorMessage(detail.error, t)} onRetry={() => void detail.reload()} />
            </PageContainer>
        );
    }

    const { message, bounce, text_sections, headers } = detail.data;
    const headerEntries = Object.entries(headers ?? {}).filter(([, v]) => v !== null && v !== undefined && v !== '');
    // Text sections arrive in MIME order; blank ones are dropped so the numbering only counts visible text.
    const sections = (text_sections ?? []).filter(s => s.trim() !== '');
    // HTML sections are fetched per tab from the server (<key>-1.html, ..., html_count files).
    const htmlNumbers = Array.from({ length: Math.max(0, message.html_count) }, (_, i) => i + 1);
    const hasText = sections.length > 0;
    const hasHtml = htmlNumbers.length > 0;
    // Headers first, then one tab per section with content. A lone section
    // keeps the plain label; several of a kind are numbered in MIME order.
    const bodyTabs: { key: BodyTab; label: string; icon: string }[] = [
        { key: 'headers', label: t('mail.tabHeaders'), icon: 'list' },
        ...sections.map((_, i) => ({
            key: `text-${i + 1}` as const,
            label: sections.length === 1 ? t('mail.tabText') : t('mail.tabTextN', { index: i + 1 }),
            icon: 'notes',
        })),
        ...htmlNumbers.map(n => ({
            key: `html-${n}` as const,
            label: htmlNumbers.length === 1 ? t('mail.tabHtml') : t('mail.tabHtmlN', { index: n }),
            icon: 'code',
        })),
    ];
    // The body is what a reader opens a mail for, so the first section is
    // selected by default and the headers only when there is no body.
    const defaultTab: BodyTab = hasText ? 'text-1' : hasHtml ? 'html-1' : 'headers';
    const activeTab = tab !== null && bodyTabs.some(b => b.key === tab) ? tab : defaultTab;
    const bodySourceKey = message.body_source === 'text' ? 'text' : message.body_source === 'html' ? 'html' : 'none';

    return (
        <PageContainer wide>
            <PageHeader
                title={message.subject || t('mail.noSubject')}
                crumbs={[
                    { label: t('nav.mails'), to: '/mails' },
                    { label: mailbox.data?.address ?? '...', to: `/mails/${id}` },
                    { label: t('mail.detail') },
                ]}
                actions={
                    <>
                        <a href={messageRawUrl(id, messageKey)} download={`${messageKey}.eml`} className={BUTTON_LINK}>
                            <Icon name='download' className='text-[20px]' />
                            {t('mail.downloadRaw')}
                        </a>
                        {message.group_key && (
                            <Link
                                to={`/alerts/${id}/groups/${encodeURIComponent(message.group_key)}`}
                                className={BUTTON_LINK}
                            >
                                <Icon name='notifications' className='text-[20px]' />
                                {t('mail.openGroup')}
                            </Link>
                        )}
                    </>
                }
            />

            <div className='grid grid-cols-1 gap-6 lg:grid-cols-3'>
                <div className='flex flex-col gap-6 lg:col-span-2'>
                    <Card>
                        <DescriptionList
                            items={[
                                {
                                    label: t('mail.from'),
                                    value: (
                                        <span className='break-all'>
                                            {message.from_name && (
                                                <span className='font-medium'>{message.from_name} </span>
                                            )}
                                            {message.from_address}
                                        </span>
                                    ),
                                },
                                {
                                    label: t('mail.to'),
                                    value: (
                                        <span className='break-all'>
                                            {message.to_name && <span className='font-medium'>{message.to_name} </span>}
                                            {message.to_address || '-'}
                                        </span>
                                    ),
                                },
                                ...(message.date
                                    ? [
                                          { label: t('mail.date'), value: <DateTime value={message.date} /> },
                                          {
                                              label: t('mail.receivedAt'),
                                              value: <DateTime value={message.received_at} />,
                                          },
                                      ]
                                    : [
                                          {
                                              label: t('mail.receivedAt'),
                                              value: <DateTime value={message.received_at} />,
                                          },
                                      ]),
                                {
                                    label: t('mail.messageId'),
                                    value: (
                                        <span className='inline-flex max-w-full items-center gap-1'>
                                            <code className='truncate font-mono text-sm'>
                                                {message.message_id || '-'}
                                            </code>
                                            {message.message_id && <CopyButton text={message.message_id} />}
                                        </span>
                                    ),
                                    wide: true,
                                },
                                ...(message.folder
                                    ? [{ label: t('mail.folder'), value: `${message.folder} (UID ${message.uid})` }]
                                    : []),
                                { label: t('mail.size'), value: formatBytes(message.size) },
                                {
                                    label: t('mail.kind'),
                                    value: (
                                        <span className='inline-flex flex-wrap items-center gap-2'>
                                            <BounceKindBadge kind={message.bounce_kind} isBounce={message.is_bounce} />
                                            {message.rule && (
                                                <span className='text-sm text-muted'>({message.rule})</span>
                                            )}
                                        </span>
                                    ),
                                },
                                {
                                    label: t('mail.key'),
                                    value: <code className='font-mono text-sm'>{message.message_key}</code>,
                                },
                            ]}
                        />
                    </Card>

                    <Card>
                        {!hasText && !hasHtml && (
                            <Alert tone='info' className='mb-4' title={t('mail.noBody')}>
                                {t('mail.noBodyHint')}
                            </Alert>
                        )}
                        <Tabs<BodyTab> label={t('mail.bodyTabs')} value={activeTab} onChange={setTab} tabs={bodyTabs} />
                        <TabPanel id='headers' active={activeTab === 'headers'}>
                            {headerEntries.length === 0 ? (
                                <EmptyState title={t('mail.noHeaders')} />
                            ) : (
                                <dl className='divide-y divide-line'>
                                    {headerEntries.map(([name, value]) => (
                                        <div
                                            key={name}
                                            className='grid grid-cols-1 gap-1 py-2 sm:grid-cols-[12rem_1fr] sm:gap-4'
                                        >
                                            <dt className='break-all font-mono text-sm font-semibold text-muted'>
                                                {name}
                                            </dt>
                                            <dd className='min-w-0 break-all font-mono text-sm text-ink'>
                                                <HeaderValue value={value} />
                                            </dd>
                                        </div>
                                    ))}
                                </dl>
                            )}
                        </TabPanel>
                        {sections.map((section, i) => (
                            <TabPanel key={`text-${i + 1}`} id={`text-${i + 1}`} active={activeTab === `text-${i + 1}`}>
                                <pre className={TEXT_BODY}>{section}</pre>
                            </TabPanel>
                        ))}
                        {htmlNumbers.map(n => (
                            <TabPanel key={`html-${n}`} id={`html-${n}`} active={activeTab === `html-${n}`}>
                                <Alert tone='info' className='mb-3'>
                                    {t('mail.htmlSandboxNote')}
                                </Alert>
                                <iframe
                                    src={messageHtmlUrl(id, messageKey, n)}
                                    sandbox=''
                                    referrerPolicy='no-referrer'
                                    title={
                                        htmlNumbers.length === 1 ? t('mail.tabHtml') : t('mail.tabHtmlN', { index: n })
                                    }
                                    className='h-[70vh] w-full rounded-md border border-line bg-white'
                                />
                            </TabPanel>
                        ))}
                    </Card>
                </div>

                <aside>
                    <Card>
                        <CardHeader title={t('bounce.title')} />
                        <p className='mb-3 text-sm text-muted'>
                            {t('bounce.bodySource')}:{' '}
                            <span className='text-ink'>{t(`bounce.bodySource_${bodySourceKey}`)}</span>
                        </p>
                        {bounce ? (
                            <DescriptionList
                                columns={1}
                                items={[
                                    {
                                        label: t('bounce.originalRecipient'),
                                        value: <span className='break-all'>{bounce.original_recipient || '-'}</span>,
                                    },
                                    { label: t('bounce.recipientDomain'), value: bounce.recipient_domain || '-' },
                                    { label: t('bounce.action'), value: bounce.action || '-' },
                                    {
                                        label: t('bounce.statusCode'),
                                        value: `${bounce.status_code || '-'} / ${bounce.smtp_code || '-'}`,
                                    },
                                    {
                                        label: t('bounce.responsible'),
                                        value: <ResponsibleBadge responsible={bounce.responsible} />,
                                    },
                                    {
                                        label: t('bounce.remoteMta'),
                                        value: <span className='break-all'>{bounce.remote_mta || '-'}</span>,
                                    },
                                    { label: t('bounce.remoteIp'), value: bounce.remote_ip || '-' },
                                    {
                                        label: t('bounce.reportingMta'),
                                        value: <span className='break-all'>{bounce.reporting_mta || '-'}</span>,
                                    },
                                    {
                                        label: t('bounce.diagnostic'),
                                        value: (
                                            <code className='block whitespace-pre-wrap break-words rounded-md bg-well p-2 font-mono text-sm'>
                                                {bounce.diagnostic || '-'}
                                            </code>
                                        ),
                                    },
                                    {
                                        label: t('bounce.originalSubject'),
                                        value: <span className='break-words'>{bounce.original_subject || '-'}</span>,
                                    },
                                    {
                                        label: t('bounce.originalFrom'),
                                        value: <span className='break-all'>{bounce.original_from || '-'}</span>,
                                    },
                                    {
                                        label: t('bounce.originalDate'),
                                        value: <DateTime value={bounce.original_date} />,
                                    },
                                    {
                                        label: t('bounce.originalMessageId'),
                                        value: (
                                            <code className='break-all font-mono text-sm'>
                                                {bounce.original_message_id || '-'}
                                            </code>
                                        ),
                                    },
                                ]}
                            />
                        ) : (
                            <EmptyState
                                title={t('bounce.none')}
                                description={message.is_bounce ? undefined : t('bounce.notBounce')}
                            />
                        )}
                    </Card>
                </aside>
            </div>
        </PageContainer>
    );
}

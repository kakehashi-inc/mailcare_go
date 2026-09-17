import { useTranslation } from 'react-i18next';
import type { MailboxDTO } from '../../types';
import { mailboxLabel } from '../../utils/format';
import { SelectField } from '../ui/Field';

interface MailboxSelectProps {
    mailboxes: MailboxDTO[];
    /** Selected mailbox id, or "" for "all" when allowAll is set. */
    value: string;
    onChange: (value: string) => void;
    allowAll?: boolean;
    label?: string;
    disabled?: boolean;
    hint?: string;
}

export function MailboxSelect({
    mailboxes,
    value,
    onChange,
    allowAll = false,
    label,
    disabled,
    hint,
}: MailboxSelectProps) {
    const { t } = useTranslation();
    return (
        <SelectField
            label={label ?? t('mailbox.select')}
            value={value}
            disabled={disabled}
            hint={hint}
            onChange={e => onChange(e.target.value)}
        >
            {allowAll && <option value=''>{t('mailbox.all')}</option>}
            {!allowAll && value === '' && <option value=''>{t('mailbox.choose')}</option>}
            {mailboxes.map(mb => (
                <option key={mb.id} value={String(mb.id)}>
                    {mailboxLabel(mb)}
                    {!mb.enabled ? ` (${t('common.disabled')})` : ''}
                </option>
            ))}
        </SelectField>
    );
}

import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { MAX_PROBE_NOTE_LENGTH } from '../../constants';
import { useFocusTrap } from '../../hooks/useFocusTrap';
import { PROBE_RESOLVE_REASONS } from '../../types';
import { Button } from '../ui/Button';
import { RadioGroupField, TextareaField } from '../ui/Field';

interface ProbeResolveDialogProps {
    /** Whether the dialog is shown. */
    open: boolean;
    /** Which probe mail is resolved (address and send time), for the title. */
    subject: string;
    busy?: boolean;
    /** Called with the chosen reason and the details (trimmed; '' when none). The caller closes the dialog. */
    onConfirm: (reason: string, note: string) => void;
    onCancel: () => void;
}

/**
 * Asks why a probe mail is resolved by hand before its arrival: a required reason and optional details. Same
 * behavior as the group state dialog: Cancel (the button or Escape) saves nothing, a click outside does not close it.
 */
export function ProbeResolveDialog({ open, subject, busy = false, onConfirm, onCancel }: ProbeResolveDialogProps) {
    const { t } = useTranslation();
    const [reason, setReason] = useState('');
    const [note, setNote] = useState('');
    const [missing, setMissing] = useState(false);
    const dialogRef = useRef<HTMLDivElement>(null);

    // Every opening starts empty.
    useEffect(() => {
        setReason('');
        setNote('');
        setMissing(false);
    }, [open]);

    useFocusTrap(dialogRef, open, () => !busy && onCancel());

    if (!open) return null;

    const options = PROBE_RESOLVE_REASONS.map(code => ({
        value: code as string,
        label: t(`value.probeResolveReason.${code}`),
    }));

    function submit(e: React.FormEvent) {
        e.preventDefault();
        if (!reason) {
            setMissing(true);
            return;
        }
        onConfirm(reason, note.trim());
    }

    return (
        <div
            className='fixed inset-0 z-40 flex items-end justify-center overflow-y-auto bg-black/60 p-4 sm:items-center'
            role='presentation'
        >
            <div
                ref={dialogRef}
                role='dialog'
                aria-modal='true'
                aria-labelledby='probe-resolve-title'
                aria-describedby='probe-resolve-hint'
                className='my-auto w-full max-w-lg rounded-xl border border-line bg-surface p-5 shadow-2xl'
            >
                <h2 id='probe-resolve-title' className='break-words text-lg font-semibold text-ink'>
                    {t('page.probes.resolveTitle')}
                </h2>
                <p className='mt-1 break-words text-sm text-muted'>{subject}</p>
                <p id='probe-resolve-hint' className='mt-2 text-sm text-muted'>
                    {t('page.probes.resolveHint')}
                </p>
                <form className='mt-4 flex flex-col gap-4' onSubmit={submit} noValidate>
                    <RadioGroupField
                        label={t('field.probe.resolveReason')}
                        options={options}
                        value={reason}
                        onChange={v => {
                            setReason(v);
                            setMissing(false);
                        }}
                        required
                        disabled={busy}
                        error={missing ? t('validation.probe.reasonRequired') : undefined}
                    />
                    <TextareaField
                        label={t('field.probe.resolveNote')}
                        value={note}
                        onChange={e => setNote(e.target.value)}
                        rows={3}
                        maxLength={MAX_PROBE_NOTE_LENGTH}
                        disabled={busy}
                    />
                    {/* Same order as ConfirmDialog: confirm left, cancel right; stacked, cancel at the bottom. */}
                    <div className='flex flex-col gap-2 sm:flex-row sm:justify-end'>
                        <Button type='submit' variant='primary' loading={busy}>
                            {t('action.probe.resolve')}
                        </Button>
                        <Button type='button' variant='secondary' onClick={onCancel} disabled={busy}>
                            {t('common.cancel')}
                        </Button>
                    </div>
                </form>
            </div>
        </div>
    );
}

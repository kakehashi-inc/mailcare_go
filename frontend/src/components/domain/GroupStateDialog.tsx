import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { IGNORE_REASONS, MAX_STATE_NOTE_LENGTH, RESOLVE_ACTIONS } from '../../types';
import { Button } from '../ui/Button';
import { RadioGroupField, TextareaField } from '../ui/Field';

/** The states that are set through this dialog (reopening needs no input). */
export type DecidedState = 'resolved' | 'ignored';

interface GroupStateDialogProps {
    /** The state being set; null closes the dialog. */
    state: DecidedState | null;
    /** The headline of the group, for the title. */
    group: string;
    busy?: boolean;
    /** Called with the chosen code and the details (trimmed; '' when none). The caller closes the dialog. */
    onConfirm: (reason: string, note: string) => void;
    onCancel: () => void;
}

/**
 * Asks what was done (resolved) or why (ignored) before a group is marked resolved or ignored: a required choice
 * and the details. For resolved the details can always be written; for ignored only with "other". Both may stay
 * empty. Cancel (the button or Escape) saves nothing; a click outside does not close the dialog, so nothing typed
 * is lost by accident.
 */
export function GroupStateDialog({ state, group, busy = false, onConfirm, onCancel }: GroupStateDialogProps) {
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
    }, [state]);

    useEffect(() => {
        if (!state) return;
        const previous = document.activeElement as HTMLElement | null;
        dialogRef.current?.querySelector<HTMLElement>('input')?.focus();
        function onKey(e: KeyboardEvent) {
            if (e.key === 'Escape' && !busy) onCancel();
            if (e.key === 'Tab' && dialogRef.current) {
                // Keep focus inside the dialog.
                const focusable = dialogRef.current.querySelectorAll<HTMLElement>(
                    'input:not([disabled]), textarea:not([disabled]), button:not([disabled])'
                );
                if (focusable.length === 0) return;
                const first = focusable[0];
                const last = focusable[focusable.length - 1];
                if (e.shiftKey && document.activeElement === first) {
                    e.preventDefault();
                    last.focus();
                } else if (!e.shiftKey && document.activeElement === last) {
                    e.preventDefault();
                    first.focus();
                }
            }
        }
        document.addEventListener('keydown', onKey);
        return () => {
            document.removeEventListener('keydown', onKey);
            previous?.focus();
        };
    }, [state, busy, onCancel]);

    if (!state) return null;

    const resolved = state === 'resolved';
    const options = resolved
        ? RESOLVE_ACTIONS.map(code => ({ value: code as string, label: t(`value.resolveAction.${code}`) }))
        : IGNORE_REASONS.map(code => ({ value: code as string, label: t(`value.ignoreReason.${code}`) }));
    const showNote = resolved || reason === 'other';

    function submit(e: React.FormEvent) {
        e.preventDefault();
        if (!reason) {
            setMissing(true);
            return;
        }
        onConfirm(reason, showNote ? note.trim() : '');
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
                aria-labelledby='group-state-title'
                className='my-auto w-full max-w-lg rounded-xl border border-line bg-surface p-5 shadow-2xl'
            >
                <h2 id='group-state-title' className='break-words text-lg font-semibold text-ink'>
                    {t(`action.group.markAsFor.${state}`, { group })}
                </h2>
                <form className='mt-4 flex flex-col gap-4' onSubmit={submit} noValidate>
                    <RadioGroupField
                        label={resolved ? t('field.group.resolveAction') : t('field.group.ignoreReason')}
                        options={options}
                        value={reason}
                        onChange={v => {
                            setReason(v);
                            setMissing(false);
                        }}
                        required
                        disabled={busy}
                        error={
                            missing
                                ? resolved
                                    ? t('validation.group.resolveActionRequired')
                                    : t('validation.group.ignoreReasonRequired')
                                : undefined
                        }
                    />
                    {showNote && (
                        <TextareaField
                            label={t('field.group.stateNote')}
                            value={note}
                            onChange={e => setNote(e.target.value)}
                            rows={4}
                            maxLength={MAX_STATE_NOTE_LENGTH}
                            disabled={busy}
                        />
                    )}
                    {/* Same order as ConfirmDialog: confirm left, cancel right; stacked, cancel at the bottom. */}
                    <div className='flex flex-col gap-2 sm:flex-row sm:justify-end'>
                        <Button type='submit' variant={resolved ? 'primary' : 'secondary'} loading={busy}>
                            {t(`action.group.markAs.${state}`)}
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

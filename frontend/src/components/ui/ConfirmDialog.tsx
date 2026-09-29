import { useRef, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { useFocusTrap } from '../../hooks/useFocusTrap';
import { Button } from './Button';
import { Icon } from './Icon';

interface ConfirmDialogProps {
    open: boolean;
    title: string;
    message: ReactNode;
    confirmLabel?: string;
    cancelLabel?: string;
    danger?: boolean;
    busy?: boolean;
    onConfirm: () => void;
    onCancel: () => void;
}

export function ConfirmDialog({
    open,
    title,
    message,
    confirmLabel,
    cancelLabel,
    danger = false,
    busy = false,
    onConfirm,
    onCancel,
}: ConfirmDialogProps) {
    const { t } = useTranslation();
    const cancelRef = useRef<HTMLButtonElement>(null);
    const dialogRef = useRef<HTMLDivElement>(null);

    // The focus starts on Cancel, the safe choice.
    useFocusTrap(dialogRef, open, () => !busy && onCancel(), cancelRef);

    if (!open) return null;

    return (
        <div
            className='fixed inset-0 z-40 flex items-end justify-center bg-black/60 p-4 sm:items-center'
            role='presentation'
            onClick={busy ? undefined : onCancel}
        >
            <div
                ref={dialogRef}
                role='alertdialog'
                aria-modal='true'
                aria-labelledby='confirm-title'
                aria-describedby='confirm-message'
                className='w-full max-w-md rounded-xl border border-line bg-surface p-5 shadow-2xl'
                onClick={e => e.stopPropagation()}
            >
                <div className='flex items-start gap-3'>
                    <Icon
                        name={danger ? 'warning' : 'help_outline'}
                        className={`mt-0.5 text-[28px] ${danger ? 'text-danger' : 'text-accent'}`}
                    />
                    <div className='min-w-0 flex-1'>
                        <h2 id='confirm-title' className='text-lg font-semibold text-ink'>
                            {title}
                        </h2>
                        <div id='confirm-message' className='mt-2 text-base text-muted'>
                            {message}
                        </div>
                    </div>
                </div>
                {/* Like the dialogs of the OS and the browser: confirm left, cancel right; stacked, cancel at the bottom. */}
                <div className='mt-5 flex flex-col gap-2 sm:flex-row sm:justify-end'>
                    <Button variant={danger ? 'danger' : 'primary'} onClick={onConfirm} loading={busy}>
                        {confirmLabel ?? t('component.confirmDialog.confirm')}
                    </Button>
                    <Button ref={cancelRef} variant='secondary' onClick={onCancel} disabled={busy}>
                        {cancelLabel ?? t('common.cancel')}
                    </Button>
                </div>
            </div>
        </div>
    );
}

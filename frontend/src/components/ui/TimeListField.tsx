import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { isClockTime } from '../../utils/validate';
import { Alert } from './Alert';
import { Button, IconButton } from './Button';
import { CONTROL_WIDTH, controlClass } from './Field';

interface TimeListFieldProps {
    /** Prefix of the element ids (the add input and its hint). */
    id: string;
    /** Accessible name of the list. */
    label: string;
    /** The times, "HH:MM", kept sorted by the field. */
    times: string[];
    onChange: (times: string[]) => void;
    /** Shown instead of the list when it is empty. */
    emptyText?: string;
    /** The list never gets shorter than this: the remove buttons are disabled at that length. */
    minCount?: number;
}

/** Whether every time is a valid "HH:MM" clock time and none is repeated (what the server accepts as a list). */
export function timeListValid(times: string[]): boolean {
    return times.every(isClockTime) && new Set(times).size === times.length;
}

/**
 * A list of clock times with a remove button per time and an input to add one (the sync times of the general
 * settings and the notification times). Invalid or repeated times coming from the server are marked.
 */
export function TimeListField({ id, label, times, onChange, emptyText, minCount = 0 }: TimeListFieldProps) {
    const { t } = useTranslation();
    const [newTime, setNewTime] = useState('');
    const invalid = times.some(x => !isClockTime(x));
    const duplicate = new Set(times).size !== times.length;
    const newTimeValid = newTime === '' || isClockTime(newTime);
    const newTimeDuplicate = newTime !== '' && times.includes(newTime);
    const canRemove = times.length > minCount;

    function add() {
        if (!isClockTime(newTime) || times.includes(newTime)) return;
        onChange([...times, newTime].sort());
        setNewTime('');
    }

    return (
        <div>
            {times.length === 0 ? (
                emptyText && <p className='text-sm text-muted'>{emptyText}</p>
            ) : (
                <ul className={`flex flex-col gap-2 ${CONTROL_WIDTH.short}`} aria-label={label}>
                    {times.map((time, i) => (
                        <li
                            key={`${time}-${i}`}
                            className='flex items-center justify-between gap-3 rounded-md border border-line px-3 py-1'
                        >
                            <span className={`font-mono text-base ${isClockTime(time) ? 'text-ink' : 'text-danger'}`}>
                                {time}
                            </span>
                            <IconButton
                                icon='delete'
                                label={t('action.setting.removeTime', { time })}
                                disabled={!canRemove}
                                onClick={() => onChange(times.filter((_, j) => j !== i))}
                            />
                        </li>
                    ))}
                </ul>
            )}
            {(invalid || duplicate) && (
                <Alert tone='danger' className='mt-3'>
                    {invalid ? t('validation.common.timeFormat') : t('validation.common.timeDuplicate')}
                </Alert>
            )}
            <form
                className='mt-4'
                onSubmit={e => {
                    e.preventDefault();
                    add();
                }}
            >
                <label htmlFor={`${id}-new`} className='mb-1 block text-sm font-medium text-ink'>
                    {t('action.setting.addTime')}
                </label>
                <div className='flex items-center gap-2'>
                    <input
                        id={`${id}-new`}
                        type='time'
                        step={60}
                        value={newTime}
                        onChange={e => setNewTime(e.target.value)}
                        aria-invalid={!newTimeValid || newTimeDuplicate || undefined}
                        aria-describedby={`${id}-new-hint`}
                        className={`${controlClass} min-w-0 ${CONTROL_WIDTH.short}`}
                    />
                    <Button
                        type='submit'
                        icon='add'
                        disabled={!newTime || !newTimeValid || newTimeDuplicate}
                        className='shrink-0'
                    >
                        {t('common.add')}
                    </Button>
                </div>
                <p id={`${id}-new-hint`} className={`mt-1 text-sm ${newTimeDuplicate ? 'text-danger' : 'text-muted'}`}>
                    {newTimeDuplicate ? t('validation.common.timeDuplicate') : t('page.settingsGeneral.addTimeHint')}
                </p>
            </form>
        </div>
    );
}

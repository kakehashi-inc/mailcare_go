import { useLayoutEffect, useRef } from 'react';
import { formatLogTime } from '../../utils/format';

/** Distance from the bottom (px) within which the log keeps following new lines. */
const FOLLOW_THRESHOLD = 24;

interface ProgressLine {
    time: string;
    message: string;
}

/** Splits the stored progress text into lines of "<RFC 3339 UTC time>\t<message>". */
function parseProgress(text: string): ProgressLine[] {
    return text
        .split('\n')
        .filter(line => line !== '')
        .map(line => {
            const tab = line.indexOf('\t');
            return { time: formatLogTime(line.slice(0, tab)), message: line.slice(tab + 1) };
        });
}

interface JobProgressLogProps {
    text: string;
}

/**
 * Scrollable progress log of a running job. It follows new lines while the
 * reader is at the bottom and stays put once they scroll up to read older ones.
 */
export function JobProgressLog({ text }: JobProgressLogProps) {
    const ref = useRef<HTMLOListElement>(null);
    const follow = useRef(true);

    useLayoutEffect(() => {
        const el = ref.current;
        if (el && follow.current) el.scrollTop = el.scrollHeight;
    }, [text]);

    const onScroll = () => {
        const el = ref.current;
        if (!el) return;
        follow.current = el.scrollHeight - el.scrollTop - el.clientHeight <= FOLLOW_THRESHOLD;
    };

    return (
        <ol ref={ref} onScroll={onScroll} className='max-h-40 min-w-0 flex-1 overflow-y-auto font-mono text-sm'>
            {parseProgress(text).map((line, i) => (
                <li key={i} className='flex gap-3'>
                    <span className='shrink-0 text-muted'>{line.time}</span>
                    <span className='min-w-0 break-words'>{line.message}</span>
                </li>
            ))}
        </ol>
    );
}

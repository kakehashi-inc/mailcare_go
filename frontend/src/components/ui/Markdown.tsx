import { lazy, Suspense } from 'react';
import { LoadingBlock } from './Spinner';

// react-markdown is only needed on the group detail page, so it is loaded on
// demand and kept out of the main bundle.
const LazyMarkdown = lazy(() => import('./MarkdownRenderer'));

/**
 * Renders internally generated Markdown (agent reports). No sanitizer is
 * applied on purpose (see the screen design document (Documents), section 1); links open in a new tab.
 */
export function Markdown({ source }: { source: string }) {
    return (
        <Suspense fallback={<LoadingBlock />}>
            <LazyMarkdown source={source} />
        </Suspense>
    );
}

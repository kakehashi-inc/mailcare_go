import ReactMarkdown from 'react-markdown';

export default function MarkdownRenderer({ source }: { source: string }) {
    return (
        <div className='prose-report'>
            <ReactMarkdown
                components={{
                    a: ({ href, children }) => (
                        <a href={href} target='_blank' rel='noopener noreferrer'>
                            {children}
                        </a>
                    ),
                }}
            >
                {source}
            </ReactMarkdown>
        </div>
    );
}

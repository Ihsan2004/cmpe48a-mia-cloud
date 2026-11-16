import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'

/**
 * MarkdownRenderer - Renders Markdown content with styled components
 * 
 * Supports:
 * - Headings (h1-h6)
 * - Bold, italic, strikethrough
 * - Lists (ordered and unordered)
 * - Code blocks and inline code
 * - Links
 * - Tables (via remark-gfm)
 * - Blockquotes
 * - Horizontal rules
 */
export default function MarkdownRenderer({ content, className = '' }) {
  if (!content) return null

  return (
    <ReactMarkdown
      remarkPlugins={[remarkGfm]}
      className={`markdown-content ${className}`}
      components={{
        // Headings
        h1: ({ children }) => (
          <h1 className="text-h2 font-serif text-text mb-4 mt-6 first:mt-0">
            {children}
          </h1>
        ),
        h2: ({ children }) => (
          <h2 className="text-h3 font-serif text-text mb-3 mt-5 first:mt-0">
            {children}
          </h2>
        ),
        h3: ({ children }) => {
          // Get text content for section detection
          const text = typeof children === 'string'
            ? children
            : (children?.props?.children || children?.toString() || '')
          const textStr = String(text)

          // Tiered Summary Section Styling (solid colors only, no gradients)
          if (textStr.includes('📌') || textStr.toLowerCase().includes('tl;dr')) {
            return (
              <div className="bg-[var(--canvas-muted)] border-l-4 border-secondary rounded-r-lg px-4 py-3 mb-4 mt-6 first:mt-0">
                <h3 className="text-lg font-serif text-[var(--accent-ochre-ink)] flex items-center gap-2 m-0">
                  {children}
                </h3>
              </div>
            )
          }

          if (textStr.includes('✅') || textStr.toLowerCase().includes('karar')) {
            return (
              <div className="bg-[rgba(132,145,164,0.12)] border-l-4 border-secondary-hover rounded-r-lg px-4 py-3 mb-4 mt-6 first:mt-0">
                <h3 className="text-lg font-serif text-[var(--accent-ochre-ink)]-hover flex items-center gap-2 m-0">
                  {children}
                </h3>
              </div>
            )
          }

          if (textStr.includes('🚀') || textStr.toLowerCase().includes('aksiyon')) {
            return (
              <div className="bg-[var(--canvas-muted)] border-l-4 border-secondary rounded-r-lg px-4 py-3 mb-4 mt-6 first:mt-0">
                <h3 className="text-lg font-serif text-[var(--accent-ochre-ink)] flex items-center gap-2 m-0">
                  {children}
                </h3>
              </div>
            )
          }

          if (textStr.includes('⚠️') || textStr.toLowerCase().includes('risk')) {
            return (
              <div className="bg-[var(--canvas-muted)] border-l-4 border-accent rounded-r-lg px-4 py-3 mb-4 mt-6 first:mt-0">
                <h3 className="text-lg font-serif text-accent flex items-center gap-2 m-0">
                  {children}
                </h3>
              </div>
            )
          }

          if (textStr.includes('📝') || textStr.toLowerCase().includes('tartışma') || textStr.toLowerCase().includes('not')) {
            return (
              <div className="bg-[var(--canvas-muted)]/50 border-l-4 border-text-tertiary rounded-r-lg px-4 py-3 mb-4 mt-6 first:mt-0">
                <h3 className="text-lg font-serif text-text-secondary flex items-center gap-2 m-0">
                  {children}
                </h3>
              </div>
            )
          }

          // Default h3 styling
          return (
            <h3 className="text-lg font-serif text-text mb-2 mt-4 first:mt-0">
              {children}
            </h3>
          )
        },
        h4: ({ children }) => (
          <h4 className="text-base font-serif text-text mb-2 mt-3 first:mt-0">
            {children}
          </h4>
        ),
        h5: ({ children }) => (
          <h5 className="text-sm font-serif text-text mb-1 mt-2 first:mt-0">
            {children}
          </h5>
        ),
        h6: ({ children }) => (
          <h6 className="text-sm font-serif text-text-secondary mb-1 mt-2 first:mt-0">
            {children}
          </h6>
        ),

        // Paragraphs
        p: ({ children }) => (
          <p className="text-text-secondary font-sans leading-relaxed mb-3 last:mb-0">
            {children}
          </p>
        ),

        // Lists
        ul: ({ children }) => (
          <ul className="list-disc list-inside space-y-1.5 mb-4 ml-2 text-text-secondary font-sans">
            {children}
          </ul>
        ),
        ol: ({ children }) => (
          <ol className="list-decimal list-inside space-y-1.5 mb-4 ml-2 text-text-secondary font-sans">
            {children}
          </ol>
        ),
        li: ({ children }) => (
          <li className="text-text-secondary font-sans leading-relaxed">
            {children}
          </li>
        ),

        // Emphasis
        strong: ({ children }) => (
          <strong className="text-text" style={{ fontWeight: 400 }}>
            {children}
          </strong>
        ),
        em: ({ children }) => (
          <em className="italic text-text-secondary font-sans">
            {children}
          </em>
        ),
        del: ({ children }) => (
          <del className="line-through text-text-tertiary">
            {children}
          </del>
        ),

        // Code
        code: ({ inline, className, children }) => {
          if (inline) {
            return (
              <code className="bg-surface text-accent px-1.5 py-0.5 rounded text-sm font-mono">
                {children}
              </code>
            )
          }
          return (
            <code className={`block bg-surface text-text-secondary p-4 rounded-lg overflow-x-auto text-sm font-mono mb-4 ${className || ''}`}>
              {children}
            </code>
          )
        },
        pre: ({ children }) => (
          <pre className="bg-surface rounded-lg overflow-x-auto mb-4">
            {children}
          </pre>
        ),

        // Links
        a: ({ href, children }) => (
          <a
            href={href}
            target="_blank"
            rel="noopener noreferrer"
            className="text-accent hover:text-accent-hover underline underline-offset-2 transition-colors"
          >
            {children}
          </a>
        ),

        // Blockquote
        blockquote: ({ children }) => (
          <blockquote className="border-l-4 border-accent/50 pl-4 py-1 my-4 bg-[rgba(155,150,188,0.05)] rounded-r-lg">
            {children}
          </blockquote>
        ),

        // Horizontal rule
        hr: () => (
          <hr className="border-border my-6" />
        ),

        // Tables
        table: ({ children }) => (
          <div className="overflow-x-auto mb-4">
            <table className="min-w-full border-collapse">
              {children}
            </table>
          </div>
        ),
        thead: ({ children }) => (
          <thead className="bg-surface">
            {children}
          </thead>
        ),
        tbody: ({ children }) => (
          <tbody className="divide-y divide-border">
            {children}
          </tbody>
        ),
        tr: ({ children }) => (
          <tr className="hover:bg-[rgba(245,245,243,0.03)] transition-colors">
            {children}
          </tr>
        ),
        th: ({ children }) => (
          <th className="px-4 py-2 text-left text-sm font-serif text-text border-b border-border">
            {children}
          </th>
        ),
        td: ({ children }) => (
          <td className="px-4 py-2 text-sm text-text-secondary font-sans">
            {children}
          </td>
        ),

        // Images
        img: ({ src, alt }) => (
          <img
            src={src}
            alt={alt || ''}
            className="max-w-full h-auto rounded-lg my-4"
          />
        ),
      }}
    >
      {content}
    </ReactMarkdown>
  )
}

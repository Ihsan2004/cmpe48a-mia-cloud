export default function EmptyState({ icon: Icon, title, description, action }) {
  return (
    <div className="text-center py-16">
      {Icon && (
        <div className="w-14 h-14 flex items-center justify-center mx-auto mb-5 border border-[var(--canvas-muted)]">
          <Icon className="w-6 h-6 text-[var(--accent-ochre-ink)]" strokeWidth={1.5} />
        </div>
      )}
      {title && <p className="font-serif text-h3 text-[var(--ink-charcoal)] mb-2">{title}</p>}
      {description && <p className="text-meta text-text-secondary font-sans leading-[1.55] max-w-md mx-auto">{description}</p>}
      {action && <div className="mt-6">{action}</div>}
    </div>
  )
}

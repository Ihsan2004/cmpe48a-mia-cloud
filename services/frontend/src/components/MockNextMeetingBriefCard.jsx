import { Clock, Users, FileText, CheckCircle2, Circle, HelpCircle, Sparkles, ExternalLink } from 'lucide-react'

// PURELY VISUAL MOCK — no backend, no real data.
// Toggle via localStorage key `mock_brief_card_enabled` (set in Profile settings).

const MOCK = {
  title: 'Q2 Ürün Yol Haritası Sync',
  startLabel: 'Bugün • 14:30 - 15:15',
  joinUrl: '#',
  participants: [
    { name: 'Elif Yılmaz', role: 'PM', initials: 'EY', isOrganizer: true },
    { name: 'Kerem Yoldaş', role: 'Eng Lead', initials: 'KY' },
    { name: 'Deniz Aksoy', role: 'Design', initials: 'DA' },
    { name: 'Mert Çelik', role: 'Backend', initials: 'MÇ' },
    { name: 'Selin Korkmaz', role: 'Data', initials: 'SK' },
  ],
  summary:
    'Geçen sync\'te Q2 önceliklerinin yeniden sıralanması ve ödeme akışının faz 2\'ye taşınması kararlaştırıldı. Bu toplantıda yeni takvim ve kaynak planı netleştirilecek.',
  decisions: [
    {
      text: 'Ödeme akışı refaktörü Faz 2\'ye taşınacak',
      owner: 'Mert Ç.',
      jiraKey: 'PAY-412',
      jiraStatus: 'done',
    },
    {
      text: 'Onboarding A/B testi canlıya alınacak',
      owner: 'Elif Y.',
      jiraKey: 'GROW-188',
      jiraStatus: 'in_progress',
    },
    {
      text: 'Analitik event şeması yeniden tasarlanacak',
      owner: 'Selin K.',
      jiraKey: 'DATA-77',
      jiraStatus: 'todo',
    },
  ],
  followUps: [
    'PAY-412 prod\'a çıktı mı, metrikler beklenen aralıkta mı?',
    'GROW-188 A/B testi için yeterli örneklem süresi tanımlandı mı?',
    'DATA-77 yeni şema, mevcut dashboard\'ları kıracak mı?',
    'Faz 2 için ek backend kapasitesine ihtiyacımız olacak mı?',
  ],
}

function JiraStatusBadge({ status }) {
  const map = {
    done: {
      label: 'Tamamlandı',
      icon: CheckCircle2,
      className: 'border-green-500/30 bg-green-500/10 text-[var(--accent-ochre-ink)]',
    },
    in_progress: {
      label: 'Devam Ediyor',
      icon: Circle,
      className: 'border-blue-500/30 bg-blue-500/10 text-blue-400',
    },
    todo: {
      label: 'Bekliyor',
      icon: Circle,
      className: 'border-gray-500/30 bg-gray-500/10 text-text-tertiary',
    },
  }
  const cfg = map[status] || map.todo
  const Icon = cfg.icon
  return (
    <span className={`inline-flex items-center gap-1 px-2 py-0.5 rounded-md text-micro font-serif border ${cfg.className}`}>
      <Icon className="w-3 h-3" />
      {cfg.label}
    </span>
  )
}

export default function MockNextMeetingBriefCard() {
  return (
    <div className="mb-8">
      <div className="flex items-center gap-3 mb-4">
        <h2 className="text-lg font-serif text-text flex items-center gap-2">
          <Sparkles className="w-5 h-5 text-primary" />
          Sıradaki Toplantı — Brief
        </h2>
        <span className="badge badge-primary">Önizleme</span>
      </div>

      <div className="card relative overflow-hidden">
        {/* Accent gradient stripe */}
        <div
          className="absolute top-0 left-0 right-0 h-[2px]"
          style={{
            background:
              'linear-gradient(90deg, rgba(231,112,75,0.8), rgba(141,105,226,0.8), transparent)',
          }}
        />

        {/* Header */}
        <div className="flex items-start justify-between mb-5">
          <div className="flex-1 min-w-0">
            <h3 className="text-h3 font-serif text-text mb-2 tracking-tight">
              {MOCK.title}
            </h3>
            <div className="flex items-center flex-wrap gap-4 text-sm text-text-secondary font-sans">
              <div className="flex items-center gap-1.5">
                <Clock className="w-4 h-4 text-[var(--accent-ochre-ink)]" />
                <span>{MOCK.startLabel}</span>
              </div>
              <div className="flex items-center gap-1.5">
                <Users className="w-4 h-4" />
                <span>{MOCK.participants.length} katılımcı</span>
              </div>
            </div>
          </div>
          <a
            href={MOCK.joinUrl}
            onClick={(e) => e.preventDefault()}
            className="btn-primary flex items-center gap-2 flex-shrink-0"
          >
            <ExternalLink className="w-4 h-4" />
            Katıl
          </a>
        </div>

        {/* Participants */}
        <div className="mb-5">
          <div className="text-micro font-serif text-text-tertiary uppercase tracking-wider mb-2">
            Katılımcılar
          </div>
          <div className="flex flex-wrap gap-2">
            {MOCK.participants.map((p) => (
              <div
                key={p.name}
                className="flex items-center gap-2 px-2.5 py-1.5 rounded-full bg-background border border-border"
              >
                <div
                  className={`w-6 h-6 rounded-full flex items-center justify-center text-micro font-serif ${
                    p.isOrganizer
                      ? 'bg-primary/20 text-primary border border-primary/40'
                      : 'bg-[rgba(141,105,226,0.15)] text-accent border border-[rgba(141,105,226,0.3)]'
                  }`}
                >
                  {p.initials}
                </div>
                <div className="flex items-center gap-1.5">
                  <span className="text-sm text-text font-sans">{p.name}</span>
                  <span className="text-micro text-text-tertiary">· {p.role}</span>
                  {p.isOrganizer && (
                    <span className="text-micro text-primary opacity-80">(Sahip)</span>
                  )}
                </div>
              </div>
            ))}
          </div>
        </div>

        {/* Summary */}
        <div className="mb-5 p-4 rounded-md bg-background border border-border">
          <div className="flex items-center gap-2 mb-2">
            <FileText className="w-4 h-4 text-[var(--accent-ochre-ink)]" />
            <span className="text-micro font-serif text-text-tertiary uppercase tracking-wider">
              Önceki Toplantı Özeti
            </span>
          </div>
          <p className="text-sm text-text-secondary font-sans leading-relaxed line-clamp-2">
            {MOCK.summary}
          </p>
        </div>

        {/* Decisions from previous chain */}
        <div className="mb-5">
          <div className="text-micro font-serif text-text-tertiary uppercase tracking-wider mb-2">
            Önceki Zincirden Kararlar · Jira Durumu
          </div>
          <div className="space-y-2">
            {MOCK.decisions.map((d, i) => (
              <div
                key={i}
                className="flex items-start justify-between gap-3 p-3 rounded-md bg-background border border-border"
              >
                <div className="flex items-start gap-2 flex-1 min-w-0">
                  <span className="text-micro mt-0.5">📋</span>
                  <div className="min-w-0">
                    <div className="text-sm text-text font-sans">{d.text}</div>
                    <div className="flex items-center gap-2 mt-1 text-micro text-text-tertiary">
                      <span>Sahip: {d.owner}</span>
                      <span>·</span>
                      <a
                        href="#"
                        onClick={(e) => e.preventDefault()}
                        className="text-[var(--accent-ochre-ink)] hover:text-accent transition-colors"
                      >
                        {d.jiraKey}
                      </a>
                    </div>
                  </div>
                </div>
                <JiraStatusBadge status={d.jiraStatus} />
              </div>
            ))}
          </div>
        </div>

        {/* Follow-ups */}
        <div
          className="p-4 rounded-md border"
          style={{
            background: 'rgba(231, 112, 75, 0.06)',
            borderColor: 'rgba(231, 112, 75, 0.25)',
          }}
        >
          <div className="flex items-center gap-2 mb-3">
            <HelpCircle className="w-4 h-4 text-primary" />
            <span className="text-micro font-serif text-primary uppercase tracking-wider">
              Mutlaka Takip Et — Sorulması Gereken Sorular
            </span>
          </div>
          <ul className="space-y-2">
            {MOCK.followUps.map((q, i) => (
              <li key={i} className="flex items-start gap-2 text-sm text-text font-sans">
                <span className="text-primary font-serif mt-0.5">{i + 1}.</span>
                <span className="leading-relaxed">{q}</span>
              </li>
            ))}
          </ul>
        </div>
      </div>
    </div>
  )
}

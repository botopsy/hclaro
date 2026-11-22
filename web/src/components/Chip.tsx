import type { ReactNode } from 'react';

export type Tone = 'neutral' | 'create' | 'update' | 'replace' | 'delete' | 'read' | 'ok' | 'accent';

// A small pill. Colour never carries meaning alone: callers put a word in it (§6).
export function Chip({ tone = 'neutral', icon, children, title }: { tone?: Tone; icon?: ReactNode; children: ReactNode; title?: string }) {
  return <span className={`chip chip-tone-${tone}`} title={title}>{icon}{children}</span>;
}

export type ChangeAction = 'create' | 'update' | 'replace' | 'delete' | 'read';
const symbols: Record<ChangeAction, string> = { create: '+', update: '~', replace: '±', delete: '−', read: '<=' };

// A plan change type: always a symbol plus a word, optionally with a count ("+ 3 create").
export function ChangeChip({ action, count }: { action: ChangeAction; count?: number }) {
  return <Chip tone={action}><span aria-hidden="true">{symbols[action]}</span>{count !== undefined && <span>{count}</span>}<span>{action}</span></Chip>;
}

// A status dot with its label; the label is visible unless `hideLabel`, then it stays for screen readers.
export function StatusDot({ tone, label, hideLabel = false }: { tone: Tone; label: string; hideLabel?: boolean }) {
  return <span className="status-dot-label"><span className={`status-dot chip-tone-${tone}`} aria-hidden="true" />{hideLabel ? <span className="sr-only">{label}</span> : label}</span>;
}

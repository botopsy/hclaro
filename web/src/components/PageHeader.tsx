import type { ReactNode } from 'react';
import { ShieldCheck } from 'lucide-react';

// §3: H1, an optional count, at most one safety chip and an actions slot on the right.
export function PageHeader({ title, count, chip, children }: { title: string; count?: number; chip?: string; children?: ReactNode }) {
  return <header className="page-header">
   <h1>{title}</h1>
   {count !== undefined && <span className="page-count" aria-label={`${count} total`}>{count}</span>}
   {chip && <span className="safety-chip"><ShieldCheck size={11} aria-hidden="true" />{chip}</span>}
   {children && <div className="page-header-actions">{children}</div>}
  </header>;
}

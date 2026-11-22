import type { ReactNode } from 'react';
import { Disclosure } from './Disclosure';

// §3/§4.3: dashed box, icon, title, one sentence, at most one primary action and an optional
// collapsed "How this works" for the long explanation.
export function EmptyState({ icon, title, children, action, how }: { icon?: ReactNode; title: string; children?: ReactNode; action?: ReactNode; how?: ReactNode }) {
  return <section className="empty-box">
   {icon && <span className="empty-box-icon" aria-hidden="true">{icon}</span>}
   <h2>{title}</h2>
   {children && <p>{children}</p>}
   {action}
   {how && <Disclosure summary="How this works">{how}</Disclosure>}
  </section>;
}

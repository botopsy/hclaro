import type { ReactNode } from 'react';
import { CircleHelp, ShieldCheck, type LucideIcon } from 'lucide-react';
import { Button } from './ui/button';

// The one error block of the app. By default it is a page-level workspace error with an icon,
// a title and a no-change note; inline shows only the parts passed. children are extra actions.
export function ErrorState({
  title,
  message,
  retry,
  retryLabel = 'Retry connection',
  icon,
  iconSize = 28,
  note,
  inline,
  children
}: {
  title?: string;
  message: ReactNode;
  retry?: () => void;
  retryLabel?: string;
  icon?: LucideIcon;
  iconSize?: number;
  note?: string;
  inline?: boolean;
  children?: ReactNode;
}) {
  const Icon = icon ?? (inline ? undefined : CircleHelp);
  const heading = title ?? (inline ? undefined : 'Unable to read workspace');
  const safe = note ?? (inline ? undefined : 'No infrastructure was changed.');
  return <div className="empty-state error-state" role="alert">{Icon && <Icon size={iconSize} />}{heading && <h2>{heading}</h2>}<p>{message}</p>{retry && <Button variant="outline" onClick={retry}>{retryLabel}</Button>}{children}{safe && <span className="safe-note"><ShieldCheck size={13} />{safe}</span>}</div>;
}

import { CircleHelp, ShieldCheck } from 'lucide-react';
import { Button } from '../components/ui/button';
export function ErrorState({
  title = 'Unable to read workspace',
  message,
  retry,
  retryLabel = 'Retry connection'
}: {
  title?: string;
  message: string;
  retry?: () => void;
  retryLabel?: string;
}) {
  return <div className="empty-state error-state" role="alert"><CircleHelp size={28} /><h2>{title}</h2><p>{message}</p>{retry && <Button variant="outline" onClick={retry}>{retryLabel}</Button>}<span className="safe-note"><ShieldCheck size={13} />No infrastructure was changed.</span></div>;
}

import { useEffect, useState } from 'react';
import { Eye, EyeOff, LockKeyhole } from 'lucide-react';
import { ApiError } from '../api';
import { Button } from './ui/button';
import { CopyButton } from './CopyButton';

export type RevealFn = (path: (string | number)[]) => Promise<string>;

// The page may stay hidden this long before a revealed value is dropped.
const hiddenLimitMs = 5 * 60 * 1000;

// RedactedLeaf is one value the server redacted. Revealing asks the server for that one value;
// it is kept in this component's state only (never in a query cache or storage), and dropped
// on Hide, on unmount (drawer closed, navigation, context switch) and after the page was
// hidden for a while.
export function RedactedLeaf({ name, path, reveal }: { name?: string; path: (string | number)[]; reveal: RevealFn }) {
  const [step, setStep] = useState<'hidden' | 'error' | 'loading' | 'shown'>('hidden');
  const [value, setValue] = useState('');
  const [error, setError] = useState<ApiError | Error>();
  useEffect(() => {
    if (step !== 'shown') return;
    let since = 0;
    const onVisibility = () => {
      if (document.hidden) since = Date.now();
      else if (since && Date.now() - since > hiddenLimitMs) hide();
    };
    document.addEventListener('visibilitychange', onVisibility);
    return () => document.removeEventListener('visibilitychange', onVisibility);
  }, [step]);
  function hide() {
    setValue('');
    setError(undefined);
    setStep('hidden');
  }
  async function run() {
    setStep('loading');
    setError(undefined);
    try {
      setValue(await reveal(path));
      setStep('shown');
    } catch (e) {
      setError(e instanceof Error ? e : new Error('The value could not be revealed.'));
      setStep('error');
    }
  }
  const denied = error instanceof ApiError && error.code === 'reveal_denied';
  return <div className="json-leaf revealable">
   <span className="json-key">{name}</span>
   {step === 'shown' ? <>
    <span className="json-string revealed-value" data-testid="revealed-value">{value}</span>
    <CopyButton value={value} label={`Copy value of ${name}`} iconOnly />
    <Button type="button" variant="ghost" size="icon" aria-label="Hide" title="Hide" onClick={hide}><EyeOff size={12} aria-hidden="true" /></Button>
   </> : <>
    <span className="redacted"><LockKeyhole size={11} />redacted</span>
    {(step === 'hidden' || step === 'loading') && <Button type="button" variant="ghost" size="icon" aria-label="Reveal" title="Reveal" onClick={() => void run()} disabled={step === 'loading'}><Eye size={12} aria-hidden="true" /></Button>}
   </>}
   {step === 'error' && error && <div className="reveal-note" role="group" aria-label={`Reveal ${name}`}>
    <p role="alert">{denied ? 'Your credentials can\u2019t read this value. The backend denied access. It stays hidden.' : error.message}</p>
    <Button type="button" variant="ghost" size="small" onClick={hide}>Cancel</Button>
    <Button type="button" size="small" onClick={() => void run()}>Try again</Button>
   </div>}
  </div>;
}

import { useEffect, useRef, useState } from 'react';
import { CircleStop, LoaderCircle, Terminal } from 'lucide-react';
import { post, streamEvents } from '../api';
import { operationActive, type Operation, type OperationEvent } from '../types';
import { Button } from '../components/ui/button';
import { duration } from './labels';

const maxLines = 2000;

// Streams one operation's sanitized log. Reconnects resume from the last received event.
export function OperationLog({
  operation: initial,
  onFinished
}: {
  operation: Operation;
  onFinished?: (operation: Operation) => void;
}) {
  const [events, setEvents] = useState<OperationEvent[]>([]);
  const [operation, setOperation] = useState(initial);
  const [truncated, setTruncated] = useState(false);
  const [error, setError] = useState('');
  const [cancelling, setCancelling] = useState(false);
  const finished = useRef(onFinished);
  const body = useRef<HTMLOListElement>(null);
  useEffect(() => {
    finished.current = onFinished;
  }, [onFinished]);
  useEffect(() => {
    const controller = new AbortController();
    let last = 0;
    let stopped = false;
    async function run() {
      for (let attempt = 0; !stopped && attempt < 20; attempt++) {
        try {
          const ended = await streamEvents(initial.id, last, {
            log: event => {
              last = event.seq;
              setEvents(current => [...current, event].slice(-maxLines));
            },
            status: op => setOperation(op),
            truncated: () => setTruncated(true)
          }, controller.signal);
          if (ended) return;
        } catch (e) {
          if (controller.signal.aborted) return;
          setError(e instanceof Error ? e.message : 'The log stream was interrupted.');
          if (e instanceof Error && 'code' in e && e.code === 'log_not_retained') return;
        }
        await new Promise(resolve => setTimeout(resolve, 1000));
      }
    }
    void run();
    return () => {
      stopped = true;
      controller.abort();
    };
  }, [initial.id]);
  const done = !operationActive(operation);
  const noun = operation.kind === 'apply' ? 'Apply' : operation.kind === 'init' ? 'Init' : 'Plan';
  useEffect(() => {
    if (done) finished.current?.(operation);
  }, [done, operation]);
  useEffect(() => {
    body.current?.lastElementChild?.scrollIntoView?.({ block: 'nearest' });
  }, [events.length]);
  async function cancel() {
    setCancelling(true);
    try {
      setOperation(await post<Operation>(`operations/${operation.id}/cancel`, {}));
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Cancellation failed.');
      setCancelling(false);
    }
  }
  return <section className="operation-log" aria-label="Operation output">
   <header>
    <span className="operation-title"><Terminal size={14} /><strong>{operation.command.join(' ')}</strong></span>
    <span className={`status-pill status-${operation.status}`} role="status" aria-live="polite">{!done ? <><LoaderCircle className="spin" size={12} />{operation.cancelRequested ? 'Cancelling' : operation.stage}</> : operation.status}</span>
    <span className="muted">{duration(operation.durationMs)}{operation.exitCode !== undefined && ` · exit ${operation.exitCode}`}</span>
    {!done && <Button variant="outline" size="small" onClick={() => void cancel()} disabled={cancelling || operation.cancelRequested}><CircleStop size={13} />{operation.kind === 'apply' ? 'Interrupt apply' : `Cancel ${noun.toLowerCase()}`}</Button>}
   </header>
   {truncated && <p className="log-note">Earlier lines were dropped to keep memory bounded.</p>}
   <ol ref={body} className="log-lines" role="log" aria-label={`${noun} output`} aria-live="polite">{events.map(e => <li key={e.seq} className={`log-${e.level}`}><time>{new Date(e.time).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })}</time><span>{e.text}</span></li>)}{!events.length && <li className="log-info"><span>Waiting for output…</span></li>}</ol>
   {error && <p className="log-note" role="alert">{error}</p>}
   {!done && <p className="log-note">{operation.kind === 'apply' ? 'Interrupting asks the engine to stop after the changes in progress. Objects being created or destroyed at that moment can be left partly changed.' : operation.kind === 'init' ? 'Cancelling interrupts the engine and anything it started. Partly downloaded providers or modules may remain under the data directory; running init again completes them.' : 'Cancelling interrupts the engine so it can release any state lock. Planning never changes infrastructure.'}</p>}
  </section>;
}

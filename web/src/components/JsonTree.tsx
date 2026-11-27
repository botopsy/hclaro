import { LockKeyhole } from 'lucide-react';
import { RedactedLeaf, prettyJson, type RevealFn } from './RedactedLeaf';
export type { RevealFn } from './RedactedLeaf';
export function JsonTree({
  value,
  name,
  depth = 0,
  reveal,
  path = []
}: {
  value: unknown;
  name?: string;
  depth?: number;
  // reveal, when set, lets the user fetch one redacted value at path (M30).
  reveal?: RevealFn;
  path?: (string | number)[];
}) {
  if (value !== null && typeof value === 'object') {
    const entries = Object.entries(value);
    const list = Array.isArray(value);
    return <details className="json-group" open={depth < 1}>
   <summary><span className="json-key">{name || 'attributes'}</span><span className="json-shape">{list ? `[${entries.length}]` : `{${entries.length}}`}</span></summary>
   <div className="json-children">{entries.length ? entries.map(([key, item]) => <JsonTree key={key} name={key} value={item} depth={depth + 1} reveal={reveal} path={[...path, list ? Number(key) : key]} />) : <span className="muted">Empty</span>}</div>
  </details>;
  }
  if (value === '[redacted]') return reveal && path.length ? <RedactedLeaf name={name} path={path} reveal={reveal} /> : <div className="json-leaf"><span className="json-key">{name}</span><span className="redacted"><LockKeyhole size={11} />redacted</span></div>;
  const pretty = typeof value === 'string' ? prettyJson(value) : undefined;
  return <div className="json-leaf"><span className="json-key">{name}</span><span className={typeof value === 'string' ? `json-string${pretty ? ' json-pretty' : ''}` : 'json-value'}>{pretty ?? (value === null ? 'null' : String(value))}</span></div>;
}

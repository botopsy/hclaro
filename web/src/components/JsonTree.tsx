import { LockKeyhole } from 'lucide-react';
export function JsonTree({
  value,
  name,
  depth = 0
}: {
  value: unknown;
  name?: string;
  depth?: number;
}) {
  if (value !== null && typeof value === 'object') {
    const entries = Object.entries(value);
    return <details className="json-group" open={depth < 1}>
   <summary><span className="json-key">{name || 'attributes'}</span><span className="json-shape">{Array.isArray(value) ? `[${entries.length}]` : `{${entries.length}}`}</span></summary>
   <div className="json-children">{entries.length ? entries.map(([key, item]) => <JsonTree key={key} name={key} value={item} depth={depth + 1} />) : <span className="muted">Empty</span>}</div>
  </details>;
  }
  return <div className="json-leaf"><span className="json-key">{name}</span>{value === '[redacted]' ? <span className="redacted"><LockKeyhole size={11} />redacted</span> : <span className={typeof value === 'string' ? 'json-string' : 'json-value'}>{value === null ? 'null' : String(value)}</span>}</div>;
}

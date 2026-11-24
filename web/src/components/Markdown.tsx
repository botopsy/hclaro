import type { ReactNode } from 'react';

// Formats an AI reply for reading. Everything is built as React elements, so HTML, links and
// commands in the reply stay inert text. shortcut: headings, lists, code, bold/italic only; no tables or links.
function inline(text: string): ReactNode[] {
  return text.split(/(`[^`\n]+`|\*\*[^*\n]+\*\*|\*[^*\s][^*\n]*\*)/).map((part, i) =>
    part.startsWith('`') && part.length > 2 ? <code key={i}>{part.slice(1, -1)}</code>
      : part.startsWith('**') && part.length > 4 ? <strong key={i}>{part.slice(2, -2)}</strong>
      : part.startsWith('*') && part.length > 2 ? <em key={i}>{part.slice(1, -1)}</em> : part);
}

export function Markdown({ text, label }: { text: string; label?: string }) {
  const lines = text.split('\n');
  const out: ReactNode[] = [];
  for (let i = 0; i < lines.length;) {
    const line = lines[i];
    const key = out.length;
    if (line.trimStart().startsWith('```')) {
      const code: string[] = [];
      for (i++; i < lines.length && !lines[i].trimStart().startsWith('```'); i++) code.push(lines[i]);
      i++;
      out.push(<pre key={key}><code>{code.join('\n')}</code></pre>);
    } else if (/^#{1,6}\s/.test(line)) {
      out.push(<h4 key={key}>{inline(line.replace(/^#+\s+/, ''))}</h4>);
      i++;
    } else if (/^\s*([-*]|\d+[.)])\s/.test(line)) {
      const ordered = /^\s*\d/.test(line);
      const items: string[] = [];
      for (; i < lines.length && /^\s*([-*]|\d+[.)])\s/.test(lines[i]); i++) items.push(lines[i].replace(/^\s*([-*]|\d+[.)])\s+/, ''));
      const li = items.map((t, n) => <li key={n}>{inline(t)}</li>);
      out.push(ordered ? <ol key={key}>{li}</ol> : <ul key={key}>{li}</ul>);
    } else if (!line.trim()) {
      i++;
    } else {
      const para: string[] = [];
      for (; i < lines.length && lines[i].trim() && !/^(#{1,6}\s|\s*([-*]|\d+[.)])\s|\s*```)/.test(lines[i]); i++) para.push(lines[i]);
      out.push(<p key={key}>{inline(para.join('\n'))}</p>);
    }
  }
  return <div className="assist-text assist-md" aria-label={label} tabIndex={0}>{out}</div>;
}

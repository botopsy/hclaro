import { useState } from 'react';
import { Check, Copy } from 'lucide-react';
import { Button } from './ui/button';

// Copies `value` and says so in a live region; a denied clipboard is reported, never silent.
export function CopyButton({ value, label = 'Copy', iconOnly = false }: { value: string; label?: string; iconOnly?: boolean }) {
  const [note, setNote] = useState('');
  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      setNote('Copied');
    } catch {
      setNote('Clipboard is unavailable. Select and copy the text manually.');
    }
  }
  return <span className="copy-button">
   <Button type="button" variant="ghost" size={iconOnly ? 'icon' : 'small'} aria-label={iconOnly ? label : undefined} onClick={() => void copy()}>{note === 'Copied' ? <Check size={12} aria-hidden="true" /> : <Copy size={12} aria-hidden="true" />}{!iconOnly && label}</Button>
   <span className={iconOnly && note === 'Copied' ? 'sr-only' : 'muted'} role="status">{note}</span>
  </span>;
}

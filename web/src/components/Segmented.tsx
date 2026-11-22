import type { ReactNode } from 'react';

export type SegmentedOption<T extends string> = { value: T; label: string; icon?: ReactNode };

// A row of toggle buttons where exactly one is pressed (Group by, theme, density).
export function Segmented<T extends string>({ label, options, value, onChange }: { label: string; options: SegmentedOption<T>[]; value: T; onChange: (value: T) => void }) {
  return <div className="segmented" role="group" aria-label={label}>
   {options.map(o => <button key={o.value} type="button" aria-pressed={o.value === value} onClick={() => onChange(o.value)}>{o.icon}{o.label}</button>)}
  </div>;
}

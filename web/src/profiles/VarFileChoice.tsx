import { useState } from 'react';
import type { PlanOptions } from '../types';

// Variable files a launch operation may use: optional files in the project root, plus the
// files the selected profile approved. The profile's files start checked; unchecking one is a
// per-operation override that does not change the profile.
export function useVarFileChoice(options?: Pick<PlanOptions, 'varFiles' | 'context'>) {
  const approved = options?.context?.context.varFiles.map(f => f.ref) || [];
  const key = options?.context?.context.fingerprint || '';
  const [state, setState] = useState<{ key: string; chosen: string[] }>({ key, chosen: approved });
  // A different profile or revision resets the choice to that profile's files.
  const chosen = state.key === key ? state.chosen : approved;
  const setChosen = (next: string[]) => setState({ key, chosen: next });
  const names = Array.from(new Set([...approved, ...(options?.varFiles || [])]));
  return { names, approved, chosen, setChosen };
}

export function VarFileChoice({ choice, legend = 'Variable files' }: { choice: ReturnType<typeof useVarFileChoice>; legend?: string }) {
  const { names, approved, chosen, setChosen } = choice;
  if (names.length === 0) return null;
  return <fieldset className="var-files"><legend>{legend} <span className="muted">(*.auto.tfvars and terraform.tfvars load automatically)</span></legend>{names.map(name => <label key={name}><input type="checkbox" checked={chosen.includes(name)} onChange={e => setChosen(e.target.checked ? [...chosen, name] : chosen.filter(v => v !== name))} /><code>{name}</code>{approved.includes(name) && <span className="hub-tag hub-tag-quiet">profile</span>}</label>)}</fieldset>;
}

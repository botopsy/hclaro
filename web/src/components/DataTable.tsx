import { Fragment, useState, type ReactNode } from 'react';
import { ChevronDown, ChevronRight } from 'lucide-react';

export type Column<T> = { key: string; header: ReactNode; cell: (row: T) => ReactNode; className?: string };
// count: the group's full size when rows holds only a first page of it.
export type RowGroup<T> = { key: string; label: ReactNode; rows: T[]; count?: number };
export type Density = 'comfortable' | 'compact';

type Props<T> = {
  label: string;
  columns: Column<T>[];
  rowKey: (row: T) => string;
  // Either flat rows or groups with collapsible header rows.
  rows?: T[];
  groups?: RowGroup<T>[];
  // Checkbox selection, owned by the caller (the selection bar lives outside the table).
  selected?: ReadonlySet<string>;
  onSelectedChange?: (keys: Set<string>) => void;
  // Expandable rows: the content shown under an expanded row.
  expand?: (row: T) => ReactNode;
  // The row whose details are open in a drawer, highlighted.
  active?: string;
  density?: Density;
  rowLabel?: (row: T) => string;
};

// One-line rows (38px, 30px compact). Group headers collapse; rows can be checked and expanded.
export function DataTable<T>({ label, columns, rowKey, rows, groups, selected, onSelectedChange, expand, active, density = 'comfortable', rowLabel }: Props<T>) {
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(new Set());
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(new Set());
  const all = groups ?? [{ key: '', label: '', rows: rows ?? [] }];
  const keys = all.flatMap(g => g.rows.map(rowKey));
  const selectable = !!onSelectedChange;
  const span = columns.length + (selectable ? 1 : 0) + (expand ? 1 : 0);
  const checkedCount = selected ? keys.filter(k => selected.has(k)).length : 0;

  function toggle(set: ReadonlySet<string>, key: string) {
    const next = new Set(set);
    if (!next.delete(key)) next.add(key);
    return next;
  }
  function select(keysToSet: string[], on: boolean) {
    const next = new Set(selected);
    keysToSet.forEach(k => on ? next.add(k) : next.delete(k));
    onSelectedChange?.(next);
  }

  return <table className={`data-table density-${density}`} aria-label={label}>
   <thead><tr>
    {selectable && <th className="data-table-check"><input type="checkbox" aria-label="Select all rows" checked={keys.length > 0 && checkedCount === keys.length} ref={el => {
      if (el) el.indeterminate = checkedCount > 0 && checkedCount < keys.length;
    }} onChange={e => select(keys, e.target.checked)} /></th>}
    {expand && <th className="data-table-toggle"><span className="sr-only">Details</span></th>}
    {columns.map(c => <th key={c.key} className={c.className}>{c.header}</th>)}
   </tr></thead>
   <tbody>
    {all.map(g => {
      const shut = collapsed.has(g.key);
      return <Fragment key={g.key}>
       {groups && <tr className="data-table-group"><td colSpan={span}><button type="button" aria-expanded={!shut} onClick={() => setCollapsed(toggle(collapsed, g.key))}>{shut ? <ChevronRight size={13} aria-hidden="true" /> : <ChevronDown size={13} aria-hidden="true" />}<span>{g.label}</span><span className="data-table-count">{g.count ?? g.rows.length}</span></button></td></tr>}
       {!shut && g.rows.map(row => {
         const key = rowKey(row);
         const open = expanded.has(key);
         const name = rowLabel?.(row) ?? key;
         return <Fragment key={key}>
          <tr className={[key === active ? 'active' : '', expand ? 'data-table-clickable' : ''].filter(Boolean).join(' ') || undefined} onClick={expand ? e => {
           // The toggle button handles keyboard use; a click on any other control or a text selection must not toggle.
           if (!(e.target as HTMLElement).closest('button, a, input, label, select, textarea') && !window.getSelection()?.toString()) setExpanded(toggle(expanded, key));
          } : undefined}>
           {selectable && <td className="data-table-check"><input type="checkbox" aria-label={`Select ${name}`} checked={!!selected?.has(key)} onChange={e => select([key], e.target.checked)} /></td>}
           {expand && <td className="data-table-toggle"><button type="button" aria-expanded={open} aria-label={`${open ? 'Collapse' : 'Expand'} ${name}`} onClick={() => setExpanded(toggle(expanded, key))}>{open ? <ChevronDown size={13} aria-hidden="true" /> : <ChevronRight size={13} aria-hidden="true" />}</button></td>}
           {columns.map(c => <td key={c.key} className={c.className}>{c.cell(row)}</td>)}
          </tr>
          {expand && open && <tr className="data-table-detail"><td colSpan={span}>{expand(row)}</td></tr>}
         </Fragment>;
       })}
      </Fragment>;
    })}
   </tbody>
  </table>;
}

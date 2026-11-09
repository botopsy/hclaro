import { Ban, BookOpen, CircleHelp, Equal, Minus, Pencil, Plus, RefreshCw, Unlink, type LucideIcon } from 'lucide-react';
import type { PlanAction } from '../types';

// Actions are distinguished by icon and text, never by color alone.
export const actionMeta: Record<PlanAction, { label: string; icon: LucideIcon; tone: string }> = {
  create: { label: 'Create', icon: Plus, tone: 'create' },
  update: { label: 'Update', icon: Pencil, tone: 'update' },
  replace: { label: 'Replace', icon: RefreshCw, tone: 'destroy' },
  delete: { label: 'Delete', icon: Minus, tone: 'destroy' },
  read: { label: 'Read', icon: BookOpen, tone: 'neutral' },
  forget: { label: 'Forget', icon: Unlink, tone: 'warn' },
  unknown: { label: 'Unsupported', icon: CircleHelp, tone: 'warn' },
  'no-op': { label: 'No change', icon: Equal, tone: 'muted' }
};

export const actionOrder: PlanAction[] = ['create', 'update', 'replace', 'delete', 'read', 'forget', 'unknown', 'no-op'];

export function meta(action: string) {
  return actionMeta[action as PlanAction] ?? { label: 'Unsupported', icon: Ban, tone: 'warn' };
}

export function replaceOrder(actions: string[]) {
  if (actions.join(',') === 'delete,create') return 'Destroy the existing object, then create a replacement';
  if (actions.join(',') === 'create,delete') return 'Create a replacement first, then destroy the existing object';
  return undefined;
}

const reasons: Record<string, string> = {
  replace_because_tainted: 'The object is tainted',
  replace_because_cannot_update: 'The change cannot be made in place',
  replace_by_request: 'Replacement was requested',
  delete_because_no_resource_config: 'The resource was removed from configuration',
  delete_because_no_module: 'Its module was removed from configuration',
  delete_because_wrong_repetition: 'count or for_each changed for this resource',
  delete_because_count_index: 'This count index no longer exists',
  delete_because_each_key: 'This for_each key no longer exists',
  delete_because_no_move_target: 'The moved block target does not exist',
  read_because_config_unknown: 'Read during apply: configuration depends on unknown values',
  read_because_dependency_pending: 'Read during apply: a dependency has pending changes',
  read_because_check_nested: 'Read during apply: used by a check block'
};

export function reasonLabel(reason?: string) {
  if (!reason) return undefined;
  return reasons[reason] ?? reason.replaceAll('_', ' ');
}

export function clock(time?: string) {
  return time ? new Date(time).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' }) : '—';
}

export function duration(ms: number) {
  const seconds = Math.round(ms / 1000);
  return seconds < 60 ? `${seconds}s` : `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
}

export function engineLabel(engine: string) {
  return engine === 'tofu' ? 'OpenTofu' : engine === 'terraform' ? 'Terraform' : 'The engine';
}

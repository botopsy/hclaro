import type { ImportRequest } from './imports';
// AI assistance (AI-01.03 onward): settings, context packages, conversations and proposed changes.
export type AssistDisclosure = { version: string; lines: string[] };
// send: Hclaro can run it; usable: it can at least be copied to; installed: found on PATH.
export type AssistProvider = { id: string; name: string; mode: string; send: boolean; usable: boolean; installed: boolean; version?: string; path?: string; reason?: string; message?: string };
export type AssistSettings = {
  enabled: boolean; strictLocal: boolean; handoff: boolean; headless: boolean; disclosure: AssistDisclosure;
  disclosureAccepted?: boolean; providers?: AssistProvider[]; defaultProvider?: string;
  // checked/checking: whether the assistants were detected (AI-01.14), which happens only on
  // request; warning: saved settings were unreadable.
  checked?: boolean; checking?: boolean; warning?: string;
  // The server's byte limit for one typed message.
  maxMessage: number;
};
export type AssistTurn = { role: 'user' | 'assistant' | 'context'; text: string; at: string; prompt?: string; sha256?: string; hidden?: boolean };
export type Conversation = {
  id: string; provider: string; kind: string; packageSha256: string; state: 'ready' | 'running' | 'failed' | 'cancelled'; stale?: boolean;
  turns: AssistTurn[]; draft?: string; error?: { code: string; message: string }; usedBytes: number; limitBytes: number; startedAt: string;
  // Saved conversations (AI-01.11): the file is kept; readOnly says why a reopened one cannot continue.
  saved: boolean; saveError?: string; readOnly?: string; needsReview?: boolean;
};
// action is a quick action prompt (AI-01.10); cost attaches the session's existing cost estimate.
export type AssistRequest = { kind: 'project'; action?: string; cost?: boolean } | { kind: 'plan'; planId: string } | { kind: 'drift'; scanId: string } | { kind: 'resource'; resourceId: string } | { kind: 'import'; import: ImportRequest };
// review is the first turn (AI-01.08): the review prompt and the package, byte for byte what
// Send submits and what Copy and Download hand over.
export type AssistPackage = { kind: string; markdown: string; sha256: string; sections: string[]; dropped: string[]; disclosure: AssistDisclosure; prompt: string; review: string; costAvailable?: boolean; costHidden?: number };
// prompt and review are set for kinds that have a review prompt.
export type AssistExport = { kind: string; sha256: string; markdown: string; filename: string; prompt?: string; review?: string };
export type AssistListed = { id: string; provider: string; kind: string; state: string; title: string; project: string; workspace: string; here: boolean; saved: boolean; startedAt: string };
export type AssistSaved = { id: string; title: string; provider: string; providerVersion: string; kind: string; workspace: string; createdAt: string; lastActivity: string; open: boolean };
export type ChangeView = {
  turn: number; index: number; path: string; status: 'ready' | 'invalid' | 'applied' | 'rejected' | 'undone';
  code?: string; reason?: string; created: boolean; baseSha256?: string; newSha256?: string; diff?: string;
  added: number; removed: number; canUndo?: boolean; aside?: string;
};

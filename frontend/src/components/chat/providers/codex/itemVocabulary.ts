/**
 * The Codex wire vocabulary the browser alone reads: the item `status` words, and the
 * one category that rides on a JSON-RPC method rather than an item type.
 *
 * The item types and the method names cross the language boundary -- the Go worker
 * classifies each item and routes each method -- so they live in
 * contracts/codex-protocol.json. A call site reads `CODEX_ITEM` and `CODEX_METHOD`
 * from `~/generated/contracts/codex-protocol` directly.
 */

/**
 * Canonical Codex `status` literals. Codex emits `status` on tool-call items
 * (`commandExecution`, `fileChange`, `mcpToolCall`, `collabAgentToolCall`);
 * classifiers and renderers branch on these strings, centralized here so
 * call sites can reference them by name and TypeScript catches typos.
 */
export const CODEX_STATUS = {
  COMPLETED: 'completed',
  FAILED: 'failed',
  IN_PROGRESS: 'inProgress',
  /**
   * The reader refused the approval, so Codex never ran the call. Reported on a
   * `commandExecution` and on a `fileChange`. It is a FINISHED state: a row that
   * folds it into `inProgress` reads as a call still running, forever.
   */
  DECLINED: 'declined',
} as const

export type CodexStatus = typeof CODEX_STATUS[keyof typeof CODEX_STATUS]

/**
 * Codex tool/category labels that don't ride on `item.type`. `TURN_PLAN`
 * dispatches off `parent.method === 'turn/plan/updated'` rather than an
 * `item.type`, so a dispatch table spells it as a constant beside the generated
 * `CODEX_ITEM` members instead of retyping the word.
 */
export const CODEX_INTERNAL_TOOL = {
  TURN_PLAN: 'turnPlan',
} as const

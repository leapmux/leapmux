import type { ControlQuestion } from './question'

export interface PermissionOption {
  optionId: string
  kind: string
  name?: string
}

export const KIND_ALLOW_ONCE = 'allow_once'
export const KIND_ALLOW_ALWAYS = 'allow_always'
export const KIND_REJECT_ONCE = 'reject_once'
export const KIND_REJECT_ALWAYS = 'reject_always'

export const CANONICAL_KINDS = [KIND_ALLOW_ONCE, KIND_ALLOW_ALWAYS, KIND_REJECT_ONCE, KIND_REJECT_ALWAYS]

export function isRejectPermissionKind(kind: string): boolean {
  return kind === KIND_REJECT_ONCE || kind === KIND_REJECT_ALWAYS
}

export function isAllowPermissionKind(kind: string): boolean {
  return kind === KIND_ALLOW_ONCE || kind === KIND_ALLOW_ALWAYS
}

export interface PlanPermission {
  tool: string
  prompt: string
}

export interface ElicitationRequest {
  purpose?: 'permission'
  mode: string
  title?: string
  description?: string
  message: string
  server?: string
  schema?: unknown
  url?: string
  arguments?: unknown
  argumentNotice?: string
  acceptChoices?: { key: string, label: string, metadata?: Record<string, unknown> }[]
}

/**
 * One permission request, after its provider read its own wire format.
 *
 * Every provider asks the same question -- may this call run -- and states it in a
 * shape of its own: Claude and ZCode state a tool and its input, Codex states a method
 * and a command, the Agent Client Protocol family sends a tool call and a list of
 * options. The fields below are what a reader needs, and each provider's plugin is
 * what fills them.
 */
export interface PermissionPrompt {
  /** The operation the reader approves: a tool name, or the runtime's own title. */
  title?: string
  /** Why the call needs approval, in the runtime's own words. */
  reason?: string
  /**
   * The command the call runs, drawn as code above the arguments.
   *
   * Separate from `input` because the row draws it differently, and because
   * `PermissionRequestContent` drops a `command` key from the arguments when the two
   * agree -- one command, shown once.
   */
  command?: string
  workingDirectory?: string
  /** The call's arguments, drawn as JSON. */
  input?: unknown
  /**
   * The answers the runtime offers, in the order it sent them.
   *
   * EMPTY is a real answer and not a missing one: Claude, Codex, ZCode and Pi state
   * no options at all, and the shared Allow/Deny pair answers for them. The Agent
   * Client Protocol family and Goose send their own, and Copilot builds its own from
   * the request kind -- its wire carries no list, but its model always states one.
   * `layoutPermissionOptions` lays those out.
   */
  options: PermissionOption[]
}

/**
 * One dialog an extension raised through the runtime, rather than a decision about a
 * tool call.
 *
 * Pi is the one provider that sends these: `pi-mono` lets an extension open a
 * confirm, an input or an editor over RPC, and the runtime blocks until the reader
 * answers. They are neither a permission nor a question -- there is nothing to
 * approve and no option list -- so they take a variant of their own.
 */
export interface DialogPrompt {
  /** The dialog's own heading. */
  title: string
  /** The sentence a confirm dialog states above its buttons. */
  message?: string
  /** The hint an input dialog shows inside its field. */
  placeholder?: string
  /** What the editor starts from, when the runtime supplied a draft. */
  prefill?: string
  /**
   * Which control the reader answers with. `confirm` is two buttons, `input` is one
   * line beside them, and `editor` is a text area above them.
   */
  variant: 'confirm' | 'input' | 'editor'
  /**
   * How long the runtime waits before it answers for the reader, in milliseconds.
   *
   * The banner states it, because a dialog that resolves itself is one the reader
   * must be able to see a deadline on.
   */
  timeoutMs?: number
}

/**
 * What ONE control request asks, apart from how it is drawn.
 *
 * A CLOSED union, and the reason is the one every closed set in this model has: the
 * shared banner switches exhaustively over it, so a provider that returned a shape
 * no component knew would be a compile error rather than a blank banner. Each
 * provider maps its own payload onto one variant in its own plugin
 * (`Provider.extractControl`), so the banner holds no provider's wire format and no
 * `switch` on provider.
 *
 * A `plan` carries the two optional lists the approval draws beside the plan, and
 * the plan TEXT when its own request carries one. Most providers put the plan in the
 * transcript and send an approval that identifies it -- Copilot is the one that
 * sends the whole plan in the request, so the banner draws it there rather than
 * pointing at a row the reader would have to find.
 */
export type ControlPrompt
  = | { kind: 'question', questions: ControlQuestion[] }
    | { kind: 'elicitation', elicitation: ElicitationRequest }
    | { kind: 'plan', text?: string, permissions?: readonly PlanPermission[], details?: readonly string[] }
    | { kind: 'permission', permission: PermissionPrompt }
    | { kind: 'dialog', dialog: DialogPrompt }

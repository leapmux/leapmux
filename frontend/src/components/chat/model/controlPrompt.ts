import type { ControlQuestion } from './question'
import { ACP_PERMISSION_KIND } from '~/generated/contracts/acp-protocol'
import { assertNever } from '~/lib/assertNever'

/**
 * Why the Worker could not read a control request's payload as a request of its provider:
 *
 * - `malformed`: the bytes are not JSON.
 * - `not-an-object`: the bytes are JSON, but not an object.
 * - `unrecognized`: the bytes are a JSON object, but the provider does not recognize it
 *   as a request that its agent raises, so the provider can build no answer for it.
 */
export type ControlPayloadFault = 'malformed' | 'not-an-object' | 'unrecognized'

/**
 * Whether the payload of a request with this fault is a JSON object, so the browser can
 * decode it. A request with no fault is one that the Worker read as an object.
 */
export function controlPayloadIsObject(fault: ControlPayloadFault | undefined): boolean {
  switch (fault) {
    case undefined:
    case 'unrecognized':
      return true
    case 'malformed':
    case 'not-an-object':
      return false
    default:
      return assertNever(fault)
  }
}

/**
 * How far a remembered answer reaches: this session, the workspace, the project, or
 * every project of the user.
 */
export type PermissionScope = 'session' | 'workspace' | 'project' | 'user'

export interface PermissionOption {
  optionId: string
  kind: string
  name?: string
  /**
   * The reach of a remembered answer (an always-allow or an always-reject), when the
   * provider knows it. Without it, the scope label reads the agent's option name,
   * which can hold the text of the call.
   */
  scope?: PermissionScope
  /**
   * The label of the scope pill that selects this option, when the runtime names its
   * own scopes, such as Codex's "Host rule". Two options of one scope and different
   * labels keep different rules, so a remembered refusal pairs only with the pill of
   * its own scope and label.
   */
  scopeLabel?: string
  /**
   * Choosing the option also ends the agent's turn, rather than refusing only
   * this call. The decision button names such a refusal Cancel.
   */
  endsTurn?: boolean
}

export const KIND_ALLOW_ALWAYS = ACP_PERMISSION_KIND.AllowAlways
export const KIND_REJECT_ONCE = ACP_PERMISSION_KIND.RejectOnce
export const KIND_REJECT_ALWAYS = ACP_PERMISSION_KIND.RejectAlways

export const CANONICAL_KINDS: readonly string[] = [
  ACP_PERMISSION_KIND.AllowOnce,
  KIND_ALLOW_ALWAYS,
  KIND_REJECT_ONCE,
  KIND_REJECT_ALWAYS,
]

export function isRejectPermissionKind(kind: string): boolean {
  return kind === KIND_REJECT_ONCE || kind === KIND_REJECT_ALWAYS
}

export function isAllowPermissionKind(kind: string): boolean {
  return kind === ACP_PERMISSION_KIND.AllowOnce || kind === KIND_ALLOW_ALWAYS
}

export interface PlanPermission {
  tool: string
  prompt: string
}

/**
 * What one plan choice does with the plan: the agent implements it (`approve`), keeps
 * planning or leaves plan mode without it (`refuse`), or neither, such as saving the
 * plan for later (`other`).
 */
export type PlanChoiceEffect = 'approve' | 'refuse' | 'other'

/**
 * One answer a plan approval offers beside its plain Approve and Reject.
 *
 * A runtime can offer the reader more than a yes or a no to a plan: the approaches the
 * plan itself lists, a request for revisions, a refusal that also ends plan mode, or an
 * extension's own action. The answer carries the `id` back, and the Worker's provider
 * maps it onto the runtime's own answer. The ids are the provider's own words.
 */
export interface PlanChoice {
  /** What the answer carries back, which the provider maps onto its own answer. */
  id: string
  label: string
  description?: string
  effect: PlanChoiceEffect
}

/** Conditions use typed earlier answers; an absent answer never matches. */
export interface ElicitationFieldRule {
  key: string
  hidden?: boolean
  conditions?: {
    field: string
    comparison: 'equal' | 'not-equal'
    value: string | number | boolean
  }[]
}

export interface ElicitationRequest {
  purpose?: 'permission'
  mode: string
  title?: string
  description?: string
  message: string
  server?: string
  schema?: unknown
  fieldRules?: ElicitationFieldRule[]
  url?: string
  arguments?: unknown
  argumentNotice?: string
  acceptChoices?: { key: string, label: string }[]
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
   * What the runtime asks the reader to approve, in prose, drawn as markdown: a
   * subagent's plan, for example. A plugin that states it leaves it out of `input`,
   * so the reader sees it once.
   */
  text?: string
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
   * Every answer is an option. A provider whose runtime states no options offers
   * LeapMux's own Allow and Deny (`agent.AllowDenyOptions` in Go), as Cline and
   * CodeWhale do. Other providers word their own options from the request, as
   * Claude does for its session grant and Copilot does for its approval kinds, and
   * the Agent Client Protocol family sends its own. `layoutPermissionOptions` lays
   * them out.
   */
  options: PermissionOption[]
  /** Each option makes a distinct decision. No permission scope combines these options. */
  independentOptions?: boolean
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
 * shared banner switches exhaustively over it, so a shape that no component knew
 * would be a compile error rather than a blank banner. The Worker's provider package
 * reads its own payload into one variant (`Provider.ReadControlRequest` in Go), and
 * `controlPromptFromWire` brings it here, so the browser holds no provider's wire
 * format and no `switch` on provider.
 *
 * A `plan` carries the two optional lists the approval draws beside the plan, the
 * plan TEXT when its own request carries one, and the extra answers the runtime offers
 * beside Approve and Reject. Most providers put the plan in the
 * transcript and send an approval that identifies it -- Copilot is the one that
 * sends the whole plan in the request, so the banner draws it there rather than
 * pointing at a row the reader would have to find.
 */
export type ControlPrompt
  = | {
    kind: 'question'
    questions: ControlQuestion[]
    /**
     * An option selection and a typed note coexist in one answer, instead of
     * replacing each other.
     */
    preservesSelectionNotes?: boolean
  }
  | { kind: 'elicitation', elicitation: ElicitationRequest }
  | { kind: 'plan', text?: string, permissions?: readonly PlanPermission[], details?: readonly string[], choices?: readonly PlanChoice[] }
  | { kind: 'permission', permission: PermissionPrompt }
  | { kind: 'dialog', dialog: DialogPrompt }

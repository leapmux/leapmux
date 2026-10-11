import type { ControlExtractionInput, ExtractedControlRequest } from '../registry'
import type { PermissionOption } from '~/components/chat/model/controlPrompt'
import { KIND_ALLOW_ALWAYS, KIND_ALLOW_ONCE, KIND_REJECT_ONCE } from '~/components/chat/model/controlPrompt'
import { MIMO_CONTROL_FIELD, MIMO_EVENT, MIMO_PERMISSION_REPLY, MIMO_TOOL } from '~/generated/contracts/mimo-protocol'
import { OPENCODE_EVENT } from '~/generated/contracts/opencode-protocol'
import { isObject, pickObject, pickString, stringArray } from '~/lib/jsonPick'
import { getToolName } from '~/utils/controlResponse'
import { MIMO_FORCED_ASK_PERMISSIONS } from './protocol'

/**
 * The three answers MiMo takes for a permission, in the order MiMo's own prompt offers
 * them. `always` approves every later call that matches the request's `always`
 * patterns, for as long as the server runs: MiMo keeps the rule for the working
 * directory, and each session of the server in that directory reads it.
 * {@link mimoPermissionOptions} decides which of them one request offers.
 */
export const MIMO_PERMISSION_OPTIONS: readonly PermissionOption[] = [
  { optionId: MIMO_PERMISSION_REPLY.Once, kind: KIND_ALLOW_ONCE, name: 'Allow once' },
  { optionId: MIMO_PERMISSION_REPLY.Always, kind: KIND_ALLOW_ALWAYS, name: 'Always allow' },
  { optionId: MIMO_PERMISSION_REPLY.Reject, kind: KIND_REJECT_ONCE, name: 'Reject' },
]

/**
 * The answers one permission request offers.
 *
 * `always` is offered only where MiMo keeps it. MiMo saves the request's `always`
 * patterns, so a request with no pattern saves nothing, and it reads `always` as
 * `once` for a permission that it always asks for. MiMo's own prompt hides the answer
 * in both cases, and an offer here would save an answer that the next call ignores.
 */
function mimoPermissionOptions(permission: string, always: readonly string[]): PermissionOption[] {
  const keepsAlways = !MIMO_FORCED_ASK_PERMISSIONS.has(permission) && always.some(pattern => pattern !== '')
  return MIMO_PERMISSION_OPTIONS.filter(option => keepsAlways || option.optionId !== MIMO_PERMISSION_REPLY.Always)
}

/** The permissions whose patterns are the shell command the call runs. */
const COMMAND_PERMISSIONS: ReadonlySet<string> = new Set([MIMO_TOOL.Bash, ...MIMO_FORCED_ASK_PERMISSIONS])

/**
 * The lines a plan approval states beside the plan when the worker could not read the
 * plan text: the plan file that MiMo gives, else MiMo's own question.
 *
 * The worker reads a Markdown file of limited size and leaves the text out of every
 * other plan, so the reader still learns where the plan it approves is.
 */
function planDetails(payload: Record<string, unknown>): string[] {
  const questions = pickObject(payload, 'properties')?.questions
  const question = Array.isArray(questions) && isObject(questions[0]) ? questions[0] : {}
  const path = pickString(pickObject(question, 'params'), 'plan').trim()
  if (path)
    return [`Plan file: ${path}`]
  const words = pickString(question, 'question').trim()
  return words ? [words] : []
}

/**
 * `Provider.extractControl` for MiMo Code.
 *
 * The worker stores MiMo's own event with a `request` header beside it, and the event
 * TYPE is what this switches on:
 *
 *   permission.asked                      -> a permission
 *   question.asked, recorded as plan_exit -> the plan approval
 *
 * Every other `question.asked` reaches its own surface before this reader runs: a
 * question, which `askUserQuestion.isRequest` recognizes, or an MCP server's
 * confirmation, which the `elicitation` hook recognizes.
 */
export function mimoExtractControl(input: ControlExtractionInput): ExtractedControlRequest | null {
  const { payload } = input
  const type = pickString(payload, 'type')
  if (type === OPENCODE_EVENT.QuestionAsked && getToolName(payload) === MIMO_TOOL.PlanExit) {
    // The worker reads the plan file the approval gives, and states the text beside
    // the event, so the banner draws the plan it asks about.
    const plan = pickString(payload, MIMO_CONTROL_FIELD.Plan)
    if (plan)
      return { kind: 'plan', text: plan }
    const details = planDetails(payload)
    return { kind: 'plan', ...(details.length > 0 ? { details } : {}) }
  }
  if (type !== MIMO_EVENT.PermissionAsked)
    return null
  const properties = pickObject(payload, 'properties') ?? {}
  const permission = pickString(properties, 'permission')
  const patterns = stringArray(properties.patterns)
  const always = stringArray(properties.always)
  const metadata = pickObject(properties, 'metadata') ?? {}
  const command = COMMAND_PERMISSIONS.has(permission)
    ? pickString(metadata, 'command') || patterns.join('\n')
    : ''
  // A shell permission's patterns ARE the command, which the banner draws as code, so
  // the arguments leave them out rather than state the command twice.
  const statedPatterns = command ? [] : patterns
  const options = mimoPermissionOptions(permission, always)
  const offersAlways = options.some(option => option.optionId === MIMO_PERMISSION_REPLY.Always)
  return {
    kind: 'permission',
    permission: {
      title: permission,
      // What the request asks for, and what `Always allow` would approve from now on,
      // for a request that offers it.
      input: {
        ...(statedPatterns.length > 0 ? { patterns: statedPatterns } : {}),
        ...(offersAlways ? { always } : {}),
        ...metadata,
      },
      ...(command ? { command } : {}),
      options,
    },
  }
}

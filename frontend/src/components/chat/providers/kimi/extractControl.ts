import type { PermissionOption, PlanChoice } from '../../model/controlPrompt'
import type { ControlExtractionInput, ExtractedControlRequest } from '../registry'
import type { ControlResponseSender } from '~/components/chat/controls/types'
import { KIND_ALLOW_ALWAYS, KIND_ALLOW_ONCE, KIND_REJECT_ONCE } from '~/components/chat/model/controlPrompt'
import { KIMI_APPROVAL_SCOPE, KIMI_DISPLAY, KIMI_EVENT, KIMI_GOAL_MODE, KIMI_PLAN_LABEL, KIMI_REPLY } from '~/generated/contracts/kimi-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { buildControlResponseEnvelope, buildDenyResponse, withControlChoice } from '~/utils/controlResponse'
import { sendResponse } from '../../controls/types'
import { KIMI_MAIN_AGENT, kimiDisplay } from './protocol'

/**
 * Kimi Code's approval requests, read into the shared control model.
 *
 * The server announces an approval as `event.approval.requested`, and the worker
 * publishes that payload verbatim. Its `tool_input_display` words the call, and its
 * `kind` decides which control answers it:
 *
 *   plan_review -> the main agent's plan: the plan approval, with the approaches the
 *                  plan offers. A subagent's plan: a permission with the same answers,
 *                  because the plan approval acts on the MAIN agent -- it leaves the
 *                  main agent's plan mode, can clear the main context, and switches
 *                  the main permission mode.
 *   goal_start  -> a permission, whose options start the goal in a permission mode
 *   anything    -> a permission: approve once, approve for the session, or reject
 *
 * A question is a different event, which `askUserQuestion.isRequest` recognizes before
 * this reader runs.
 */

/** The option ids of a Kimi Code permission. They are LeapMux's own, never sent. */
export const KIMI_OPTION = {
  Approve: 'approve',
  ApproveForSession: 'approve_for_session',
  Reject: 'reject',
  /** The prefix of an option that starts a goal in one permission mode. */
  GoalModePrefix: 'goal_mode:',
  /** The prefix of an option that approves a subagent's plan with one of the plan's own approaches. */
  PlanApprovePrefix: 'plan_approve:',
  /** The prefix of an option that refuses a subagent's plan with a plan label: Revise, or Reject and Exit. */
  PlanRejectPrefix: 'plan_reject:',
} as const

/** The three answers every approval that is not a plan or a goal offers. */
const KIMI_PERMISSION_OPTIONS: readonly PermissionOption[] = [
  { optionId: KIMI_OPTION.Approve, kind: KIND_ALLOW_ONCE, name: 'Allow' },
  { optionId: KIMI_OPTION.ApproveForSession, kind: KIND_ALLOW_ALWAYS, name: 'Allow for this session' },
  { optionId: KIMI_OPTION.Reject, kind: KIND_REJECT_ONCE, name: 'Deny' },
]

/** The permission modes a goal can start in, with the names Kimi's own UI gives them. */
const KIMI_GOAL_MODE_NAMES: readonly (readonly [string, string])[] = [
  [KIMI_GOAL_MODE.Manual, 'Always Ask'],
  [KIMI_GOAL_MODE.Yolo, 'Ask When Needed'],
  [KIMI_GOAL_MODE.Auto, 'Never Ask'],
]

/** The answers a goal start offers: start it as it stands, start it in a mode, or decline. */
const KIMI_GOAL_OPTIONS: readonly PermissionOption[] = [
  { optionId: KIMI_OPTION.Approve, kind: KIND_ALLOW_ONCE, name: 'Start the goal' },
  ...KIMI_GOAL_MODE_NAMES.map(([mode, name]) => ({ optionId: `${KIMI_OPTION.GoalModePrefix}${mode}`, kind: KIND_ALLOW_ONCE, name: `Start in ${name}` })),
  { optionId: KIMI_OPTION.Reject, kind: KIND_REJECT_ONCE, name: 'Decline' },
]

/** Whether a stored control payload is an approval request. */
export function kimiIsApproval(payload: Record<string, unknown>): boolean {
  return pickString(payload, 'type') === KIMI_EVENT.ApprovalRequested
}

/**
 * The agent an approval belongs to: `main`, or a subagent's `agent-N`. The server tags
 * the interaction with its agent and repeats the tag on the event envelope, and it
 * words an interaction with no tag as the main agent's.
 */
function kimiApprovalAgentId(payload: Record<string, unknown>): string {
  return pickString(payload, 'agent_id') || pickString(payload, 'agentId') || KIMI_MAIN_AGENT
}

/** The `tool_input_display.kind` of an approval. */
export function kimiApprovalDisplayKind(payload: Record<string, unknown>): string {
  return pickString(kimiDisplay(payload, 'tool_input_display'), 'kind')
}

/**
 * The extra answers a plan review offers: each approach the plan lists, a request for
 * revisions, and a refusal that also leaves plan mode.
 */
export function kimiPlanChoices(display: Record<string, unknown> | undefined): PlanChoice[] {
  const options = display && Array.isArray(display.options) ? display.options.filter(isObject) : []
  const approaches = options.flatMap((option) => {
    const label = pickString(option, 'label')
    if (!label)
      return []
    const description = pickString(option, 'description')
    return [{ id: label, label: `Approve: ${label}`, ...(description ? { description } : {}), approves: true }]
  })
  return [
    ...approaches,
    { id: KIMI_PLAN_LABEL.Revise, label: 'Request revisions', approves: false },
    { id: KIMI_PLAN_LABEL.RejectAndExit, label: 'Reject and exit plan mode', approves: false },
  ]
}

/**
 * The answers a subagent's plan review offers as permission options: the plain
 * approval, each approach the plan lists, the plain refusal, and the two refusals
 * with a plan label. `kimiPlanChoices` states the words, so the main agent's plan
 * approval and this one read alike.
 */
function kimiSubagentPlanOptions(display: Record<string, unknown> | undefined): PermissionOption[] {
  const choices = kimiPlanChoices(display)
  return [
    { optionId: KIMI_OPTION.Approve, kind: KIND_ALLOW_ONCE, name: 'Approve the plan' },
    ...choices.filter(choice => choice.approves).map(choice => ({ optionId: `${KIMI_OPTION.PlanApprovePrefix}${choice.id}`, kind: KIND_ALLOW_ONCE, name: choice.label })),
    { optionId: KIMI_OPTION.Reject, kind: KIND_REJECT_ONCE, name: 'Reject' },
    ...choices.filter(choice => !choice.approves).map(choice => ({ optionId: `${KIMI_OPTION.PlanRejectPrefix}${choice.id}`, kind: KIND_REJECT_ONCE, name: choice.label })),
  ]
}

/** The options of an approval that reads as a permission, by its display kind. */
function kimiPermissionOptions(displayKind: string, display: Record<string, unknown> | undefined): PermissionOption[] {
  switch (displayKind) {
    case KIMI_DISPLAY.PlanReview:
      return kimiSubagentPlanOptions(display)
    case KIMI_DISPLAY.GoalStart:
      return [...KIMI_GOAL_OPTIONS]
    default:
      return [...KIMI_PERMISSION_OPTIONS]
  }
}

/** `Provider.extractControl` for Kimi Code. */
export function kimiExtractControl(input: ControlExtractionInput): ExtractedControlRequest | null {
  const { payload } = input
  if (!kimiIsApproval(payload))
    return null
  const display = kimiDisplay(payload, 'tool_input_display')
  const displayKind = pickString(display, 'kind')
  if (displayKind === KIMI_DISPLAY.PlanReview && kimiApprovalAgentId(payload) === KIMI_MAIN_AGENT) {
    const plan = pickString(display, 'plan')
    return { kind: 'plan', ...(plan.trim() ? { text: plan } : {}), choices: kimiPlanChoices(display) }
  }
  const toolName = pickString(payload, 'tool_name')
  const reason = pickString(payload, 'action')
  const command = pickString(display, 'command')
  const cwd = pickString(display, 'cwd')
  // A subagent's plan review states its plan in prose, which reads as text. The rest
  // of the display stays in the arguments.
  const planText = displayKind === KIMI_DISPLAY.PlanReview ? pickString(display, 'plan') : ''
  const plan = planText.trim() ? planText : ''
  const args = plan && display ? Object.fromEntries(Object.entries(display).filter(([key]) => key !== 'plan')) : display
  return {
    kind: 'permission',
    permission: {
      title: toolName,
      ...(reason ? { reason } : {}),
      ...(plan ? { text: plan } : {}),
      ...(command ? { command } : {}),
      ...(cwd ? { workingDirectory: cwd } : {}),
      // The display is the whole statement of the call the approval carries: the server
      // sends no arguments beside it.
      ...(args ? { input: args } : {}),
      options: kimiPermissionOptions(displayKind, display),
    },
  }
}

/**
 * Send ONE chosen permission option, in the neutral envelope with the Kimi fields beside
 * the behavior. The worker turns it into the server's own approval body.
 *
 * Only an option that approves sends an approval. Every other id is a refusal: the ids
 * are LeapMux's own, so an id no control offers is a defect in the caller, and reading
 * it as an approval would run a call the reader never allowed.
 */
export async function sendKimiPermissionOption(onRespond: ControlResponseSender, requestId: string, optionId: string): Promise<void> {
  const allow = buildControlResponseEnvelope(requestId, { behavior: 'allow' })
  if (optionId === KIMI_OPTION.Approve)
    return sendResponse(onRespond, allow)
  if (optionId === KIMI_OPTION.ApproveForSession)
    return sendResponse(onRespond, buildControlResponseEnvelope(requestId, { behavior: 'allow', [KIMI_REPLY.Scope]: KIMI_APPROVAL_SCOPE.Session }))
  if (optionId.startsWith(KIMI_OPTION.GoalModePrefix))
    return sendResponse(onRespond, withControlChoice(allow, optionId.slice(KIMI_OPTION.GoalModePrefix.length)))
  if (optionId.startsWith(KIMI_OPTION.PlanApprovePrefix))
    return sendResponse(onRespond, withControlChoice(allow, optionId.slice(KIMI_OPTION.PlanApprovePrefix.length)))
  if (optionId.startsWith(KIMI_OPTION.PlanRejectPrefix))
    return sendResponse(onRespond, withControlChoice(buildDenyResponse(requestId), optionId.slice(KIMI_OPTION.PlanRejectPrefix.length)))
  return sendResponse(onRespond, buildDenyResponse(requestId))
}

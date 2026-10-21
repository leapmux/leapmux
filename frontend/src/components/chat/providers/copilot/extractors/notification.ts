import type { CompactionDetails, NotificationEntry } from '../../../model/notification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { COPILOT_EVENT, COPILOT_PERMISSION_DECISION_SOURCE, COPILOT_PERMISSION_OUTCOME } from '~/generated/contracts/copilot-protocol'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { getInnerMessage } from '~/lib/messageParser'
import { toTokenCount } from '../../../model/notification'
import { formatDuration, formatNumber } from '../../../rendererUtils'
import { COPILOT_SYSTEM_NOTIFICATION_KIND, copilotEvent, copilotEventData } from '../protocol'

/** Read the native kind before falling back to the notification's content. */
function copilotSystemNotification(data: Record<string, unknown>): string | null {
  const kind = pickObject(data, 'kind')
  const name = pickString(kind, 'displayName').trim()
  const agent = `Background agent${name ? ` ${name}` : ''}`
  const description = pickString(kind, 'description').trim()
  switch (pickString(kind, 'type')) {
    case COPILOT_SYSTEM_NOTIFICATION_KIND.AgentCompleted: {
      const status = pickString(kind, 'status')
      if (status === 'completed' || status === 'failed')
        return `${agent} ${status}`
      break
    }
    case COPILOT_SYSTEM_NOTIFICATION_KIND.AgentIdle:
      return `${agent} is idle`
    case COPILOT_SYSTEM_NOTIFICATION_KIND.ShellCompleted: {
      const code = pickNumber(kind, 'exitCode', undefined)
      return `Shell${description ? ` ${description}` : ''} exited${code !== undefined && Number.isSafeInteger(code) ? ` (code ${code})` : ''}`
    }
    case COPILOT_SYSTEM_NOTIFICATION_KIND.ShellDetachedCompleted:
      return `Detached shell${description ? ` ${description}` : ''} exited`
    case COPILOT_SYSTEM_NOTIFICATION_KIND.InstructionDiscovered: {
      const path = pickString(kind, 'sourcePath').trim()
      if (path)
        return `Read the instructions in ${path}`
      break
    }
    case COPILOT_SYSTEM_NOTIFICATION_KIND.FactoryCompleted: {
      const factory = pickString(kind, 'factoryName').trim()
      const status = pickString(kind, 'status').trim()
      if (status)
        return `Factory${factory ? ` ${factory}` : ''} ${status}`
      break
    }
  }
  const content = pickString(data, 'content').trim()
  const wrapped = /^<system_notification>([\s\S]*)<\/system_notification>$/.exec(content)
  return (wrapped?.[1] ?? content).trim() || null
}

/** The sentence a subagent outcome reads as, with what the runtime measured. */
function copilotSubagentLine(data: Record<string, unknown>, failed: boolean): string {
  const name = pickString(data, 'agentDisplayName') || pickString(data, 'agentName') || 'A subagent'
  const head = data.cancelled === true
    ? `${name} stopped`
    : failed
      ? `${name} failed`
      : `${name} finished`
  const parts: string[] = []
  const error = pickString(data, 'error')
  if (failed && error)
    parts.push(error)
  for (const [label, key] of [['tool calls', 'totalToolCalls'], ['tokens', 'totalTokens']] as const) {
    const value = pickNumber(data, key, undefined)
    if (value !== undefined && Number.isSafeInteger(value) && value >= 0)
      parts.push(`${formatNumber(value)} ${label}`)
  }
  const duration = pickNumber(data, 'durationMs', undefined)
  if (duration !== undefined && Number.isSafeInteger(duration) && duration >= 0)
    parts.push(formatDuration(duration))
  return parts.length > 0 ? `${head} — ${parts.join(', ')}` : head
}

/**
 * The rules that refused, as `<kind>: <argument>` for the ones that take an argument.
 *
 * The runtime states them on the `denied-by-rules` outcome alone, and they are the
 * useful half of that refusal: the kind and the argument together say WHICH rule to
 * change. A rule with no argument states its kind only, which the wire marks by a
 * null rather than an absent field.
 */
function copilotDeniedByRules(result: Record<string, unknown>): string {
  const rules = Array.isArray(result.rules) ? result.rules : []
  return rules
    .flatMap((rule) => {
      if (!isObject(rule))
        return []
      const kind = pickString(rule, 'kind')
      if (!kind)
        return []
      const argument = pickString(rule, 'argument')
      return [argument ? `${kind}: ${argument}` : kind]
    })
    .join(', ')
}

/**
 * Identify a permission decision that the reader did not make.
 * A human_response returns null because the saved control-response row already records that decision.
 * Automatic decisions can carry the same result as human decisions. The source field distinguishes them.
 * An absent source returns null also. Absence proves neither a human decision nor a particular automatic source.
 * The saved control-response row remains the evidence for a human decision.
 */
function copilotDecisionSourceLine(data: Record<string, unknown>): string | null {
  switch (pickString(data, 'decisionSource')) {
    case COPILOT_PERMISSION_DECISION_SOURCE.AssistedApproval:
      return 'the assisted-approval judge'
    case COPILOT_PERMISSION_DECISION_SOURCE.HostPolicy:
      return 'a standing host policy'
    case COPILOT_PERMISSION_DECISION_SOURCE.UnattendedFallback:
      return 'the runtime, with nobody available to ask'
    case COPILOT_PERMISSION_DECISION_SOURCE.AuthorizationCarryForward:
      return 'an earlier approval of yours in this session'
    default:
      return null
  }
}

function copilotPermissionOutcomeLine(data: Record<string, unknown>): string | null {
  const result = pickObject(data, 'result')
  if (!result)
    return null
  const decidedBy = copilotDecisionSourceLine(data)
  switch (pickString(result, 'kind')) {
    case COPILOT_PERMISSION_OUTCOME.DeniedByRules: {
      const rules = copilotDeniedByRules(result)
      return rules ? `Denied by the permission rules: ${rules}` : 'Denied by the permission rules'
    }
    case COPILOT_PERMISSION_OUTCOME.DeniedNoApprovalRuleAndCouldNotRequestFromUser:
      return 'Denied: no approval rule matched, and the runtime could not ask'
    case COPILOT_PERMISSION_OUTCOME.DeniedByContentExclusionPolicy:
      return 'Denied by the content exclusion policy'
    case COPILOT_PERMISSION_OUTCOME.DeniedByPermissionRequestHook:
      return 'Denied by a permission hook'
    // Human decisions have a control-response row. An automatic decision can use the same outcome without that row.
    // State the automatic source when the runtime supplies it.
    default:
      return decidedBy ? `${copilotOutcomeVerb(pickString(result, 'kind'))} by ${decidedBy}` : null
  }
}

/** The word one outcome takes when the line has to state who decided it. */
function copilotOutcomeVerb(kind: string): string {
  switch (kind) {
    case COPILOT_PERMISSION_OUTCOME.Approved:
      return 'Approved'
    case COPILOT_PERMISSION_OUTCOME.ApprovedForSession:
      return 'Approved for the session'
    case COPILOT_PERMISSION_OUTCOME.ApprovedForLocation:
      return 'Approved for this location'
    case COPILOT_PERMISSION_OUTCOME.Cancelled:
      return 'Cancelled'
    case COPILOT_PERMISSION_OUTCOME.DeniedInteractivelyByUser:
      return 'Denied'
    default:
      return 'Decided'
  }
}

/** The question, the ask or the plan summary one control request states. */
function copilotControlLine(type: string, data: Record<string, unknown>): string | null {
  switch (type) {
    case COPILOT_EVENT.PermissionRequested: {
      const request = pickObject(data, 'permissionRequest')
      const kind = pickString(request, 'kind') || pickString(request, 'type')
      return kind ? `Asked to approve ${kind}` : 'Asked to approve a tool call'
    }
    case COPILOT_EVENT.UserInputRequested: {
      const question = pickString(data, 'question')
      return question ? `Asked: ${question}` : 'Asked a question'
    }
    case COPILOT_EVENT.ExitPlanModeRequested: {
      const summary = pickString(data, 'summary')
      return summary ? `Proposed a plan: ${summary}` : 'Proposed a plan'
    }
    case COPILOT_EVENT.ElicitationRequested: {
      const message = pickString(data, 'message')
      return message ? `Asked for input: ${message}` : 'Asked for input'
    }
    // The Worker answers these native asks through its control surfaces:
    // - Tool permission and user input.
    // - Plan exit and elicitation.
    // - Sampling and an Auto model switch.
    case COPILOT_EVENT.SamplingRequested: {
      const server = pickString(data, 'serverName')
      return server ? `The MCP server ${server} asked the model to answer` : 'An MCP server asked the model to answer'
    }
    case COPILOT_EVENT.ExternalToolRequested: {
      const tool = pickString(data, 'name') || pickString(data, 'toolName')
      return tool ? `Asked the client to run ${tool}` : 'Asked the client to run a tool'
    }
    case COPILOT_EVENT.ToolUserRequested: {
      const tool = pickString(data, 'toolName') || pickString(data, 'name')
      return tool ? `Asked to run the ${tool} tool` : 'Asked to run a tool'
    }
    case COPILOT_EVENT.AutoModeSwitchRequested: {
      const code = pickString(data, 'errorCode')
      return code ? `Asked to switch the model after ${code}` : 'Asked to switch the model'
    }
    default:
      return null
  }
}

/** One MCP-server line: the server's own name when the event carries it. */
function copilotServerLine(data: Record<string, unknown>, verb: string, fallback: string): string {
  const server = pickString(data, 'serverName') || pickString(data, 'name')
  return server ? `The MCP server ${server} ${verb}` : fallback
}

/**
 * One schedule line, with the cron expression or the fire time when the event
 * carries one. A schedule with neither still states that it moved.
 */
function copilotScheduleLine(data: Record<string, unknown>, head: string): string {
  const cron = pickString(data, 'cron')
  if (cron)
    return `${head}: ${cron}`
  const at = pickNumber(data, 'at', undefined)
  // `pickNumber` proves only that the value is a number, so an out-of-range one
  // would print "Invalid Date" as the schedule a reader chose. The unit is the
  // runtime's; nothing in the contract pins it, so a wrong one shows as a 1970 date
  // rather than as a crash.
  if (at === undefined || !Number.isFinite(at))
    return head
  const stamp = new Date(at)
  return Number.isNaN(stamp.getTime()) ? head : `${head} for ${stamp.toLocaleString()}`
}

/**
 * A readable line for one Copilot notification row.
 *
 * Return null for a shape outside Copilot's vocabulary so the shared notification reader can try it.
 * Return null for an empty native notice also. The classifier hides that notice.
 */
export function describeCopilotNotification(parsed: unknown): string | null {
  const event = copilotEvent(parsed)
  if (!event)
    return null
  const data = event.data
  switch (event.type) {
    case COPILOT_EVENT.PermissionCompleted:
      return copilotPermissionOutcomeLine(data)
    // A tool an EARLIER approval of the reader's already covered, so the runtime ran it
    // without asking again. No control surface sees this and no response row records
    // it, which makes the transcript the only place a reader can find out that one
    // approval admitted a second call.
    case COPILOT_EVENT.PermissionCarriedForward:
      return 'Ran under an earlier approval of yours in this session'
    case COPILOT_EVENT.SessionError: {
      const message = pickString(data, 'message') || pickString(data, 'errorType')
      return message ? `Error: ${message}` : null
    }
    case COPILOT_EVENT.SessionWarning: {
      const message = pickString(data, 'message')
      return message ? `Warning: ${message}` : null
    }
    case COPILOT_EVENT.SessionInfo:
      return pickString(data, 'message') || null
    case COPILOT_EVENT.SystemNotification:
      return copilotSystemNotification(data)
    case COPILOT_EVENT.SessionCompactionComplete:
      return 'Conversation compacted'
    case COPILOT_EVENT.SessionContextCleared:
      return 'Context cleared'
    case COPILOT_EVENT.SessionTruncation: {
      const removed = pickNumber(data, 'messagesRemovedDuringTruncation', undefined)
      return removed !== undefined && removed > 0
        ? `Removed ${formatNumber(removed)} messages to fit the context window`
        : 'Removed older messages to fit the context window'
    }
    case COPILOT_EVENT.SkillInvoked:
    case COPILOT_EVENT.SkillInvokedRef: {
      const skill = pickString(data, 'skill') || pickString(data, 'name')
      return skill ? `Loaded the skill ${skill}` : null
    }
    case COPILOT_EVENT.SubagentCompleted:
      return copilotSubagentLine(data, false)
    case COPILOT_EVENT.SubagentFailed:
      return copilotSubagentLine(data, true)
    // An MCP server that needs the reader to sign in. Each one BLOCKS that server's
    // tools until the reader acts, so the row identifies the server.
    case COPILOT_EVENT.McpOauthRequired:
      return copilotServerLine(data, 'needs you to sign in', 'An MCP server needs you to sign in')
    case COPILOT_EVENT.McpOauthCompleted:
      return copilotServerLine(data, 'is signed in', 'An MCP server is signed in')
    case COPILOT_EVENT.McpHeadersRefreshRequired:
      return copilotServerLine(data, 'needs its headers refreshed', 'An MCP server needs its headers refreshed')
    case COPILOT_EVENT.McpHeadersRefreshCompleted:
      return copilotServerLine(data, 'refreshed its headers', 'An MCP server refreshed its headers')
    // The runtime moved the Auto tier, or could not. Either changes which model
    // answers, which is a fact about the turn the reader reads.
    case COPILOT_EVENT.SessionAutoTierRecommendation: {
      const tier = pickString(data, 'autoTier')
      const reason = pickString(data, 'reason')
      const head = tier ? `Auto switched to ${tier}` : 'Auto switched the model'
      return reason ? `${head}: ${reason}` : head
    }
    case COPILOT_EVENT.SessionAutoTierSwitchFailed: {
      const message = pickString(data, 'message') || pickString(data, 'reason')
      return message ? `Auto could not switch the model: ${message}` : 'Auto could not switch the model'
    }
    // A prompt the reader scheduled with /every or /after. It runs later and owns no
    // other surface, so the transcript is where it is recorded.
    case COPILOT_EVENT.SessionScheduleCreated:
      return copilotScheduleLine(data, 'Scheduled a prompt')
    case COPILOT_EVENT.SessionScheduleCancelled:
      return copilotScheduleLine(data, 'Cancelled a scheduled prompt')
    case COPILOT_EVENT.SessionScheduleRearmed:
      return copilotScheduleLine(data, 'Re-armed a scheduled prompt')
    // An extension's own notification. The runtime states nothing about what it
    // means, so the row shows the name and the source that produced it.
    case COPILOT_EVENT.SessionCustomNotification: {
      const name = pickString(data, 'name')
      const source = pickString(data, 'source')
      if (!name)
        return source ? `${source} sent a notification` : null
      return source ? `${source}: ${name}` : name
    }
    default:
      return copilotControlLine(event.type, data)
  }
}

/**
 * The components of the context size after compaction.
 * Each field is optional. When the runtime supplies the breakdown, the sum represents the whole context.
 */
const COPILOT_CONTEXT_TOKEN_PARTS = ['systemTokens', 'conversationTokens', 'toolDefinitionsTokens'] as const

/**
 * The context size one completed compaction left behind.
 *
 * Copilot's interface prefers the breakdown because it includes the system message and tool definitions.
 * postCompactionTokens counts only the conversation. Use it when the frame supplies no breakdown component.
 */
function copilotPostCompactionTokens(data: Record<string, unknown>): number | undefined {
  const parts = COPILOT_CONTEXT_TOKEN_PARTS.map(key => pickNumber(data, key, undefined))
  if (parts.some(part => part !== undefined))
    return parts.reduce<number>((total, part) => total + (part ?? 0), 0)
  return pickNumber(data, 'postCompactionTokens', undefined)
}

/**
 * What one successful `session.compaction_complete` frame states about the context.
 *
 * The row displays the native trigger beside the size change.
 */
function copilotCompactionDetail(data: Record<string, unknown>): CompactionDetails {
  // Omit an absent or invalid count instead of adding a field with undefined.
  const trigger = pickString(data, 'trigger') || undefined
  const pre = toTokenCount(pickNumber(data, 'preCompactionTokens', undefined))
  const post = toTokenCount(copilotPostCompactionTokens(data))
  return {
    ...(trigger !== undefined ? { trigger } : {}),
    ...(pre !== undefined ? { pre } : {}),
    ...(post !== undefined ? { post } : {}),
  }
}

/**
 * Why a compaction did not succeed, in the words the runtime gave.
 *
 * Always a sentence. `blocksForEntry` falls back to the boundary RULE for an empty
 * reason, and that rule claims a rewrite this pass never made.
 */
function copilotCompactionError(data: Record<string, unknown>): string {
  const error = pickString(data, 'error').trim()
  if (error)
    return error
  // The runtime sends `statusCode` for a failed compaction alone, and only where the
  // failure carried an HTTP status.
  const status = pickNumber(data, 'statusCode', undefined)
  return status !== undefined ? `HTTP ${status}` : 'unknown reason'
}

/**
 * The compaction boundary a Copilot message states, or null when it states none.
 *
 * The context-usage grid and the notification extractor both read this result.
 * A failed compaction changes no context, so its counts must not update the grid.
 */
export function copilotCompactionBoundary(parsed: ParsedMessageContent): CompactionDetails | null {
  const data = copilotEventData(getInnerMessage(parsed), COPILOT_EVENT.SessionCompactionComplete)
  return data && data.success === true ? copilotCompactionDetail(data) : null
}

/**
 * Copilot's one notification seam, for a standalone row and for one entry of a
 * consolidated thread alike. Without it a multi-event thread would render only its
 * first message.
 *
 * Native compaction ends use the shared compaction entry.
 * A context clear uses the shared context-clear entry.
 */
export function copilotNotificationEntry(msg: Record<string, unknown>): NotificationEntry[] {
  const event = copilotEvent(msg)
  if (event?.type === COPILOT_EVENT.SessionCompactionComplete) {
    return event.data.success === true
      ? [{ kind: 'compaction', phase: 'end', detail: copilotCompactionDetail(event.data) }]
      : [{ kind: 'compaction', phase: 'end', outcome: 'failed', error: copilotCompactionError(event.data) }]
  }
  if (event?.type === COPILOT_EVENT.SessionContextCleared)
    return [{ kind: 'context-cleared' }]
  const text = describeCopilotNotification(msg)
  return text === null ? [] : [{ kind: 'text', text }]
}

/** Identify an old stored start without changing its ordinary category. */
export function copilotCompactionStart(message: Record<string, unknown>): boolean {
  return copilotEvent(message)?.type === COPILOT_EVENT.SessionCompactionStart
}

import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationInput } from '../registry'
import { COPILOT_EVENT, COPILOT_EVENT_PREFIX } from '~/generated/contracts/copilot-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { isPlainNotificationType } from '~/lib/notificationTypes'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { notificationClassifierFor } from '../../notificationClassification'
import { turnEndLabel } from '../../turnEndLabel'
import { retainedRowIsFinal } from '../registry'
import { copilotNotificationEntry } from './extractors/notification'
import { copilotEvent } from './protocol'

/**
 * The events that thread into chat as notifications.
 *
 * Each one states something the user acts on and owns no other surface: a failure, a
 * warning, a compaction, a subagent outcome, a context reset.
 */
export const COPILOT_NOTIFICATION_TYPES = new Set<string>([
  COPILOT_EVENT.SessionError,
  COPILOT_EVENT.SessionWarning,
  COPILOT_EVENT.SessionInfo,
  COPILOT_EVENT.SessionCompactionStart,
  COPILOT_EVENT.SessionCompactionComplete,
  COPILOT_EVENT.SessionContextCleared,
  COPILOT_EVENT.SessionTruncation,
  COPILOT_EVENT.ModelCallFailure,
  COPILOT_EVENT.SubagentCompleted,
  COPILOT_EVENT.SubagentFailed,
  COPILOT_EVENT.SystemNotification,
  // An MCP server that needs the reader to sign in, or whose headers expired. Each
  // one BLOCKS that server's tools until the reader acts, so it is the clearest case
  // in the whole set: a row that stayed silent would leave a tool failing for a
  // reason nothing on screen states.
  COPILOT_EVENT.McpOauthRequired,
  COPILOT_EVENT.McpOauthCompleted,
  COPILOT_EVENT.McpHeadersRefreshRequired,
  COPILOT_EVENT.McpHeadersRefreshCompleted,
  // The runtime moved the Auto tier, or could not. Either changes which model
  // answers, which is a fact about the turn the reader reads.
  COPILOT_EVENT.SessionAutoTierRecommendation,
  COPILOT_EVENT.SessionAutoTierSwitchFailed,
  // A prompt the reader scheduled with /every or /after. The schedule runs later and
  // owns no other surface, so the transcript is where it is recorded.
  COPILOT_EVENT.SessionScheduleCreated,
  COPILOT_EVENT.SessionScheduleCancelled,
  COPILOT_EVENT.SessionScheduleRearmed,
  // An extension's own notification. The runtime states nothing about what it means,
  // so the row shows the name and the source that produced it.
  COPILOT_EVENT.SessionCustomNotification,
])

/**
 * Events that carry no surface of their own.
 *
 * Each is hidden for a stated reason, and the reasons differ:
 *
 *   - The session lifecycle events repeat what the create and resume responses
 *     already returned.
 *   - A streaming delta is transient, and the worker never stores one; a historical
 *     row from an older build would repeat the finished message.
 *   - A turn boundary is bookkeeping: `session.idle` is the divider.
 *   - A mode, permission or model change reaches the settings panel, not the chat.
 *   - `subagent.started` repeats the `task` tool call that opened it, which is the
 *     row that already renders the launch, and `subagent.configured` states the model
 *     and effort that launch runs under -- a model change, which reaches the settings
 *     panel and not the chat, exactly as `session.model_change` does.
 *   - A control COMPLETION announces an answer the control surface recorded.
 *     `permission.completed` is the exception and is NOT listed: five of its nine
 *     outcomes are that answer, and the other four are refusals the runtime made on
 *     its own, which no row states. Its describer hides the first five and draws the
 *     rest, so the hiding is per OUTCOME rather than per type.
 *   - A title change would fight LeapMux's own naming, and a to-do or plan change
 *     states only THAT the list moved: the `update_todo` tool call carries the list.
 */
const COPILOT_HIDDEN_TYPES = new Set<string>([
  COPILOT_EVENT.SessionStart,
  COPILOT_EVENT.SessionResume,
  COPILOT_EVENT.SessionShutdown,
  COPILOT_EVENT.SessionTitleChanged,
  COPILOT_EVENT.SessionTodosChanged,
  COPILOT_EVENT.SessionPlanChanged,
  COPILOT_EVENT.SessionUsageInfo,
  COPILOT_EVENT.SessionLimitsChanged,
  COPILOT_EVENT.SessionModeChanged,
  COPILOT_EVENT.SessionPermissionsChanged,
  COPILOT_EVENT.SessionModelChange,
  COPILOT_EVENT.SessionAutopilotObjectiveChanged,
  COPILOT_EVENT.AssistantTurnStart,
  COPILOT_EVENT.AssistantTurnEnd,
  COPILOT_EVENT.AssistantUsage,
  COPILOT_EVENT.AssistantMessageDelta,
  COPILOT_EVENT.AssistantStreamingDelta,
  COPILOT_EVENT.AssistantToolCallDelta,
  COPILOT_EVENT.AssistantReasoningDelta,
  COPILOT_EVENT.ToolProgress,
  COPILOT_EVENT.ToolPartialResult,
  COPILOT_EVENT.SubagentStarted,
  COPILOT_EVENT.SubagentConfigured,
  // The runtime calls its own `task_complete` TOOL and then announces the same thing
  // here. Both halves of that call are rows already, and the result row reads
  // `Task completed:` with the summary this event repeats.
  COPILOT_EVENT.SessionTaskComplete,
  COPILOT_EVENT.Abort,
  COPILOT_EVENT.UserInputCompleted,
  COPILOT_EVENT.ExitPlanModeCompleted,
  COPILOT_EVENT.ElicitationCompleted,
  // A user message the runtime echoes back. LeapMux persists the user's own row when
  // it delivers the input, so this one would double it.
  COPILOT_EVENT.UserMessage,

  // --- Everything below reaches a row today and drew raw JSON ----------------
  // The worker persists every event the runtime does not mark ephemeral, and the
  // browser drew a raw-JSON bubble for each type it could not identify. The runtime
  // declares 131 of them and LeapMux spelled 52, so an ordinary Copilot turn wrote
  // several of these.

  // Lifecycle and streaming. Each states a transition that another row already
  // draws: the message itself, the tool row, the turn-end divider.
  COPILOT_EVENT.AssistantMessageStart,
  COPILOT_EVENT.AssistantIdle,
  COPILOT_EVENT.AssistantIntent,
  COPILOT_EVENT.AssistantServerToolProgress,
  COPILOT_EVENT.McpAppToolCallComplete,

  // Registry and configuration. Each states that a LIST moved -- the tools, the
  // skills, the extensions, the custom agents, the MCP servers, the slash commands.
  // The settings panel and the tool rows read those lists; a transcript row would
  // say only that something changed, which a reader cannot act on.
  COPILOT_EVENT.CapabilitiesChanged,
  COPILOT_EVENT.CommandsChanged,
  COPILOT_EVENT.SessionToolsUpdated,
  COPILOT_EVENT.SessionSkillsLoaded,
  COPILOT_EVENT.SessionExtensionsLoaded,
  COPILOT_EVENT.SessionCustomAgentsUpdated,
  COPILOT_EVENT.SessionExtensionsAttachmentsPushed,
  COPILOT_EVENT.SessionMcpServersLoaded,
  COPILOT_EVENT.SessionMcpServerStatusChanged,
  COPILOT_EVENT.SessionMcpServerRemoved,
  COPILOT_EVENT.SessionMcpServerNeedsReconnect,
  COPILOT_EVENT.McpToolsListChanged,
  COPILOT_EVENT.McpResourcesListChanged,
  COPILOT_EVENT.McpPromptsListChanged,
  COPILOT_EVENT.SessionBackgroundTasksChanged,
  COPILOT_EVENT.PendingMessagesModified,
  COPILOT_EVENT.SubagentSelected,
  COPILOT_EVENT.SubagentDeselected,
  COPILOT_EVENT.ToolSearchActivated,

  // Accounting and policy the meter or the settings panel reads, not the chat.
  COPILOT_EVENT.SessionUsageCheckpoint,
  COPILOT_EVENT.SessionContextChanged,
  COPILOT_EVENT.SessionCompletionReceipt,
  COPILOT_EVENT.SessionManagedSettingsResolved,
  COPILOT_EVENT.SessionManagedSettingsEnforced,
  COPILOT_EVENT.SessionAutoModeResolved,
  COPILOT_EVENT.SessionRemoteSteerableChanged,
  COPILOT_EVENT.SessionModeNoticeDelivered,

  // Session bookkeeping: a file the runtime wrote for itself, a rewind it recorded,
  // a handoff between clients, an asset it stored. None is conversation.
  COPILOT_EVENT.SessionBinaryAsset,
  COPILOT_EVENT.SessionSnapshotRewind,
  COPILOT_EVENT.SessionWorkspaceFileChanged,
  COPILOT_EVENT.SessionHandoff,
  COPILOT_EVENT.UiEphemeralQuery,

  // Control COMPLETIONS, for the same reason the four above them are hidden: each
  // announces an answer that the control surface already recorded.
  COPILOT_EVENT.SamplingCompleted,
  COPILOT_EVENT.ExternalToolCompleted,
  COPILOT_EVENT.CommandQueued,
  COPILOT_EVENT.CommandCompleted,
  COPILOT_EVENT.AutoModeSwitchCompleted,
  COPILOT_EVENT.SessionLimitsExhaustedCompleted,
])

/**
 * The event families where every member takes the same answer, so the contract holds
 * the PREFIX rather than one constant per member.
 *
 * Two of them are the runtime's own trace. The other four are the experiments the
 * model plan holds out of scope -- a canvas, a Fusion route, a factory run. LeapMux
 * renders none of them, and each drew a raw-JSON bubble until this rule identified it.
 *
 * The WHOLE contract table, not a list of its members: `COPILOT_EVENT_PREFIX` exists
 * for exactly this rule, so a hand-written copy beside it was a second place to add
 * the next prefix -- and the copy that forgot one let that family through silently.
 */
const COPILOT_RUNTIME_PREFIXES: readonly string[] = Object.values(COPILOT_EVENT_PREFIX)

/**
 * Reports whether an event type belongs to a family that describes the RUNTIME.
 *
 * The `model.` family is the runtime's own model-call trace, and the `hook.` family
 * states that the runtime ran one of its own hooks. The worker drops both, so a live
 * turn never stores one. A transcript an earlier build wrote still holds them -- ten
 * for one ordinary turn -- and each reached the reader as a raw-JSON bubble.
 *
 * `model.call_failure` is the exception, because LeapMux surfaces that one as a
 * notification.
 */
function copilotDescribesRuntime(type: string): boolean {
  if (type === COPILOT_EVENT.ModelCallFailure)
    return false
  return COPILOT_RUNTIME_PREFIXES.some(prefix => type.startsWith(prefix))
}

/**
 * The control REQUESTS. Each one reaches the control surface, which is where it is
 * answered, so the transcript copy states the ask without an action on it.
 */
const COPILOT_CONTROL_REQUEST_TYPES = new Set<string>([
  COPILOT_EVENT.PermissionRequested,
  COPILOT_EVENT.UserInputRequested,
  COPILOT_EVENT.ExitPlanModeRequested,
  COPILOT_EVENT.ElicitationRequested,
  // The runtime asks for five more answers on the same `<name>.requested` pattern,
  // each with a `requestId` it waits on. LeapMux publishes no control request for
  // them yet -- that is the control-request work phase C holds -- so the row states
  // the ASK and the turn stalls until the runtime gives up. A raw-JSON bubble stated
  // the same stall and named nothing.
  COPILOT_EVENT.SamplingRequested,
  COPILOT_EVENT.ExternalToolRequested,
  COPILOT_EVENT.CommandExecute,
  COPILOT_EVENT.AutoModeSwitchRequested,
  COPILOT_EVENT.SessionLimitsExhaustedRequested,
  COPILOT_EVENT.ToolUserRequested,
])

/** True for a Copilot event that belongs in a notification thread. */
function copilotNotifies(entry: unknown): boolean {
  const event = copilotEvent(entry)
  return !!event && (COPILOT_NOTIFICATION_TYPES.has(event.type) || COPILOT_CONTROL_REQUEST_TYPES.has(event.type)
    || event.type === COPILOT_EVENT.SkillInvoked || event.type === COPILOT_EVENT.SystemMessage
    // A permission the runtime refused on its own reaches no control surface, so this
    // event is the only place it is ever stated. Its describer reads null for the
    // outcomes a reader DID answer, which the caller hides.
    || event.type === COPILOT_EVENT.PermissionCompleted
    // Nobody was asked, so no control surface and no response row records it. The
    // transcript is the only place a reader learns that one approval admitted a
    // second call.
    || event.type === COPILOT_EVENT.PermissionCarriedForward)
}

export function classifyCopilotMessage(input: ClassificationInput): MessageCategory {
  const parent = input.parentObject
  const wrapper = input.wrapper
  const notification = notificationClassifierFor(input.agentProvider, copilotNotificationEntry)

  if (wrapper) {
    if (wrapper.messages.length === 0)
      return { kind: 'hidden' }
    // A Copilot row is a JSON-RPC frame with no top-level `type`, so the shared
    // wrapper test cannot recognize one. Read the event instead, and drop the
    // entries that state nothing so a thread of only those collapses to hidden.
    if (wrapper.messages.some(entry => copilotNotifies(entry))) {
      return notification(wrapper.messages, 'hidden')
    }
    if (isNotificationThreadWrapper(wrapper))
      return notification(wrapper.messages)
  }

  if (!parent)
    return { kind: 'unknown' }

  // A row the service layer wrote is the LeapMux-neutral `{content}` shape and carries
  // no native frame. It is matched before the event dispatch so a user echo does not
  // reach the unknown fallback and get stringified into the bubble.
  const event = copilotEvent(parent)
  if (!event) {
    if (typeof parent.content === 'string') {
      if (parent.hidden === true)
        return { kind: 'hidden' }
      if (parent.planExecution === true)
        return { kind: 'plan_execution' }
      return { kind: 'user_content' }
    }
    if (isPlainNotificationType(pickString(parent, 'type')))
      return notification([parent])
    return { kind: 'unknown' }
  }

  switch (event.type) {
    case COPILOT_EVENT.AssistantMessage:
      return pickString(event.data, 'content').trim() ? { kind: 'assistant_text' } : { kind: 'hidden' }
    case COPILOT_EVENT.AssistantReasoning:
      return pickString(event.data, 'content').trim() ? { kind: 'assistant_thinking' } : { kind: 'hidden' }
    case COPILOT_EVENT.ToolStarted:
      // A retained copy of this frame is the call's RESULT: the turn ended while the
      // call ran, so the runtime sent no completion and the worker stored the start
      // frame again. See copilotSpanRole.
      if (retainedRowIsFinal(input.completion))
        return { kind: 'tool_result' }
      return { kind: 'tool_use' }
    case COPILOT_EVENT.ToolCompleted:
      return { kind: 'tool_result' }
    case COPILOT_EVENT.SessionIdle:
      return { kind: 'result_divider' }
  }
  if (copilotNotifies(parent)) {
    return notification([parent], 'hidden')
  }
  if (COPILOT_HIDDEN_TYPES.has(event.type) || copilotDescribesRuntime(event.type))
    return { kind: 'hidden' }
  return { kind: 'unknown' }
}

/**
 * The turn-end label for one `session.idle` row.
 *
 * The runtime reports no duration for a turn, so the label carries none.
 */
export function copilotResultDivider(parsed: unknown): { label: string, isError?: boolean } | null {
  if (!isObject(parsed))
    return null
  const data = copilotEvent(parsed)?.data
  if (!data)
    return null
  return { label: turnEndLabel(data.aborted === true ? 'interrupted' : 'ended') }
}

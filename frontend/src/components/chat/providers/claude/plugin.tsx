import type { Question } from '../../controls/types'
import type { MessageCategory } from '../../messageClassification'
import type { ClassificationContext, ClassificationInput, Provider, SpanRole } from '../registry'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ContextUsageInfo, RateLimitInfo } from '~/stores/agentSession.store'
import { CLAUDE_DEFAULT_MODE, CLAUDE_MODE } from '~/generated/contracts/claude-protocol'
import { NOTIFICATION_TYPE } from '~/generated/contracts/worker-vocab'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { getMessageContent, joinContentParagraphs } from '~/lib/contentBlocks'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { getInnerMessage, messageUsage } from '~/lib/messageParser'
import { truncatePreview } from '~/lib/textTruncate'
import { CLAUDE_TOOL } from '~/types/toolMessages'
import { buildAllowResponse, buildDenyResponse, getToolInput, getToolName } from '~/utils/controlResponse'
import { buildAskAnswers } from '../../controls/AskUserQuestionControl'
import { sendResponse } from '../../controls/types'
import { defaultMarkPreview } from '../../markPreviewShared'
import { isFinalCompactingStatus, isNotificationThreadWrapper } from '../../messageUtils'
import { controlBehaviorDisplay } from '../../persistedControlResponse'
import { buildPlanMode } from '../../settingsGroups'
import { registerProvider } from '../registry'
import { ClaudeCodeControlActions, ClaudeCodeControlContent } from './ClaudeCodeControlRequest'
import { getAssistantContent, joinToolResultText } from './extractors/assistantContent'
import { claudeToolResultImages } from './extractors/image'
import { claudeNotificationThreadEntry } from './notifications'
import { renderClaudeMessage } from './renderMessage'
import { claudeResultDivider } from './resultDivider'
import { claudeToolResultMeta } from './toolResult'

/** Extra notification types for Claude Code (plan_execution, system subtypes). */
const CLAUDE_EXTRA_TYPES = new Set([NOTIFICATION_TYPE.PlanExecution])
/**
 * System message subtypes that should never surface in the UI.
 * task_notification / task_updated are legacy guards for pre-migration
 * transcripts: new sessions never persist these rows (the backend's
 * --forward-subagent-text handling consumes them to drive the background-task
 * registry). Kept so an old transcript still renders cleanly.
 */
const HIDDEN_SYSTEM_SUBTYPES = new Set(['init', 'task_notification', 'task_updated', 'session_state_changed'])
/**
 * Tool span types whose `tool_use` row is suppressed. `ToolSearch` is a
 * deferred-tool discovery probe whose chat-side surface is uninteresting;
 * `TaskList` is a pure read-back of state that the persistent todo
 * sidebar already shows, so both sides of the call are hidden.
 */
const HIDDEN_TOOL_USE_SPAN_TYPES = new Set<string>([
  CLAUDE_TOOL.TOOL_SEARCH,
  CLAUDE_TOOL.TASK_LIST,
])
/**
 * Tool span types whose `tool_result` row is suppressed because the
 *  tool_use side already renders the full information (TodoWrite), the
 *  result is the data source for the tool_use side (TaskCreate /
 *  TaskUpdate / TaskGet), or the entire call is hidden (ToolSearch,
 *  TaskList — see {@link HIDDEN_TOOL_USE_SPAN_TYPES}).
 */
const HIDDEN_TOOL_RESULT_SPAN_TYPES = new Set<string>([
  CLAUDE_TOOL.TODO_WRITE,
  CLAUDE_TOOL.TOOL_SEARCH,
  CLAUDE_TOOL.TASK_CREATE,
  CLAUDE_TOOL.TASK_UPDATE,
  CLAUDE_TOOL.TASK_GET,
  CLAUDE_TOOL.TASK_LIST,
])
function isClaudeNotifThread(wrapper: { messages: unknown[] } | null): wrapper is { messages: unknown[] } {
  return isNotificationThreadWrapper(wrapper, CLAUDE_EXTRA_TYPES, (t, st) =>
    t === 'system' && !HIDDEN_SYSTEM_SUBTYPES.has(st ?? ''))
}

/**
 * Per-message hidden rules shared by the standalone `system`/`rate_limit_event`
 * classifiers and the consolidated-thread filter, so a notification that is
 * hidden on its own stays hidden when Hub threads it into a
 * `notification_thread` wrapper. Without this single source of truth the two
 * paths drift: the wrapper branch used to drop only allowed `rate_limit_event`s,
 * so a terminal compaction status leaked through as a `notification` and
 * rendered as raw JSON.
 *
 * Covers the type/subtype-driven rules only. The `task_started`/`task_progress`
 * rule needs the envelope's `parentSpanId` (absent from a consolidated inner
 * message), so it stays inline in the `system` classifier.
 *
 * - `rate_limit_event` whose `rate_limit_info.status` is "allowed" -- a no-op
 *   refresh, not a throttle the user needs to see.
 * - `system` whose subtype is in {@link HIDDEN_SYSTEM_SUBTYPES}.
 * - `system` `status` updates other than the live "compacting" one -- e.g. the
 *   terminal `{status:null, compact_result:"success"}` ending a compaction. The
 *   user-facing "Context compacted (...)" line comes from compact_boundary, so
 *   this terminal status carries nothing to show.
 */
function isHiddenClaudeNotification(m: Record<string, unknown>): boolean {
  const type = m.type as string | undefined
  if (type === 'rate_limit_event') {
    const info = pickObject(m, 'rate_limit_info')
    return info?.status === 'allowed'
  }
  if (type === 'system') {
    const subtype = m.subtype as string | undefined
    if (HIDDEN_SYSTEM_SUBTYPES.has(subtype ?? ''))
      return true
    if (isFinalCompactingStatus(m))
      return true
  }
  return false
}

type ClaudeTypeClassifier = (
  parent: Record<string, unknown>,
  input: ClassificationInput,
  context?: ClassificationContext,
) => MessageCategory

/**
 * Classifiers for type-keyed Claude messages whose result preempts
 * `isCompactSummary` and synthetic-control-response checks. These are
 * notification-shaped: their type alone determines the category.
 */
const CLAUDE_NOTIFICATION_CLASSIFIERS: Record<string, ClaudeTypeClassifier> = {
  system(parent, input) {
    const subtype = parent.subtype as string | undefined
    if (input.parentSpanId && (subtype === 'task_started' || subtype === 'task_progress'))
      return { kind: 'hidden' }
    if (isHiddenClaudeNotification(parent))
      return { kind: 'hidden' }
    return { kind: 'notification', messages: [parent] }
  },
  rate_limit_event(parent) {
    if (isHiddenClaudeNotification(parent))
      return { kind: 'hidden' }
    return { kind: 'notification', messages: [parent] }
  },
  [NOTIFICATION_TYPE.Interrupted]: parent => ({ kind: 'notification', messages: [parent] }),
  [NOTIFICATION_TYPE.ContextCleared]: parent => ({ kind: 'notification', messages: [parent] }),
  [NOTIFICATION_TYPE.SettingsChanged]: parent => ({ kind: 'notification', messages: [parent] }),
  [NOTIFICATION_TYPE.PlanUpdated]: parent => ({ kind: 'notification', messages: [parent] }),
  result: () => ({ kind: 'result_divider' }),
}

/**
 * Classifiers for content-shaped Claude messages (`assistant`/`user`). These
 * run AFTER the `isCompactSummary` / synthetic-control-response guards so
 * that those flags can preempt the content dispatch.
 */
const CLAUDE_CONTENT_CLASSIFIERS: Record<string, ClaudeTypeClassifier> = {
  assistant(parent, input) {
    const message = pickObject(parent, 'message')
    if (!message)
      return { kind: 'unknown' }
    const content = message.content
    if (!Array.isArray(content))
      return { kind: 'unknown' }
    const contentArr = content as Array<Record<string, unknown>>
    const toolUse = contentArr.find(c => isObject(c) && c.type === 'tool_use') as Record<string, unknown> | undefined
    if (toolUse) {
      if (input.spanType && HIDDEN_TOOL_USE_SPAN_TYPES.has(input.spanType))
        return { kind: 'hidden' }
      return {
        kind: 'tool_use',
        toolName: pickString(toolUse, 'name'),
        toolUse,
        content: contentArr,
      }
    }
    if (contentArr.some(c => isObject(c) && c.type === 'text'))
      return { kind: 'assistant_text' }
    if (contentArr.some(c => isObject(c) && c.type === 'thinking')) {
      // Signature-only thinking blocks (no visible text) can slip past
      // --thinking-display summarized; hide them so the UI doesn't render
      // an empty row.
      const hasText = contentArr.some(c =>
        isObject(c) && c.type === 'thinking'
        && typeof c.thinking === 'string' && c.thinking.length > 0)
      return hasText ? { kind: 'assistant_thinking' } : { kind: 'hidden' }
    }
    return { kind: 'unknown' }
  },
  user(parent, input, context) {
    if (input.spanType === CLAUDE_TOOL.ENTER_PLAN_MODE || parent.span_type === CLAUDE_TOOL.ENTER_PLAN_MODE)
      return { kind: 'hidden' }

    const message = pickObject(parent, 'message')
    if (message) {
      const content = message.content
      if (typeof content === 'string')
        return { kind: 'user_text' }
      if (Array.isArray(content)) {
        // tool_result takes priority over agent_prompt (subagent tool results
        // also have parent_tool_use_id but should be rendered as tool results).
        if ((content as Array<Record<string, unknown>>).some(c => isObject(c) && c.type === 'tool_result')) {
          if (input.spanType && HIDDEN_TOOL_RESULT_SPAN_TYPES.has(input.spanType))
            return { kind: 'hidden' }
          return { kind: 'tool_result' }
        }
      }
    }
    // A user message carrying parent_tool_use_id is the prompt sent TO a
    // subagent -- but only in the transcript that spawned it. Inside the
    // subagent's OWN transcript every forwarded message carries that same id,
    // including its interrupt notices and local command output, and none of
    // those is a prompt. The child's real prompt is persisted separately, as a
    // plain user message, so nothing here is ever one.
    if (typeof parent.parent_tool_use_id === 'string')
      return context?.isChildTranscript ? { kind: 'user_text' } : { kind: 'agent_prompt' }
    return { kind: 'unknown' }
  },
}

/** Claude Code message classification. */
function classifyClaudeCodeMessage(
  input: ClassificationInput,
  context?: ClassificationContext,
): MessageCategory {
  const parentObject = input.parentObject
  const wrapper = input.wrapper

  // Empty wrapper (all notifications consolidated to no-ops) — hide.
  if (wrapper && wrapper.messages.length === 0)
    return { kind: 'hidden' }

  // Notification thread (wrapper with notification-type first message). Drop the
  // per-message hidden shapes (the same ones the standalone classifiers hide) so
  // a thread of only-hidden entries collapses to `hidden` rather than surfacing
  // an empty notification or a raw-JSON fallback.
  if (isClaudeNotifThread(wrapper)) {
    const msgs = wrapper.messages.filter(m => !isObject(m) || !isHiddenClaudeNotification(m))
    if (msgs.length === 0)
      return { kind: 'hidden' }
    return { kind: 'notification', messages: msgs }
  }

  if (!parentObject)
    return { kind: 'unknown' }

  const type = parentObject.type as string | undefined

  // Notification-shaped types preempt compact-summary / control-response.
  if (type) {
    const notif = CLAUDE_NOTIFICATION_CLASSIFIERS[type]
    if (notif)
      return notif(parentObject, input)
  }

  // Compact summary preempts content-shaped types.
  if (parentObject.isCompactSummary === true)
    return { kind: 'compact_summary' }

  // (The synthetic {isSynthetic, controlResponse} row -> control_response is classified upstream in
  // classifyMessage, before any plugin.classify runs, since it is a LeapMux-neutral shape.)

  // Content-shaped types (assistant / user).
  if (type) {
    const content = CLAUDE_CONTENT_CLASSIFIERS[type]
    if (content)
      return content(parentObject, input, context)
  }

  // Plain object with string .content and no .type → user_content (or hidden /
  // plan_execution variants).
  if (!type && typeof parentObject.content === 'string') {
    if (parentObject.hidden === true)
      return { kind: 'hidden' }
    if (parentObject.planExecution === true)
      return { kind: 'plan_execution' }
    return { kind: 'user_content' }
  }

  return { kind: 'unknown' }
}

function claudeExtractQuotableText(category: MessageCategory, parsed: ParsedMessageContent): string | null {
  const obj = parsed.parentObject
  if (!obj)
    return null
  if (category.kind === 'assistant_text' || category.kind === 'assistant_thinking') {
    const text = joinContentParagraphs(getAssistantContent(obj), {
      text: 'text',
      thinking: 'thinking',
    }).trim()
    return text || null
  }
  if (category.kind === 'user_text') {
    const msg = pickObject(obj, 'message')
    if (typeof msg?.content === 'string')
      return msg.content.trim() || null
    return null
  }
  if (category.kind === 'user_content' || category.kind === 'plan_execution') {
    if (typeof obj.content === 'string')
      return (obj.content as string).trim() || null
  }
  return null
}

/**
 * Scroll-rail preview for a Claude marked message. Two Claude-specific (Anthropic) shapes
 * only this plugin knows how to read are handled here before the shared fallback: a
 * self-displaying control response (AskUserQuestion / ExitPlanMode answer) re-emitted as a
 * `message.content[]` tool_result, and a transcript user row nesting its text under
 * `{message:{content:"..."}}`. The LeapMux-neutral `{content}` user send falls back to the shared
 * defaultMarkPreview. A persisted `{controlResponse}` row never reaches here -- it classifies as
 * `control_response` and the rail resolves its preview through the plugin's controlResponseDisplay
 * (chatMarkPreview.ts), before previewText runs.
 */
function claudeMarkPreview(category: MessageCategory, parsed: ParsedMessageContent): string | null {
  const toolResultText = joinToolResultText(parsed.parentObject)
  if (toolResultText)
    return truncatePreview(toolResultText)
  // Claude transcript user row: `{message:{content:"..."}}` (string content). The
  // message-array (assistant / tool_result) form is picked up by joinToolResultText above,
  // so a string here is genuine user text, never a mis-picked block array.
  const message = pickObject(parsed.parentObject, 'message')
  const nestedContent = pickString(message, 'content', '')
  if (nestedContent)
    return truncatePreview(nestedContent)
  return defaultMarkPreview(category, parsed)
}

// Claude reserves ~16.5% of the context window as an autocompact buffer, so the
// context-usage percentage is measured against the remaining usable capacity.
const CLAUDE_AUTOCOMPACT_BUFFER_PCT = 16.5

/**
 * Claude/Anthropic span role: a `tool_use` content block marks an opener, a `tool_result` block a
 * result. Scan every block before deciding and let the `tool_use` opener win -- a message holding
 * BOTH blocks IS the opener (it carries the tool input to render); early-returning on the first
 * tool_result would mis-bucket it as a result and drop its input.
 */
function claudeSpanRole(parsed: ParsedMessageContent): SpanRole {
  const blocks = getMessageContent(parsed.parentObject ?? undefined)
  if (!blocks)
    return 'other'
  let hasToolUse = false
  let hasToolResult = false
  for (const b of blocks) {
    if (!isObject(b))
      continue
    if (b.type === 'tool_use')
      hasToolUse = true
    else if (b.type === 'tool_result')
      hasToolResult = true
  }
  return hasToolUse ? 'opener' : hasToolResult ? 'result' : 'other'
}

/** Claude raw rate_limit_event: {type:"rate_limit_event", rate_limit_info:{...}}. */
function claudeRateLimitsFromMessage(parsed: ParsedMessageContent): { key: string, info: RateLimitInfo }[] | null {
  const inner = getInnerMessage(parsed)
  if (!inner || inner.type !== 'rate_limit_event')
    return null
  const rlInfo = inner.rate_limit_info as Record<string, unknown> | undefined
  if (!rlInfo || typeof rlInfo !== 'object')
    return []
  const key = (rlInfo.rateLimitType as string) || 'unknown'
  return [{ key, info: rlInfo as RateLimitInfo }]
}

/** Claude Code assistant `message.usage` shape: input_tokens + cache_creation/read_input_tokens. */
function claudeContextUsageFromMessage(parsed: ParsedMessageContent): ContextUsageInfo | null {
  const usage = messageUsage(parsed)
  if (!usage || typeof usage.input_tokens !== 'number')
    return null
  return {
    inputTokens: usage.input_tokens,
    cacheCreationInputTokens: pickNumber(usage, 'cache_creation_input_tokens', 0),
    cacheReadInputTokens: pickNumber(usage, 'cache_read_input_tokens', 0),
  }
}

const claudeCodePlugin: Provider = {
  permissionPresets: {
    smart: { sets: { permissionMode: CLAUDE_MODE.Auto } },
    bypass: { sets: { permissionMode: CLAUDE_MODE.BypassPermissions } },
  },
  contextBufferPct: CLAUDE_AUTOCOMPACT_BUFFER_PCT,
  attachments: {
    text: true,
    image: true,
    pdf: true,
    binary: false,
  },
  planMode: buildPlanMode('permissionMode', CLAUDE_MODE.Plan, CLAUDE_DEFAULT_MODE),
  // The trigger's mode segment shows the permission mode (which is also Claude's
  // plan axis, so it naturally reads "Plan Mode" when in plan).
  triggerModeGroupKey: 'permissionMode',

  classify: classifyClaudeCodeMessage,
  spanRole: claudeSpanRole,
  rateLimitsFromMessage: claudeRateLimitsFromMessage,
  contextUsageFromMessage: claudeContextUsageFromMessage,
  // Claude's thinking-token counter is driven by real per-phase telemetry (the
  // worker relays Claude's own estimated_tokens), not the streamed-text estimator
  // the other providers use. Every committed AGENT message ends a phase, so always
  // clear -- and unlike the estimator providers we cannot gate on parentSpanId,
  // since a system-injected tool_use_id gives a main-agent message a non-empty
  // parentSpanId that does not mark a subagent.
  clearsThinkingTokensForMessage: () => true,
  renderMessage: renderClaudeMessage,
  toolResultMeta: claudeToolResultMeta,
  toolResultImages: claudeToolResultImages,
  extractQuotableText: claudeExtractQuotableText,
  previewText: claudeMarkPreview,
  // Claude's native control response IS the neutral behavior envelope, so its derivation is the
  // shared reader: allow -> "Approved", deny+message -> feedback, bare deny -> "Rejected".
  controlResponseDisplay: cr => controlBehaviorDisplay(cr.response),
  notificationThreadEntry: claudeNotificationThreadEntry,
  resultDivider: claudeResultDivider,

  askUserQuestion: {
    isRequest(payload) {
      const tool = getToolName(payload)
      return tool === CLAUDE_TOOL.ASK_USER_QUESTION || tool === 'request_user_input'
    },
    extractQuestions(payload) {
      const input = getToolInput(payload) as { questions?: unknown }
      return Array.isArray(input.questions) ? input.questions as Question[] : []
    },
    sendAnswer: (request, sendControlResponse, questions, answerState) =>
      sendResponse(sendControlResponse, buildAskAnswers(answerState, questions, getToolInput(request.payload), request.requestId)),
    sendReject: (request, sendControlResponse, message) =>
      sendResponse(sendControlResponse, buildDenyResponse(request.requestId, message)),
  },

  buildControlResponse(payload, content, requestId) {
    // ExitPlanMode never goes through the editor for "approve" — that path
    // lives in the dedicated approval button. Editor input here always means
    // "reject the plan with feedback", and Send-with-no-content also rejects.
    if (getToolName(payload) === CLAUDE_TOOL.EXIT_PLAN_MODE)
      return buildDenyResponse(requestId, content)
    return content
      ? buildDenyResponse(requestId, content)
      : buildAllowResponse(requestId, getToolInput(payload))
  },

  ControlContent: ClaudeCodeControlContent,
  ControlActions: ClaudeCodeControlActions,
}

registerProvider(AgentProvider.CLAUDE_CODE, claudeCodePlugin)

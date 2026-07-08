import type { Question } from '../../controls/types'
import type { MessageCategory } from '../../messageClassification'
import type { ClassificationContext, ClassificationInput, Provider } from '../registry'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { joinContentParagraphs } from '~/lib/contentBlocks'
import { randomUUID } from '~/lib/idGenerator'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { truncatePreview } from '~/lib/textTruncate'
import { CLAUDE_TOOL } from '~/types/toolMessages'
import { buildAllowResponse, buildDenyResponse, getToolInput, getToolName } from '~/utils/controlResponse'
import { buildAskAnswers } from '../../controls/AskUserQuestionControl'
import { ClaudeCodeControlActions, ClaudeCodeControlContent } from '../../controls/ClaudeCodeControlRequest'
import { defaultMarkPreview } from '../../markPreviewShared'
import { isNotificationThreadWrapper, isTerminalCompactingStatus } from '../../messageUtils'
import { buildPlanMode } from '../../settingsGroups'
import { registerProvider } from '../registry'
import { getAssistantContent, joinToolResultText } from './extractors/assistantContent'
import { claudeNotificationThreadEntry } from './notifications'
import { renderClaudeMessage } from './renderMessage'
import { claudeResultDivider } from './resultDivider'
import { claudeToolResultMeta } from './toolResult'

function buildInterruptRequest(): string {
  return JSON.stringify({
    type: 'control_request',
    request_id: randomUUID(),
    request: { subtype: 'interrupt' },
  })
}

/** Extra notification types for Claude Code (plan_execution, system subtypes). */
const CLAUDE_EXTRA_TYPES = new Set(['plan_execution'])
/** System message subtypes that should never surface in the UI. */
const HIDDEN_SYSTEM_SUBTYPES = new Set(['init', 'task_notification', 'task_updated'])
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
    if (isTerminalCompactingStatus(m))
      return true
  }
  return false
}

type ClaudeTypeClassifier = (
  parent: Record<string, unknown>,
  input: ClassificationInput,
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
  interrupted: parent => ({ kind: 'notification', messages: [parent] }),
  context_cleared: parent => ({ kind: 'notification', messages: [parent] }),
  settings_changed: parent => ({ kind: 'notification', messages: [parent] }),
  plan_updated: parent => ({ kind: 'notification', messages: [parent] }),
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
  user(parent, input) {
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
    // Agent prompt: user message with parent_tool_use_id (prompt sent to sub-agent)
    if (typeof parent.parent_tool_use_id === 'string')
      return { kind: 'agent_prompt' }
    return { kind: 'unknown' }
  },
}

/** Claude Code message classification. */
function classifyClaudeCodeMessage(
  input: ClassificationInput,
  _context?: ClassificationContext,
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

  // Synthetic control response (also preempts content-shaped types).
  if (parentObject.isSynthetic === true && isObject(parentObject.controlResponse))
    return { kind: 'control_response' }

  // Content-shaped types (assistant / user).
  if (type) {
    const content = CLAUDE_CONTENT_CLASSIFIERS[type]
    if (content)
      return content(parentObject, input)
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
 * `{message:{content:"..."}}`. Every other marked shape (`{content}`, `{controlResponse}`)
 * is Leapmux-neutral and handled by the shared defaultMarkPreview.
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

const claudeCodePlugin: Provider = {
  bypassPermissionMode: 'bypassPermissions',
  contextBufferPct: CLAUDE_AUTOCOMPACT_BUFFER_PCT,
  attachments: {
    text: true,
    image: true,
    pdf: true,
    binary: false,
  },
  planMode: buildPlanMode('permissionMode', 'plan', 'default'),
  // The trigger's mode segment shows the permission mode (which is also Claude's
  // plan axis, so it naturally reads "Plan Mode" when in plan).
  triggerModeGroupKey: 'permissionMode',

  classify: classifyClaudeCodeMessage,
  // Claude's thinking-token counter is driven by real per-phase telemetry (the
  // worker relays Claude's own estimated_tokens), not the streamed-text estimator
  // the other providers use. Every committed AGENT message ends a phase, so always
  // clear -- and unlike the estimator providers we cannot gate on parentSpanId,
  // since a system-injected tool_use_id gives a main-agent message a non-empty
  // parentSpanId that does not mark a subagent.
  clearsThinkingTokensForMessage: () => true,
  renderMessage: renderClaudeMessage,
  toolResultMeta: claudeToolResultMeta,
  extractQuotableText: claudeExtractQuotableText,
  previewText: claudeMarkPreview,
  notificationThreadEntry: claudeNotificationThreadEntry,
  resultDivider: claudeResultDivider,

  isAskUserQuestion(payload) {
    const tool = getToolName(payload)
    return tool === CLAUDE_TOOL.ASK_USER_QUESTION || tool === 'request_user_input'
  },

  extractAskUserQuestions(payload) {
    const input = getToolInput(payload) as { questions?: unknown }
    return Array.isArray(input.questions) ? input.questions as Question[] : []
  },

  async sendAskUserQuestionResponse(agentId, sendControlResponse, requestId, questions, askState, payload) {
    const response = buildAskAnswers(askState, questions, getToolInput(payload), requestId)
    await sendControlResponse(agentId, new TextEncoder().encode(JSON.stringify(response)))
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

  buildInterruptContent(): string | null {
    return buildInterruptRequest()
  },

  ControlContent: ClaudeCodeControlContent,
  ControlActions: ClaudeCodeControlActions,
}

registerProvider(AgentProvider.CLAUDE_CODE, claudeCodePlugin)

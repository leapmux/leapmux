import type { Question } from '../../controls/types'
import type { MessageCategory } from '../../messageClassification'
import type { ClassificationContext, ClassificationInput, ProviderPlugin } from '../registry'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { PermissionMode } from '~/utils/controlResponse'
import * as workerRpc from '~/api/workerRpc'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { CLAUDE_TOOL } from '~/types/toolMessages'
import { buildAllowResponse, buildDenyResponse, getToolInput, getToolName } from '~/utils/controlResponse'
import * as styles from '../../ChatView.css'
import { buildAskAnswers } from '../../controls/AskUserQuestionControl'
import { ClaudeCodeControlActions, ClaudeCodeControlContent } from '../../controls/ClaudeCodeControlRequest'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { registerProvider } from '../registry'
import { getAssistantContent } from './extractors/assistantContent'
import { claudeNotificationThreadEntry } from './notifications'
import { renderClaudeMessage } from './renderMessage'
import {
  ClaudeCodeSettingsPanel,
  ClaudeCodeTriggerLabel,
  DEFAULT_CLAUDE_EFFORT,
  DEFAULT_CLAUDE_MODEL,
} from './settings'
import { claudeToolResultMeta } from './toolResult'

function buildSetPermissionModeRequest(mode: PermissionMode): string {
  return JSON.stringify({
    type: 'control_request',
    request_id: crypto.randomUUID(),
    request: { subtype: 'set_permission_mode', mode },
  })
}

function buildInterruptRequest(): string {
  return JSON.stringify({
    type: 'control_request',
    request_id: crypto.randomUUID(),
    request: { subtype: 'interrupt' },
  })
}

/** Extra notification types for Claude Code (plan_execution, system subtypes). */
const CLAUDE_EXTRA_TYPES = new Set(['plan_execution'])
/** System message subtypes that should never surface in the UI. */
const HIDDEN_SYSTEM_SUBTYPES = new Set(['init', 'task_notification', 'task_updated'])
function isClaudeNotifThread(wrapper: { messages: unknown[] } | null): wrapper is { messages: unknown[] } {
  return isNotificationThreadWrapper(wrapper, CLAUDE_EXTRA_TYPES, (t, st) =>
    t === 'system' && !HIDDEN_SYSTEM_SUBTYPES.has(st ?? ''))
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
    if (HIDDEN_SYSTEM_SUBTYPES.has(subtype ?? ''))
      return { kind: 'hidden' }
    if (subtype === 'status' && parent.status !== 'compacting')
      return { kind: 'hidden' }
    return { kind: 'notification' }
  },
  rate_limit(parent) {
    const info = pickObject(parent, 'rate_limit_info')
    if (info?.status === 'allowed')
      return { kind: 'hidden' }
    return { kind: 'notification' }
  },
  interrupted: () => ({ kind: 'notification' }),
  context_cleared: () => ({ kind: 'notification' }),
  compacting: () => ({ kind: 'notification' }),
  settings_changed: () => ({ kind: 'notification' }),
  agent_renamed: () => ({ kind: 'notification' }),
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
      if (input.spanType === CLAUDE_TOOL.TOOL_SEARCH)
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
          if (input.spanType === CLAUDE_TOOL.TODO_WRITE || input.spanType === CLAUDE_TOOL.TOOL_SEARCH)
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

  // Notification thread (wrapper with notification-type first message)
  if (isClaudeNotifThread(wrapper)) {
    const msgs = wrapper.messages.filter((m) => {
      if (!isObject(m))
        return true
      if (m.type !== 'rate_limit')
        return true
      const info = pickObject(m, 'rate_limit_info')
      return info?.status !== 'allowed'
    })
    if (msgs.length === 0)
      return { kind: 'hidden' }
    return { kind: 'notification_thread', messages: msgs }
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
    const content = getAssistantContent(obj)
    if (!content)
      return null
    const text = content
      .filter(c => c.type === 'text' || c.type === 'thinking')
      .map(c => String(c.type === 'thinking' ? c.thinking || '' : c.text || ''))
      .join('\n')
      .trim()
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

const claudeCodePlugin: ProviderPlugin = {
  defaultModel: DEFAULT_CLAUDE_MODEL,
  defaultEffort: DEFAULT_CLAUDE_EFFORT,
  defaultPermissionMode: 'default',
  bypassPermissionMode: 'bypassPermissions',
  attachments: {
    text: true,
    image: true,
    pdf: true,
    binary: false,
  },
  planMode: {
    currentMode: agent => agent.permissionMode || 'default',
    planValue: 'plan',
    defaultValue: 'default',
    setMode: (mode, onChange) => onChange({ kind: 'permissionMode', value: mode as PermissionMode }),
  },

  classify: classifyClaudeCodeMessage,
  renderMessage: renderClaudeMessage,
  toolResultMeta: claudeToolResultMeta,
  extractQuotableText: claudeExtractQuotableText,
  notificationThreadEntry: claudeNotificationThreadEntry,

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

  // Claude Code supports runtime permission mode changes via control_request
  // (lightweight, no agent restart needed).
  async changePermissionMode(workerId: string, agentId: string, mode: PermissionMode): Promise<void> {
    await workerRpc.sendAgentRawMessage(workerId, {
      agentId,
      content: buildSetPermissionModeRequest(mode),
    })
  },

  ControlContent: ClaudeCodeControlContent,
  ControlActions: ClaudeCodeControlActions,

  SettingsPanel: ClaudeCodeSettingsPanel,
  settingsMenuClass: styles.settingsMenuWide,

  settingsTriggerLabel: ClaudeCodeTriggerLabel,
}

registerProvider(AgentProvider.CLAUDE_CODE, claudeCodePlugin)

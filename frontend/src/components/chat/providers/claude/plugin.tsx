import type { Question } from '../../controls/types'
import type { MessageCategory } from '../../messageClassification'
import type { ClassificationContext, ClassificationInput, ProviderPlugin } from '../registry'
import type { PermissionMode } from '~/utils/controlResponse'
import * as workerRpc from '~/api/workerRpc'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { isObject } from '~/lib/jsonPick'
import { buildAllowResponse, buildDenyResponse, getToolInput, getToolName } from '~/utils/controlResponse'
import * as styles from '../../ChatView.css'
import { buildAskAnswers } from '../../controls/AskUserQuestionControl'
import { ClaudeCodeControlActions, ClaudeCodeControlContent } from '../../controls/ClaudeCodeControlRequest'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { registerProvider } from '../registry'
import { claudeNotificationThreadEntry } from './notifications'
import { renderClaudeMessage } from './renderMessage'
import {
  ClaudeCodeSettingsPanel,
  ClaudeCodeTriggerLabel,
  DEFAULT_CLAUDE_EFFORT,
  DEFAULT_CLAUDE_MODEL,
} from './settings'
import { claudeToolResultMeta } from './toolResult'

function generateRandomId(): string {
  const chars = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789'
  let result = '01'
  for (let i = 0; i < 22; i++) {
    result += chars[Math.floor(Math.random() * chars.length)]
  }
  return result
}

function buildSetPermissionModeRequest(mode: PermissionMode): string {
  const requestId = generateRandomId()
  return JSON.stringify({
    type: 'control_request',
    request_id: requestId,
    request: { subtype: 'set_permission_mode', mode },
  })
}

function buildInterruptRequest(): string {
  const requestId = generateRandomId()
  return JSON.stringify({
    type: 'control_request',
    request_id: requestId,
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

/** Claude Code message classification. */
function classifyClaudeCodeMessage(
  input: ClassificationInput,
  _context?: ClassificationContext,
): MessageCategory {
  const parentObject = input.parentObject
  const wrapper = input.wrapper

  // 0. Empty wrapper (all notifications consolidated to no-ops) — hide.
  if (wrapper && wrapper.messages.length === 0)
    return { kind: 'hidden' }

  // 1. Notification thread (wrapper with notification-type first message)
  if (isClaudeNotifThread(wrapper)) {
    const msgs = wrapper.messages.filter((m) => {
      if (!isObject(m))
        return true
      return !((m as Record<string, unknown>).type === 'rate_limit' && isObject((m as Record<string, unknown>).rate_limit_info)
        && ((m as Record<string, unknown>).rate_limit_info as Record<string, unknown>).status === 'allowed')
    })
    if (msgs.length === 0)
      return { kind: 'hidden' }
    return { kind: 'notification_thread', messages: msgs }
  }

  if (!parentObject)
    return { kind: 'unknown' }

  const type = parentObject.type as string | undefined
  const subtype = parentObject.subtype as string | undefined

  // 2. Hidden: system init, or system status (non-compacting)
  if (type === 'system') {
    if (input.parentSpanId && (subtype === 'task_started' || subtype === 'task_progress'))
      return { kind: 'hidden' }
    if (HIDDEN_SYSTEM_SUBTYPES.has(subtype ?? ''))
      return { kind: 'hidden' }
    if (subtype === 'status' && parentObject.status !== 'compacting')
      return { kind: 'hidden' }
    return { kind: 'notification' }
  }

  // Non-system notification types
  if (type === 'rate_limit') {
    if (isObject(parentObject.rate_limit_info) && (parentObject.rate_limit_info as Record<string, unknown>).status === 'allowed')
      return { kind: 'hidden' }
    return { kind: 'notification' }
  }
  if (type === 'interrupted' || type === 'context_cleared' || type === 'compacting' || type === 'settings_changed' || type === 'agent_renamed')
    return { kind: 'notification' }

  // Result divider
  if (type === 'result')
    return { kind: 'result_divider' }

  // Compact summary
  if (parentObject.isCompactSummary === true)
    return { kind: 'compact_summary' }

  // Control response (synthetic message with controlResponse)
  if (parentObject.isSynthetic === true && isObject(parentObject.controlResponse))
    return { kind: 'control_response' }

  // Assistant messages
  if (type === 'assistant') {
    const message = parentObject.message as Record<string, unknown> | undefined
    if (isObject(message)) {
      const content = (message as Record<string, unknown>).content
      if (Array.isArray(content)) {
        const contentArr = content as Array<Record<string, unknown>>
        const toolUse = contentArr.find(c => isObject(c) && c.type === 'tool_use') as Record<string, unknown> | undefined
        if (toolUse) {
          if (input.spanType === 'ToolSearch')
            return { kind: 'hidden' }
          return {
            kind: 'tool_use',
            toolName: String(toolUse.name || ''),
            toolUse,
            content: contentArr,
          }
        }
        if (contentArr.some(c => isObject(c) && c.type === 'text'))
          return { kind: 'assistant_text' }
        if (contentArr.some(c => isObject(c) && c.type === 'thinking')) {
          // Signature-only thinking blocks (no visible text) can slip past
          // --thinking-display summarized; hide them so the UI doesn't
          // render an empty row.
          const hasText = contentArr.some(c =>
            isObject(c) && c.type === 'thinking'
            && typeof c.thinking === 'string' && c.thinking.length > 0)
          if (!hasText)
            return { kind: 'hidden' }
          return { kind: 'assistant_thinking' }
        }
      }
    }
    return { kind: 'unknown' }
  }

  // User messages
  if (type === 'user') {
    if (input.spanType === 'EnterPlanMode' || parentObject.span_type === 'EnterPlanMode')
      return { kind: 'hidden' }

    const message = parentObject.message as Record<string, unknown> | undefined
    if (isObject(message)) {
      const content = (message as Record<string, unknown>).content
      if (typeof content === 'string')
        return { kind: 'user_text' }
      if (Array.isArray(content)) {
        // tool_result takes priority over agent_prompt (subagent tool results
        // also have parent_tool_use_id but should be rendered as tool results).
        if ((content as Array<Record<string, unknown>>).some(c => isObject(c) && c.type === 'tool_result')) {
          if (input.spanType === 'TodoWrite' || input.spanType === 'ToolSearch')
            return { kind: 'hidden' }
          return { kind: 'tool_result' }
        }
      }
    }
    // Agent prompt: user message with parent_tool_use_id (prompt sent to sub-agent)
    if (typeof parentObject.parent_tool_use_id === 'string')
      return { kind: 'agent_prompt' }
    return { kind: 'unknown' }
  }

  // Plain object with string .content and no .type → user_content
  if (!type && typeof parentObject.content === 'string') {
    if (parentObject.hidden === true)
      return { kind: 'hidden' }
    if (parentObject.planExecution === true)
      return { kind: 'plan_execution' }
    return { kind: 'user_content' }
  }

  return { kind: 'unknown' }
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
    setMode: (mode, cb) => cb.onPermissionModeChange?.(mode as PermissionMode),
  },

  classify: classifyClaudeCodeMessage,
  renderMessage: renderClaudeMessage,
  toolResultMeta: claudeToolResultMeta,
  notificationThreadEntry: claudeNotificationThreadEntry,

  isAskUserQuestion(payload) {
    const tool = getToolName(payload)
    return tool === 'AskUserQuestion' || tool === 'request_user_input'
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
    if (getToolName(payload) === 'ExitPlanMode')
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

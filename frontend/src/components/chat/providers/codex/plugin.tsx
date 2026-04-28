/* eslint-disable solid/components-return-once -- renderMessage is a plain dispatcher returning JSX, not a Solid component */
import type { JSX } from 'solid-js'
import type { Question } from '../../controls/types'
import type { MessageCategory } from '../../messageClassification'
import type { RenderContext } from '../../messageRenderers'
import type { ClassificationContext, ClassificationInput, ProviderPlugin } from '../registry'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { PermissionMode } from '~/utils/controlResponse'
import * as workerRpc from '~/api/workerRpc'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { CODEX_RATE_LIMITS_METHOD, iterCodexRateLimitTiers } from '~/lib/rateLimitUtils'
import { CODEX_INTERNAL_TOOL, CODEX_ITEM, CODEX_METHOD, CODEX_STATUS } from '~/types/toolMessages'
import { getToolName } from '~/utils/controlResponse'
import * as styles from '../../ChatView.css'
import { CodexControlActions, CodexControlContent, sendCodexUserInputResponse } from '../../controls/CodexControlRequest'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { acpBuildControlResponse, isJsonRpcResponseObject } from '../acp/classification'
import { registerProvider } from '../registry'
import { CODEX_RENDERERS } from './defineRenderer'
import { codexNotificationRenderer, codexNotificationThreadEntry } from './notifications'
// The named imports below are the renderers dispatched explicitly (not via
// the registry) by `renderMessage` for non-`item.type` shapes.
import {
  CodexAgentMessageRenderer,
  CodexMcpToolCallRenderer,
  CodexReasoningRenderer,
  CodexTurnCompletedRenderer,
  CodexTurnPlanRenderer,
} from './renderers'
import { extractItem } from './renderHelpers'
import {
  CODEX_EXTRA_COLLABORATION_MODE,
  CodexSettingsPanel,
  CodexTriggerLabel,
  DEFAULT_CODEX_COLLABORATION_MODE,
  DEFAULT_CODEX_EFFORT,
  DEFAULT_CODEX_MODEL,
} from './settings'
import { codexToolResultMeta } from './toolResult'
// Side-effect import: each renderer module's `defineCodexRenderer(...)` call
// runs at load time and registers itself in `CODEX_RENDERERS`. Without this
// import a tree-shaking bundler could drop the modules whose named exports
// aren't directly referenced below.
import './renderers/registerAll'

const CODEX_TURN_FAILED_NOTIFICATION = 'Codex turn failed'

let codexReqIdCounter = 1000

/**
 * Builds a JSON-RPC request for interrupting a Codex turn.
 */
function buildCodexInterruptRequest(threadId: string, turnId: string): string {
  return JSON.stringify({
    jsonrpc: '2.0',
    id: ++codexReqIdCounter,
    method: 'turn/interrupt',
    params: { threadId, turnId },
  })
}

function isCodexInterruptRequestText(content: string): boolean {
  // Cheap discriminator first — `JSON.parse` runs on every assistant message
  // during classify, but only Codex interrupt echoes carry this method name.
  if (!content.includes('"turn/interrupt"'))
    return false
  try {
    const parsed: unknown = JSON.parse(content)
    return isObject(parsed) && parsed.method === 'turn/interrupt'
  }
  catch {
    return false
  }
}

function codexAssistantInterruptEcho(parent: Record<string, unknown>): boolean {
  if (parent.role !== 'assistant')
    return false

  if (typeof parent.content === 'string')
    return isCodexInterruptRequestText(parent.content)

  if (parent.type !== 'assistant')
    return false

  const message = pickObject(parent, 'message')
  if (!message || !Array.isArray(message.content))
    return false

  let text = ''
  for (const c of message.content) {
    if (isObject(c) && c.type === 'text' && typeof c.text === 'string')
      text += c.text
  }
  return text.length > 0 && isCodexInterruptRequestText(text)
}

function isCodexJsonRpcResponse(parent: Record<string, unknown>): boolean {
  if ('item' in parent || 'turn' in parent)
    return false
  return isJsonRpcResponseObject(parent)
}

function isCodexEmptyCompletedWebSearch(item: Record<string, unknown>): boolean {
  const query = pickString(item, 'query').trim()
  const action = pickObject(item, 'action')
  const actionType = pickString(action, 'type')

  if (actionType === 'other')
    return query.length === 0

  if (actionType === 'openPage')
    return !action?.url

  return false
}

/** Extra notification types for Codex (agent_error). */
const CODEX_EXTRA_NOTIF_TYPES = new Set(['agent_error'])
function isCodexNotifThread(wrapper: { old_seqs: number[], messages: unknown[] } | null): wrapper is { old_seqs: number[], messages: unknown[] } {
  if (isNotificationThreadWrapper(wrapper, CODEX_EXTRA_NOTIF_TYPES, (t, st) =>
    t === 'system' && st !== 'init' && st !== 'task_notification')) {
    return true
  }
  // Codex method-based notifications (e.g. account/rateLimits/updated)
  if (!wrapper || (wrapper as { messages: unknown[] }).messages.length < 1)
    return false
  return (wrapper as { messages: unknown[] }).messages.some((msg: unknown) =>
    isObject(msg) && msg.method === CODEX_RATE_LIMITS_METHOD)
}

/** Returns true when a Codex rate limit message has all tiers below the warning threshold. */
function isCodexRateLimitAllAllowed(m: Record<string, unknown>): boolean {
  if (m.method !== CODEX_RATE_LIMITS_METHOD)
    return false
  for (const { info } of iterCodexRateLimitTiers(m)) {
    if (info.status !== 'allowed')
      return false
  }
  return true
}

function isCodexHiddenNotificationThreadMessage(m: unknown): boolean {
  if (!isObject(m))
    return false
  const msg = m as Record<string, unknown>
  if (msg.method === CODEX_METHOD.THREAD_TOKEN_USAGE_UPDATED)
    return true
  if (msg.type === 'agent_error' && msg.error === CODEX_TURN_FAILED_NOTIFICATION)
    return true
  return isCodexRateLimitAllAllowed(msg)
}

type CodexItemClassifier = (item: Record<string, unknown>, context?: ClassificationContext) => MessageCategory

/**
 * Per-item-type classifier for messages shaped as `{item: {type, ...}, ...}`.
 * Keyed by the `item.type` string; missing entries fall through to `'unknown'`.
 */
const CODEX_ITEM_CLASSIFIERS: Record<string, CodexItemClassifier> = {
  [CODEX_ITEM.AGENT_MESSAGE]: () => ({ kind: 'assistant_text' }),
  [CODEX_ITEM.PLAN]: item => ({ kind: 'tool_use', toolName: CODEX_ITEM.PLAN, toolUse: item, content: [] }),
  [CODEX_ITEM.COMMAND_EXECUTION]: item => ({ kind: 'tool_use', toolName: CODEX_ITEM.COMMAND_EXECUTION, toolUse: item, content: [] }),
  [CODEX_ITEM.FILE_CHANGE]: item => ({ kind: 'tool_use', toolName: CODEX_ITEM.FILE_CHANGE, toolUse: item, content: [] }),
  [CODEX_ITEM.MCP_TOOL_CALL]: item => ({ kind: 'tool_use', toolName: pickString(item, 'tool') || 'mcpTool', toolUse: item, content: [] }),
  [CODEX_ITEM.DYNAMIC_TOOL_CALL]: item => ({ kind: 'tool_use', toolName: pickString(item, 'tool') || 'dynamicTool', toolUse: item, content: [] }),
  [CODEX_ITEM.COLLAB_AGENT_TOOL_CALL]: (item) => {
    if (item.tool === 'spawnAgent' && item.status === CODEX_STATUS.COMPLETED)
      return { kind: 'hidden' }
    return { kind: 'tool_use', toolName: CODEX_ITEM.COLLAB_AGENT_TOOL_CALL, toolUse: item, content: [] }
  },
  [CODEX_ITEM.WEB_SEARCH]: (item) => {
    if (isCodexEmptyCompletedWebSearch(item))
      return { kind: 'hidden' }
    return { kind: 'tool_use', toolName: CODEX_ITEM.WEB_SEARCH, toolUse: item, content: [] }
  },
  [CODEX_ITEM.REASONING]: (item, context) => {
    const summary = item.summary as unknown[] | undefined
    const content = item.content as unknown[] | undefined
    if ((!summary || summary.length === 0) && (!content || content.length === 0))
      return context?.hasCommandStream ? { kind: 'assistant_thinking' } : { kind: 'hidden' }
    return { kind: 'assistant_thinking' }
  },
  [CODEX_ITEM.USER_MESSAGE]: () => ({ kind: 'hidden' }),
}

/**
 * Lifecycle methods that signal turn/thread state transitions but should not
 * appear in the chat. Persisted upstream; classified out here.
 */
const CODEX_HIDDEN_LIFECYCLE_METHODS = new Set<string>([
  CODEX_METHOD.THREAD_STARTED,
  CODEX_METHOD.TURN_STARTED,
  CODEX_METHOD.THREAD_STATUS_CHANGED,
  CODEX_METHOD.THREAD_TOKEN_USAGE_UPDATED,
])

/** LeapMux-side notification `type` values produced by the worker. */
const CODEX_LEAPMUX_NOTIFICATION_TYPES = new Set<string>([
  'settings_changed',
  'context_cleared',
  'interrupted',
  'agent_error',
  'agent_renamed',
  'compacting',
])

const codexPlugin: ProviderPlugin = {
  defaultModel: DEFAULT_CODEX_MODEL,
  defaultEffort: DEFAULT_CODEX_EFFORT,
  defaultPermissionMode: 'on-request',
  bypassPermissionMode: 'never',
  attachments: {
    text: true,
    image: true,
    pdf: false,
    binary: false,
  },
  planMode: {
    currentMode: agent => agent.extraSettings?.[CODEX_EXTRA_COLLABORATION_MODE] || DEFAULT_CODEX_COLLABORATION_MODE,
    planValue: 'plan',
    defaultValue: DEFAULT_CODEX_COLLABORATION_MODE,
    setMode: (mode, onChange) => onChange({ kind: 'optionGroup', key: CODEX_EXTRA_COLLABORATION_MODE, value: mode }),
  },
  classify(input: ClassificationInput, context?: ClassificationContext): MessageCategory {
    const parent = input.parentObject
    const wrapper = input.wrapper

    // Notification threads (settings_changed, context_cleared, etc.)
    if (isCodexNotifThread(wrapper)) {
      const msgs = wrapper.messages.filter(m => !isCodexHiddenNotificationThreadMessage(m))
      return msgs.length === 0
        ? { kind: 'hidden' }
        : { kind: 'notification_thread', messages: msgs }
    }

    // Empty wrapper — hide.
    if (wrapper && (wrapper as { messages: unknown[] }).messages.length === 0)
      return { kind: 'hidden' }

    if (!parent)
      return { kind: 'unknown' }

    if (isCodexJsonRpcResponse(parent) || codexAssistantInterruptEcho(parent))
      return { kind: 'hidden' }

    const type = parent.type as string | undefined
    const subtype = parent.subtype as string | undefined
    const method = parent.method as string | undefined

    // Lifecycle methods are transient signals; persist upstream, hide here.
    if (method && CODEX_HIDDEN_LIFECYCLE_METHODS.has(method))
      return { kind: 'hidden' }

    if (type === 'system') {
      if (subtype === 'init' || subtype === 'task_notification')
        return { kind: 'hidden' }
      if (subtype === 'status' && parent.status !== 'compacting')
        return { kind: 'hidden' }
      return { kind: 'notification' }
    }

    if (type === 'agent_error' && parent.error === CODEX_TURN_FAILED_NOTIFICATION)
      return { kind: 'hidden' }

    if (method === CODEX_METHOD.TURN_PLAN_UPDATED && isObject(parent.params))
      return { kind: 'tool_use', toolName: CODEX_INTERNAL_TOOL.TURN_PLAN, toolUse: parent, content: [] }

    // turn/completed → result divider
    const turn = pickObject(parent, 'turn')
    if (turn && turn.status)
      return { kind: 'result_divider' }

    // item/completed dispatch — keyed on item.type via the classifier table.
    const item = pickObject(parent, 'item') ?? undefined
    const itemType = item ? pickString(item, 'type', undefined) : undefined
    if (item && itemType) {
      const itemClassifier = CODEX_ITEM_CLASSIFIERS[itemType]
      if (itemClassifier)
        return itemClassifier(item, context)
    }

    // User message (persisted by LeapMux service layer)
    if (!parent.item && typeof parent.content === 'string') {
      if (parent.hidden === true)
        return { kind: 'hidden' }
      if (parent.planExecution === true)
        return { kind: 'plan_execution' }
      return { kind: 'user_content' }
    }

    // Codex method-based notifications
    if (method === CODEX_RATE_LIMITS_METHOD) {
      return isCodexRateLimitAllAllowed(parent) ? { kind: 'hidden' } : { kind: 'notification' }
    }

    if (method === CODEX_METHOD.MCP_SERVER_STARTUP_STATUS_UPDATED)
      return { kind: 'notification' }

    if (type && CODEX_LEAPMUX_NOTIFICATION_TYPES.has(type))
      return { kind: 'notification' }

    return { kind: 'unknown' }
  },

  renderMessage(category: MessageCategory, parsed: unknown, role: MessageRole, context?: RenderContext): JSX.Element | null {
    if (category.kind === 'assistant_text')
      return <CodexAgentMessageRenderer parsed={parsed} role={role} context={context} />
    if (category.kind === 'assistant_thinking')
      return <CodexReasoningRenderer parsed={parsed} role={role} context={context} />
    if (category.kind === 'result_divider')
      return <CodexTurnCompletedRenderer parsed={parsed} role={role} context={context} />
    if (category.kind === 'notification') {
      const codexResult = codexNotificationRenderer(parsed, role, context)
      if (codexResult !== null)
        return codexResult
      // Fall through to Claude-shaped notification renderers (settings_changed,
      // interrupted, agent_renamed, etc.) — Codex emits these too.
      return null
    }
    if (category.kind === 'tool_use') {
      // Use the item stored in category.toolUse (resolved to final state in classify).
      const cat = category as { toolName: string, toolUse: Record<string, unknown> }
      // turnPlan dispatches off `parent.method` (not item.type), so it's
      // not in CODEX_RENDERERS — handle it explicitly.
      if (cat.toolName === CODEX_INTERNAL_TOOL.TURN_PLAN)
        return <CodexTurnPlanRenderer parsed={cat.toolUse} role={role} context={context} />
      // For mcp/dynamic tool calls the toolName comes from item.tool, not
      // item.type. Look up by the actual item type (after unwrapping the
      // optional `{item, threadId}` envelope) and fall back to the generic
      // MCP body for any unrecognized tool-call shape.
      const item = extractItem(cat.toolUse)
      const itemType = item ? pickString(item, 'type', undefined) : undefined
      const Renderer = itemType ? CODEX_RENDERERS.get(itemType) : undefined
      if (Renderer && item)
        return <Renderer item={item} role={role} context={context} />
      return <CodexMcpToolCallRenderer parsed={cat.toolUse} role={role} context={context} />
    }
    return null
  },

  toolResultMeta: codexToolResultMeta,

  extractQuotableText(category: MessageCategory, parsed: ParsedMessageContent): string | null {
    const obj = parsed.parentObject
    if (!obj)
      return null
    if (category.kind === 'assistant_text' || category.kind === 'assistant_thinking') {
      const item = pickObject(obj, 'item')
      const text = pickString(item, 'text')
      return text.trim() || null
    }
    if (category.kind === 'user_content' || category.kind === 'plan_execution') {
      if (typeof obj.content === 'string')
        return (obj.content as string).trim() || null
    }
    return null
  },

  notificationThreadEntry: codexNotificationThreadEntry,

  buildInterruptContent(agentSessionId: string, codexTurnId?: string): string | null {
    if (!agentSessionId || !codexTurnId)
      return null
    return buildCodexInterruptRequest(agentSessionId, codexTurnId)
  },

  isAskUserQuestion(payload) {
    return (payload as Record<string, unknown>).method === 'item/tool/requestUserInput'
  },

  extractAskUserQuestions(payload) {
    const params = pickObject(payload, 'params')
    return Array.isArray(params?.questions) ? params!.questions as Question[] : []
  },

  async sendAskUserQuestionResponse(agentId, sendControlResponse, requestId, questions, askState, _payload) {
    await sendCodexUserInputResponse(agentId, sendControlResponse, requestId, questions, askState)
  },

  buildControlResponse(payload, content, requestId) {
    const response = acpBuildControlResponse(payload, content, requestId)
    // Codex's plan-mode prompt response carries an extra marker so the worker
    // can route it back into the plan-mode handshake instead of the regular
    // tool-approval flow.
    if (getToolName(payload) === 'CodexPlanModePrompt')
      response.codexPlanModePrompt = true
    return response
  },

  // Codex applies the new approval policy on the next turn/start.
  async changePermissionMode(workerId: string, agentId: string, mode: PermissionMode): Promise<void> {
    await workerRpc.updateAgentSettings(workerId, {
      agentId,
      settings: { permissionMode: mode },
    })
  },
  ControlContent: CodexControlContent,
  ControlActions: CodexControlActions,

  SettingsPanel: CodexSettingsPanel,
  settingsMenuClass: styles.settingsMenuWide,

  settingsTriggerLabel: CodexTriggerLabel,
}

registerProvider(AgentProvider.CODEX, codexPlugin)

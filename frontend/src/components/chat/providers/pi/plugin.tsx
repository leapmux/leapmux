/* eslint-disable solid/components-return-once -- PI_RENDERERS is a plain dispatcher table whose entries return JSX, not Solid components */
import type { JSX } from 'solid-js'
import type { MessageCategory } from '../../messageClassification'
import type { RenderContext } from '../../messageRenderers'
import type { FileEditDiffSource } from '../../results/fileEditDiff'
import type { ClassificationInput, Provider, ToolResultMeta } from '../registry'
import type { PiExtensionResponse } from './controlResponse'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { formatUnifiedDiffText } from '../../diff'
import { PlanExecutionMessage, UserContentMessage } from '../../messageRenderers'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { fileEditDiffHunks, fileEditHasDiff } from '../../results/fileEditDiff'
import { hasMoreLinesThan } from '../../results/useCollapsedLines'
import { COLLAPSED_RESULT_ROWS } from '../../toolRenderers'
import { registerProvider } from '../registry'
import { piQuestionsFromPayload } from './askUserQuestion'
import {
  piAskAnswerValue,
  piCancelResponse,
  piConfirmResponse,
  piValueResponse,
  sendPiExtensionResponse,
} from './controlResponse'
import { PiControlActions, PiControlContent } from './controls'
import { extractPiBash } from './extractors/bash'
import { extractPiEdit, extractPiRead, extractPiWrite, resolvePiResultDiff } from './extractors/fileEdit'
import { piExtractTool } from './extractors/toolCommon'
import { piContentText, piIsThinkingOnly } from './messageContent'
import { PI_DIALOG_METHOD, PI_EVENT, PI_TOOL } from './protocol'
import {
  PiAssistantMessage,
  PiAssistantThinking,
  piNotificationRenderer,
  PiToolExecutionRenderer,
  PiToolResultRenderer,
  renderPiResultDivider,
} from './renderers'
import { DEFAULT_PI_EFFORT, DEFAULT_PI_MODEL, PiSettingsPanel, PiTriggerLabel } from './settings'

/** Pi event types that carry no UI surface (lifecycle markers / fan-out). */
const PI_HIDDEN_EVENT_TYPES = new Set<string>([
  PI_EVENT.AgentStart,
  PI_EVENT.TurnStart,
  PI_EVENT.TurnEnd,
  PI_EVENT.MessageStart,
  PI_EVENT.ToolExecutionUpdate,
])

/** Pi notification-style event types. */
const PI_NOTIFICATION_EVENT_TYPES = new Set<string>([
  PI_EVENT.CompactionStart,
  PI_EVENT.CompactionEnd,
  PI_EVENT.AutoRetryStart,
  PI_EVENT.AutoRetryEnd,
  PI_EVENT.ExtensionError,
])

function piFallbackDiffSources(
  toolName: string,
  toolUseParsed: ParsedMessageContent | undefined,
): FileEditDiffSource[] {
  const startPayload = toolUseParsed?.parentObject
  if (!isObject(startPayload))
    return []
  if (toolName === PI_TOOL.Edit)
    return extractPiEdit(startPayload)?.sources.filter(fileEditHasDiff) ?? []
  if (toolName === PI_TOOL.Write) {
    const source = extractPiWrite(startPayload)
    return fileEditHasDiff(source) ? [source] : []
  }
  return []
}

function formatPiDiffSources(sources: FileEditDiffSource[]): string | null {
  if (sources.length === 0)
    return null
  return sources
    .map(source => formatUnifiedDiffText(fileEditDiffHunks(source), source.filePath))
    .filter(Boolean)
    .join('\n') || null
}

type PiRenderer = (
  category: MessageCategory,
  parsed: unknown,
  context: RenderContext | undefined,
) => JSX.Element | null

const PI_RENDERERS: Partial<Record<MessageCategory['kind'], PiRenderer>> = {
  assistant_text: (_cat, parsed, context) =>
    <PiAssistantMessage parsed={parsed} context={context} />,
  assistant_thinking: (_cat, parsed, context) =>
    <PiAssistantThinking parsed={parsed} context={context} />,
  tool_use: (category, _parsed, context) => {
    const cat = category as { toolName: string, toolUse: Record<string, unknown> }
    return <PiToolExecutionRenderer parsed={cat.toolUse} context={context} />
  },
  tool_result: (_cat, parsed, context) =>
    <PiToolResultRenderer parsed={parsed} context={context} />,
  result_divider: (_cat, parsed) => renderPiResultDivider(parsed),
  user_content: (_cat, parsed) => <UserContentMessage parsed={parsed} />,
  plan_execution: (_cat, parsed, context) => {
    const obj = isObject(parsed) ? parsed : null
    const text = obj && typeof obj.content === 'string' ? obj.content : ''
    return text ? <PlanExecutionMessage text={text} context={context} /> : null
  },
  notification: (_cat, parsed) => piNotificationRenderer(parsed),
}

function piToolResultMeta(
  category: MessageCategory,
  parsed: unknown,
  _spanType: string | undefined,
  toolUseParsed: ParsedMessageContent | undefined,
): ToolResultMeta | null {
  if (category.kind !== 'tool_result' || !isObject(parsed))
    return null

  const tool = piExtractTool(parsed)
  if (!tool)
    return null

  const resultText = tool.result?.text ?? ''
  const startArgs = pickObject(toolUseParsed?.parentObject, 'args') ?? {}

  if (tool.toolName === PI_TOOL.Bash) {
    const bash = extractPiBash(parsed)
    if (!bash)
      return null
    return {
      collapsible: hasMoreLinesThan(bash.output, COLLAPSED_RESULT_ROWS),
      hasDiff: false,
      hasCopyable: bash.output !== '',
      copyableContent: () => bash.output || null,
    }
  }

  if (tool.toolName === PI_TOOL.Read) {
    const read = extractPiRead(parsed, startArgs)
    if (!read)
      return null
    return {
      collapsible: (read.source.lines?.length ?? 0) > COLLAPSED_RESULT_ROWS,
      hasDiff: false,
      hasCopyable: resultText !== '',
      copyableContent: () => resultText || null,
    }
  }

  if (tool.toolName === PI_TOOL.Edit || tool.toolName === PI_TOOL.Write) {
    if (tool.isError) {
      return {
        collapsible: false,
        hasDiff: false,
        hasCopyable: resultText !== '',
        copyableContent: () => resultText || null,
      }
    }

    const resultDiff = resolvePiResultDiff(parsed, startArgs).source
    const sources = resultDiff ? [resultDiff] : piFallbackDiffSources(tool.toolName, toolUseParsed)
    const hasDiff = sources.length > 0
    return {
      collapsible: false,
      hasDiff,
      hasCopyable: hasDiff || resultText !== '',
      copyableContent: () => formatPiDiffSources(sources) ?? (resultText || null),
    }
  }

  return null
}

const piPlugin: Provider = {
  defaultModel: DEFAULT_PI_MODEL,
  defaultEffort: DEFAULT_PI_EFFORT,
  defaultPermissionMode: undefined,
  bypassPermissionMode: undefined,
  attachments: {
    text: true,
    image: true,
    pdf: false,
    binary: false,
  },
  // Pi's wire format dispatches via top-level `type`. Lifecycle / status /
  // extension events here are visible-but-non-progress: they thread into
  // the chat as notifications but must not register as agent activity for
  // the working-state heuristic.
  nonProgressTypes: new Set<string>([
    ...PI_NOTIFICATION_EVENT_TYPES,
    PI_EVENT.ExtensionUIRequest,
  ]),

  classify(input: ClassificationInput): MessageCategory {
    const parent = input.parentObject
    const wrapper = input.wrapper

    // Wrapper-style notification thread (settings_changed, context_cleared, etc.)
    if (isNotificationThreadWrapper(wrapper))
      return { kind: 'notification_thread', messages: wrapper.messages }
    if (wrapper && (wrapper as { messages: unknown[] }).messages.length === 0)
      return { kind: 'hidden' }

    if (!parent)
      return { kind: 'unknown' }

    const type = pickString(parent, 'type')

    // User messages persisted by the Leapmux service layer are stored as
    // plain `{"content":"...","attachments":[...]}` with no `type` field —
    // not a Pi RPC event. Match this shape *before* event-type dispatch so
    // Pi-persisted user echoes don't fall through to the unknown fallback
    // (which would JSON-stringify the body into the chat bubble).
    if (!type && typeof parent.content === 'string') {
      if (parent.hidden === true)
        return { kind: 'hidden' }
      if (parent.planExecution === true)
        return { kind: 'plan_execution' }
      return { kind: 'user_content' }
    }

    if (type === PI_EVENT.AgentEnd)
      return { kind: 'result_divider' }

    if (PI_HIDDEN_EVENT_TYPES.has(type))
      return { kind: 'hidden' }

    if (type === PI_EVENT.MessageEnd) {
      // Pi emits message_end for *every* message added to the conversation —
      // the user's prompt, tool results, and bash-execution echoes — not just
      // the assistant's reply. Leapmux already persists the user message via
      // the synthetic user_content row, and tool results render through the
      // tool_execution_* span. Hide these to avoid duplicates; only the
      // assistant's text/thinking message_end should reach the chat view.
      // Pi's wire envelope carries the message author under `role` (Anthropic
      // Messages API style), distinct from the proto-side MessageSource that
      // describes who persisted the row. Read the wire field by name.
      const messageRole = pickString(pickObject(parent, 'message'), 'role')
      if (messageRole !== 'assistant')
        return { kind: 'hidden' }
      if (piIsThinkingOnly(parent))
        return { kind: 'assistant_thinking' }
      if (piContentText(parent, 'text').trim() !== '')
        return { kind: 'assistant_text' }
      return { kind: 'hidden' }
    }

    if (type === PI_EVENT.ToolExecutionStart) {
      const toolName = pickString(parent, 'toolName') || 'tool'
      return { kind: 'tool_use', toolName, toolUse: parent, content: [] }
    }
    if (type === PI_EVENT.ToolExecutionEnd) {
      // Pi's tool_execution_end carries `{toolCallId, toolName, result,
      // isError}` — no args. Classify as `tool_result` so the result
      // renderer reads only what's there, and the chat store pairs it
      // with the matching tool_execution_start via spanId.
      return { kind: 'tool_result' }
    }

    if (PI_NOTIFICATION_EVENT_TYPES.has(type))
      return { kind: 'notification' }

    if (type === PI_EVENT.ExtensionUIRequest) {
      // Dialog requests are surfaced as control requests (handled outside the
      // chat flow); fire-and-forget methods become session-info or transcript
      // entries server-side. Anything that reaches the chat classifier here
      // is informational — render as a notification.
      return { kind: 'notification' }
    }

    return { kind: 'unknown' }
  },

  renderMessage(category: MessageCategory, parsed: unknown, context?: RenderContext): JSX.Element | null {
    return PI_RENDERERS[category.kind]?.(category, parsed, context) ?? null
  },

  toolResultMeta: piToolResultMeta,

  extractQuotableText(category: MessageCategory, parsed: ParsedMessageContent): string | null {
    const obj = parsed.parentObject
    if (!obj)
      return null
    if (category.kind === 'assistant_text')
      return piContentText(obj, 'text').trim() || null
    if (category.kind === 'assistant_thinking')
      return piContentText(obj, 'thinking').trim() || null
    if ((category.kind === 'user_content' || category.kind === 'plan_execution') && typeof obj.content === 'string')
      return obj.content.trim() || null
    return null
  },

  buildInterruptContent(): string | null {
    return JSON.stringify({ type: 'abort' })
  },

  isAskUserQuestion(payload) {
    return payload.type === PI_EVENT.ExtensionUIRequest
      && (payload.method === PI_DIALOG_METHOD.Input || payload.method === PI_DIALOG_METHOD.Select)
  },

  extractAskUserQuestions(payload) {
    return piQuestionsFromPayload(payload)
  },

  async sendAskUserQuestionResponse(agentId, sendControlResponse, requestId, _questions, askState, payload) {
    const method = pickString(payload, 'method')
    if (method === PI_DIALOG_METHOD.Select) {
      const value = piAskAnswerValue(askState)
      const response = value.trim() ? piValueResponse(requestId, value) : piCancelResponse(requestId)
      await sendPiExtensionResponse(agentId, sendControlResponse, response)
      return
    }
    const text = askState.customTexts()[0] ?? ''
    await sendPiExtensionResponse(agentId, sendControlResponse, piValueResponse(requestId, text))
  },

  buildControlResponse(payload, content, requestId) {
    const method = pickString(payload, 'method')
    let response: PiExtensionResponse
    switch (method) {
      case PI_DIALOG_METHOD.Confirm:
        // Editor content is interpreted as a feedback message — but `confirm`
        // can only be true/false. If the user typed a deny reason, treat any
        // content as a deny.
        response = piConfirmResponse(requestId, content.trim() === '')
        break
      case PI_DIALOG_METHOD.Input:
      case PI_DIALOG_METHOD.Editor:
        // Pi distinguishes an empty submitted value from cancellation.
        response = piValueResponse(requestId, content)
        break
      case PI_DIALOG_METHOD.Select:
        // Without a typed reply, treat as cancellation.
        response = content.trim() ? piValueResponse(requestId, content) : piCancelResponse(requestId)
        break
      default:
        response = piCancelResponse(requestId)
    }
    return response
  },

  ControlContent: PiControlContent,
  ControlActions: PiControlActions,

  SettingsPanel: PiSettingsPanel,
  settingsTriggerLabel: PiTriggerLabel,
}

registerProvider(AgentProvider.PI, piPlugin)

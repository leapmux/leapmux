import type { JSX } from 'solid-js'
import type { MessageCategory } from '../../messageClassification'
import type { RenderContext } from '../../messageRenderers'
import type { Provider, ToolMessageInput, ToolResultMeta } from '../registry'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ContextUsageInfo } from '~/stores/agentSession.store'
import { COPILOT_EVENT, COPILOT_MODE, COPILOT_OPTION, COPILOT_PERMISSION_MODE } from '~/generated/contracts/copilot-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickNumber, pickString } from '~/lib/jsonPick'
import { buildControlResponseEnvelope, buildDenyResponse } from '~/utils/controlResponse'
import { withElicitationResponse } from '../../controls/elicitationResponse'
import { sendResponse } from '../../controls/types'
import { defaultMarkPreview } from '../../markPreviewShared'
import { PlanExecutionMessage, UserContentMessage } from '../../messageRenderers'
import { toolPresentationMeta } from '../../results/toolResultMeta'
import { buildPlanMode } from '../../settingsGroups'
import { registerProvider, retainedRowIsFinal } from '../registry'
import { classifyCopilotMessage, copilotQuotableText, copilotResultDivider } from './classification'
import { copilotControlResponseDisplay } from './controlResponse'
import {
  CopilotControlActions,
  CopilotControlContent,
  copilotElicitation,
  copilotIsQuestion,
  copilotQuestions,
} from './controls'
import { copilotToolImages } from './images'
import { copilotNotificationThreadEntry } from './notification'
import { copilotEvent } from './protocol'
import { copilotAssistantRenderer, copilotReasoningRenderer, copilotToolRenderer } from './renderers'
import { copilotToolPresentation, copilotToolRow } from './toolPresentation'

/**
 * The `tool.execution_start` row opens a span; its completion closes it.
 *
 * A turn that ended while the call ran stores the start frame AGAIN as the closing
 * row, because the runtime sends no completion for it. The completion column is what
 * separates the two copies.
 */
function copilotSpanRole(parsed: ParsedMessageContent) {
  switch (copilotEvent(parsed.parentObject)?.type) {
    case COPILOT_EVENT.ToolStarted:
      return retainedRowIsFinal(parsed.completion) ? 'result' as const : 'opener' as const
    case COPILOT_EVENT.ToolCompleted:
      return 'result' as const
    default:
      return 'other' as const
  }
}

/**
 * Copilot's context counter, for a row the worker's own normalization did not reach.
 *
 * The worker broadcasts the live counter from the same event, so this is the fallback
 * a replayed or unaugmented row takes.
 */
function copilotContextUsage(parsed: ParsedMessageContent): ContextUsageInfo | null {
  const event = copilotEvent(parsed.parentObject)
  if (!event || event.type !== COPILOT_EVENT.SessionUsageInfo)
    return null
  const current = pickNumber(event.data, 'currentTokens')
  if (current == null || current <= 0)
    return null
  const info: ContextUsageInfo = { inputTokens: 0, cacheCreationInputTokens: 0, cacheReadInputTokens: 0, outputTokens: 0, contextTokens: current }
  const limit = pickNumber(event.data, 'tokenLimit')
  if (limit != null && limit > 0)
    info.contextWindow = limit
  return info
}

function copilotRenderMessage(category: MessageCategory, parsed: unknown, context?: RenderContext): JSX.Element | null {
  switch (category.kind) {
    case 'assistant_text':
      return copilotAssistantRenderer(parsed, context)
    case 'assistant_thinking':
      return copilotReasoningRenderer(parsed, context)
    case 'tool_use':
    case 'tool_result':
      return copilotToolRenderer(parsed, context)
    case 'user_content':
      return <UserContentMessage parsed={parsed} context={context} />
    case 'plan_execution': {
      const text = isObject(parsed) ? pickString(parsed, 'content') : ''
      return text ? <PlanExecutionMessage text={text} context={context} /> : null
    }
    default:
      return null
  }
}

/** The toolbar reads the same presentation the result body does. */
function copilotToolResultMeta(category: MessageCategory, input: ToolMessageInput): ToolResultMeta | null {
  if (category.kind !== 'tool_result')
    return null
  const row = copilotToolRow(input.parsed.parentObject, input.spanType, input.request, input.parsed.completion)
  return row ? toolPresentationMeta(copilotToolPresentation(row)) : null
}

const copilotPlugin: Provider = {
  attachments: { text: true, image: true, pdf: true, binary: true },

  classify: classifyCopilotMessage,
  renderMessage: copilotRenderMessage,
  spanRole: copilotSpanRole,
  relatedMessages: (parsed) => {
    const role = copilotSpanRole(parsed)
    // A result states no tool name and no arguments of its own, so it always wants
    // its request. A request wants its result only when the body comes from there.
    if (role === 'result')
      return ['request']
    if (role !== 'opener')
      return []
    const row = copilotToolRow(parsed.parentObject)
    return row && (row.kind === 'agent' || Object.keys(row.input).length === 0) ? ['result'] : []
  },
  contextUsageFromMessage: copilotContextUsage,
  resultDivider: copilotResultDivider,
  notificationThreadEntry: copilotNotificationThreadEntry,
  toolResultMeta: copilotToolResultMeta,
  toolResultImages: (input) => {
    const row = copilotToolRow(input.parsed.parentObject, input.spanType, input.request, input.parsed.completion)
    return row ? copilotToolImages(row) : []
  },
  extractQuotableText: copilotQuotableText,
  previewText: defaultMarkPreview,
  controlResponseDisplay: withElicitationResponse(copilotElicitation, copilotControlResponseDisplay),
  elicitation: copilotElicitation,

  // The composer's own send is a rejection: Allow lives on its own button, and an
  // empty send is a refusal with no reason.
  buildControlResponse: (_payload, content, requestId) => buildDenyResponse(requestId, content),

  ControlContent: CopilotControlContent,
  ControlActions: CopilotControlActions,

  askUserQuestion: {
    isRequest: copilotIsQuestion,
    extractQuestions: copilotQuestions,
    sendAnswer: (request, sendControlResponse, questions, answerState) => {
      const selected = answerState.selections()[0] ?? []
      const typed = answerState.customTexts()[0]?.trim() ?? ''
      // The runtime distinguishes a typed answer from a selected choice, and it
      // accepts an explicit empty answer. Both facts travel exactly as given.
      const answer = selected.length > 0 ? selected.join(', ') : typed
      const response = { behavior: 'allow', answer, wasFreeform: selected.length === 0 }
      return sendResponse(sendControlResponse, buildControlResponseEnvelope(request.requestId, response))
    },
    sendReject: (request, sendControlResponse, message) =>
      sendResponse(sendControlResponse, buildDenyResponse(request.requestId, message)),
  },

  // Copilot's plan axis is its SESSION mode, beside the permission mode its presets
  // drive. The two are independent, which is why the mode chip reads the session-mode
  // group and the presets write the permission-mode one.
  triggerModeGroupKey: COPILOT_OPTION.SessionMode,
  planMode: buildPlanMode(COPILOT_OPTION.SessionMode, COPILOT_MODE.Plan, COPILOT_MODE.Interactive),
  permissionPresets: {
    smart: { sets: { permissionMode: COPILOT_PERMISSION_MODE.Assisted } },
    bypass: { sets: { permissionMode: COPILOT_PERMISSION_MODE.AllowAll } },
  },
}

registerProvider(AgentProvider.GITHUB_COPILOT, copilotPlugin)

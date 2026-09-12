/* eslint-disable solid/components-return-once -- ZCODE_RENDERERS is a plain dispatcher table whose entries return JSX, not Solid components */
import type { JSX } from 'solid-js'
import type { MessageCategory } from '../../messageClassification'
import type { RenderContext } from '../../messageRenderers'
import type { ClassificationInput, Provider, SpanRole, ToolMessageInput, ToolResultMeta } from '../registry'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ContextUsageInfo } from '~/stores/agentSession.store'
import { ZCODE_DEFAULT_MODE, ZCODE_EVENT, ZCODE_MODE, ZCODE_TOOL, ZCODE_TOOL_KIND } from '~/generated/contracts/zcode-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { buildDenyResponse, getToolInput } from '~/utils/controlResponse'
import { buildAskAnswers } from '../../controls/AskUserQuestionControl'
import { sendResponse } from '../../controls/types'
import { defaultMarkPreview } from '../../markPreviewShared'
import { PlanExecutionMessage, UserContentMessage } from '../../messageRenderers'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { MarkdownPlanLayout } from '../../widgets/MarkdownPlanLayout'
import { registerProvider } from '../registry'
import { zcodeQuestionsFromPayload } from './askUserQuestion'
import { zcodeControlResponseDisplay } from './controlResponse'
import { ZCodeControlActions, ZCodeControlContent, zcodeIsAskUserQuestion } from './controls'
import { zcodeToolResultImages } from './extractors/image'
import { zcodeControlPlanText } from './extractors/plan'
import { zcodeResultMeta, zcodeResultPresentation } from './extractors/result'
import { resolveZCodeMessage } from './extractors/supplement'
import { zcodeEnvelope, zcodeExtractTool, zcodeRow } from './extractors/toolCommon'
import { zcodeAssistantText, zcodeIsBackgroundTask, zcodeIsModelResponse } from './messageContent'
import { ZCODE_WEB_FETCH } from './protocol'
import {
  describeZCodeNotification,
  ZCodeAssistantMessage,
  zcodeNotificationThreadEntry,
  zcodeResultDivider,
  ZCodeToolExecutionRenderer,
  ZCodeToolResultRenderer,
} from './renderers'

/**
 * The event types that thread into chat as notifications.
 *
 * They are also the NON-PROGRESS set: each one is visible but says nothing about the
 * agent working, so the chat-level thinking heuristic must keep scanning past them.
 * Both uses read this one set, so they cannot drift.
 */
const ZCODE_NOTIFICATION_TYPES = new Set<string>([
  ZCODE_EVENT.PermissionResolved,
  ZCODE_EVENT.TurnSteerQueued,
  ZCODE_EVENT.TurnSteerDrained,
  ZCODE_EVENT.SessionClosed,
])

/**
 * Event types that carry no UI surface of their own.
 *
 * Each is hidden for a stated reason, and the reasons differ -- which is why this is
 * a list of deliberate decisions rather than a default branch:
 *
 *   - The session lifecycle pair repeats the state the create/resume RPC returned.
 *   - A model-written title would fight LeapMux's own naming.
 *   - The message/part projection is the desktop application's rendering model, and
 *     it repeats the text the model-response `session.updated` already carries.
 *   - The permission/userInput ANNOUNCEMENTS duplicate the interaction requests,
 *     which are the actionable copies and reach the control surface instead.
 *   - Checkpoint and rewind belong to an undo model LeapMux does not expose.
 *   - `turn.started` and `streamRecovery.updated` are worker-side bookkeeping.
 */
const ZCODE_HIDDEN_TYPES = new Set<string>([
  ZCODE_EVENT.SessionCreated,
  ZCODE_EVENT.SessionResumed,
  ZCODE_EVENT.SessionTitleUpdated,
  ZCODE_EVENT.TurnStarted,
  ZCODE_EVENT.MessageUpserted,
  ZCODE_EVENT.MessageRemoved,
  ZCODE_EVENT.PartStarted,
  ZCODE_EVENT.PartDelta,
  ZCODE_EVENT.PartUpserted,
  ZCODE_EVENT.PartRemoved,
  ZCODE_EVENT.ModelStreaming,
  ZCODE_EVENT.PermissionRequested,
  ZCODE_EVENT.UserInputRequested,
  ZCODE_EVENT.UserInputResolved,
  ZCODE_EVENT.CheckpointCreated,
  ZCODE_EVENT.RewindTriggered,
  ZCODE_EVENT.StreamRecoveryUpdated,
])

/**
 * A ZCode notification row with nothing to render.
 *
 * The only surface that can produce no line is a `permission.resolved` the describer
 * does not recognize. Applied by the standalone classifier AND the consolidated-thread
 * filter, so such a row is hidden either way instead of surfacing as raw JSON.
 */
function isHiddenZCodeNotification(msg: unknown): boolean {
  const envelope = zcodeEnvelope(msg)
  if (!envelope || !ZCODE_NOTIFICATION_TYPES.has(envelope.type))
    return false
  return describeZCodeNotification(msg) === null
}

/**
 * The tool.updated kinds that OPEN a span rather than close it.
 *
 * `scheduled` is the opener. `result`, `error`, and `batch` are final.
 * The Worker consumes `started` and `progress` for live counters.
 */
function zcodeToolSpanRole(kind: string): SpanRole {
  if (kind === ZCODE_TOOL_KIND.Scheduled)
    return 'opener'
  if (kind === ZCODE_TOOL_KIND.Result || kind === ZCODE_TOOL_KIND.Error || kind === ZCODE_TOOL_KIND.Batch)
    return 'result'
  return 'other'
}

/**
 * ZCode span role. The `tool.updated` KIND discriminates the opener from the result,
 * because both halves arrive as the same event type -- a content-block scan would
 * bucket every one of them the same way.
 */
function zcodeSpanRole(parsed: ParsedMessageContent): SpanRole {
  if (zcodeControlPlanText(parsed.parentObject) !== null)
    return 'opener'
  const envelope = zcodeEnvelope(parsed.parentObject)
  if (!envelope || envelope.type !== ZCODE_EVENT.ToolUpdated)
    return 'other'
  return zcodeToolSpanRole(pickString(envelope.payload, 'kind'))
}

type ZCodeRenderer = (
  category: MessageCategory,
  parsed: unknown,
  context: RenderContext | undefined,
) => JSX.Element | null

const ZCODE_RENDERERS: Partial<Record<MessageCategory['kind'], ZCodeRenderer>> = {
  assistant_text: (_cat, parsed, context) =>
    <ZCodeAssistantMessage parsed={parsed} context={context} />,
  tool_use: (_cat, parsed, context) => {
    const plan = zcodeControlPlanText(parsed)
    return plan === null
      ? <ZCodeToolExecutionRenderer parsed={parsed} context={context} />
      : <MarkdownPlanLayout toolName={ZCODE_TOOL.ExitPlanMode} title="Proposed Plan" planText={plan} context={context} />
  },
  tool_result: (_cat, parsed, context) =>
    <ZCodeToolResultRenderer parsed={parsed} context={context} />,
  user_content: (_cat, parsed, context) => <UserContentMessage parsed={parsed} context={context} />,
  plan_execution: (_cat, parsed, context) => {
    const text = isObject(parsed) ? pickString(parsed, 'content') : ''
    return text ? <PlanExecutionMessage text={text} context={context} /> : null
  },
}

/** The toolbar reads the same presentation as the result renderer. */
function zcodeToolResultMeta(category: MessageCategory, input: ToolMessageInput): ToolResultMeta | null {
  if (category.kind !== 'tool_result')
    return null
  const presentation = zcodeResultPresentation(zcodeRow(input.parsed.parentObject, input.spanType, input.request, input.parsed.supplementalContent))
  return presentation ? zcodeResultMeta(presentation) : null
}

/**
 * ZCode context usage from a model-response `session.updated`.
 *
 * A fallback for a raw or unaugmented row: the worker normalizes usage into a
 * top-level `context_usage` the shared reader prefers, and only an event that
 * bypassed it reaches here. `contextWindow` is the model's own limit and rides along
 * so the fill gauge has a denominator.
 */
function zcodeContextUsageFromMessage(parsed: ParsedMessageContent): ContextUsageInfo | null {
  const envelope = zcodeEnvelope(parsed.parentObject)
  if (!envelope || envelope.type !== ZCODE_EVENT.SessionUpdated)
    return null
  const usage = pickObject(envelope.payload, 'usage')
  if (!usage)
    return null
  const inputTokens = pickNumber(usage, 'inputTokens', 0)
  const outputTokens = pickNumber(usage, 'outputTokens', 0)
  const cacheReadInputTokens = pickNumber(usage, 'cacheReadTokens', 0)
  const cacheCreationInputTokens = pickNumber(usage, 'cacheWriteTokens', 0)
  if (inputTokens === 0 && outputTokens === 0 && cacheReadInputTokens === 0 && cacheCreationInputTokens === 0)
    return null
  const info: ContextUsageInfo = { inputTokens, cacheCreationInputTokens, cacheReadInputTokens, outputTokens }
  const total = pickNumber(usage, 'totalTokens')
  if (total != null && total > 0)
    info.contextTokens = total
  return info
}

const ZCODE_REQUESTS_WITH_TITLES = new Set<string>([
  ZCODE_TOOL.Bash,
  ZCODE_TOOL.Read,
  ZCODE_TOOL.Write,
  ZCODE_TOOL.Edit,
  ZCODE_TOOL.Glob,
  ZCODE_TOOL.Grep,
  ZCODE_TOOL.TodoWrite,
  ZCODE_TOOL.Agent,
  ZCODE_WEB_FETCH,
])

const zcodePlugin: Provider = {
  resolveMessage: resolveZCodeMessage,
  spanRole: zcodeSpanRole,
  relatedMessages: (parsed) => {
    if (zcodeControlPlanText(parsed.parentObject) !== null)
      return []
    const role = zcodeSpanRole(parsed)
    if (role === 'result')
      return ['request']
    const tool = zcodeExtractTool(parsed.parentObject)
    return role === 'opener' && (tool?.toolName === ZCODE_TOOL.Agent || tool?.toolName === ZCODE_TOOL.TodoWrite || Object.keys(tool?.input ?? {}).length === 0 || !ZCODE_REQUESTS_WITH_TITLES.has(tool?.toolName ?? '')) ? ['result'] : []
  },
  contextUsageFromMessage: zcodeContextUsageFromMessage,

  // Text is inlined into the prompt and an image rides `session/send.attachments`.
  // A PDF is refused: the app-server's normalizer knows image, video, file and audio
  // and nothing else, so a PDF arrives as a generic file -- decoded as text (binary
  // garbage to the model) when small, and dropped with no message when large.
  attachments: {
    text: true,
    image: true,
    pdf: false,
    binary: false,
  },

  // ZCode's mode axis rides LeapMux's permission-mode channel, so the mode chip and
  // the plan toggle drive `session/setMode`.
  triggerModeGroupKey: 'permissionMode',
  permissionPresets: { bypass: { sets: { permissionMode: ZCODE_MODE.Yolo } } },
  planMode: {
    groupKey: 'permissionMode',
    currentMode: agent => agent.optionValues?.permissionMode ?? ZCODE_DEFAULT_MODE,
    planValue: ZCODE_MODE.Plan,
    defaultValue: ZCODE_DEFAULT_MODE,
  },

  classify(input: ClassificationInput): MessageCategory {
    const parent = input.parentObject
    const wrapper = input.wrapper

    // The empty-wrapper check runs FIRST so the type guard below stays the only
    // narrowing on `wrapper`: it narrows the false branch to null, which would make a
    // later `wrapper.messages` read need a cast to compile.
    if (wrapper && wrapper.messages.length === 0)
      return { kind: 'hidden' }
    if (isNotificationThreadWrapper(wrapper, ZCODE_NOTIFICATION_TYPES)) {
      // A thread of only unrenderable notifications collapses to hidden rather than
      // falling through to a raw-JSON bubble.
      const messages = wrapper.messages.filter(m => !isHiddenZCodeNotification(m))
      if (messages.length === 0)
        return { kind: 'hidden' }
      return { kind: 'notification', messages }
    }

    if (!parent)
      return { kind: 'unknown' }
    if (zcodeControlPlanText(parent) !== null)
      return { kind: 'tool_use', toolName: ZCODE_TOOL.ExitPlanMode, toolUse: parent, content: [] }

    // A user row the service layer persisted is the LeapMux-neutral `{content}`
    // shape, with no ZCode `type`. It is matched BEFORE the event dispatch so a user
    // echo does not fall through to the unknown fallback and get JSON-stringified
    // into the bubble.
    const type = pickString(parent, 'type')
    if (!type && typeof parent.content === 'string') {
      if (parent.hidden === true)
        return { kind: 'hidden' }
      if (parent.planExecution === true)
        return { kind: 'plan_execution' }
      return { kind: 'user_content' }
    }

    if (type === ZCODE_EVENT.TurnCompleted || type === ZCODE_EVENT.TurnFailed)
      return { kind: 'result_divider' }

    if (type === ZCODE_EVENT.ToolUpdated) {
      const payload = pickObject(parent, 'payload') ?? {}
      const role = zcodeToolSpanRole(pickString(payload, 'kind'))
      if (role === 'result') {
        return { kind: 'tool_result' }
      }
      if (role === 'opener') {
        return {
          kind: 'tool_use',
          toolName: pickString(payload, 'toolName') || 'tool',
          toolUse: parent,
          content: [],
        }
      }
      // The Worker consumes `started` and `progress` for live counters. One
      // reaching a transcript means a provider changed its protocol.
      return { kind: 'hidden' }
    }

    if (type === ZCODE_EVENT.SessionUpdated) {
      const payload = pickObject(parent, 'payload') ?? {}
      // The catch-all event. A background-task update belongs to the registry, and
      // the request-telemetry variants carry no conversation, so only a model
      // response with text becomes a bubble.
      if (zcodeIsBackgroundTask(payload) || !zcodeIsModelResponse(payload))
        return { kind: 'hidden' }
      return zcodeAssistantText(parent).trim() ? { kind: 'assistant_text' } : { kind: 'hidden' }
    }

    if (ZCODE_NOTIFICATION_TYPES.has(type)) {
      if (isHiddenZCodeNotification(parent))
        return { kind: 'hidden' }
      return { kind: 'notification', messages: [parent] }
    }

    if (ZCODE_HIDDEN_TYPES.has(type))
      return { kind: 'hidden' }

    return { kind: 'unknown' }
  },

  renderMessage(category: MessageCategory, parsed: unknown, context?: RenderContext): JSX.Element | null {
    return ZCODE_RENDERERS[category.kind]?.(category, parsed, context) ?? null
  },

  notificationThreadEntry: zcodeNotificationThreadEntry,
  resultDivider: zcodeResultDivider,
  toolResultMeta: zcodeToolResultMeta,
  toolResultImages: input => zcodeToolResultImages(zcodeRow(input.parsed.parentObject, input.spanType, input.request, input.parsed.supplementalContent)),

  extractQuotableText(category: MessageCategory, parsed: ParsedMessageContent): string | null {
    const obj = parsed.parentObject
    if (!obj)
      return null
    const plan = zcodeControlPlanText(obj)
    if (plan !== null)
      return plan
    if (category.kind === 'assistant_text')
      return zcodeAssistantText(obj).trim() || null
    if ((category.kind === 'user_content' || category.kind === 'plan_execution') && typeof obj.content === 'string')
      return obj.content.trim() || null
    return null
  },

  // ZCode marks user sends (the neutral `{content}` shape the shared extractor
  // handles) and control answers -- and an answer classifies as `control_response`,
  // which the rail resolves through controlResponseDisplay rather than here.
  previewText: defaultMarkPreview,
  controlResponseDisplay: zcodeControlResponseDisplay,

  askUserQuestion: {
    isRequest: zcodeIsAskUserQuestion,
    extractQuestions: zcodeQuestionsFromPayload,
    sendAnswer: (request, sendControlResponse, questions, answerState) =>
      sendResponse(sendControlResponse, buildAskAnswers(answerState, questions, getToolInput(request.payload), request.requestId)),
    sendReject: (request, sendControlResponse, message) =>
      sendResponse(sendControlResponse, buildDenyResponse(request.requestId, message)),
  },

  // Composer send is always a rejection. The placeholder says "Type a rejection
  // reason...", Allow lives on its own button, and an empty send is a deny with
  // no extra message. Claude's empty-send-is-allow does not apply.
  buildControlResponse(_payload, content, requestId) {
    return buildDenyResponse(requestId, content)
  },

  ControlContent: ZCodeControlContent,
  ControlActions: ZCodeControlActions,
}

registerProvider(AgentProvider.ZCODE, zcodePlugin)

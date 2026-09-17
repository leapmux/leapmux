import type { ProviderPlugin } from '../capabilities'
import { buildJsonRpcResult, questionsFromWire } from '~/components/chat/controls/types'
import { buildPlanMode } from '~/components/chat/settingsGroups'
import { CODEX_BYPASS_SETTINGS } from '~/generated/contracts/codex-bypass'
import { CODEX_ITEM, CODEX_OPTION, CODEX_OPTION_DEFAULT } from '~/generated/contracts/codex-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { pickObject, pickString } from '~/lib/jsonPick'
import { buildAllowResponse, buildDenyResponse, getToolInput, getToolName } from '~/utils/controlResponse'
import { withElicitationResponse } from '../../controls/elicitationResponse'
import { registerProvider } from '../registry'
import { classifyCodexMessage } from './classification'
import { CodexControlActions } from './CodexControlActions'
import {
  codexControlResponseDisplay,
  codexRequestedPermissions,
  resolveCodexDecisions,
  sendCodexUserInputRejectResponse,
  sendCodexUserInputResponse,
} from './controlResponse'
import { codexElicitation } from './elicitation'
import { codexExtractControl } from './extractControl'
import { extractItem } from './extractors/item'
import { codexCompactionBoundary, codexNotificationEntry } from './extractors/notification'
import { codexResultDivider } from './extractors/resultDivider'
import { codexExtractRow } from './extractors/row'
import { codexRateLimitsFromMessage } from './rateLimits'
import { resolveCodexMessage } from './resolveMessage'
import { codexContextUsageFromNotification } from './sessionMetadata'
import { codexSpanRole } from './spanRole'

const codexPlugin: ProviderPlugin = {
  transcript: {
    resolveMessage: resolveCodexMessage,
    spanRole: codexSpanRole,
    relatedMessages: (parsed) => {
      const item = extractItem(parsed.parentObject)
      if (item?.type === CODEX_ITEM.ImageView || item?.type === CODEX_ITEM.CollabAgentToolCall)
        return ['request', 'result']
      return (item?.type === CODEX_ITEM.McpToolCall || item?.type === CODEX_ITEM.DynamicToolCall) && codexSpanRole(parsed) === 'result' ? ['request'] : []
    },
    classify: classifyCodexMessage,
    extractRow: codexExtractRow,
    extractDivider: codexResultDivider,
    notificationEntry: codexNotificationEntry,
  },
  controls: {
    permissionPresets: { bypass: CODEX_BYPASS_SETTINGS },
    // Codex accepts an option selection AND a free-text note together, so the
    // AskUserQuestion UI keeps both instead of treating them as mutually exclusive.
    preservesSelectionNotes: true,
    controlResponseDisplay: withElicitationResponse(codexElicitation, codexControlResponseDisplay),
    askUserQuestion: {
      isRequest: payload => payload.method === 'item/tool/requestUserInput',
      // The shared reader, not a cast: an `Array.isArray` on the OUTER array says
      // nothing about the elements, and a `null` or a bare string among them reached
      // `AskUserQuestionControl`, which dereferences `question` and hands `options` to
      // a `<For>`.
      extractQuestions: payload => questionsFromWire(pickObject(payload, 'params')?.questions),
      sendAnswer: (request, sendControlResponse, questions, answerState) =>
        sendCodexUserInputResponse(sendControlResponse, request.requestId, questions, answerState),
      sendReject: (request, sendControlResponse) =>
        sendCodexUserInputRejectResponse(sendControlResponse, request.requestId),
    },
    elicitation: codexElicitation,
    buildControlResponse(payload, content, requestId) {
      const method = pickString(payload, 'method', '')
      if (getToolName(payload) === 'CodexPlanModePrompt')
        return content ? buildDenyResponse(requestId, content) : buildAllowResponse(requestId, getToolInput(payload))
      if (method === 'item/permissions/requestApproval') {
        return buildJsonRpcResult(requestId, {
          permissions: content ? {} : codexRequestedPermissions(payload),
          scope: 'turn',
        })
      }
      const decisions = resolveCodexDecisions(pickObject(payload, 'params')?.availableDecisions)
      // This path MUST answer: the user sent a message while the request was
      // pending, and an unanswered request blocks the agent forever. So it falls
      // back to the canonical token of a polarity the request offered none of,
      // where the banner instead draws no button. The two differ on purpose --
      // the banner can decline to offer a decision, and this cannot decline to
      // send one.
      const decision = content
        ? decisions.negative ?? 'cancel'
        : decisions.positive ?? 'accept'
      return buildJsonRpcResult(requestId, { decision })
    },
    // The worker already forwards synthetic plan feedback as a user message.
    controlFeedbackAsFollowUpMessage: payload => getToolName(payload) !== 'CodexPlanModePrompt',
    extractControl: codexExtractControl,
    // Codex states the decisions it accepts per request, as WORDS, and one of them
    // carries a network-policy amendment as an object rather than an id. Neither the
    // shared option row nor the Allow/Deny pair can send those.
    controlActionsFor: () => CodexControlActions,
  },
  session: {
    rateLimitsFromMessage: codexRateLimitsFromMessage,
    contextUsageFromMessage: codexContextUsageFromNotification,
    compactionBoundaryFromMessage: codexCompactionBoundary,
  },
  configuration: {
    // Seed a new Codex agent with its default collaboration mode.
    defaultProviderOptions: { [CODEX_OPTION.CollaborationMode]: CODEX_OPTION_DEFAULT.CollaborationMode },
    // Multi-Agent V2 rejects direct app-server input for spawned child threads.
    // The child tab is a read-only transcript.
    supportsSubagentSend: false,
    supportsSubagentInterrupt: true,
    attachments: {
      text: true,
      image: true,
      pdf: false,
      binary: false,
    },
    planMode: buildPlanMode(CODEX_OPTION.CollaborationMode, 'plan', CODEX_OPTION_DEFAULT.CollaborationMode),
    // The trigger's mode segment shows the "Workflow" (collaboration_mode) group --
    // Codex's mode axis -- not the approval policy. It reads "Plan Mode" when the
    // workflow sits at its plan value.
    triggerModeGroupKey: CODEX_OPTION.CollaborationMode,
  },
}

registerProvider(AgentProvider.CODEX, codexPlugin)

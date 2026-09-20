import type { ProviderPlugin } from '../capabilities'
import { CLAUDE_DEFAULT_MODE, CLAUDE_MODE } from '~/generated/contracts/claude-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { buildAllowResponse, buildDenyResponse, getToolInput } from '~/utils/controlResponse'
import { buildAskAnswers } from '../../controls/AskUserQuestionControl'
import { withElicitationResponse } from '../../controls/elicitationResponse'
import { sendResponse } from '../../controls/types'
import { controlBehaviorDisplay, controlDecisionWords } from '../../persistedControlResponse'
import { buildPlanMode } from '../../settingsGroups'
import { registerProvider } from '../registry'
import { claudeAskUserQuestions, claudeIsAskUserQuestion } from './askUserQuestion'
import { classifyClaudeCodeMessage } from './classification'
import { claudeElicitation } from './elicitation'
import { claudeExtractControl } from './extractControl'
import { claudeCompactionBoundary, claudeNotificationEntry } from './extractors/notification'
import { claudeResultDivider } from './extractors/resultDivider'
import { claudeExtractRow } from './extractors/row'
import { claudeRateLimitsFromMessage } from './rateLimits'
import { claudeContextUsageFromMessage } from './sessionMetadata'
import { claudeRelatedMessages, claudeSpanRole } from './spanRole'

// Claude reserves ~16.5% of the context window as an autocompact buffer, so the
// context-usage percentage is measured against the remaining usable capacity.
const CLAUDE_AUTOCOMPACT_BUFFER_PCT = 16.5

const claudeCodePlugin: ProviderPlugin = {
  transcript: {
    classify: classifyClaudeCodeMessage,
    spanRole: claudeSpanRole,
    relatedMessages: claudeRelatedMessages,
    extractRow: claudeExtractRow,
    notificationEntry: claudeNotificationEntry,
    extractDivider: claudeResultDivider,
  },
  controls: {
    permissionPresets: {
      smart: { sets: { permissionMode: CLAUDE_MODE.Auto } },
      bypass: { sets: { permissionMode: CLAUDE_MODE.BypassPermissions } },
    },
    // Claude's native control response IS the neutral behavior envelope, so its derivation is the
    // shared reader. The envelope does not say WHICH control it answers, and the two Claude offers
    // carry different words on their buttons -- Approve and Reject for a plan, Allow and Deny for a
    // permission -- so the request decides which pair the saved row shows. It decides through the
    // SAME reader the banner drew the request with.
    controlResponseDisplay: withElicitationResponse(claudeElicitation, cr => controlBehaviorDisplay(
      cr.response,
      controlDecisionWords(claudeExtractControl({ payload: cr.request ?? {} })),
    )),
    askUserQuestion: {
      isRequest: claudeIsAskUserQuestion,
      extractQuestions: claudeAskUserQuestions,
      sendAnswer: (request, sendControlResponse, questions, answerState) =>
        sendResponse(sendControlResponse, buildAskAnswers(answerState, questions, getToolInput(request.payload), request.requestId)),
      sendReject: (request, sendControlResponse, message) =>
        sendResponse(sendControlResponse, buildDenyResponse(request.requestId, message)),
    },
    elicitation: claudeElicitation,
    buildControlResponse(payload, content, requestId) {
      // A plan never goes through the editor for "approve" -- that path lives in the
      // dedicated approval button. Editor input here always means "reject the plan with
      // feedback", and Send with nothing typed also rejects. Which requests are plans is
      // the reader's answer, not a second tool-name test beside it.
      if (claudeExtractControl({ payload })?.kind === 'plan')
        return buildDenyResponse(requestId, content)
      return content
        ? buildDenyResponse(requestId, content)
        : buildAllowResponse(requestId, getToolInput(payload))
    },
    extractControl: claudeExtractControl,
  },
  session: {
    rateLimitsFromMessage: claudeRateLimitsFromMessage,
    contextUsageFromMessage: claudeContextUsageFromMessage,
    compactionBoundaryFromMessage: claudeCompactionBoundary,
    contextBufferPct: CLAUDE_AUTOCOMPACT_BUFFER_PCT,
  },
  configuration: {
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
  },
}

registerProvider(AgentProvider.CLAUDE_CODE, claudeCodePlugin)

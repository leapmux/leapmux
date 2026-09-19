import type { ProviderPlugin } from '../capabilities'
import { ZCODE_DEFAULT_MODE, ZCODE_MODE, ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { buildDenyResponse, getToolInput } from '~/utils/controlResponse'
import { buildAskAnswers } from '../../controls/AskUserQuestionControl'
import { sendResponse } from '../../controls/types'
import { registerProvider } from '../registry'
import { zcodeIsAskUserQuestion, zcodeQuestionsFromPayload } from './askUserQuestion'
import { classifyZCodeMessage } from './classification'
import { zcodeControlResponseDisplay } from './controlResponse'
import { zcodeExtractControl } from './extractControl'
import { zcodeNotificationEntry } from './extractors/notification'
import { zcodeControlPlanText } from './extractors/plan'
import { zcodeResultDivider } from './extractors/resultDivider'
import { zcodeExtractRow } from './extractors/row'
import { zcodeExtractTool } from './extractors/toolCommon'
import { resolveZCodeMessage } from './resolveMessage'
import { zcodeContextUsageFromMessage } from './sessionMetadata'
import { zcodeSpanRole } from './spanRole'

const ZCODE_REQUESTS_WITH_TITLES = new Set<string>([
  ZCODE_TOOL.Bash,
  ZCODE_TOOL.Read,
  ZCODE_TOOL.Write,
  ZCODE_TOOL.Edit,
  ZCODE_TOOL.Glob,
  ZCODE_TOOL.Grep,
  ZCODE_TOOL.TodoWrite,
  ZCODE_TOOL.Agent,
  ZCODE_TOOL.WebFetch,
])

const zcodePlugin: ProviderPlugin = {
  transcript: {
    resolveMessage: resolveZCodeMessage,
    spanRole: zcodeSpanRole,
    relatedMessages: (parsed) => {
      if (zcodeControlPlanText(parsed.parentObject) !== null)
        return []
      const role = zcodeSpanRole(parsed)
      if (role === 'result')
        return ['request']
      const tool = zcodeExtractTool(parsed.parentObject)
      return role === 'request' && (tool?.toolName === ZCODE_TOOL.Agent || tool?.toolName === ZCODE_TOOL.TodoWrite || Object.keys(tool?.input ?? {}).length === 0 || !ZCODE_REQUESTS_WITH_TITLES.has(tool?.toolName ?? '')) ? ['result'] : []
    },
    classify: classifyZCodeMessage,
    extractRow: zcodeExtractRow,
    notificationEntry: zcodeNotificationEntry,
    extractDivider: zcodeResultDivider,
  },
  controls: {
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
    extractControl: zcodeExtractControl,
    permissionPresets: { bypass: { sets: { permissionMode: ZCODE_MODE.Yolo } } },
  },
  session: {
    contextUsageFromMessage: zcodeContextUsageFromMessage,
  },
  configuration: {
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
    planMode: {
      groupKey: 'permissionMode',
      currentMode: agent => agent.optionValues?.permissionMode ?? ZCODE_DEFAULT_MODE,
      planValue: ZCODE_MODE.Plan,
      defaultValue: ZCODE_DEFAULT_MODE,
    },
  },
}

registerProvider(AgentProvider.ZCODE, zcodePlugin)

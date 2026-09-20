import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerACPProvider } from '../acp/registerACPProvider'
import { getCursorQuestions, isCursorAskQuestionPayload, isCursorCreatePlanPayload, sendCursorQuestionRejectResponse, sendCursorQuestionResponse } from './askUserQuestion'
import { cursorControlResponseSummary } from './controlResponse'
import { CursorControlActions } from './CursorControlActions'
import { cursorExtractControl } from './extractControl'
import { cursorToolCallAdapter } from './extractors/toolCall'

registerACPProvider({
  provider: AgentProvider.CURSOR,
  toolCallAdapter: cursorToolCallAdapter,
  defaultPermissionMode: 'agent',
  controlResponseDisplay: cursorControlResponseSummary,
  extractControl: cursorExtractControl,
  // Cursor answers its create-plan itself: the verdict its worker transforms is
  // neither a permission decision nor the shared plan approval's. Every Agent
  // Client Protocol permission beside it takes the shared decision row.
  controlActionsFor: payload => isCursorCreatePlanPayload(payload) ? CursorControlActions : undefined,
  planValue: 'plan',
  questionHandling: {
    isRequest: payload => !!payload && isCursorAskQuestionPayload(payload),
    extractQuestions: payload => getCursorQuestions(payload),
    sendAnswer: (request, sendControlResponse, questions, answerState) =>
      sendCursorQuestionResponse(sendControlResponse, request.requestId, questions, answerState),
    sendReject: (request, sendControlResponse, message) =>
      sendCursorQuestionRejectResponse(sendControlResponse, request.requestId, message),
  },
})

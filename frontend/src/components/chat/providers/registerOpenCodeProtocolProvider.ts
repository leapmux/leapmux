import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerACPProvider } from './acp/registerACPProvider'
import { extractOpenCodeQuestions, OpenCodeControlActions, OpenCodeControlContent, sendOpenCodeQuestionRejectResponse, sendOpenCodeQuestionResponse } from './opencode/OpenCodeControlRequest'
import { opencodeControlResponseDisplay } from './opencode/questionAnswers'

interface OpenCodeProtocolOptions {
  provider: AgentProvider
  /** Default primary-agent option, e.g. `'build'` for OpenCode, `'code'` for Kilo. */
  defaultPrimaryAgent: string
}

const PRIMARY_AGENT_KEY = 'primaryAgent'
const PLAN_PRIMARY_AGENT = 'plan'

/**
 * Register a provider that speaks the OpenCode question/control protocol.
 * OpenCode and Kilo run different daemons but share the same wire format —
 * the only deltas are the provider enum and the default primary-agent label.
 */
export function registerOpenCodeProtocolProvider(opts: OpenCodeProtocolOptions): void {
  registerACPProvider({
    provider: opts.provider,
    settingsConfig: {
      kind: 'optionGroup',
      optionGroupKey: PRIMARY_AGENT_KEY,
      defaultValue: opts.defaultPrimaryAgent,
    },
    ControlContent: OpenCodeControlContent,
    ControlActions: OpenCodeControlActions,
    planValue: PLAN_PRIMARY_AGENT,
    // OpenCode and Kilo share the question-answer derivation from this single registration site
    // (mirroring the backend's questionRequestContext hook), so it can't drift per provider.
    controlResponseDisplay: opencodeControlResponseDisplay,
    questionHandling: {
      isRequest: payload => payload?.type === 'question.asked',
      extractQuestions: extractOpenCodeQuestions,
      sendAnswer: (request, sendControlResponse, questions, answerState) =>
        sendOpenCodeQuestionResponse(sendControlResponse, request.requestId, questions, answerState),
      sendReject: (request, sendControlResponse) =>
        sendOpenCodeQuestionRejectResponse(sendControlResponse, request.requestId),
    },
  })
}

import type { OpenCodeFamilyToolKinds } from './opencode/extractors/toolCall'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { OPENCODE_EVENT } from '~/generated/contracts/opencode-protocol'
import { registerACPProvider } from './acp/registerACPProvider'
import { openCodeControlResponseSummary } from './opencode/controlResponse'
import { openCodeExtractControl } from './opencode/extractControl'
import { openCodeToolCallAdapterFor } from './opencode/extractors/toolCall'
import { extractOpenCodeQuestions, sendOpenCodeQuestionRejectResponse, sendOpenCodeQuestionResponse } from './openCodeQuestions'

interface OpenCodeProtocolOptions {
  provider: AgentProvider
  /** Default primary-agent option, e.g. `'build'` for OpenCode, `'code'` for Kilo. */
  defaultPrimaryAgent: string
  /**
   * The kinds this provider knows that the shared protocol layer does not state.
   *
   * The two daemons share a wire format and run different TOOL SETS, so the identity
   * table is the one part of the family adapter that is per-provider.
   */
  toolKinds?: OpenCodeFamilyToolKinds
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
    toolCallAdapter: openCodeToolCallAdapterFor(opts.toolKinds),
    settingsConfig: {
      kind: 'optionGroup',
      optionGroupKey: PRIMARY_AGENT_KEY,
      defaultValue: opts.defaultPrimaryAgent,
    },
    // The two daemons answer a permission with one of their own option ids, and
    // send one back when the request itself offered none.
    extractControl: openCodeExtractControl,
    planValue: PLAN_PRIMARY_AGENT,
    // OpenCode and Kilo share the question-answer derivation from this single registration site
    // (mirroring the backend's questionRequestContext hook), so it can't drift per provider.
    controlResponseDisplay: openCodeControlResponseSummary,
    questionHandling: {
      isRequest: payload => payload?.type === OPENCODE_EVENT.QuestionAsked,
      extractQuestions: extractOpenCodeQuestions,
      sendAnswer: (request, sendControlResponse, questions, answerState) =>
        sendOpenCodeQuestionResponse(sendControlResponse, request.requestId, questions, answerState),
      sendReject: (request, sendControlResponse) =>
        sendOpenCodeQuestionRejectResponse(sendControlResponse, request.requestId),
    },
  })
}

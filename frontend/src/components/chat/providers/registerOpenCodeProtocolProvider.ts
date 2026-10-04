import type { ProviderTranscriptCapability } from './capabilities'
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
  /** Default primary agent: `'build'` for OpenCode or `'code'` for Kilo. */
  defaultPrimaryAgent: string
  /**
   * The provider's tool kinds that the shared protocol does not state.
   * The daemons share a wire format, but their tool sets differ.
   * Each provider supplies its own identity table to the family adapter.
   */
  toolKinds?: OpenCodeFamilyToolKinds
  /** The provider's pure reader of reported output file paths. */
  outputFilePaths?: ProviderTranscriptCapability['outputFilePaths']
}

const PRIMARY_AGENT_KEY = 'primaryAgent'
const PLAN_PRIMARY_AGENT = 'plan'

/**
 * Register a provider that speaks the OpenCode question and control protocol.
 * Each provider supplies its enum and default primary agent.
 * Kilo also supplies its additional tool kinds.
 */
export function registerOpenCodeProtocolProvider(opts: OpenCodeProtocolOptions): void {
  registerACPProvider({
    provider: opts.provider,
    ...(opts.outputFilePaths ? { outputFilePaths: opts.outputFilePaths } : {}),
    toolCallAdapter: openCodeToolCallAdapterFor(opts.toolKinds),
    settingsConfig: {
      kind: 'optionGroup',
      optionGroupKey: PRIMARY_AGENT_KEY,
      defaultValue: opts.defaultPrimaryAgent,
    },
    // Each daemon uses its own permission option IDs, including requests that supply no options.
    extractControl: openCodeExtractControl,
    planValue: PLAN_PRIMARY_AGENT,
    attachments: { text: true, image: true, pdf: true, binary: false },
    // This registration supplies the same question handling for both providers.
    // The backend's questionRequestContext hook also supplies one family implementation.
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

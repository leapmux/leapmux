import type { ProviderTranscriptCapability } from './capabilities'
import type { OpenCodeFamilyVocabulary } from './opencode/extractors/toolCall'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { OPENCODE_EVENT } from '~/generated/contracts/opencode-protocol'
import { registerACPProvider } from './acp/registerACPProvider'
import { openCodeControlResponseSummary } from './opencode/controlResponse'
import { openCodeExtractControl } from './opencode/extractControl'
import { openCodeToolCallAdapterFor } from './opencode/extractors/toolCall'
import { extractOpenCodeQuestions, sendOpenCodeQuestionRejectResponse, sendOpenCodeQuestionResponse } from './openCodeQuestions'

/**
 * What one provider of the family supplies: its identity, and the vocabulary the shared
 * tool-call adapter reads (`OpenCodeFamilyVocabulary`).
 */
interface OpenCodeProtocolOptions extends OpenCodeFamilyVocabulary {
  provider: AgentProvider
  /** Default primary agent: `'build'` for OpenCode or `'code'` for Kilo. */
  defaultPrimaryAgent: string
  /** The provider's pure reader of reported output file paths. */
  outputFilePaths?: ProviderTranscriptCapability['outputFilePaths']
}

const PRIMARY_AGENT_KEY = 'primaryAgent'
const PLAN_PRIMARY_AGENT = 'plan'

/**
 * Register a provider that speaks the OpenCode question and control protocol.
 *
 * Each provider supplies these values:
 *   - Its enum.
 *   - Its default primary agent.
 *   - Its refusal errors.
 *
 * Kilo also supplies its additional tool kinds.
 */
export function registerOpenCodeProtocolProvider(opts: OpenCodeProtocolOptions): void {
  registerACPProvider({
    provider: opts.provider,
    ...(opts.outputFilePaths ? { outputFilePaths: opts.outputFilePaths } : {}),
    toolCallAdapter: openCodeToolCallAdapterFor(opts),
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

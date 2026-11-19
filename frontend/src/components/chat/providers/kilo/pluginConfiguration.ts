import type { ProviderTranscriptCapability } from '../capabilities'
import type { KiloToolVocabulary } from './extractors/toolCall'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { OPENCODE_AUDIO_MIME, OPENCODE_EVENT } from '~/generated/contracts/opencode-protocol'
import { registerACPProvider } from '../acp/registerACPProvider'
import { openCodeNativeNotifications } from '../openCodeEvents'
import { openCodePermissionPresets } from '../openCodePermissionPresets'
import { extractOpenCodeQuestions, sendOpenCodeQuestionRejectResponse, sendOpenCodeQuestionResponse } from '../openCodeQuestions'
import { kiloControlResponseSummary } from './controlResponse'
import { kiloExtractControl } from './extractControl'
import { kiloToolCallAdapterFor } from './extractors/toolCall'

/**
 * Kilo's identity, default agent, and vocabulary for its ACP adapter.
 */
interface KiloProtocolOptions extends KiloToolVocabulary {
  provider: AgentProvider
  /** Default primary agent, normally `'code'`. */
  defaultPrimaryAgent: string
  /** The provider's pure reader of reported output file paths. */
  outputFilePaths?: ProviderTranscriptCapability['outputFilePaths']
}

const PRIMARY_AGENT_KEY = 'primaryAgent'
const PLAN_PRIMARY_AGENT = 'plan'

/**
 * Register Kilo's Agent Client Protocol plugin.
 *
 * The registration supplies these values:
 *   - Its enum.
 *   - Its default primary agent.
 *   - Its refusal errors.
 *
 * It also supplies Kilo's tool kinds.
 */
export function registerKiloProtocolProvider(opts: KiloProtocolOptions): void {
  registerACPProvider({
    provider: opts.provider,
    nativeNotifications: openCodeNativeNotifications,
    ...(opts.outputFilePaths
      ? { outputFilePaths: opts.outputFilePaths }
      : {}),
    toolCallAdapter: kiloToolCallAdapterFor(opts),
    settingsConfig: {
      kind: 'optionGroup',
      optionGroupKey: PRIMARY_AGENT_KEY,
      defaultValue: opts.defaultPrimaryAgent,
    },
    // Each daemon uses its own permission option IDs, including requests that supply no options.
    extractControl: kiloExtractControl,
    planValue: PLAN_PRIMARY_AGENT,
    permissionPresets: openCodePermissionPresets,
    attachments: {
      text: true,
      image: true,
      pdf: true,
      audio: [...OPENCODE_AUDIO_MIME],
      binary: false,
    },
    // This registration supplies the same question handling for both providers.
    // The backend's questionRequestContext hook also supplies one family implementation.
    controlResponseDisplay: kiloControlResponseSummary,
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

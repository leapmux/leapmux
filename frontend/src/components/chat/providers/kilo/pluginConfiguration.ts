import type { ProviderTranscriptCapability } from '../capabilities'
import type { KiloToolVocabulary } from './extractors/toolCall'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { OPENCODE_AUDIO_MIME } from '~/generated/contracts/opencode-protocol'
import { registerACPProvider } from '../acp/registerACPProvider'
import { openCodeNativeNotifications } from '../openCodeEvents'
import { openCodePermissionPresets } from '../openCodePermissionPresets'
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
    planValue: PLAN_PRIMARY_AGENT,
    permissionPresets: openCodePermissionPresets,
    attachments: {
      text: true,
      image: true,
      pdf: true,
      audio: [...OPENCODE_AUDIO_MIME],
      binary: false,
    },
  })
}

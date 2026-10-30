import { KILO_AUDIO_MIME } from '~/generated/contracts/kilo-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerACPProvider } from '../acp/registerACPProvider'
import { clientAutoApprovePermissionPresets } from '../clientApproval'
import { kiloControlResponseSummary } from './controlResponse'
import { kiloExtractControl } from './extractControl'
import { kiloNativeNotifications } from './extractors/notification'
import { kiloOutputFilePaths } from './extractors/outputFilePaths'
import { kiloToolCallAdapterFor } from './extractors/toolCall'
import { kiloQuestionHandling } from './pluginControls'
import { KILO_REFUSED_TOOL_ERRORS } from './protocol'
import { kiloToolKind } from './toolKinds'

registerACPProvider({
  provider: AgentProvider.KILO,
  nativeNotifications: kiloNativeNotifications,
  outputFilePaths: kiloOutputFilePaths,
  toolCallAdapter: kiloToolCallAdapterFor({ refusals: KILO_REFUSED_TOOL_ERRORS, toolKinds: kiloToolKind }),
  settingsConfig: { kind: 'optionGroup', optionGroupKey: 'primaryAgent', defaultValue: 'code' },
  extractControl: kiloExtractControl,
  planValue: 'plan',
  permissionPresets: clientAutoApprovePermissionPresets,
  attachments: {
    text: true,
    image: true,
    pdf: true,
    audio: [...KILO_AUDIO_MIME],
    binary: false,
  },
  controlResponseDisplay: kiloControlResponseSummary,
  questionHandling: kiloQuestionHandling,
})

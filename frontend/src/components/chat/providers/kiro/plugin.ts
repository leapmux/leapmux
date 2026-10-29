import { KIRO_BINARY_MIME, KIRO_CONFIG, KIRO_MODE } from '~/generated/contracts/kiro-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerACPProvider } from '../acp/registerACPProvider'
import { kiroAssistantTextSegments } from './assistantText'
import { kiroAgentTurnEnd } from './classification'
import { kiroControlResponseSummary, kiroPermissionRejectReason } from './controlResponse'
import { kiroElicitation, kiroExtractControl } from './extractControl'
import { kiroOutputFilePaths } from './extractors/outputFilePaths'
import { kiroToolCallAdapter } from './extractors/toolCall'
import { kiroNativeNotifications } from './notification'
import { kiroPermissionPresets } from './permissionPresets'
import { kiroQuestionHandling } from './pluginControls'

registerACPProvider({
  outputFilePaths: kiroOutputFilePaths,
  provider: AgentProvider.KIRO,
  effortGroupKey: KIRO_CONFIG.EffortLevel,
  toolCallAdapter: kiroToolCallAdapter,
  defaultPermissionMode: KIRO_MODE.Default,
  planValue: KIRO_MODE.Plan,
  // Kiro's policy preset is a LeapMux option, because Kiro never reports it back.
  // Kiro has no preset between its own rules and every call, so Smart has no match.
  permissionPresets: kiroPermissionPresets,
  // Match the worker's ValidateAttachment policy:
  // - Text.
  // - Images.
  // - PDF files.
  // - Word and Excel files.
  attachments: {
    text: true,
    image: true,
    pdf: true,
    audio: false,
    binary: [...KIRO_BINARY_MIME],
  },
  extractControl: kiroExtractControl,
  elicitation: kiroElicitation,
  controlResponseDisplay: kiroControlResponseSummary,
  permissionRejectReason: kiroPermissionRejectReason,
  questionHandling: kiroQuestionHandling,
  agentTurnEnd: kiroAgentTurnEnd,
  assistantTextSegments: kiroAssistantTextSegments,
  nativeNotifications: kiroNativeNotifications,
})

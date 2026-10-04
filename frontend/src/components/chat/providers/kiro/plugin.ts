import { KIRO_CONFIG, KIRO_MODE } from '~/generated/contracts/kiro-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerACPProvider } from '../acp/registerACPProvider'
import { kiroAgentTurnEnd } from './classification'
import { kiroControlResponseSummary, kiroPermissionRejectReason } from './controlResponse'
import { kiroElicitation, kiroExtractControl } from './extractControl'
import { kiroOutputFilePaths } from './extractors/outputFilePaths'
import { kiroToolCallAdapter } from './extractors/toolCall'
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
  attachments: { text: true, image: true, pdf: true, binary: false },
  extractControl: kiroExtractControl,
  elicitation: kiroElicitation,
  controlResponseDisplay: kiroControlResponseSummary,
  permissionRejectReason: kiroPermissionRejectReason,
  questionHandling: kiroQuestionHandling,
  agentTurnEnd: kiroAgentTurnEnd,
})

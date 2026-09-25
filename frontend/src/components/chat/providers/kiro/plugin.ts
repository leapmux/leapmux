import { KIRO_CONFIG, KIRO_MODE, KIRO_OPTION, KIRO_POLICY_PRESET } from '~/generated/contracts/kiro-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerACPProvider } from '../acp/registerACPProvider'
import { kiroAgentTurnEnd } from './classification'
import { kiroControlResponseSummary, kiroPermissionRejectReason } from './controlResponse'
import { kiroElicitation, kiroExtractControl } from './extractControl'
import { kiroToolCallAdapter } from './extractors/toolCall'
import { kiroQuestionHandling } from './pluginControls'

registerACPProvider({
  provider: AgentProvider.KIRO,
  effortGroupKey: KIRO_CONFIG.EffortLevel,
  toolCallAdapter: kiroToolCallAdapter,
  defaultPermissionMode: KIRO_MODE.Default,
  planValue: KIRO_MODE.Plan,
  // Kiro's policy preset is a LeapMux option, because Kiro never reports it back.
  // Kiro has no preset between its own rules and every call, so Smart has no match.
  permissionPresets: {
    bypass: { sets: { [KIRO_OPTION.PolicyPreset]: KIRO_POLICY_PRESET.AllowAll } },
  },
  // The same policy as the worker's ValidateAttachment: text, images and PDFs.
  attachments: { text: true, image: true, pdf: true, binary: false },
  extractControl: kiroExtractControl,
  elicitation: kiroElicitation,
  controlResponseDisplay: kiroControlResponseSummary,
  permissionRejectReason: kiroPermissionRejectReason,
  questionHandling: kiroQuestionHandling,
  agentTurnEnd: kiroAgentTurnEnd,
})

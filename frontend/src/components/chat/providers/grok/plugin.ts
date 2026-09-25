import { GROK_APPROVAL_MODE, GROK_CONFIG, GROK_MODE, GROK_OPTION } from '~/generated/contracts/grok-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerACPProvider } from '../acp/registerACPProvider'
import { grokAgentTurnEnd } from './classification'
import { grokControlResponseSummary, grokPermissionRejectReason } from './controlResponse'
import { grokElicitation, grokExtractControl } from './extractControl'
import { grokToolCallAdapter } from './extractors/toolCall'
import { grokQuestionHandling } from './pluginControls'

registerACPProvider({
  provider: AgentProvider.GROK_BUILD,
  effortGroupKey: GROK_CONFIG.ReasoningEffort,
  toolCallAdapter: grokToolCallAdapter,
  defaultPermissionMode: GROK_MODE.Default,
  planValue: GROK_MODE.Plan,
  // Grok's approval mode is a LeapMux option, because Grok never reports its own.
  permissionPresets: {
    smart: { sets: { [GROK_OPTION.ApprovalMode]: GROK_APPROVAL_MODE.Auto } },
    bypass: { sets: { [GROK_OPTION.ApprovalMode]: GROK_APPROVAL_MODE.AlwaysApprove } },
  },
  extractControl: grokExtractControl,
  elicitation: grokElicitation,
  controlResponseDisplay: grokControlResponseSummary,
  permissionRejectReason: grokPermissionRejectReason,
  questionHandling: grokQuestionHandling,
  preservesSelectionNotes: true,
  agentTurnEnd: grokAgentTurnEnd,
})

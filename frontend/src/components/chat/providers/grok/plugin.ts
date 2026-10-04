import { GROK_CONFIG, GROK_MODE } from '~/generated/contracts/grok-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerACPProvider } from '../acp/registerACPProvider'
import { grokAgentTurnEnd } from './classification'
import { grokControlResponseSummary, grokPermissionRejectReason } from './controlResponse'
import { grokElicitation, grokExtractControl } from './extractControl'
import { grokOutputFilePaths } from './extractors/outputFilePaths'
import { grokToolCallAdapter } from './extractors/toolCall'
import { grokPermissionPresets } from './permissionPresets'
import { grokQuestionHandling } from './pluginControls'

registerACPProvider({
  outputFilePaths: grokOutputFilePaths,
  provider: AgentProvider.GROK_BUILD,
  effortGroupKey: GROK_CONFIG.ReasoningEffort,
  toolCallAdapter: grokToolCallAdapter,
  defaultPermissionMode: GROK_MODE.Default,
  planValue: GROK_MODE.Plan,
  // Grok's approval mode is a LeapMux option, because Grok never reports its own.
  permissionPresets: grokPermissionPresets,
  extractControl: grokExtractControl,
  elicitation: grokElicitation,
  controlResponseDisplay: grokControlResponseSummary,
  permissionRejectReason: grokPermissionRejectReason,
  questionHandling: grokQuestionHandling,
  preservesSelectionNotes: true,
  agentTurnEnd: grokAgentTurnEnd,
})

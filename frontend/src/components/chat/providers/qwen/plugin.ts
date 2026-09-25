import { QWEN_CONFIG, QWEN_MODE } from '~/generated/contracts/qwen-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { OPTION_ID_PERMISSION_MODE } from '../../settingsGroups'
import { registerACPProvider } from '../acp/registerACPProvider'
import { qwenAgentTurnEnd } from './classification'
import { qwenControlResponseSummary } from './controlResponse'
import { qwenExtractControl } from './extractControl'
import { qwenToolCallAdapter } from './extractors/toolCall'
import { qwenQuestionHandling } from './pluginControls'

registerACPProvider({
  provider: AgentProvider.QWEN_CODE,
  effortGroupKey: QWEN_CONFIG.ReasoningEffort,
  toolCallAdapter: qwenToolCallAdapter,
  defaultPermissionMode: QWEN_MODE.Default,
  planValue: QWEN_MODE.Plan,
  // Qwen's approval modes ARE its session modes, on the permission-mode axis.
  permissionPresets: {
    smart: { sets: { [OPTION_ID_PERMISSION_MODE]: QWEN_MODE.Auto } },
    bypass: { sets: { [OPTION_ID_PERMISSION_MODE]: QWEN_MODE.Yolo } },
  },
  extractControl: qwenExtractControl,
  controlResponseDisplay: qwenControlResponseSummary,
  questionHandling: qwenQuestionHandling,
  agentTurnEnd: qwenAgentTurnEnd,
})

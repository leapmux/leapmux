import { QWEN_CONFIG, QWEN_MODE } from '~/generated/contracts/qwen-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerACPProvider } from '../acp/registerACPProvider'

import { qwenAgentTurnEnd } from './classification'
import { qwenControlResponseSummary } from './controlResponse'
import { qwenExtractControl } from './extractControl'
import { qwenOutputFilePaths } from './extractors/outputFilePaths'
import { qwenToolCallAdapter } from './extractors/toolCall'
import { qwenPermissionPresets } from './permissionPresets'
import { qwenQuestionHandling } from './pluginControls'

registerACPProvider({
  provider: AgentProvider.QWEN_CODE,
  attachments: { text: true, image: true, pdf: true, binary: false },
  effortGroupKey: QWEN_CONFIG.ReasoningEffort,
  toolCallAdapter: qwenToolCallAdapter,
  outputFilePaths: qwenOutputFilePaths,
  defaultPermissionMode: QWEN_MODE.Default,
  planValue: QWEN_MODE.Plan,
  // Qwen uses its session modes as approval modes on the permission-mode axis.
  permissionPresets: qwenPermissionPresets,
  extractControl: qwenExtractControl,
  controlResponseDisplay: qwenControlResponseSummary,
  questionHandling: qwenQuestionHandling,
  agentTurnEnd: qwenAgentTurnEnd,
})

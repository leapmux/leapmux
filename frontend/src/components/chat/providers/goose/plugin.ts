import { GOOSE_CONFIG, GOOSE_DEFAULT_MODE } from '~/generated/contracts/goose-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerACPProvider } from '../acp/registerACPProvider'
import { classifyGooseToolCallUpdate } from './classification'
import { gooseOutputFilePaths } from './extractors/outputFilePaths'
import { gooseToolCallAdapter } from './extractors/toolCall'
import { goosePermissionPresets } from './permissionPresets'

registerACPProvider({
  toolCallAdapter: gooseToolCallAdapter,
  outputFilePaths: gooseOutputFilePaths,
  provider: AgentProvider.GOOSE,
  effortGroupKey: GOOSE_CONFIG.ThinkingEffort,
  defaultPermissionMode: GOOSE_DEFAULT_MODE,
  permissionPresets: goosePermissionPresets,
  classifyToolCallUpdate: classifyGooseToolCallUpdate,
  attachments: { text: true, image: true, pdf: false, audio: false, binary: false },
})

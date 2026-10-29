import { REASONIX_MODE } from '~/generated/contracts/reasonix-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerACPProvider } from '../acp/registerACPProvider'
import { reasonixControlResponseSummary } from './controlResponse'
import { reasonixExtractControl } from './extractControl'
import { reasonixOutputFilePaths } from './extractors/outputFilePaths'
import { reasonixToolCallAdapter } from './extractors/toolCall'
import { reasonixNativeNotifications } from './notification'
import { reasonixPermissionPresets } from './permissionPresets'

registerACPProvider({
  toolCallAdapter: reasonixToolCallAdapter,
  provider: AgentProvider.REASONIX,
  defaultPermissionMode: REASONIX_MODE.Normal,
  planValue: REASONIX_MODE.Plan,
  extractControl: reasonixExtractControl,
  controlResponseDisplay: reasonixControlResponseSummary,
  permissionPresets: reasonixPermissionPresets,
  nativeNotifications: reasonixNativeNotifications,
  attachments: {
    text: true,
    image: true,
    pdf: true,
    audio: false,
    binary: true,
  },
  outputFilePaths: reasonixOutputFilePaths,
})

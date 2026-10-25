import { REASONIX_MODE } from '~/generated/contracts/reasonix-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerACPProvider } from '../acp/registerACPProvider'
import { reasonixControlResponseSummary } from './controlResponse'
import { reasonixElicitation } from './elicitation'
import { reasonixExtractControl } from './extractControl'
import { reasonixToolCallAdapter } from './extractors/toolCall'
import { reasonixNativeNotifications } from './notification'
import { reasonixPermissionPresets } from './permissionPresets'

registerACPProvider({
  toolCallAdapter: reasonixToolCallAdapter,
  provider: AgentProvider.REASONIX,
  defaultPermissionMode: REASONIX_MODE.Normal,
  planValue: REASONIX_MODE.Plan,
  elicitation: reasonixElicitation,
  extractControl: reasonixExtractControl,
  controlResponseDisplay: reasonixControlResponseSummary,
  permissionPresets: reasonixPermissionPresets,
  nativeNotifications: reasonixNativeNotifications,
  // Reasonix advertises text input without image support.
  attachments: { text: true, image: false, pdf: false, binary: false },
})

import { REASONIX_APPROVAL, REASONIX_CONFIG, REASONIX_MODE } from '~/generated/contracts/reasonix-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerACPProvider } from '../acp/registerACPProvider'
import { reasonixToolAdapter } from './toolPresentation'

registerACPProvider({
  toolAdapter: reasonixToolAdapter,
  provider: AgentProvider.REASONIX,
  defaultPermissionMode: REASONIX_MODE.Normal,
  planValue: REASONIX_MODE.Plan,
  permissionPresets: {
    bypass: { sets: { [REASONIX_CONFIG.ToolApproval]: REASONIX_APPROVAL.Yolo } },
  },
  // Reasonix advertises text input without image support.
  attachments: { text: true, image: false, pdf: false, binary: false },
})

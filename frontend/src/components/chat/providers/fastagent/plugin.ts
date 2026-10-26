import { FASTAGENT_AUDIO_MIME, FASTAGENT_MODE } from '~/generated/contracts/fastagent-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerACPProvider } from '../acp/registerACPProvider'
import { fastAgentOutputFilePaths } from './extractors/outputFilePaths'
import { fastAgentToolCallAdapter } from './extractors/toolCall'
import { fastAgentPermissionPresets } from './permissionPresets'

registerACPProvider({
  permissionPresets: fastAgentPermissionPresets,
  provider: AgentProvider.FAST_AGENT,
  toolCallAdapter: fastAgentToolCallAdapter,
  outputFilePaths: fastAgentOutputFilePaths,
  defaultPermissionMode: FASTAGENT_MODE.Agent,
  // Fast Agent exposes no plan mode and no live model switch over ACP.
  // The native launch policy controls tool permission rules and prompts.
  attachments: { text: true, image: true, pdf: true, audio: [...FASTAGENT_AUDIO_MIME], binary: false },
})

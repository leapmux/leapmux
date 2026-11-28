import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerACPProvider } from '../acp/registerACPProvider'
import { cursorToolCallAdapter } from './extractors/toolCall'
import { cursorPermissionPresets } from './permissionPresets'

registerACPProvider({
  provider: AgentProvider.CURSOR,
  toolCallAdapter: cursorToolCallAdapter,
  attachments: {
    text: true,
    image: true,
    pdf: false,
    audio: false,
    binary: false,
  },
  defaultPermissionMode: 'agent',
  permissionPresets: cursorPermissionPresets,
  planValue: 'plan',
})

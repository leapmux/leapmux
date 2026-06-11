import type { PermissionMode } from '~/utils/controlResponse'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { registerACPProvider } from '../acp/registerACPProvider'

const GOOSE_MODE_AUTO = 'auto' as PermissionMode

registerACPProvider({
  provider: AgentProvider.GOOSE,
  defaultPermissionMode: GOOSE_MODE_AUTO,
  bypassPermissionMode: GOOSE_MODE_AUTO,
})

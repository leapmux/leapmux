import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { kiloTest } from '../kilo-fixtures'
import { readOpenCodeShellOutcome } from '../opencode/nativeShellOutcome'

kiloTest('keeps actual native shell output and a failed command result', async ({ authenticatedKiloWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiloWorkspace.workspaceId, provider: AgentProvider.KILO }
  await exerciseShellToolExecution({ ...context, readToolResult: (request, callId) => readOpenCodeShellOutcome(context, request, callId) })
})

import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { exerciseNativeCodeExecution, nativeCodeExecutionSchema } from '../helpers/nativeCodeExecution'
import { withNativeWorker } from '../helpers/nativeWorker'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace } from '../helpers/ui'
import { mimoTest } from '../mimo-fixtures'

mimoTest('runs native code and retains computed output and script errors after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  await withNativeWorker(leapmuxServer, { dataDirPrefix: 'native-code-worker', workerName: 'Native code executor', env: { MIMOCODE_ENABLE_EXEC_TOOL: '1' } }, async ({ server }) => {
    const context = { page, modelScript, leapmuxServer: server, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.MIMO_CODE }
    await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, context.workspaceId, createTestDirectory('native-code-execution-'), agentOpenOptions(context.provider))
    await openWorkspace(page, context.workspaceId)
    await exerciseNativeCodeExecution(context, {
      catalogProof: (request) => {
        nativeCodeExecutionSchema(request, 'exec', { code: 'string' })
      },
      scripts: marker => [
        { label: 'output', source: `return ${JSON.stringify(marker)} + (40 + 2);`, expected: `${marker}42`, failed: false },
        { label: 'failure', source: `throw new Error(${JSON.stringify(marker)} + (70 + 7));`, expected: `${marker}77`, failed: true },
      ],
    })
  })
})

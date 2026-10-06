import { openNativeAgent } from '../helpers/nativeAgentOpen'
import { exerciseNativeCodeExecution, nativeCodeExecutionSchema } from '../helpers/nativeCodeExecution'
import { withNativeWorker } from '../helpers/nativeWorker'
import { MIMO_AGENT, mimoTest } from '../mimo-fixtures'
import { nativeContext } from './scenarios'

mimoTest('runs native code and retains computed output and script errors after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  await withNativeWorker(leapmuxServer, { dataDirPrefix: 'native-code-worker', workerName: 'Native code executor', env: { MIMOCODE_ENABLE_EXEC_TOOL: '1' } }, async ({ server }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer: server, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await openNativeAgent(context, MIMO_AGENT, { directoryPrefix: 'native-code-execution-' })
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

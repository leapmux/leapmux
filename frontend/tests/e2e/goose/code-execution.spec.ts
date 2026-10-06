import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { gooseTest } from '../goose-fixtures'
import { openNativeAgent } from '../helpers/nativeAgentOpen'
import { exerciseNativeCodeExecution, nativeCodeExecutionSchema } from '../helpers/nativeCodeExecution'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { getGlobalState } from '../helpers/server'
import { toolCallRow } from '../helpers/ui'
import { nativeContext } from './scenarios'

gooseTest('runs native code and retains computed output and script errors after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const config = join(leapmuxServer.agentEnv.GOOSE_PATH_ROOT!, 'config', 'config.yaml')
  const content = readFileSync(config, 'utf8').replace('extensions:\n', 'extensions:\n  code_execution:\n    enabled: true\n    type: platform\n    name: code_execution\n')
  await withNativeConfigurationFile({ path: config, content, runDir: getGlobalState().tmpDir }, async () => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await openNativeAgent(context, { directoryPrefix: 'native-code-execution-' })
    await exerciseNativeCodeExecution(context, {
      catalogProof: (request) => {
        nativeCodeExecutionSchema(request, 'execute_typescript', { code: 'string' })
      },
      prepareResultView: async (callId) => {
        await expandNativeResultView(toolCallRow(page, callId))
      },
      scripts: marker => [
        { label: 'output', source: `async function run() { return ${JSON.stringify(marker)} + (40 + 2); }`, expected: `${marker}42`, failed: false },
        { label: 'failure', source: `async function run() { throw new Error(${JSON.stringify(marker)} + (70 + 7)); }`, expected: `${marker}77`, failed: true },
      ],
    })
  })
})

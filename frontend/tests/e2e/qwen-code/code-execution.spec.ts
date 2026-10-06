import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { openNativeAgent } from '../helpers/nativeAgentOpen'
import { exerciseNativeCodeExecution, nativeCodeExecutionSchema } from '../helpers/nativeCodeExecution'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { getGlobalState } from '../helpers/server'
import { QWEN_AGENT, qwenTest } from '../qwen-fixtures'
import { nativeContext } from './scenarios'

qwenTest('runs native code and retains computed output and script errors after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const config = join(leapmuxServer.agentEnv.QWEN_HOME!, 'settings.json')
  const settings = JSON.parse(readFileSync(config, 'utf8'))
  const content = JSON.stringify({ ...settings, tools: { ...settings.tools, codeModeOnly: true } })
  await withNativeConfigurationFile({ path: config, content, runDir: getGlobalState().tmpDir }, async () => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await openNativeAgent(context, QWEN_AGENT, { directoryPrefix: 'native-code-execution-' })
    await exerciseNativeCodeExecution(context, {
      catalogProof: (request) => {
        nativeCodeExecutionSchema(request, 'exec', { source: 'string' })
      },
      scripts: marker => [
        { label: 'output', source: `text(${JSON.stringify(marker)} + (40 + 2));`, expected: `${marker}42`, failed: false },
        { label: 'failure', source: `throw new Error(${JSON.stringify(marker)} + (70 + 7));`, expected: `${marker}77`, failed: true },
      ],
    })
  })
})

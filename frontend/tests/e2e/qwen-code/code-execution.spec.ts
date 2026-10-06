import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { exerciseNativeCodeExecution, nativeCodeExecutionSchema } from '../helpers/nativeCodeExecution'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { createTestDirectory } from '../helpers/runDirectory'
import { getGlobalState } from '../helpers/server'
import { openWorkspace } from '../helpers/ui'
import { qwenTest } from '../qwen-fixtures'

qwenTest('runs native code and retains computed output and script errors after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const config = join(leapmuxServer.agentEnv.QWEN_HOME!, 'settings.json')
  const settings = JSON.parse(readFileSync(config, 'utf8'))
  const content = JSON.stringify({ ...settings, tools: { ...settings.tools, codeModeOnly: true } })
  await withNativeConfigurationFile({ path: config, content, runDir: getGlobalState().tmpDir }, async () => {
    const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.QWEN_CODE }
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, context.workspaceId, createTestDirectory('native-code-execution-'), agentOpenOptions(context.provider))
    await openWorkspace(page, context.workspaceId)
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

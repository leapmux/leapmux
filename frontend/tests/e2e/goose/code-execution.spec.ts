import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { gooseTest } from '../goose-fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { exerciseNativeCodeExecution, nativeCodeExecutionSchema } from '../helpers/nativeCodeExecution'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { createTestDirectory } from '../helpers/runDirectory'
import { getGlobalState } from '../helpers/server'
import { openWorkspace } from '../helpers/ui'

gooseTest('runs native code and retains computed output and script errors after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const config = join(leapmuxServer.agentEnv.GOOSE_PATH_ROOT!, 'config', 'config.yaml')
  const content = readFileSync(config, 'utf8').replace('extensions:\n', 'extensions:\n  code_execution:\n    enabled: true\n    type: platform\n    name: code_execution\n')
  await withNativeConfigurationFile({ path: config, content, runDir: getGlobalState().tmpDir }, async () => {
    const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.GOOSE }
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, context.workspaceId, createTestDirectory('native-code-execution-'), { agentProvider: context.provider, ...agentOpenOptions(agentSettings(context.provider)) })
    await openWorkspace(page, context.workspaceId)
    await exerciseNativeCodeExecution(context, {
      catalogProof: (request) => {
        nativeCodeExecutionSchema(request, 'execute_typescript', { code: 'string' })
      },
      prepareResultView: async (callId) => {
        const bubble = page.locator(`[data-testid="message-bubble"][data-tool-call-id="${callId}"][data-tool-row-role="result"]:visible`)
        await expandNativeResultView(bubble)
      },
      scripts: marker => [
        { label: 'output', source: `async function run() { return ${JSON.stringify(marker)} + (40 + 2); }`, expected: `${marker}42`, failed: false },
        { label: 'failure', source: `async function run() { throw new Error(${JSON.stringify(marker)} + (70 + 7)); }`, expected: `${marker}77`, failed: true },
      ],
    })
  })
})

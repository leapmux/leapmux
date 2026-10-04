import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { exerciseNativeCodeExecution, nativeCodeExecutionSchema } from '../helpers/nativeCodeExecution'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace } from '../helpers/ui'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest('runs native code and retains computed output and script errors after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.OH_MY_PI }
  await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, context.workspaceId, createTestDirectory('native-code-execution-'), { agentProvider: context.provider, ...agentOpenOptions(agentSettings(context.provider)) })
  await openWorkspace(page, context.workspaceId)
  await exerciseNativeCodeExecution(context, {
    catalogProof: (request) => {
      nativeCodeExecutionSchema(request, 'eval', { language: 'string', code: 'string' })
    },
    scripts: marker => [
      { label: 'output', source: `console.log(${JSON.stringify(marker)} + (40 + 2));`, expected: `${marker}42`, failed: false },
      { label: 'failure', source: `throw new Error(${JSON.stringify(marker)} + (70 + 7));`, expected: `${marker}77`, failed: true },
    ],
  })
})

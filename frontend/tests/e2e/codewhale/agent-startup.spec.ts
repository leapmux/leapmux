import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEWHALE_E2E_SKIP_REASON, codewhaleTest } from '../codewhale-fixtures'
import { findBinary } from '../helpers/binaryOnPath'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { hubSpawnEnv } from '../helpers/server'

codewhaleTest.skip(!!CODEWHALE_E2E_SKIP_REASON, CODEWHALE_E2E_SKIP_REASON || '')

codewhaleTest('delivers input through a controlled native startup', async ({ page, modelScript, leapmuxServer, authenticatedCodewhaleWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCodewhaleWorkspace.workspaceId, provider: AgentProvider.CODEWHALE }
  const executable = findBinary('codewhale', hubSpawnEnv(leapmuxServer.agentEnv))
  expect(executable).not.toBeNull()
  if (!executable)
    throw new Error('The installed native startup executable is absent.')
  await exerciseAgentStartup(context, { launch: { binaryName: 'codewhale', executable, holdWhen: ['app-server'], lazy: false } })
})

codewhaleTest('retains input after the actual native launch fails', async ({ page, modelScript, leapmuxServer, authenticatedCodewhaleWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCodewhaleWorkspace.workspaceId, provider: AgentProvider.CODEWHALE }
  const executable = findBinary('codewhale', hubSpawnEnv(leapmuxServer.agentEnv))
  expect(executable).not.toBeNull()
  if (!executable)
    throw new Error('The installed native startup executable is absent.')
  await exerciseAgentStartup(context, { launch: { binaryName: 'codewhale', executable, holdWhen: ['app-server'], lazy: false }, failed: true })
})

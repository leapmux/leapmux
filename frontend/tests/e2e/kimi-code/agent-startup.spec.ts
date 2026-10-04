import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { findBinary } from '../helpers/binaryOnPath'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { hubSpawnEnv } from '../helpers/server'
import { KIMI_E2E_SKIP_REASON, kimiTest } from '../kimi-fixtures'

kimiTest.skip(!!KIMI_E2E_SKIP_REASON, KIMI_E2E_SKIP_REASON || '')

kimiTest('delivers input through a controlled native startup', async ({ page, modelScript, leapmuxServer, authenticatedKimiWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKimiWorkspace.workspaceId, provider: AgentProvider.KIMI_CODE }
  const executable = findBinary('kimi', hubSpawnEnv(leapmuxServer.agentEnv))
  expect(executable).not.toBeNull()
  if (!executable)
    throw new Error('The installed native startup executable is absent.')
  await exerciseAgentStartup(context, { launch: { binaryName: 'kimi', executable, holdWhen: ['web'], lazy: false } })
})

kimiTest('retains input after the actual native launch fails', async ({ page, modelScript, leapmuxServer, authenticatedKimiWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKimiWorkspace.workspaceId, provider: AgentProvider.KIMI_CODE }
  const executable = findBinary('kimi', hubSpawnEnv(leapmuxServer.agentEnv))
  expect(executable).not.toBeNull()
  if (!executable)
    throw new Error('The installed native startup executable is absent.')
  await exerciseAgentStartup(context, { launch: { binaryName: 'kimi', executable, holdWhen: ['web'], lazy: false }, failed: true })
})

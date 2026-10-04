import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { AMP_E2E_SKIP_REASON, ampTest } from '../amp-fixtures'
import { findBinary } from '../helpers/binaryOnPath'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { hubSpawnEnv } from '../helpers/server'

ampTest.skip(!!AMP_E2E_SKIP_REASON, AMP_E2E_SKIP_REASON || '')

ampTest('delivers input through a controlled native startup', async ({ page, modelScript, leapmuxServer, authenticatedAmpWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedAmpWorkspace.workspaceId, provider: AgentProvider.AMP }
  const executable = findBinary('amp', hubSpawnEnv(leapmuxServer.agentEnv))
  expect(executable).not.toBeNull()
  if (!executable)
    throw new Error('The installed native startup executable is absent.')
  await exerciseAgentStartup(context, { launch: { binaryName: 'amp', executable, holdWhen: ['--execute'], lazy: true } })
})

ampTest('retains input after the actual native launch fails', async ({ page, modelScript, leapmuxServer, authenticatedAmpWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedAmpWorkspace.workspaceId, provider: AgentProvider.AMP }
  const executable = findBinary('amp', hubSpawnEnv(leapmuxServer.agentEnv))
  expect(executable).not.toBeNull()
  if (!executable)
    throw new Error('The installed native startup executable is absent.')
  await exerciseAgentStartup(context, { launch: { binaryName: 'amp', executable, holdWhen: ['--execute'], lazy: true }, failed: true })
})

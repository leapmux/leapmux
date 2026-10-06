import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { findBinary } from '../helpers/binaryOnPath'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { hubSpawnEnv } from '../helpers/server'
import { createKiroWorkingDir, kiroTest } from '../kiro-fixtures'

kiroTest('delivers input through a controlled native startup', async ({ page, modelScript, leapmuxServer, authenticatedKiroWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiroWorkspace.workspaceId, provider: AgentProvider.KIRO }
  const executable = findBinary('kiro-cli-chat', hubSpawnEnv(leapmuxServer.agentEnv))
  expect(executable).not.toBeNull()
  if (!executable)
    throw new Error('The installed native startup executable is absent.')
  await exerciseAgentStartup(context, { launch: { binaryName: 'kiro-cli-chat', executable, holdWhen: ['acp'], lazy: false }, workingDir: createKiroWorkingDir() })
})

kiroTest('retains input after the actual native launch fails', async ({ page, modelScript, leapmuxServer, authenticatedKiroWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiroWorkspace.workspaceId, provider: AgentProvider.KIRO }
  const executable = findBinary('kiro-cli-chat', hubSpawnEnv(leapmuxServer.agentEnv))
  expect(executable).not.toBeNull()
  if (!executable)
    throw new Error('The installed native startup executable is absent.')
  await exerciseAgentStartup(context, { launch: { binaryName: 'kiro-cli-chat', executable, holdWhen: ['acp'], lazy: false }, workingDir: createKiroWorkingDir(), failed: true })
})

import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { exerciseTurnEndSound } from '../helpers/nativeTurnEndSound'
import { readToolCall } from '../helpers/providerToolCalls'
import { KIRO_E2E_SKIP_REASON, kiroTest } from '../kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

kiroTest('plays the chosen sound once after a native tool turn', async ({ page, modelScript, leapmuxServer, authenticatedKiroWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiroWorkspace.workspaceId, provider: AgentProvider.KIRO }
  const agent = await currentNativeAgent(context)
  const path = join(agent.workingDir, 'native-notification-file.txt')
  writeFileSync(path, 'Native notification file contents.\n')
  await exerciseTurnEndSound(context, { toolActivity: true, steps: [{ toolCalls: [readToolCall(AgentProvider.KIRO, 'notification-read', path)] }, { text: 'The native read and turn completed.' }] })
})

kiroTest('keeps a text-only native turn quiet', async ({ page, modelScript, leapmuxServer, authenticatedKiroWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiroWorkspace.workspaceId, provider: AgentProvider.KIRO }
  await exerciseTurnEndSound(context, { toolActivity: false })
})

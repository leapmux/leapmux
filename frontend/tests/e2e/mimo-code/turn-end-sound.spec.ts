import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { exerciseTurnEndSound } from '../helpers/nativeTurnEndSound'
import { readToolCall } from '../helpers/providerToolCalls'
import { mimoTest } from '../mimo-fixtures'

mimoTest('plays the chosen sound once after a native tool turn', async ({ page, modelScript, leapmuxServer, authenticatedMiMoWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedMiMoWorkspace.workspaceId, provider: AgentProvider.MIMO_CODE }
  const agent = await currentNativeAgent(context)
  const path = join(agent.workingDir, 'native-notification-file.txt')
  writeFileSync(path, 'Native notification file contents.\n')
  await exerciseTurnEndSound(context, { toolActivity: true, steps: [{ toolCalls: [readToolCall(AgentProvider.MIMO_CODE, 'notification-read', path)] }, { text: 'The native read and turn completed.' }] })
})

mimoTest('keeps a text-only native turn quiet', async ({ page, modelScript, leapmuxServer, authenticatedMiMoWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedMiMoWorkspace.workspaceId, provider: AgentProvider.MIMO_CODE }
  await exerciseTurnEndSound(context, { toolActivity: false })
})

import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { GROK_E2E_SKIP_REASON, grokTest } from '../grok-fixtures'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { exerciseTurnEndSound } from '../helpers/nativeTurnEndSound'
import { readToolCall } from '../helpers/providerToolCalls'

grokTest.skip(!!GROK_E2E_SKIP_REASON, GROK_E2E_SKIP_REASON || '')

grokTest('plays the chosen sound once after a native tool turn', async ({ page, modelScript, leapmuxServer, authenticatedGrokWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedGrokWorkspace.workspaceId, provider: AgentProvider.GROK_BUILD }
  const agent = await currentNativeAgent(context)
  const path = join(agent.workingDir, 'native-notification-file.txt')
  writeFileSync(path, 'Native notification file contents.\n')
  await exerciseTurnEndSound(context, { toolActivity: true, steps: [{ toolCalls: [readToolCall(AgentProvider.GROK_BUILD, 'notification-read', path)] }, { text: 'The native read and turn completed.' }] })
})

grokTest('keeps a text-only native turn quiet', async ({ page, modelScript, leapmuxServer, authenticatedGrokWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedGrokWorkspace.workspaceId, provider: AgentProvider.GROK_BUILD }
  await exerciseTurnEndSound(context, { toolActivity: false })
})

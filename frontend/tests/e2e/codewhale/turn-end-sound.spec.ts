import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codewhaleTest } from '../codewhale-fixtures'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { exerciseTurnEndSound } from '../helpers/nativeTurnEndSound'
import { readToolCall } from '../helpers/providerToolCalls'

codewhaleTest('plays the chosen sound once after a native tool turn', async ({ page, modelScript, leapmuxServer, authenticatedCodewhaleWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCodewhaleWorkspace.workspaceId, provider: AgentProvider.CODEWHALE }
  const agent = await currentNativeAgent(context)
  const path = join(agent.workingDir, 'native-notification-file.txt')
  writeFileSync(path, 'Native notification file contents.\n')
  await exerciseTurnEndSound(context, { toolActivity: true, steps: [{ toolCalls: [readToolCall(AgentProvider.CODEWHALE, 'notification-read', path)] }, { text: 'The native read and turn completed.' }] })
})

codewhaleTest('keeps a text-only native turn quiet', async ({ page, modelScript, leapmuxServer, authenticatedCodewhaleWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCodewhaleWorkspace.workspaceId, provider: AgentProvider.CODEWHALE }
  await exerciseTurnEndSound(context, { toolActivity: false })
})

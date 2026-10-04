import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseTurnEndSound } from '../helpers/nativeTurnEndSound'
import { piTodoToolCall } from '../helpers/providerToolCalls'
import { piTest } from '../pi-fixtures'

piTest('plays a selected sound once for native tool activity and keeps text-only turns quiet', async ({ authenticatedPiWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedPiWorkspace.workspaceId, provider: AgentProvider.PI }
  await exerciseTurnEndSound(context, { toolActivity: true, steps: [{ toolCalls: [piTodoToolCall('sound-enabled-tool', { action: 'create', subject: 'Native sound activity' })] }, { text: 'The native sound tool completed.' }] })
  await exerciseTurnEndSound(context, { toolActivity: false })
  await exerciseTurnEndSound(context, { toolActivity: true, sound: 'none', steps: [{ toolCalls: [piTodoToolCall('sound-muted-tool', { action: 'create', subject: 'Native sound activity' })] }, { text: 'The muted native tool completed.' }] })
})

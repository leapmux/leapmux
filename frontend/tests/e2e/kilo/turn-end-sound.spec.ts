import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseTurnEndSound } from '../helpers/nativeTurnEndSound'
import { updateTodosToolCall } from '../helpers/providerToolCalls'
import { kiloTest } from '../kilo-fixtures'

kiloTest('plays a selected sound once for native tool activity and keeps text-only turns quiet', async ({ authenticatedKiloWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiloWorkspace.workspaceId, provider: AgentProvider.KILO }
  await exerciseTurnEndSound(context, { toolActivity: true, steps: [{ toolCalls: [updateTodosToolCall(AgentProvider.KILO, 'sound-enabled-tool', [{ step: 'Native sound activity', status: 'in_progress' }])] }, { text: 'The native sound tool completed.' }] })
  await exerciseTurnEndSound(context, { toolActivity: false })
  await exerciseTurnEndSound(context, { toolActivity: true, sound: 'none', steps: [{ toolCalls: [updateTodosToolCall(AgentProvider.KILO, 'sound-muted-tool', [{ step: 'Native sound activity', status: 'in_progress' }])] }, { text: 'The muted native tool completed.' }] })
})

import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseTurnEndSound } from '../helpers/nativeTurnEndSound'
import { updateTodosToolCall } from '../helpers/providerToolCalls'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('plays a selected sound once for native tool activity and keeps text-only turns quiet', async ({ authenticatedOpencodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedOpencodeWorkspace.workspaceId, provider: AgentProvider.OPENCODE }
  await exerciseTurnEndSound(context, { toolActivity: true, steps: [{ toolCalls: [updateTodosToolCall(AgentProvider.OPENCODE, 'sound-enabled-tool', [{ step: 'Native sound activity', status: 'in_progress' }])] }, { text: 'The native sound tool completed.' }] })
  await exerciseTurnEndSound(context, { toolActivity: false })
  await exerciseTurnEndSound(context, { toolActivity: true, sound: 'none', steps: [{ toolCalls: [updateTodosToolCall(AgentProvider.OPENCODE, 'sound-muted-tool', [{ step: 'Native sound activity', status: 'in_progress' }])] }, { text: 'The muted native tool completed.' }] })
})

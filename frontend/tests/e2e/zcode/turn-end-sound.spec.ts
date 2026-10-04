import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseTurnEndSound } from '../helpers/nativeTurnEndSound'
import { updateTodosToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('plays a selected sound once for native tool activity and keeps text-only turns quiet', async ({ authenticatedZCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedZCodeWorkspace.workspaceId, provider: AgentProvider.ZCODE }
  await exerciseTurnEndSound(context, { toolActivity: true, steps: [{ toolCalls: [updateTodosToolCall(AgentProvider.ZCODE, 'sound-enabled-tool', [{ step: 'Native sound activity', status: 'in_progress' }])] }, { text: 'The native sound tool completed.' }], prepare: () => applyPermissionPreset(page, 'bypass') })
  await exerciseTurnEndSound(context, { toolActivity: false })
  await exerciseTurnEndSound(context, { toolActivity: true, sound: 'none', steps: [{ toolCalls: [updateTodosToolCall(AgentProvider.ZCODE, 'sound-muted-tool', [{ step: 'Native sound activity', status: 'in_progress' }])] }, { text: 'The muted native tool completed.' }] })
})

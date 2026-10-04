import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { gooseTest } from '../goose-fixtures'
import { exerciseTurnEndSound } from '../helpers/nativeTurnEndSound'
import { updateTodosToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset } from '../helpers/ui'

gooseTest('plays a selected sound once for native tool activity and keeps text-only turns quiet', async ({ authenticatedGooseWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedGooseWorkspace.workspaceId, provider: AgentProvider.GOOSE }
  await exerciseTurnEndSound(context, { toolActivity: true, steps: [{ toolCalls: [updateTodosToolCall(AgentProvider.GOOSE, 'sound-enabled-tool', [{ step: 'Native sound activity', status: 'in_progress' }])] }, { text: 'The native sound tool completed.' }], prepare: () => applyPermissionPreset(page, 'bypass') })
  await exerciseTurnEndSound(context, { toolActivity: false })
  await exerciseTurnEndSound(context, { toolActivity: true, sound: 'none', steps: [{ toolCalls: [updateTodosToolCall(AgentProvider.GOOSE, 'sound-muted-tool', [{ step: 'Native sound activity', status: 'in_progress' }])] }, { text: 'The muted native tool completed.' }] })
})

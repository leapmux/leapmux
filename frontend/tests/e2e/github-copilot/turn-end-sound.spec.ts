import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { copilotTest } from '../copilot-fixtures'
import { exerciseTurnEndSound } from '../helpers/nativeTurnEndSound'
import { updateTodosToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset } from '../helpers/ui'

copilotTest('plays a selected sound once for native tool activity and keeps text-only turns quiet', async ({ authenticatedCopilotWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCopilotWorkspace.workspaceId, provider: AgentProvider.GITHUB_COPILOT }
  await exerciseTurnEndSound(context, { toolActivity: true, steps: [{ toolCalls: [updateTodosToolCall(AgentProvider.GITHUB_COPILOT, 'sound-enabled-tool', [{ step: 'Native sound activity', status: 'in_progress' }])] }, { text: 'The native sound tool completed.' }], prepare: () => applyPermissionPreset(page, 'bypass') })
  await exerciseTurnEndSound(context, { toolActivity: false })
  await exerciseTurnEndSound(context, { toolActivity: true, sound: 'none', steps: [{ toolCalls: [updateTodosToolCall(AgentProvider.GITHUB_COPILOT, 'sound-muted-tool', [{ step: 'Native sound activity', status: 'in_progress' }])] }, { text: 'The muted native tool completed.' }] })
})

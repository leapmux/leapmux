import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { cursorTest } from '../cursor-fixtures'
import { exerciseTurnEndSound } from '../helpers/nativeTurnEndSound'
import { updateTodosToolCall } from '../helpers/providerToolCalls'

cursorTest('plays a selected sound once for native tool activity and keeps text-only turns quiet', async ({ authenticatedCursorWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCursorWorkspace.workspaceId, provider: AgentProvider.CURSOR }
  await exerciseTurnEndSound(context, { toolActivity: true, steps: [{ toolCalls: [updateTodosToolCall(AgentProvider.CURSOR, 'sound-native-todo', [{ step: 'Native sound tool proof', status: 'pending' }])], text: 'The native sound tool turn ended.' }], prompt: 'Create the native sound to-do item.' })
  await exerciseTurnEndSound(context, { toolActivity: false })
  await exerciseTurnEndSound(context, { toolActivity: true, sound: 'none', steps: [{ toolCalls: [updateTodosToolCall(AgentProvider.CURSOR, 'sound-muted-native-todo', [{ step: 'Muted native sound proof', status: 'pending' }])], text: 'The muted native tool turn ended.' }] })
})

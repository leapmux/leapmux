import { commandCodeTest } from '../command-code-fixtures'
import { exerciseTurnEndSound } from '../helpers/nativeTurnEndSound'
import { bashToolCall } from '../helpers/providerToolCalls'
import { nativeContext } from './scenarios'

commandCodeTest('plays the selected sound once for native tool activity and keeps text turns quiet', async ({ authenticatedCommandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCommandCodeWorkspace.workspaceId })
  await exerciseTurnEndSound(context, { toolActivity: false })
  await exerciseTurnEndSound(context, { toolActivity: true, steps: [{ toolCalls: [bashToolCall(context.provider, 'sound-tool', 'printf native-sound')] }, { text: 'The native sound tool completed.' }] })
  await exerciseTurnEndSound(context, { toolActivity: true, sound: 'none', steps: [{ toolCalls: [bashToolCall(context.provider, 'quiet-tool', 'printf native-quiet')] }, { text: 'The native quiet tool completed.' }] })
})

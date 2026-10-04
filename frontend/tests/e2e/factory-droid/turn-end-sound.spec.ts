import { droidTest } from '../droid-fixtures'
import { nativeTextStep } from '../helpers/nativeScenario'
import { exerciseTurnEndSound } from '../helpers/nativeTurnEndSound'
import { bashToolCall } from '../helpers/providerToolCalls'
import { nativeContext } from './scenarios'

droidTest('applies the completion sound policy to the native answer turn', async ({ authenticatedDroidWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDroidWorkspace.workspaceId })
  await exerciseTurnEndSound(context, { toolActivity: false })
})

droidTest('plays one sound for a completed native tool turn', async ({ authenticatedDroidWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDroidWorkspace.workspaceId })
  await exerciseTurnEndSound(context, {
    toolActivity: true,
    sound: 'ding-dong',
    steps: [
      { toolCalls: [bashToolCall(context.provider, 'native-sound-tool', 'printf "SOUND%s\n" "$((40 + 2))"')] },
      nativeTextStep(context, 'The native sound tool ended.'),
    ],
    approveTool: false,
  })
})

droidTest('keeps a completed native tool turn quiet when sound is disabled', async ({ authenticatedDroidWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDroidWorkspace.workspaceId })
  await exerciseTurnEndSound(context, {
    toolActivity: true,
    sound: 'none',
    steps: [
      { toolCalls: [bashToolCall(context.provider, 'native-sound-tool', 'printf "SOUND%s\n" "$((40 + 2))"')] },
      nativeTextStep(context, 'The native sound tool ended.'),
    ],
    approveTool: false,
  })
})

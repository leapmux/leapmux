import { codebuddyTest } from '../codebuddy-fixtures'
import { nativeTextStep } from '../helpers/nativeScenario'
import { exerciseTurnEndSound } from '../helpers/nativeTurnEndSound'
import { bashToolCall } from '../helpers/providerToolCalls'
import { nativeContext } from './scenarios'

codebuddyTest('applies the completion sound policy to the native answer turn', async ({ codebuddyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: codebuddyWorkspace.workspaceId })
  await exerciseTurnEndSound(context, { toolActivity: false })
})

codebuddyTest('plays one sound for a completed native tool turn', async ({ codebuddyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: codebuddyWorkspace.workspaceId })
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

codebuddyTest('keeps a completed native tool turn quiet when sound is disabled', async ({ codebuddyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: codebuddyWorkspace.workspaceId })
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

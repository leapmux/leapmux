import { nativeTextStep } from '../helpers/nativeScenario'
import { exerciseTurnEndSound } from '../helpers/nativeTurnEndSound'
import { bashToolCall } from '../helpers/providerToolCalls'
import { lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest('applies the completion sound policy to the native answer turn', async ({ authenticatedLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedLettaWorkspace.workspaceId })
  await exerciseTurnEndSound(context, { toolActivity: false })
})

lettaTest('plays one sound for a completed native tool turn', async ({ authenticatedLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedLettaWorkspace.workspaceId })
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

lettaTest('keeps a completed native tool turn quiet when sound is disabled', async ({ authenticatedLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedLettaWorkspace.workspaceId })
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

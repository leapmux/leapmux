import { nativeTextStep } from '../helpers/nativeScenario'
import { exerciseTurnEndSound } from '../helpers/nativeTurnEndSound'
import { bashToolCall } from '../helpers/providerToolCalls'
import { junieTest } from '../junie-fixtures'
import { nativeContext } from './scenarios'

// The `\\n` in the source is a backslash and the letter n, which printf reads as a newline escape.
// A real newline character in the command makes Junie ask for a permission (probe of Junie 26.9.22),
// and these cases never answer it.
const SOUND_COMMAND = 'printf "SOUND%s\\n" "$((40 + 2))"'

junieTest('applies the completion sound policy to the native answer turn', async ({ authenticatedJunieWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedJunieWorkspace.workspaceId })
  // Junie delivers its answer through the `answer` tool and reports it as text, not as a tool row. The turn has no tool activity.
  await exerciseTurnEndSound(context, { toolActivity: false })
})

junieTest('plays one sound for a completed native tool turn', async ({ authenticatedJunieWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedJunieWorkspace.workspaceId })
  await exerciseTurnEndSound(context, {
    toolActivity: true,
    sound: 'ding-dong',
    steps: [
      { toolCalls: [bashToolCall(context.provider, 'native-sound-tool', SOUND_COMMAND)] },
      nativeTextStep(context, 'The native sound tool ended.'),
    ],
    approveTool: false,
  })
})

junieTest('keeps a completed native tool turn quiet when sound is disabled', async ({ authenticatedJunieWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedJunieWorkspace.workspaceId })
  await exerciseTurnEndSound(context, {
    toolActivity: true,
    sound: 'none',
    steps: [
      { toolCalls: [bashToolCall(context.provider, 'native-sound-tool', SOUND_COMMAND)] },
      nativeTextStep(context, 'The native sound tool ended.'),
    ],
    approveTool: false,
  })
})

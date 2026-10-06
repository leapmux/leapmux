import { exerciseTurnEndSound, NATIVE_SOUND_COMMAND } from '../helpers/nativeTurnEndSound'
import { bashToolCall } from '../helpers/providerToolCalls'
import { junieTest } from '../junie-fixtures'

junieTest('applies the completion sound policy to the native answer turn', async ({ native }) => {
  // Junie delivers its answer through the `answer` tool and reports it as text, not as a tool row. The turn has no tool activity.
  await exerciseTurnEndSound(native)
})

junieTest('plays one sound for a completed native tool turn', async ({ native }) => {
  await exerciseTurnEndSound(native, { sound: 'ding-dong', tool: bashToolCall(native.provider, 'native-sound-tool', NATIVE_SOUND_COMMAND) })
})

junieTest('keeps a completed native tool turn quiet when sound is disabled', async ({ native }) => {
  await exerciseTurnEndSound(native, { sound: 'none', tool: bashToolCall(native.provider, 'native-sound-tool', NATIVE_SOUND_COMMAND) })
})

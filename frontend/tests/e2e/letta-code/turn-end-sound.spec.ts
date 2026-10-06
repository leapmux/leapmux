import { exerciseTurnEndSound, NATIVE_SOUND_COMMAND } from '../helpers/nativeTurnEndSound'
import { bashToolCall } from '../helpers/providerToolCalls'
import { lettaTest } from '../letta-fixtures'

lettaTest('applies the completion sound policy to the native answer turn', async ({ native }) => {
  await exerciseTurnEndSound(native)
})

lettaTest('plays one sound for a completed native tool turn', async ({ native }) => {
  await exerciseTurnEndSound(native, { sound: 'ding-dong', tool: bashToolCall(native.provider, 'native-sound-tool', NATIVE_SOUND_COMMAND) })
})

lettaTest('keeps a completed native tool turn quiet when sound is disabled', async ({ native }) => {
  await exerciseTurnEndSound(native, { sound: 'none', tool: bashToolCall(native.provider, 'native-sound-tool', NATIVE_SOUND_COMMAND) })
})

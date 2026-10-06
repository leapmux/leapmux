import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseTurnEndSound, NATIVE_SOUND_COMMAND } from '../helpers/nativeTurnEndSound'
import { bashToolCall } from '../helpers/providerToolCalls'

codebuddyTest('applies the completion sound policy to the native answer turn', async ({ native }) => {
  await exerciseTurnEndSound(native)
})

codebuddyTest('plays one sound for a completed native tool turn', async ({ native }) => {
  await exerciseTurnEndSound(native, { sound: 'ding-dong', tool: bashToolCall(native.provider, 'native-sound-tool', NATIVE_SOUND_COMMAND) })
})

codebuddyTest('keeps a completed native tool turn quiet when sound is disabled', async ({ native }) => {
  await exerciseTurnEndSound(native, { sound: 'none', tool: bashToolCall(native.provider, 'native-sound-tool', NATIVE_SOUND_COMMAND) })
})

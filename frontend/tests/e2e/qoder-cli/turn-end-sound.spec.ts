import { exerciseTurnEndSound, NATIVE_SOUND_COMMAND } from '../helpers/nativeTurnEndSound'
import { bashToolCall } from '../helpers/providerToolCalls'
import { qoderTest } from '../qoder-fixtures'

qoderTest('applies the completion sound policy to the native answer turn', async ({ native }) => {
  await exerciseTurnEndSound(native)
})

// The tool must run with no banner, because the case approves no tool. `NATIVE_SOUND_COMMAND` states why qodercli
// runs it at once in Accept Edits.
qoderTest('plays one sound for a completed native tool turn', async ({ native }) => {
  await exerciseTurnEndSound(native, { sound: 'ding-dong', tool: bashToolCall(native.provider, 'native-sound-tool', NATIVE_SOUND_COMMAND) })
})

qoderTest('keeps a completed native tool turn quiet when sound is disabled', async ({ native }) => {
  await exerciseTurnEndSound(native, { sound: 'none', tool: bashToolCall(native.provider, 'native-sound-tool', NATIVE_SOUND_COMMAND) })
})

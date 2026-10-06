import { diracTest } from '../dirac-fixtures'
import { exerciseTurnEndSound, NATIVE_SOUND_COMMAND } from '../helpers/nativeTurnEndSound'
import { bashToolCall } from '../helpers/providerToolCalls'

// Dirac answers through a call of its `respond` tool, and its context states no answer tool. The answer turn
// therefore holds tool activity, and the chosen sound rings for it.
diracTest('applies the completion sound policy to the native answer turn', async ({ native }) => {
  await exerciseTurnEndSound(native)
})

diracTest('plays one sound for a completed native tool turn', async ({ native }) => {
  await exerciseTurnEndSound(native, { sound: 'ding-dong', tool: bashToolCall(native.provider, 'native-sound-tool', NATIVE_SOUND_COMMAND) })
})

diracTest('keeps a completed native tool turn quiet when sound is disabled', async ({ native }) => {
  await exerciseTurnEndSound(native, { sound: 'none', tool: bashToolCall(native.provider, 'native-sound-tool', NATIVE_SOUND_COMMAND) })
})

import { commandCodeTest } from '../command-code-fixtures'
import { exerciseTurnEndSound, NATIVE_SOUND_COMMAND } from '../helpers/nativeTurnEndSound'
import { bashToolCall } from '../helpers/providerToolCalls'

commandCodeTest('plays the chosen sound once after a native tool turn', async ({ native }) => {
  await exerciseTurnEndSound(native, { tool: bashToolCall(native.provider, 'sound-tool', NATIVE_SOUND_COMMAND) })
})

commandCodeTest('keeps a text-only native turn quiet', async ({ native }) => {
  await exerciseTurnEndSound(native)
})

commandCodeTest('plays no sound after a native tool turn when the chosen sound is none', async ({ native }) => {
  await exerciseTurnEndSound(native, { sound: 'none', tool: bashToolCall(native.provider, 'quiet-tool', NATIVE_SOUND_COMMAND) })
})

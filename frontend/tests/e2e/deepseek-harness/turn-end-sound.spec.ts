import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseTurnEndSound, NATIVE_SOUND_COMMAND } from '../helpers/nativeTurnEndSound'
import { bashToolCall } from '../helpers/providerToolCalls'

deepseekHarnessTest('plays the chosen sound once after a native tool turn', async ({ native }) => {
  await exerciseTurnEndSound(native, { tool: bashToolCall(native.provider, 'sound-tool', NATIVE_SOUND_COMMAND) })
})

deepseekHarnessTest('keeps a text-only native turn quiet', async ({ native }) => {
  await exerciseTurnEndSound(native)
})

deepseekHarnessTest('plays no sound after a native tool turn when the chosen sound is none', async ({ native }) => {
  await exerciseTurnEndSound(native, { sound: 'none', tool: bashToolCall(native.provider, 'quiet-tool', NATIVE_SOUND_COMMAND) })
})

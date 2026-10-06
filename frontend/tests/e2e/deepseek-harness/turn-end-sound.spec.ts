import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseTurnEndSound, NATIVE_SOUND_COMMAND } from '../helpers/nativeTurnEndSound'
import { bashToolCall } from '../helpers/providerToolCalls'

deepseekHarnessTest('plays the selected sound once for native tool activity and keeps text turns quiet', async ({ native }) => {
  await exerciseTurnEndSound(native)
  await exerciseTurnEndSound(native, { tool: bashToolCall(native.provider, 'sound-tool', NATIVE_SOUND_COMMAND) })
  await exerciseTurnEndSound(native, { sound: 'none', tool: bashToolCall(native.provider, 'quiet-tool', NATIVE_SOUND_COMMAND) })
})

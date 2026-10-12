import { geminiTest } from '../gemini-fixtures'
import { exerciseTurnEndSound, NATIVE_SOUND_COMMAND } from '../helpers/nativeTurnEndSound'
import { bashToolCall } from '../helpers/providerToolCalls'

// Gemini CLI 0.62.0 runs `echo` with no permission request in its default mode: its default policy approves the
// command (`approvedTools` of `[modes.default]` in `bundle/policies/sandbox-default.toml`). So the tool cases approve
// no tool.
for (const toolActivity of [false, true]) {
  geminiTest(`uses the actual native activity when tool activity is ${toolActivity}`, async ({ native }) => {
    await exerciseTurnEndSound(native, toolActivity ? { tool: bashToolCall(native.provider, 'gemini-sound-tool', NATIVE_SOUND_COMMAND) } : {})
  })
}

geminiTest('uses the actual native activity when tool activity is true and the sound is none', async ({ native }) => {
  await exerciseTurnEndSound(native, { sound: 'none', tool: bashToolCall(native.provider, 'gemini-sound-tool', NATIVE_SOUND_COMMAND) })
})

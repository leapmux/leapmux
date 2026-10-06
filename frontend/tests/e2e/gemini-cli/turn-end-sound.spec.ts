import { geminiTest } from '../gemini-fixtures'
import { exerciseTurnEndSound, NATIVE_SOUND_COMMAND } from '../helpers/nativeTurnEndSound'
import { bashToolCall } from '../helpers/providerToolCalls'

for (const toolActivity of [false, true]) {
  geminiTest(`uses the actual native activity when tool activity is ${toolActivity}`, async ({ native }) => {
    await exerciseTurnEndSound(native, toolActivity ? { tool: bashToolCall(native.provider, 'gemini-sound-tool', NATIVE_SOUND_COMMAND), approveTool: true } : {})
  })
}

geminiTest('uses the actual native activity when tool activity is true and the sound is none', async ({ native }) => {
  await exerciseTurnEndSound(native, { sound: 'none', tool: bashToolCall(native.provider, 'gemini-sound-tool', NATIVE_SOUND_COMMAND), approveTool: true })
})

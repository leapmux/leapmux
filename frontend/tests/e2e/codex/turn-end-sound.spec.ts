import { codexTest } from '../codex-fixtures'
import { exerciseTurnEndSound, NATIVE_SOUND_COMMAND } from '../helpers/nativeTurnEndSound'
import { bashToolCall } from '../helpers/providerToolCalls'

codexTest.describe('Codex Turn End Sound', () => {
  codexTest('should play ding-dong sound when Codex turn ends with tool use', async ({ native }) => {
    await exerciseTurnEndSound(native, { tool: bashToolCall(native.provider, 'sound-call', NATIVE_SOUND_COMMAND) })
  })

  codexTest('should NOT play sound for simple Codex text exchange', async ({ native }) => {
    await exerciseTurnEndSound(native)
  })

  codexTest('should NOT play sound when Codex turn ends with tool use and the sound is none', async ({ native }) => {
    await exerciseTurnEndSound(native, { sound: 'none', tool: bashToolCall(native.provider, 'sound-call', NATIVE_SOUND_COMMAND) })
  })
})

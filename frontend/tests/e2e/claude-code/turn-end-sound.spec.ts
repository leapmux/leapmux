import { claudeTest } from '../claude-fixtures'
import { exerciseTurnEndSound, NATIVE_SOUND_COMMAND } from '../helpers/nativeTurnEndSound'
import { bashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset } from '../helpers/ui'

for (const sound of ['ding-dong', 'none'] as const) {
  claudeTest(`plays the selected sound once after native tool activity with preference ${sound}`, async ({ native }) => {
    await exerciseTurnEndSound(native, {
      sound,
      prepare: () => applyPermissionPreset(native.page, 'bypass'),
      tool: bashToolCall(native.provider, 'native-sound-shell', NATIVE_SOUND_COMMAND),
    })
  })
}

claudeTest('stays quiet after a native turn without tool activity', async ({ native }) => {
  await exerciseTurnEndSound(native)
})

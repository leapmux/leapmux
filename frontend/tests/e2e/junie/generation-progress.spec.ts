import { JUNIE_BRAVE } from '../../../src/components/chat/providers/junie/permissionPresets'
import { JUNIE_CONFIG } from '../../../src/generated/contracts/junie-protocol'
import { exerciseOutputByteProgress, exerciseTokenProgress } from '../helpers/generationProgress'
import { expectNativeOptionValue } from '../helpers/nativeScenario'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { junieTest } from '../junie-fixtures'

// Junie sends no ACP frame while the model streams its answer: it holds the whole response and states the
// answer text once, with the end of the turn. No live token count exists to report.
junieTest('exposes no token or byte counter throughout the completed native stream', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: false })
})

junieTest('reports an advancing byte count while native output arrives', async ({ native }) => {
  // Junie asks to approve the controlled shell command, and then streams its output while it runs.
  await exerciseOutputByteProgress(native, {
    supported: true,
    approveTool: true,
    prepare: async () => {
      await chooseSettingsOption(native.page, `${JUNIE_CONFIG.BraveMode}-${JUNIE_BRAVE.Off}`)
      await waitForSettingsIdle(native.page)
      await expectNativeOptionValue(native, JUNIE_CONFIG.BraveMode, JUNIE_BRAVE.Off)
    },
  })
})

import { codexTest } from '../codex-fixtures'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { nativeLaunch } from './scenarios'

for (const failed of [false, true]) {
  codexTest(`delivers queued input through controlled native startup with failure ${failed}`, async ({ native }) => {
    await exerciseAgentStartup(native, { launch: nativeLaunch(native), failed })
  })
}

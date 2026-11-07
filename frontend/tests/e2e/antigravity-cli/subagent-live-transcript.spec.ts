import { antigravityTest } from '../antigravity-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { expectAntigravityLiveChild, finishAntigravityChildWithReload, openAntigravityRunningChild } from './childScenarios'

antigravityTest('shows original child content and tool output before the native child completes', async ({ native }) => {
  const child = await openAntigravityRunningChild(native)
  await withCleanup(async () => {
    await expectAntigravityLiveChild(native, child)
    await finishAntigravityChildWithReload(native, child)
  }, child.finish)
})

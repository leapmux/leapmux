import { unlinkSync } from 'node:fs'
import { join } from 'node:path'
import { antigravityTest } from '../antigravity-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { nativeAgentById } from '../helpers/nativeScenario'
import { expectAntigravityLiveChild, finishAntigravityChildWithReload, openAntigravityRunningChild } from './childScenarios'
import { antigravityNativeSessionDir } from './nativeStore'

antigravityTest('stores the original native child prompt and results in its distinct tab after reload', async ({ native }) => {
  const child = await openAntigravityRunningChild(native)
  await withCleanup(async () => {
    await expectAntigravityLiveChild(native, child)
    await finishAntigravityChildWithReload(native, child, {
      beforeReload: async () => {
        const storedChild = await nativeAgentById(native, child.childId)
        if (!storedChild)
          throw new Error('The stored native child lost its identity.')
        unlinkSync(join(antigravityNativeSessionDir(native, storedChild), '.system_generated', 'logs', 'transcript.jsonl'))
      },
    })
  }, child.finish)
})

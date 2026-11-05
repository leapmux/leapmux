import { antigravityTest } from '../antigravity-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { openChildTabFromRow, stopChildWithInterrupt } from '../helpers/subagentRegistry'
import { tabById } from '../helpers/ui'
import { openAntigravityRunningChild } from './childScenarios'

antigravityTest('interrupts only the native child and leaves its parent usable', async ({ native }) => {
  const child = await openAntigravityRunningChild(native)
  await withCleanup(async () => {
    await openChildTabFromRow(native.page, child.row)
    await stopChildWithInterrupt(native.page, child.row, 'stopped')
    await tabById(native.page, child.parentId).click()
    await sendNativeAnswer(native, 'Continue the parent after the child interruption.', 'The native parent remained available.')
  }, child.finish)
})

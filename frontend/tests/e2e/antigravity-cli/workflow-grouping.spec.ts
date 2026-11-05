import { antigravityTest } from '../antigravity-fixtures'
import { exerciseUngroupedNativeChildren } from '../helpers/workflowGrouping'
import { openAntigravityRunningChild } from './childScenarios'

antigravityTest('keeps two exact native child UUIDs distinct without an invented workflow group', async ({ native }) => {
  await exerciseUngroupedNativeChildren(native, {
    openChild: slot => openAntigravityRunningChild(native, slot.index),
  })
})

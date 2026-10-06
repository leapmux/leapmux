import { droidTest } from '../droid-fixtures'
import { exerciseUngroupedNativeChildren } from '../helpers/workflowGrouping'
import { runningChild } from './scenarios'

droidTest('keeps two native child work rows without a workflow group', async ({ native }) => {
  await exerciseUngroupedNativeChildren(native, { openChild: slot => runningChild(native, slot) })
})

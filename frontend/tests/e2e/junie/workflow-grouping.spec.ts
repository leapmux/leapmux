import { exerciseUngroupedNativeChildren } from '../helpers/workflowGrouping'
import { junieTest } from '../junie-fixtures'
import { runningChild } from './scenarios'

junieTest('keeps two native child work rows without a workflow group', async ({ native }) => {
  await exerciseUngroupedNativeChildren(native, { openChild: slot => runningChild(native, slot) })
})

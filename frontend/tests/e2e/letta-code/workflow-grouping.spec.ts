import { exerciseUngroupedNativeChildren } from '../helpers/workflowGrouping'
import { lettaTest } from '../letta-fixtures'
import { runningChild } from './scenarios'

lettaTest('keeps two native child work rows without a workflow group', async ({ native }) => {
  await exerciseUngroupedNativeChildren(native, { openChild: slot => runningChild(native, slot) })
})

import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseUngroupedNativeChildren } from '../helpers/workflowGrouping'
import { runningChild } from './scenarios'

fastAgentTest('keeps two native child work rows without a workflow group', async ({ native }) => {
  await exerciseUngroupedNativeChildren(native, { openChild: slot => runningChild(native, slot) })
})

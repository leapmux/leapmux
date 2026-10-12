import { commandCodeTest } from '../command-code-fixtures'
import { exerciseUngroupedNativeChildren } from '../helpers/workflowGrouping'
import { runningChild } from './scenarios'

commandCodeTest('keeps two actual native child tasks independent before and after reload', async ({ native }) => {
  await exerciseUngroupedNativeChildren(native, { openChild: slot => runningChild(native, slot) })
})

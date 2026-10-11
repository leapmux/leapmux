import { cursorTest } from '../cursor-fixtures'
import { exerciseUngroupedNativeChildren } from '../helpers/workflowGrouping'
import { openCursorRunningChild } from './childScenario'

cursorTest('keeps two actual native children outside workflow groups after reload', async ({ native }) => {
  await exerciseUngroupedNativeChildren(native, { openChild: slot => openCursorRunningChild(native, slot) })
})

import { openProfiledNativeChild } from '../helpers/runningChildProof'
import { exerciseUngroupedNativeChildren } from '../helpers/workflowGrouping'
import { opencodeTest } from '../opencode-fixtures'
import { OPENCODE_CHILD } from './childScenario'

opencodeTest('keeps two actual native children outside workflow groups after reload', async ({ native }) => {
  await exerciseUngroupedNativeChildren(native, { openChild: slot => openProfiledNativeChild(native, OPENCODE_CHILD, slot), catalogProof: true })
})

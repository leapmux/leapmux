import { copilotTest } from '../copilot-fixtures'
import { openProfiledNativeChild } from '../helpers/runningChildProof'
import { exerciseUngroupedNativeChildren } from '../helpers/workflowGrouping'
import { COPILOT_CHILD } from './childScenario'

copilotTest('keeps two actual native children outside workflow groups after reload', async ({ native }) => {
  await exerciseUngroupedNativeChildren(native, { openChild: slot => openProfiledNativeChild(native, COPILOT_CHILD, slot), catalogProof: true })
})

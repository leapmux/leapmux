import { openProfiledNativeChild } from '../helpers/runningChildProof'
import { exerciseUngroupedNativeChildren } from '../helpers/workflowGrouping'
import { reasonixTest } from '../reasonix-fixtures'
import { REASONIX_CHILD } from './childScenario'

reasonixTest('keeps two actual native children outside workflow groups after reload', async ({ native }) => {
  await exerciseUngroupedNativeChildren(native, { openChild: slot => openProfiledNativeChild(native, REASONIX_CHILD, slot), catalogProof: true })
})

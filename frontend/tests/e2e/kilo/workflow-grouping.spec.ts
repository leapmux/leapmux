import { openProfiledNativeChild } from '../helpers/runningChildProof'
import { exerciseUngroupedNativeChildren } from '../helpers/workflowGrouping'
import { kiloTest } from '../kilo-fixtures'
import { KILO_CHILD } from './childScenario'

kiloTest('keeps two actual native children outside workflow groups after reload', async ({ native }) => {
  await exerciseUngroupedNativeChildren(native, { openChild: slot => openProfiledNativeChild(native, KILO_CHILD, slot), catalogProof: true })
})

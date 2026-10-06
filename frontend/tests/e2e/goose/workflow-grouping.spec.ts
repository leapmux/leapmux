import { gooseTest } from '../goose-fixtures'
import { openProfiledNativeChild } from '../helpers/runningChildProof'
import { exerciseUngroupedNativeChildren } from '../helpers/workflowGrouping'
import { GOOSE_CHILD } from './childScenario'

gooseTest('keeps two actual native children outside workflow groups after reload', async ({ native }) => {
  await exerciseUngroupedNativeChildren(native, { openChild: slot => openProfiledNativeChild(native, GOOSE_CHILD, slot), catalogProof: true })
})

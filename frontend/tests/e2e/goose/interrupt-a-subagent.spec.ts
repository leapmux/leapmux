import { gooseTest } from '../goose-fixtures'
import { openProfiledNativeChild } from '../helpers/runningChildProof'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { GOOSE_CHILD } from './childScenario'

gooseTest('refuses native child interrupt while the actual child task runs', async ({ native }) => {
  await expectUnsupportedSubagent(native, { operation: 'interrupt', openChild: () => openProfiledNativeChild(native, GOOSE_CHILD) })
})

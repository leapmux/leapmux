import { openProfiledNativeChild } from '../helpers/runningChildProof'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { piTest } from '../pi-fixtures'
import { PI_CHILD } from './childScenario'

piTest('refuses native child interrupt while the actual child task runs', async ({ native }) => {
  await expectUnsupportedSubagent(native, { operation: 'interrupt', openChild: () => openProfiledNativeChild(native, PI_CHILD) })
})

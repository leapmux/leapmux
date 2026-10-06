import { openProfiledNativeChild } from '../helpers/runningChildProof'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { zcodeTest } from '../zcode-fixtures'
import { ZCODE_CHILD } from './childScenario'

zcodeTest('refuses native child interrupt while the actual child task runs', async ({ native }) => {
  await expectUnsupportedSubagent(native, { operation: 'interrupt', openChild: () => openProfiledNativeChild(native, ZCODE_CHILD) })
})

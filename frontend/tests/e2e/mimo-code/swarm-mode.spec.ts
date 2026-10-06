import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { mimoTest } from '../mimo-fixtures'
import { relatedNativeProof } from './scenarios'

mimoTest('proves the missing swarm-mode setting against the live catalog and a native tool', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'swarm-mode', relatedProof: () => relatedNativeProof(native) })
})

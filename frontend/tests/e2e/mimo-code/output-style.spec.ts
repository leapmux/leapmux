import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { mimoTest } from '../mimo-fixtures'
import { relatedNativeProof } from './scenarios'

mimoTest('proves the missing output-style setting against the live catalog and a native tool', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'output-style', relatedProof: () => relatedNativeProof(native) })
})

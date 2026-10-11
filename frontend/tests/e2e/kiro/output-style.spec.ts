import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { kiroTest } from '../kiro-fixtures'
import { relatedNativeProof } from './scenarios'

kiroTest('proves the missing output-style setting against the live catalog and a native tool', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'output-style', relatedProof: () => relatedNativeProof(native) })
})

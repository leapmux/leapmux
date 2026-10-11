import { ampTest } from '../amp-fixtures'
import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { relatedNativeProof } from './scenarios'

ampTest('proves the missing output-style setting against the live catalog and a native tool', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'output-style', relatedProof: () => relatedNativeProof(native) })
})

import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { qoderTest } from '../qoder-fixtures'
import { relatedNativeProof } from './scenarios'

qoderTest('exposes no separate native output style setting', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'output-style', relatedProof: () => relatedNativeProof(native) })
})

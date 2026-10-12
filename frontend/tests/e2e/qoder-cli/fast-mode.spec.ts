import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { qoderTest } from '../qoder-fixtures'
import { relatedNativeProof } from './scenarios'

qoderTest('exposes no separate native fast mode setting', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'fast-mode', relatedProof: () => relatedNativeProof(native) })
})

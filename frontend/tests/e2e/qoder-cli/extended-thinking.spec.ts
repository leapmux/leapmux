import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { qoderTest } from '../qoder-fixtures'
import { relatedNativeProof } from './scenarios'

qoderTest('exposes no separate native extended thinking setting', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'extended-thinking', relatedProof: () => relatedNativeProof(native) })
})

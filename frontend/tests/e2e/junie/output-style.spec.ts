import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { junieTest } from '../junie-fixtures'
import { relatedNativeProof } from './scenarios'

junieTest('exposes no separate native output style setting', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'output-style', relatedProof: () => relatedNativeProof(native) })
})

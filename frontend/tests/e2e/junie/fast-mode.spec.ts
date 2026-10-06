import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { junieTest } from '../junie-fixtures'
import { relatedNativeProof } from './scenarios'

junieTest('exposes no separate native fast mode setting', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'fast-mode', relatedProof: () => relatedNativeProof(native) })
})

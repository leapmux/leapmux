import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { lettaTest } from '../letta-fixtures'
import { relatedNativeProof } from './scenarios'

lettaTest('exposes no separate native output style setting', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'output-style', relatedProof: () => relatedNativeProof(native) })
})

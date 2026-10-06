import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { lettaTest } from '../letta-fixtures'
import { relatedNativeProof } from './scenarios'

lettaTest('exposes no separate native extended thinking setting', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'extended-thinking', relatedProof: () => relatedNativeProof(native) })
})

import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { junieTest } from '../junie-fixtures'
import { relatedNativeProof } from './scenarios'

junieTest('exposes no separate native extended thinking setting', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'extended-thinking', relatedProof: () => relatedNativeProof(native) })
})

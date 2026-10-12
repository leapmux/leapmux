import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { lettaTest } from '../letta-fixtures'
import { relatedNativeProof } from './scenarios'

lettaTest('exposes no native reasoning effort setting', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'reasoning-effort', relatedProof: () => relatedNativeProof(native) })
})

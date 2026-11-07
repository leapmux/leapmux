import { antigravityTest } from '../antigravity-fixtures'
import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { relatedNativeProof } from './scenarios'

antigravityTest('exposes no separate native fast serving setting', async ({ native }) => {
  await expectMissingSetting(native, {
    feature: 'fast-mode',
    relatedProof: () => relatedNativeProof(native),
  })
})

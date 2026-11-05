import { antigravityTest } from '../antigravity-fixtures'
import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { relatedNativeProof } from './scenarios'

antigravityTest('exposes no separate native setting for extended thinking', async ({ native }) => {
  await expectMissingSetting(native, {
    feature: 'extended-thinking',
    relatedProof: () => relatedNativeProof(native),
  })
})

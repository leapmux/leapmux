import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { kimiTest } from '../kimi-fixtures'
import { relatedNativeProof } from './scenarios'

kimiTest('proves the missing output-style setting against the live catalog and a native tool', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'output-style', relatedProof: () => relatedNativeProof(native) })
})

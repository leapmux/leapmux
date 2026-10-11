import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { kimiTest } from '../kimi-fixtures'
import { relatedNativeProof } from './scenarios'

kimiTest('proves the missing fast-mode setting against the live catalog and a native tool', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'fast-mode', relatedProof: () => relatedNativeProof(native) })
})

import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { kimiTest } from '../kimi-fixtures'
import { relatedNativeProof } from './scenarios'

kimiTest('proves the missing extended-thinking setting against the live catalog and a native tool', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'extended-thinking', relatedProof: () => relatedNativeProof(native) })
})

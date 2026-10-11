import { ampTest } from '../amp-fixtures'
import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { relatedNativeProof } from './scenarios'

ampTest('proves the missing extended-thinking setting against the live catalog and a native tool', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'extended-thinking', relatedProof: () => relatedNativeProof(native) })
})

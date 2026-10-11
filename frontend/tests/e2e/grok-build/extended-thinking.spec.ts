import { grokTest } from '../grok-fixtures'
import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { relatedNativeProof } from './scenarios'

grokTest('proves the missing extended-thinking setting against the live catalog and a native tool', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'extended-thinking', relatedProof: () => relatedNativeProof(native) })
})

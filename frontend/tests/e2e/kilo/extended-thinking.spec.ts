import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { kiloTest } from '../kilo-fixtures'
import { relatedNativeProof } from './scenarios'

kiloTest('proves the native extended-thinking limit after a real sidebar operation', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'extended-thinking', relatedProof: () => relatedNativeProof(native) })
})

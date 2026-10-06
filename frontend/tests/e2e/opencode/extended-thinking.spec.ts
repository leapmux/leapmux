import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { opencodeTest } from '../opencode-fixtures'
import { relatedNativeProof } from './scenarios'

opencodeTest('proves the native extended-thinking limit after a real sidebar operation', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'extended-thinking', relatedProof: () => relatedNativeProof(native) })
})

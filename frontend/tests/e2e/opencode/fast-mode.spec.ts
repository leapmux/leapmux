import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { opencodeTest } from '../opencode-fixtures'
import { relatedNativeProof } from './scenarios'

opencodeTest('proves the native fast-mode limit after a real sidebar operation', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'fast-mode', relatedProof: () => relatedNativeProof(native) })
})

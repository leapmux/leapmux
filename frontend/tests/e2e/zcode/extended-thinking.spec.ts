import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { zcodeTest } from '../zcode-fixtures'
import { relatedNativeProof } from './scenarios'

zcodeTest('proves the native extended-thinking limit after a real sidebar operation', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'extended-thinking', relatedProof: () => relatedNativeProof(native) })
})

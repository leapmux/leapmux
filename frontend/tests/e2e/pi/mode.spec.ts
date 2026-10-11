import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { piTest } from '../pi-fixtures'
import { relatedNativeProof } from './scenarios'

piTest('proves the native mode limit after a real sidebar operation', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'mode', relatedProof: () => relatedNativeProof(native) })
})

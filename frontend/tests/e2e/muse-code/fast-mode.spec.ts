import { exerciseCapabilityProbe, expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { museTest } from '../muse-fixtures'

museTest('proves the native fast-mode limit after a real sidebar operation', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'fast-mode', relatedProof: () => exerciseCapabilityProbe(native) })
})

import { exerciseCapabilityProbe, expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { museTest } from '../muse-fixtures'

museTest('proves the native extended-thinking limit after a real sidebar operation', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'extended-thinking', relatedProof: () => exerciseCapabilityProbe(native) })
})

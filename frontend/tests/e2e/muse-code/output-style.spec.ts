import { exerciseCapabilityProbe, expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { museTest } from '../muse-fixtures'

museTest('proves the native output-style limit after a real sidebar operation', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'output-style', relatedProof: () => exerciseCapabilityProbe(native) })
})

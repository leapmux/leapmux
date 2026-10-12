import { exerciseCapabilityProbe, expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { museTest } from '../muse-fixtures'

museTest('proves the native swarm-mode limit after a real sidebar operation', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'swarm-mode', relatedProof: () => exerciseCapabilityProbe(native) })
})

import { fastAgentTest } from '../fastagent-fixtures'
import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { relatedNativeProof } from './scenarios'

fastAgentTest('exposes no separate native swarm mode setting', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'swarm-mode', relatedProof: () => relatedNativeProof(native) })
})

import { fastAgentTest } from '../fastagent-fixtures'
import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { relatedNativeProof } from './scenarios'

fastAgentTest('exposes no native reasoning effort setting', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'reasoning-effort', relatedProof: () => relatedNativeProof(native) })
})

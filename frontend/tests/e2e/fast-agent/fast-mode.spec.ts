import { fastAgentTest } from '../fastagent-fixtures'
import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { relatedNativeProof } from './scenarios'

fastAgentTest('exposes no separate native fast mode setting', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'fast-mode', relatedProof: () => relatedNativeProof(native) })
})

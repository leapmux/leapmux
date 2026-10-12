import { fastAgentTest } from '../fastagent-fixtures'
import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { relatedNativeProof } from './scenarios'

fastAgentTest('exposes no separate native output style setting', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'output-style', relatedProof: () => relatedNativeProof(native) })
})

import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { ohMyPiTest } from '../ohmypi-fixtures'
import { relatedNativeProof } from './scenarios'

ohMyPiTest('proves the missing extended-thinking setting against the live catalog and a native tool', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'extended-thinking', relatedProof: () => relatedNativeProof(native) })
})

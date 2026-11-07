import { antigravityTest } from '../antigravity-fixtures'
import { exerciseNativeWorkspaceTrustLimit, instructionFileConfiguration } from '../helpers/nativeWorkspaceTrustLimit'

antigravityTest('reads native workspace instructions without a workspace trust control', async ({ native }) => {
  await exerciseNativeWorkspaceTrustLimit(native, {
    projectConfiguration: instructionFileConfiguration('GEMINI.md'),
  })
})

import { geminiTest } from '../gemini-fixtures'
import { exerciseNativeWorkspaceTrustLimit, instructionFileConfiguration } from '../helpers/nativeWorkspaceTrustLimit'

geminiTest('reads native workspace instructions without a workspace trust control', async ({ native }) => {
  await exerciseNativeWorkspaceTrustLimit(native, { projectConfiguration: instructionFileConfiguration('GEMINI.md') })
})

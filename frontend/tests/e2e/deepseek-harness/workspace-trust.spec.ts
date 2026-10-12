import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseNativeWorkspaceTrustLimit, instructionFileConfiguration } from '../helpers/nativeWorkspaceTrustLimit'

deepseekHarnessTest('loads actual workspace instructions without a native workspace trust question', async ({ native }) => {
  await exerciseNativeWorkspaceTrustLimit(native, { projectConfiguration: instructionFileConfiguration('AGENTS.md') })
})

import { clineExtractControl } from '../../../src/components/chat/providers/cline/extractControl'
import { clineTest } from '../cline-fixtures'
import { exerciseMissingWorkspaceTrustRoute, exerciseNativeWorkspaceTrustLimit, instructionFileConfiguration } from '../helpers/nativeWorkspaceTrustLimit'

clineTest('classifies real native controls and proves the missing workspace-trust route', async ({ native }) => {
  await exerciseMissingWorkspaceTrustRoute(native, { askOption: 'permissionMode-act', classify: clineExtractControl })
})

clineTest('loads project rules without a workspace trust decision', async ({ native }) => {
  await exerciseNativeWorkspaceTrustLimit(native, { projectConfiguration: instructionFileConfiguration('AGENTS.md') })
})

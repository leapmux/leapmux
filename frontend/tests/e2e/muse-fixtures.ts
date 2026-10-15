import { missingBinaryReason } from './helpers/binaryOnPath'
import { MUSE_AGENT, nativeContext } from './muse-code/scenarios'
import { createProviderTest } from './provider-fixture-factory'

export const MUSE_E2E_SKIP_REASON: string | null = missingBinaryReason('muse', 'Muse Code E2E requires the native muse executable.')

export const museTest = createProviderTest({
  agent: MUSE_AGENT,
  nativeContext,
  skipReason: MUSE_E2E_SKIP_REASON,
  workspaceName: 'authenticatedMuseWorkspace',
  workspaceOptions: { openOptions: { optionValues: { permissionMode: 'allowAll' } } },
})

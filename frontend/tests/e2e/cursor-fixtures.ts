import { CURSOR_AGENT, nativeContext } from './cursor/scenarios'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { createProviderTest } from './provider-fixture-factory'

export const CURSOR_E2E_SKIP_REASON: string | null = missingBinaryReason('agent', 'Cursor E2E requires an agent CLI on PATH')

export const cursorTest = createProviderTest({
  agent: CURSOR_AGENT,
  nativeContext,
  skipReason: CURSOR_E2E_SKIP_REASON,
  workspaceName: 'authenticatedCursorWorkspace',
})

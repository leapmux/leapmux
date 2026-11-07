import { CURSOR_AGENT, nativeContext } from './cursor/scenarios'
import { createProviderTest } from './provider-fixture-factory'

export const cursorTest = createProviderTest({
  agent: CURSOR_AGENT,
  nativeContext,
  workspaceName: 'authenticatedCursorWorkspace',
})

import { MUSE_AGENT, nativeContext } from './muse-code/scenarios'
import { createProviderTest } from './provider-fixture-factory'

export const museTest = createProviderTest({
  agent: MUSE_AGENT,
  nativeContext,
  workspaceName: 'authenticatedMuseWorkspace',
  workspaceOptions: {
    openOptions: {
      optionValues: { permissionMode: 'allowAll' },
    },
  },
})

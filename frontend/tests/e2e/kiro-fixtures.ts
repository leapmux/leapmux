/**
 * Kiro E2E fixtures, on the shared agent workspace lifetime.
 *
 * Kiro calls its native service. `helpers/kiroSurface.ts` answers those calls at the mock endpoint.
 * `helpers/mockAgentEnvironment.ts` points every Kiro setting at that endpoint under an isolated HOME.
 * No test reaches a real Kiro account.
 *
 * The skip check finds the `kiro-cli-chat` binary without running it.
 * The check uses the developer's HOME. Kiro reads its configuration directory on every start.
 * See `helpers/binaryOnPath.ts`.
 */
import type { AgentWorkspace } from './helpers/workspace'
import { OPTION_ID_PERMISSION_MODE } from '../../src/components/chat/settingsGroups'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { KIRO_AGENT, nativeContext } from './kiro/scenarios'
import { createProviderTest } from './provider-fixture-factory'

export const KIRO_E2E_SKIP_REASON: string | null = missingBinaryReason('kiro-cli-chat', 'Kiro E2E requires the kiro-cli-chat CLI on PATH')

export const kiroTest = createProviderTest({
  agent: KIRO_AGENT,
  nativeContext,
  skipReason: KIRO_E2E_SKIP_REASON,
  workspaceName: 'authenticatedKiroWorkspace',
}).extend<{
  askingKiroWorkspace: AgentWorkspace
}>({
  askingKiroWorkspace: authenticatedAgentWorkspace({
    ...KIRO_AGENT,
    prefix: 'kiro-e2e-ask',
    openOptions: {
      optionValues: { [OPTION_ID_PERMISSION_MODE]: 'spec' },
    },
  }),
})

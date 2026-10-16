/**
 * Cline E2E fixtures.
 *
 * The worker starts one private Cline hub for each agent and drives it over Cline's
 * hub WebSocket protocol. Cline calls the model through its `deepseek` provider.
 * `helpers/mockAgentEnvironment.ts` points that provider at the mock endpoint in Cline's settings under an isolated HOME.
 * Tests use no Cline account or real model. They never reach the developer's Cline hub.
 *
 * The skip check finds the `cline` binary without running it.
 * The check uses the developer's HOME. Cline writes to its data directory on every start.
 * See `helpers/binaryOnPath.ts`.
 */
import type { AgentWorkspace } from './helpers/workspace'
import { CLINE_PERMISSION_MODE } from '../../src/generated/contracts/cline-protocol'
import { CLINE_AGENT, nativeContext } from './cline/scenarios'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { createProviderTest } from './provider-fixture-factory'

export const CLINE_E2E_SKIP_REASON: string | null = missingBinaryReason('cline', 'Cline E2E requires the cline CLI on PATH (https://cline.bot/cli)')

/**
 * The agent opens in Auto-approve, which answers every tool call at once.
 *
 * LeapMux opens a new Cline session in Act, which raises a banner for each edit and
 * each command. A spec that tests tool rendering would otherwise answer a banner
 * before each call. The control-request spec opens its own workspace in Act.
 */
const AUTO_APPROVE = { optionValues: { permissionMode: CLINE_PERMISSION_MODE.AutoApprove } }

export const clineTest = createProviderTest({
  agent: CLINE_AGENT,
  nativeContext,
  skipReason: CLINE_E2E_SKIP_REASON,
  /** An agent in Auto-approve, which raises no banner for a tool call. */
  workspaceName: 'authenticatedClineWorkspace',
  workspaceOptions: { openOptions: AUTO_APPROVE },
}).extend<{
  /** An agent in LeapMux's default Act mode, which asks before each edit and command. */
  askingClineWorkspace: AgentWorkspace
  /** An agent in Plan mode, which offers the plan tool. */
  planningClineWorkspace: AgentWorkspace
}>({
  askingClineWorkspace: authenticatedAgentWorkspace({ ...CLINE_AGENT, prefix: 'cline-e2e-act' }),
  planningClineWorkspace: authenticatedAgentWorkspace({ ...CLINE_AGENT, prefix: 'cline-e2e-plan', openOptions: { optionValues: { permissionMode: CLINE_PERMISSION_MODE.Plan } } }),
})

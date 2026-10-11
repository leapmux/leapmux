import type { AgentWorkspace } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { AMP_PERMISSION_MODE } from '../../src/generated/contracts/amp-protocol'
import { AMP_AGENT, nativeContext } from './amp/scenarios'
import { test as base } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { cliSkipFixture } from './provider-fixture-factory'

/**
 * Skip the Amp specs when the `amp` CLI is not installed. The worker is what starts
 * `amp --execute --stream-json`. Without the binary the agent cannot take a turn.
 *
 * The check looks for the binary without running it: it runs in the Playwright
 * process, which carries the developer's own HOME, and Amp reads its configuration
 * and its login under that HOME on every start. See `helpers/binaryOnPath.ts`.
 */
export const AMP_E2E_SKIP_REASON: string | null = missingBinaryReason('amp', 'Amp E2E requires the amp CLI on PATH (https://ampcode.com)')

/**
 * The agent opens in Allow All, which answers every tool call at once.
 *
 * LeapMux opens a new Amp session in Ask, which raises a banner for every call that
 * Amp's executor runs. A spec that tests what a tool DRAWS would otherwise answer a
 * banner before each call. The permission spec opens its own workspace in Ask.
 */
export const AMP_ALLOW_ALL = { optionValues: { permissionMode: AMP_PERMISSION_MODE.AllowAll } }

export const ampTest = base.extend<CliSkipFixture & NativeFixture & {
  authenticatedAmpWorkspace: AgentWorkspace
  /** An agent in LeapMux's default Ask mode, which raises a banner for each call. */
  askingAmpWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(AMP_E2E_SKIP_REASON),
  authenticatedAmpWorkspace: authenticatedAgentWorkspace({ ...AMP_AGENT, openOptions: AMP_ALLOW_ALL }),
  askingAmpWorkspace: authenticatedAgentWorkspace({ ...AMP_AGENT, prefix: 'amp-e2e-ask' }),
  native: async ({ page, modelScript, leapmuxServer, authenticatedAmpWorkspace }, use) => {
    await use(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedAmpWorkspace.workspaceId }))
  },
})

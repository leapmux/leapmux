/** Pi fixtures use the shared agent workspace lifetime. */
import type { AgentWorkspace, ProviderAgent } from './helpers/workspace'
import type { CliSkipFixture } from './provider-fixture-factory'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { test as base, expect } from './fixtures'
import { lookupBinary, versionOutput } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { cliSkipFixture } from './provider-fixture-factory'

const PI_MISSING_REASON = 'Pi E2E requires pi CLI on PATH (https://github.com/badlogic/pi-mono)'

/**
 * Skip Pi E2E tests when the worker cannot start the `pi` CLI. The agent server
 * is what actually contacts Pi's RPC mode; without the binary the agent
 * cannot start and any test would fail before reaching the chat surface.
 *
 * The check finds the file first without running it, which also refuses a mise
 * shim (see `helpers/binaryOnPath.ts`). Only then does it run that file's
 * `--version`, to refuse an install that does not start.
 */
const PI = lookupBinary('pi', PI_MISSING_REASON)
export const PI_E2E_SKIP_REASON: string | null = PI.path === null
  ? PI.skipReason
  : (versionOutput(PI.path) === null ? PI_MISSING_REASON : null)

/** How a Pi agent opens. */
export const PI_AGENT: ProviderAgent = { provider: AgentProvider.PI, prefix: 'pi-e2e' }

export const piTest = base.extend<CliSkipFixture & {
  authenticatedPiWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(PI_E2E_SKIP_REASON),
  authenticatedPiWorkspace: authenticatedAgentWorkspace(PI_AGENT),
})

export { expect }

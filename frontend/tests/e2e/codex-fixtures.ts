/**
 * Codex-specific e2e test fixtures.
 * The shared provider factory opens a Codex agent.
 */
import { CODEX_EXECUTABLE } from '../../src/generated/contracts/codex-protocol'
import { CODEX_AGENT, nativeContext } from './codex/scenarios'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { createProviderTest } from './provider-fixture-factory'

/**
 * The Worker starts the first executable from the contract that its search path holds.
 * The specs run when the path holds any listed executable.
 */
export const CODEX_E2E_SKIP_REASON: string | null = missingBinaryReason(
  Object.values(CODEX_EXECUTABLE),
  `Codex E2E requires a Codex CLI on PATH under one of these names: ${Object.values(CODEX_EXECUTABLE).join(', ')}`,
)

export const codexTest = createProviderTest({
  agent: CODEX_AGENT,
  nativeContext,
  skipReason: CODEX_E2E_SKIP_REASON,
  workspaceName: 'authenticatedCodexWorkspace',
})

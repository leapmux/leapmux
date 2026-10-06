/**
 * Dirac e2e test fixtures.
 *
 * A Dirac turn ends only when the model calls `respond` with `operation: "complete"`,
 * so every scripted turn must queue a tool call, not text.
 */
import type { AgentWorkspace } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { DIRAC_AGENT, nativeContext } from './dirac/scenarios'
import { test as base } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { cliSkipFixture } from './provider-fixture-factory'

export const DIRAC_E2E_SKIP_REASON: string | null = missingBinaryReason('dirac', 'Dirac E2E requires a dirac CLI on PATH')

/** The asking workspace opens its agent only after the isolated Dirac home turns automatic approval off. */
const askingDiracWorkspace = authenticatedAgentWorkspace({ ...DIRAC_AGENT, prefix: 'dirac-e2e-ask' })

export const diracTest = base.extend<CliSkipFixture & NativeFixture & {
  authenticatedDiracWorkspace: AgentWorkspace
  approvalDisabledDiracHome: string
  askingDiracWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(DIRAC_E2E_SKIP_REASON),
  authenticatedDiracWorkspace: authenticatedAgentWorkspace(DIRAC_AGENT),

  approvalDisabledDiracHome: async ({ leapmuxServer }, use) => {
    const home = leapmuxServer.agentEnv.DIRAC_DIR
    if (!home)
      throw new Error('the isolated Dirac home is absent')
    const path = join(home, 'data', 'globalState.json')
    const original = readFileSync(path, 'utf8')
    const parsed: unknown = JSON.parse(original)
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed))
      throw new Error('the isolated Dirac state is not an object')
    writeFileSync(path, JSON.stringify({ ...parsed, autoApproveAllToggled: false, yoloModeToggled: false }))
    try {
      await use(home)
    }
    finally {
      writeFileSync(path, original)
    }
  },

  askingDiracWorkspace: async ({ page, leapmuxServer, approvalDisabledDiracHome }, use, testInfo) => {
    void approvalDisabledDiracHome
    await askingDiracWorkspace({ page, leapmuxServer }, use, testInfo)
  },
  native: async ({ page, modelScript, leapmuxServer, authenticatedDiracWorkspace }, use) => {
    await use(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDiracWorkspace.workspaceId }))
  },
})

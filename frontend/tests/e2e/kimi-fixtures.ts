/**
 * Kimi Code end-to-end fixtures.
 *
 * Every spec reaches the mock model endpoint through the configuration that
 * `helpers/mockAgentEnvironment.ts` writes to `KIMI_CODE_HOME`, never a Kimi
 * account.
 */
import type { MockModelRequestRecord } from './helpers/mockModelScript'
import type { WorkspaceFixture } from './helpers/workspace'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { test as base, expect } from './fixtures'
import { lookupBinary, versionOutput } from './helpers/binaryOnPath'
import { createTestDirectory } from './helpers/runDirectory'
import { loginViaToken, openWorkspace } from './helpers/ui'
import { withAgentWorkspace } from './helpers/workspace'
import { computeKimiE2ESkipReason, KIMI_MISSING_REASON } from './kimi-install'

// The check finds the file first without running it, which also refuses a mise
// shim (see `helpers/binaryOnPath.ts`). Only then does it run that file's
// `--version`, because the version decides between Kimi Code and the legacy
// kimi-cli. A kimi that exists and fails `--version` is not one that the worker
// can start either, so its empty answer skips too.
const KIMI = lookupBinary('kimi', KIMI_MISSING_REASON)
export const KIMI_E2E_SKIP_REASON: string | null = KIMI.path === null
  ? KIMI.skipReason
  : computeKimiE2ESkipReason(versionOutput(KIMI.path) ?? '')

/**
 * The capture for the plan file path that Kimi Code chooses at random.
 *
 * Kimi states the path only in the plan-mode system reminder
 * (`Plan file: <path>`), and its `ExitPlanMode` raises the plan from that file.
 * A scripted `Write` to `{{planFile}}` puts the plan there. The path always
 * ends in `.md`, which keeps the match off the closing tag of the reminder.
 */
export const KIMI_PLAN_FILE_CAPTURE = { planFile: 'Plan file: (\\S+?\\.md)' } as const

/**
 * How many times `needle` occurs in `text`.
 *
 * A model request repeats the whole conversation, so a marker that an earlier
 * message holds is in every later request body. Compare the counts of two
 * bodies to prove that the messages between them hold the marker.
 */
export function occurrences(text: string, needle: string): number {
  if (needle === '')
    throw new Error('occurrences needs a needle that is not empty')
  return text.split(needle).length - 1
}

/** The JSON text of the model request that one scripted step answered. The test fails when no request did. */
export function stepRequestBody(requests: readonly MockModelRequestRecord[], step: number): string {
  const request = requests.find(candidate => candidate.stepIndex === step)
  expect(request, `the request that step ${step} answered`).toBeDefined()
  return JSON.stringify(request?.body)
}

/** A workspace with one Kimi Code agent, and the directory that agent works in. */
export interface KimiWorkspaceFixture extends WorkspaceFixture {
  /** The agent's working directory, where its tool commands run. */
  workingDir: string
}

export const kimiTest = base.extend<{
  kimiWorkspace: KimiWorkspaceFixture
  authenticatedKimiWorkspace: KimiWorkspaceFixture
}>({
  kimiWorkspace: async ({ leapmuxServer }, use) => {
    const workingDir = createTestDirectory('kimi-e2e-wd-')
    await withAgentWorkspace(
      leapmuxServer,
      { provider: AgentProvider.KIMI_CODE, prefix: 'kimi-e2e', workingDir: () => workingDir },
      workspace => use({ ...workspace, workingDir }),
    )
  },

  authenticatedKimiWorkspace: async ({ page, kimiWorkspace, leapmuxServer }, use) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, kimiWorkspace.workspaceId)
    await use(kimiWorkspace)
  },
})

export { expect }

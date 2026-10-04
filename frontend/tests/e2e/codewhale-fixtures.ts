/**
 * Codewhale end-to-end fixtures.
 *
 * Codewhale speaks its own runtime API (REST and server-sent events), not the
 * Agent Client Protocol, so these fixtures use the shared agent workspace
 * lifetime directly, as the ZCode fixtures do. The worker runs the native
 * binary behind the `codewhale` npm wrapper when the wrapper downloaded it, and
 * the wrapper otherwise, so the wrapper on PATH is what the skip check asks for.
 */
import type { Page } from '@playwright/test'
import type { WorkspaceFixture } from './helpers/workspace'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { test as base, expect } from './fixtures'
import { lookupBinary, versionOutput } from './helpers/binaryOnPath'
import { closeComposerMenus, loginViaToken, openSettingsMenu, openWorkspace } from './helpers/ui'
import { withAgentWorkspace } from './helpers/workspace'

/** What `codewhale --version` states: whether the CLI runs, and its version. */
interface CodewhaleInstall {
  installed: boolean
  /** [major, minor, patch], or null for a version line this file cannot read. */
  version: readonly [number, number, number] | null
}

/**
 * What the file at path states for `--version`. The npm wrapper reports a
 * first-run download on stderr, so stdout alone holds the version line.
 */
function codewhaleInstall(path: string): CodewhaleInstall {
  const output = versionOutput(path)
  if (output === null)
    return { installed: false, version: null }
  const match = /(\d+)\.(\d+)\.(\d+)/.exec(output)
  return { installed: true, version: match ? [Number(match[1]), Number(match[2]), Number(match[3])] : null }
}

const CODEWHALE_MISSING_REASON = 'Codewhale E2E requires a codewhale CLI on PATH'

// The check finds the file first without running it, which also refuses a mise
// shim (see `helpers/binaryOnPath.ts`). Only then does it run that file's
// `--version`, which also gives the version that the specs read.
const CODEWHALE = lookupBinary('codewhale', CODEWHALE_MISSING_REASON)

const INSTALL: CodewhaleInstall = CODEWHALE.path === null ? { installed: false, version: null } : codewhaleInstall(CODEWHALE.path)

export const CODEWHALE_E2E_SKIP_REASON: string | null = CODEWHALE.path === null
  ? CODEWHALE.skipReason
  : (INSTALL.installed ? null : CODEWHALE_MISSING_REASON)

/**
 * Whether the installed runtime serves the routes of a thread's background shell
 * jobs, which the worker reads to learn that a job ended.
 *
 * The routes exist from 0.10.0. An older runtime reports a job's end to the model
 * alone, so the worker cannot close the job's row until something else states it.
 */
export const CODEWHALE_SERVES_JOB_ROUTES: boolean = INSTALL.version !== null && (INSTALL.version[0] > 0 || INSTALL.version[1] >= 10)

/** The tool rows on screen. */
export function codewhaleToolMessages(page: Page) {
  return page.locator('[data-tool-message]:visible')
}

/**
 * Assert the permission posture the agent reports.
 *
 * The status bar draws one mode chip, and Codewhale's is its agent/plan mode, so
 * the posture is read off the checked radio of its own settings group instead.
 */
export async function expectCodewhalePosture(page: Page, posture: string): Promise<void> {
  const group = await openSettingsMenu(page, 'permissionMode')
  await expect(group.locator(`[data-testid="permissionMode-${posture}"] input[type="radio"]`)).toBeChecked()
  await closeComposerMenus(page)
}

export const codewhaleTest = base.extend<{
  codewhaleWorkspace: WorkspaceFixture
  authenticatedCodewhaleWorkspace: WorkspaceFixture
}>({
  codewhaleWorkspace: async ({ leapmuxServer }, use) => {
    await withAgentWorkspace(leapmuxServer, { provider: AgentProvider.CODEWHALE, prefix: 'codewhale-e2e' }, use)
  },

  authenticatedCodewhaleWorkspace: async ({ page, codewhaleWorkspace, leapmuxServer }, use) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, codewhaleWorkspace.workspaceId)

    await use(codewhaleWorkspace)
  },
})

export { expect }

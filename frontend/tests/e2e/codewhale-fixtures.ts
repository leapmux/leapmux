/**
 * Codewhale end-to-end fixtures.
 *
 * Codewhale speaks its own runtime API (REST and server-sent events), not the
 * Agent Client Protocol, so these fixtures use the shared agent workspace
 * lifetime directly, as the ZCode fixtures do. The worker runs the native
 * binary behind the `codewhale` npm wrapper when the wrapper downloaded it, and
 * the wrapper otherwise, so the wrapper on PATH is what the skip check asks for.
 */
import type { AgentWorkspace } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { CODEWHALE_AGENT, nativeContext } from './codewhale/scenarios'
import { test as base, expect } from './fixtures'
import { lookupBinary, versionOutput } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { cliSkipFixture } from './provider-fixture-factory'

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
 *
 * The flag stays here, beside the skip reason, because both read the one
 * `codewhale --version` run of this file. `codewhale/scenarios.ts` cannot hold
 * it: this file imports that module, so an import of the version back forms a
 * cycle. A module of its own in `codewhale/` needs the run too. It must either
 * run `codewhale --version` a second time, or take the run out of this file,
 * and with it the skip reason that each provider fixture file computes itself.
 */
export const CODEWHALE_SERVES_JOB_ROUTES: boolean = INSTALL.version !== null && (INSTALL.version[0] > 0 || INSTALL.version[1] >= 10)

export const codewhaleTest = base.extend<CliSkipFixture & NativeFixture & {
  authenticatedCodewhaleWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(CODEWHALE_E2E_SKIP_REASON),
  authenticatedCodewhaleWorkspace: authenticatedAgentWorkspace(CODEWHALE_AGENT),
  native: async ({ page, modelScript, leapmuxServer, authenticatedCodewhaleWorkspace }, use) => {
    await use(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCodewhaleWorkspace.workspaceId }))
  },
})

export { expect }

/**
 * Codewhale end-to-end fixtures.
 *
 * Codewhale uses its native REST API and server-sent events. It does not use the Agent Client Protocol.
 * These fixtures use the shared agent workspace lifetime directly, as the ZCode fixtures do.
 * The worker runs the native binary when the `codewhale` npm wrapper downloaded it.
 * Otherwise, the worker runs the wrapper. The skip check therefore requires the wrapper on PATH.
 */
import { CODEWHALE_AGENT, nativeContext } from './codewhale/scenarios'
import { lookupBinary, versionOutput } from './helpers/binaryOnPath'
import { createProviderTest } from './provider-fixture-factory'

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
 * The flag and skip reason share one `codewhale --version` result in this file.
 * Moving the flag into `codewhale/scenarios.ts` creates an import cycle because this file imports that module.
 * A separate module in `codewhale/` needs the version result too.
 * It must repeat the command or own the command and skip reason together.
 * Each provider fixture file currently computes its own skip reason.
 */
export const CODEWHALE_SERVES_JOB_ROUTES: boolean = INSTALL.version !== null && (INSTALL.version[0] > 0 || INSTALL.version[1] >= 10)

export const codewhaleTest = createProviderTest({
  agent: CODEWHALE_AGENT,
  nativeContext,
  skipReason: CODEWHALE_E2E_SKIP_REASON,
  workspaceName: 'authenticatedCodewhaleWorkspace',
})

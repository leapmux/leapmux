/**
 * The Kimi Code install probe the E2E specs share with the worker.
 *
 * Kept out of `kimi-fixtures.ts` so a vitest unit test can import it without
 * loading Playwright or running `kimi --version`.
 *
 * Two programs install a `kimi` binary. The provider speaks Kimi Code 2.0 or
 * later, whose `web` command starts the server that the worker talks to. The
 * legacy Python kimi-cli has no such server, and the worker refuses to start
 * it. `checkKimiVersion` in
 * `backend/internal/worker/agent/providers/kimi/version.go` states the same
 * rule, so a skip here is the same refusal that a start would report.
 */

/** The earliest Kimi Code release that the provider speaks. */
export const KIMI_MINIMUM_MAJOR_VERSION = 2

/** The skip reason when no `kimi` that the worker can start is on PATH. */
export const KIMI_MISSING_REASON = 'Kimi Code E2E requires a kimi CLI on PATH'

/**
 * Why the Kimi Code specs cannot run, or null when they can.
 *
 * `versionOutput` is what `kimi --version` printed, or null when no `kimi`
 * runs at all.
 */
export function computeKimiE2ESkipReason(versionOutput: string | null): string | null {
  if (versionOutput === null)
    return KIMI_MISSING_REASON
  const match = /(\d+)\.\d+\.\d+/.exec(versionOutput)
  if (!match)
    return `Kimi Code E2E requires Kimi Code ${KIMI_MINIMUM_MAJOR_VERSION}.0 or later, and \`kimi --version\` states no release number`
  const major = Number(match[1])
  if (major < KIMI_MINIMUM_MAJOR_VERSION)
    return `Kimi Code E2E requires Kimi Code ${KIMI_MINIMUM_MAJOR_VERSION}.0 or later, and the kimi on PATH is ${match[0]}, the legacy Python kimi-cli`
  return null
}

/**
 * The Kimi Code install probe the E2E specs share with the worker.
 *
 * Kept out of `kimi-fixtures.ts` so a vitest unit test can import it without
 * loading Playwright or running `kimi --version`.
 *
 * Two programs install a `kimi` binary. The provider requires the Kimi Code release in the shared protocol
 * contract, whose `web` command starts the server that the worker talks to. The
 * legacy Python kimi-cli has no such server, and the worker refuses to start
 * it. `checkKimiVersion` in
 * `backend/internal/worker/agent/providers/kimi/version.go` states the same
 * rule, so a skip here is the same refusal that a start would report.
 */

import { KIMI_VERSION } from '../../src/generated/contracts/kimi-protocol'

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
  const match = /(\d+)\.(\d+)\.(\d+)/.exec(versionOutput)
  if (!match)
    return `Kimi Code E2E requires Kimi Code ${KIMI_VERSION.Minimum} or later, and \`kimi --version\` states no release number`
  const minimum = KIMI_VERSION.Minimum.split('.')
  const comparisons = [
    [
      Number(match[1]),
      Number(minimum[0]),
    ],
    [
      Number(match[2]),
      Number(minimum[1]),
    ],
    [
      Number(match[3]),
      Number(minimum[2]),
    ],
  ] as const
  for (const [versionPart, minimumPart] of comparisons) {
    if (versionPart > minimumPart)
      return null
    if (versionPart < minimumPart) {
      const detail = Number(match[1]) < 2
        ? ', the legacy Python kimi-cli'
        : ''
      return `Kimi Code E2E requires Kimi Code ${KIMI_VERSION.Minimum} or later, and the kimi on PATH is ${match[0]}${detail}`
    }
  }
  return null
}

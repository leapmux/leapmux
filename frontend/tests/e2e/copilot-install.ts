/**
 * The Copilot CLI install probe the E2E specs share with the worker.
 *
 * Kept out of `copilot-fixtures.ts` so a vitest unit test can import it without
 * loading Playwright or running `copilot --version`.
 *
 * Copilot CLI 1.0.103 removes `--no-remote` (remote control became opt-in) and
 * renames `--no-remote-export` to `--sync-session=off`, and LeapMux launches the
 * native server with the new arguments. An older CLI fails in its argument parser
 * before any useful message appears. `checkCopilotVersion` in
 * `backend/internal/worker/agent/providers/copilot/version.go` states the same
 * rule, so a skip here is the same refusal that a start would report.
 */
import process from 'node:process'
import { lookupBinary, versionOutput } from './helpers/binaryOnPath'

interface Release {
  major: number
  minor: number
  patch: number
}

/** The earliest Copilot CLI release that accepts the server arguments LeapMux uses. */
const copilotMinimumRelease: Release = { major: 1, minor: 0, patch: 103 }

/** The same release, in words, for every skip reason. */
export const COPILOT_MINIMUM_VERSION = `${copilotMinimumRelease.major}.${copilotMinimumRelease.minor}.${copilotMinimumRelease.patch}`

/** The skip reason when no `copilot` that the worker can start is on PATH. */
export const COPILOT_MISSING_REASON = 'Copilot E2E requires a copilot CLI on PATH'

/** The release that `text` states first, or null when it states none that compares safely. */
function parseRelease(text: string): Release | null {
  const match = /(\d+)\.(\d+)\.(\d+)/.exec(text)
  if (match === null)
    return null
  const release = { major: Number(match[1]), minor: Number(match[2]), patch: Number(match[3]) }
  if (![release.major, release.minor, release.patch].every(Number.isSafeInteger))
    return null
  return release
}

/** Whether `release` is earlier than `other`, one component at a time. */
function earlierRelease(release: Release, other: Release): boolean {
  return release.major < other.major
    || (release.major === other.major && release.minor < other.minor)
    || (release.major === other.major && release.minor === other.minor && release.patch < other.patch)
}

/**
 * Why the Copilot specs cannot run, or null when they can.
 *
 * `versionOutput` is what `copilot --version` printed, or null when no `copilot`
 * runs at all.
 */
export function computeCopilotE2ESkipReason(versionOutput: string | null): string | null {
  const requirement = `Copilot E2E requires GitHub Copilot CLI ${COPILOT_MINIMUM_VERSION} or later`
  if (versionOutput === null)
    return COPILOT_MISSING_REASON
  const detected = parseRelease(versionOutput)
  if (detected === null)
    return `${requirement}, and \`copilot --version\` states no release number`
  if (earlierRelease(detected, copilotMinimumRelease))
    return `${requirement}, and the copilot on PATH is ${detected.major}.${detected.minor}.${detected.patch}`
  return null
}

/**
 * Why the Copilot specs cannot run, or null when they can.
 *
 * The lookup finds the file that the worker starts, and the version probe then
 * runs that file with `COPILOT_AUTO_UPDATE=false`: the variable selects which
 * runtime `--version` reports, so the probe must pin it just as the worker's
 * launch does. The `lookup` and `version` parameters carry production defaults,
 * so a unit test can exercise the composition without Playwright.
 */
export function probeCopilotE2ESkipReason(
  env: NodeJS.ProcessEnv = process.env,
  lookup: typeof lookupBinary = lookupBinary,
  version: typeof versionOutput = versionOutput,
): string | null {
  const found = lookup('copilot', COPILOT_MISSING_REASON, env)
  if (found.path === null)
    return found.skipReason
  return computeCopilotE2ESkipReason(version(found.path, { ...env, COPILOT_AUTO_UPDATE: 'false' }))
}

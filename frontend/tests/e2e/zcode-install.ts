/**
 * The ZCode install probe the E2E specs share with the worker.
 *
 * Kept out of `zcode-fixtures.ts` so a vitest unit test can import it without
 * loading Playwright.
 */
import { posix, win32 } from 'node:path'

function joinFor(platform: NodeJS.Platform, ...parts: string[]): string {
  return (platform === 'win32'
    ? win32
    : posix).join(...parts)
}

/**
 * The per-OS locations the worker probes for `zcode.cjs`. Kept in the same
 * order as `zcodeScriptCandidates` in `backend/internal/worker/agent/providers/zcode/resolve.go`,
 * so a skip here is the same absence that `ListAvailableProviders` would report.
 */
export function zcodeScriptCandidatePaths(
  platform: NodeJS.Platform,
  home: string,
  env: NodeJS.ProcessEnv,
): string[] {
  switch (platform) {
    case 'darwin':
      return [
        joinFor(platform, home, 'Applications', 'ZCode.app', 'Contents', 'Resources', 'glm', 'zcode.cjs'),
        joinFor(platform, '/Applications', 'ZCode.app', 'Contents', 'Resources', 'glm', 'zcode.cjs'),
      ]
    case 'win32': {
      const out: string[] = []
      const local = env.LOCALAPPDATA
      if (local)
        out.push(joinFor(platform, local, 'Programs', 'ZCode', 'resources', 'glm', 'zcode.cjs'))
      for (const key of ['ProgramFiles', 'ProgramFiles(x86)'] as const) {
        const base = env[key]
        if (base)
          out.push(joinFor(platform, base, 'ZCode', 'resources', 'glm', 'zcode.cjs'))
      }
      return out
    }
    default:
      return [
        joinFor(platform, home, '.local', 'share', 'ZCode', 'resources', 'glm', 'zcode.cjs'),
        joinFor(platform, '/opt', 'ZCode', 'resources', 'glm', 'zcode.cjs'),
        joinFor(platform, '/usr', 'share', 'zcode', 'resources', 'glm', 'zcode.cjs'),
        joinFor(platform, '/usr', 'lib', 'zcode', 'resources', 'glm', 'zcode.cjs'),
      ]
  }
}

/**
 * The skip message, or null when the launcher or bundled script can start.
 * The suite supplies private provider configuration and a mock credential.
 */
export function computeZCodeE2ESkipReason(opts: {
  scriptOverride: string | undefined
  scriptExists: (path: string) => boolean
  launcherOnPath: boolean
  launcherUnusableReason: string | null
  platform: NodeJS.Platform
  home: string
  env: NodeJS.ProcessEnv
}): string | null {
  const installed = opts.scriptOverride
    ? (opts.scriptExists(opts.scriptOverride)
        ? null
        : `LEAPMUX_ZCODE_SCRIPT points at a missing file: ${opts.scriptOverride}`)
    : opts.launcherUnusableReason !== null
      ? `ZCode E2E cannot start the zcode launcher on PATH. ${opts.launcherUnusableReason}`
      : (opts.launcherOnPath || zcodeScriptCandidatePaths(opts.platform, opts.home, opts.env).some(opts.scriptExists)
          ? null
          : 'ZCode E2E requires a zcode launcher on PATH or a ZCode.app / ZCode install that carries zcode.cjs')
  if (installed)
    return installed

  return null
}

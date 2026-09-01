/**
 * The ZCode install probe the E2E specs share with the worker.
 *
 * Kept out of `zcode-fixtures.ts` so a vitest unit test can import it without
 * loading Playwright or spawning `zcode --help`.
 */
import { posix, win32 } from 'node:path'

function joinFor(platform: NodeJS.Platform, ...parts: string[]): string {
  return (platform === 'win32' ? win32 : posix).join(...parts)
}

/**
 * The per-OS locations the worker probes for `zcode.cjs`. Kept in the same
 * order as `zcodeScriptCandidates` in `backend/internal/worker/agent/zcode_resolve.go`,
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
 * The path of the configuration `StartZCode` reads, matching `zcodeConfigRelPath` in
 * `backend/internal/worker/agent/zcode_config.go`.
 */
export function zcodeConfigPath(platform: NodeJS.Platform, home: string): string {
  return joinFor(platform, home, '.zcode', 'v2', 'config.json')
}

/**
 * Whether the configuration carries a provider `StartZCode` could actually run on.
 *
 * The same two filters `buildZCodeCatalog` applies: an API key that is not blank, and
 * at least one model. A provider that fails either is skipped there, and a
 * configuration with no surviving provider fails the launch.
 */
export function zcodeConfigHasUsableProvider(text: string): boolean {
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  }
  catch {
    return false
  }
  if (typeof parsed !== 'object' || parsed === null)
    return false
  const providers = (parsed as { provider?: unknown }).provider
  if (typeof providers !== 'object' || providers === null)
    return false
  return Object.values(providers as Record<string, unknown>).some((entry) => {
    if (typeof entry !== 'object' || entry === null)
      return false
    const options = (entry as { options?: unknown }).options
    const apiKey = typeof options === 'object' && options !== null
      ? (options as { apiKey?: unknown }).apiKey
      : undefined
    if (typeof apiKey !== 'string' || apiKey.trim() === '')
      return false
    const models = (entry as { models?: unknown }).models
    return typeof models === 'object' && models !== null && Object.keys(models).length > 0
  })
}

/**
 * The skip message, or null when a ZCode install is usable.
 *
 * FOUR states, not three. `StartZCode` resolves the launch AND reads
 * `~/.zcode/v2/config.json`, and it fails on either — so a machine that has the desktop
 * application but was never signed in must SKIP, not run six specs that each time out
 * at `sendMessage` with a message that names nothing.
 */
export function computeZCodeE2ESkipReason(opts: {
  scriptOverride: string | undefined
  scriptExists: (path: string) => boolean
  launcherOnPath: boolean
  platform: NodeJS.Platform
  home: string
  env: NodeJS.ProcessEnv
  readConfig: (path: string) => string | null
}): string | null {
  const installed = opts.scriptOverride
    ? (opts.scriptExists(opts.scriptOverride)
        ? null
        : `LEAPMUX_ZCODE_SCRIPT points at a missing file: ${opts.scriptOverride}`)
    : (opts.launcherOnPath || zcodeScriptCandidatePaths(opts.platform, opts.home, opts.env).some(opts.scriptExists)
        ? null
        : 'ZCode E2E requires a zcode launcher on PATH or a ZCode.app / ZCode install that carries zcode.cjs')
  if (installed)
    return installed

  const configPath = zcodeConfigPath(opts.platform, opts.home)
  const text = opts.readConfig(configPath)
  if (text === null || !zcodeConfigHasUsableProvider(text)) {
    return `ZCode E2E requires ${configPath} to carry a model provider with an API key `
      + '(sign in with the ZCode application once)'
  }
  return null
}

import type { OAuthProviderInfo } from '~/generated/leapmux/v1/auth_pb'
import { authClient } from '~/api/clients'
import { getCapabilities, isTauriApp } from '~/api/platformBridge'
import { formatLocalDateTime } from './dateFormat'

export interface BuildInfo {
  version: string
  commitHash: string
  commitTime: string
  buildTime: string
  branch: string
}

let soloMode = false
let signupEnabled = false
let setupRequired = false
let workerHubUrl = ''
let emailEnabled = false
let loaded = false

let backendBuildInfo: BuildInfo = { version: '', commitHash: '', commitTime: '', buildTime: '', branch: '' }

const frontendBuildInfo: BuildInfo = {
  version: import.meta.env.LEAPMUX_VERSION || '',
  commitHash: import.meta.env.LEAPMUX_COMMIT_HASH || '',
  commitTime: import.meta.env.LEAPMUX_COMMIT_TIME || '',
  buildTime: import.meta.env.LEAPMUX_BUILD_TIME || '',
  branch: import.meta.env.LEAPMUX_BRANCH || '',
}

export async function loadSystemInfo(force = false): Promise<void> {
  if (loaded && !force)
    return
  try {
    const resp = await authClient.getSystemInfo({})
    soloMode = resp.soloMode
    signupEnabled = resp.signupEnabled
    setupRequired = resp.setupRequired
    workerHubUrl = resp.workerHubUrl
    emailEnabled = resp.emailEnabled
    backendBuildInfo = {
      version: resp.version,
      commitHash: resp.commitHash,
      commitTime: resp.commitTime,
      buildTime: resp.buildTime,
      branch: resp.branch,
    }
    loaded = true
  }
  catch {
    // Ignore — defaults to false (non-solo)
  }
}

export function isSoloMode(): boolean {
  return soloMode
}

export function isSignupEnabled(): boolean {
  return signupEnabled
}

export function isSetupRequired(): boolean {
  return setupRequired
}

// isEmailEnabled returns whether the hub has SMTP configured. Components
// gate optional email affordances (e.g. the "Send email" button on the
// worker registration dialog) on this flag — the corresponding RPC
// returns FailedPrecondition without SMTP, so showing a button that
// can't possibly work would mislead users.
export function isEmailEnabled(): boolean {
  return emailEnabled
}

// getWorkerHubUrl returns the URL workers should target when registering.
// Populated when the hub has an explicit --public-url configured (e.g. behind
// a reverse proxy) or when TCP is disabled (desktop app's local-only mode,
// where the browser origin resolves to `tauri://localhost` and the only
// viable URL is the unix-socket / named-pipe address). Empty otherwise — the
// caller should fall back to `window.location.origin`, which already reflects
// the public-facing URL the user is connecting through.
export function getWorkerHubUrl(): string {
  return workerHubUrl
}

let cachedOAuthProviders: OAuthProviderInfo[] | null = null

export async function loadOAuthProviders(): Promise<OAuthProviderInfo[]> {
  if (cachedOAuthProviders !== null) {
    return cachedOAuthProviders
  }
  try {
    const resp = await authClient.getOAuthProviders({})
    cachedOAuthProviders = resp.providers
    return cachedOAuthProviders
  }
  catch {
    return []
  }
}

export function isDesktopApp(): boolean {
  const capabilities = getCapabilities()
  return isTauriApp() && capabilities.mode !== 'tauri-mobile-distributed'
}

export function getBackendBuildInfo(): BuildInfo {
  return backendBuildInfo
}

export function getFrontendBuildInfo(): BuildInfo {
  return frontendBuildInfo
}

const logoColor = '#0D9488'

const logoArt = [
  '█   █▀▀ █▀█ █▀█ █▄ ▄█ █ █ █ █',
  '█   █▀  █▀█ █▀▀ █ ▀ █ █ █ ▄▀▄',
  '▀▀▀ ▀▀▀ ▀ ▀ ▀   ▀   ▀ ▀▀▀ ▀ ▀',
].map(l => l.replaceAll(' ', ' '))

export function formatBuildTime(iso: string): string {
  if (!iso)
    return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime()))
    return iso
  return formatLocalDateTime(d)
}

// Canonical single-line identity string, matching backend/util/version.Format:
//   '0.0.1-dev · 9c81b87 · feature/foo · Thu, 4/23/2026, 11:45:00 PM KST'
// Branch is shown verbatim when present and non-main. Detached HEAD
// (tag / ad-hoc checkouts) and 'main' both render as empty so the
// banner stays clean; the '-dev' suffix on version is what
// distinguishes a dev build from a release.
export function formatVersionLine(info: BuildInfo): string {
  const parts: string[] = [info.version || 'dev']
  if (info.commitHash)
    parts.push(info.commitHash)
  if (info.branch && info.branch !== 'main')
    parts.push(info.branch)
  const time = formatBuildTime(info.buildTime)
  if (time)
    parts.push(time)
  return parts.join(' · ')
}

let bannerPrinted = false

export function printConsoleBanner(): void {
  if (bannerPrinted)
    return
  bannerPrinted = true

  const backend = backendBuildInfo
  const frontend = frontendBuildInfo
  const same = formatVersionLine(backend) === formatVersionLine(frontend)

  // Build styled console.log arguments.
  // Each art line: logo portion in teal, then reset.
  const lines = logoArt.map(l => `%c${l}%c`)
  const styles = logoArt.flatMap(() => [`color:${logoColor};font-weight:bold`, ''])

  // Version info below the art.
  if (same) {
    lines.push(formatVersionLine(backend))
  }
  else {
    lines.push(`Backend:  ${formatVersionLine(backend)}`)
    lines.push(`Frontend: ${formatVersionLine(frontend)}`)
  }
  const year = backend.commitTime ? new Date(backend.commitTime).getFullYear() : new Date().getFullYear()
  lines.push(`Copyright © ${year} Event Loop, Inc.`)

  // eslint-disable-next-line no-console
  console.log(lines.join('\n'), ...styles)
}

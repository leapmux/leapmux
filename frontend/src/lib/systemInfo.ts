import type { OAuthProviderInfo } from '~/generated/leapmux/v1/auth_pb'
import type { BuildInfo } from '~/lib/buildEnv'
import { createSignal } from 'solid-js'
import { authClient } from '~/api/clients'
import { getCapabilities, isTauriApp } from '~/api/platformBridge'
import { frontendBuildInfo } from '~/lib/buildEnv'
import { formatLocalDateTime } from './dateFormat'

let soloMode = false
let signupEnabled = false
let setupRequired = false
let workerHubUrl = ''
let emailEnabled = false
let backendBuildInfo: BuildInfo = { version: '', commitHash: '', commitTime: '', buildTime: '', branch: '' }

// The captcha state is a signal, not a plain variable, so `<Show>` gates in
// the auth forms re-evaluate when a (re)load of the system info flips the
// flag — a plain module read evaluates once at mount and never again.
const [captchaEnabledSignal, setCaptchaEnabled] = createSignal(false)
const [captchaAlgorithmSignal, setCaptchaAlgorithm] = createSignal('')
const [systemInfoLoadedSignal, setSystemInfoLoaded] = createSignal(false)

// loadSystemInfo fetches the hub's system info and caches it (unless `force`).
//
// A failure REJECTS rather than being swallowed. The module's defaults
// (notably `soloMode = false`) are fabrications until a call succeeds, and a
// caller that cannot tell "the hub said non-solo" from "we never asked" makes
// unrecoverable decisions on them: on a solo hub the app would render a "Log
// out" button whose one click sends the user to a login form no credentials
// can satisfy. The loaded flag still flips only on success, so a failed load
// is retried by the next unforced call.
//
// `force` re-fetches and rewrites the signals, so a caller that suspects its
// snapshot is stale (a login denied as "captcha verification failed" after
// the admin toggled captcha at runtime) can converge on the hub's current
// state without a page reload.
export async function loadSystemInfo(force = false): Promise<void> {
  if (systemInfoLoadedSignal() && !force)
    return
  const resp = await authClient.getSystemInfo({})
  soloMode = resp.soloMode
  signupEnabled = resp.signupEnabled
  setupRequired = resp.setupRequired
  workerHubUrl = resp.workerHubUrl
  emailEnabled = resp.emailEnabled
  setCaptchaEnabled(resp.captchaEnabled)
  setCaptchaAlgorithm(resp.captchaAlgorithm)
  backendBuildInfo = {
    version: resp.version,
    commitHash: resp.commitHash,
    commitTime: resp.commitTime,
    buildTime: resp.buildTime,
    branch: resp.branch,
  }
  setSystemInfoLoaded(true)
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

// isSystemInfoLoaded reports whether a system-info answer has arrived.
// The value is a signal, so form gates re-evaluate when bootstrap lands.
// Read any other getter only after this flips, or you read the fabricated
// pre-fetch default.
export function isSystemInfoLoaded(): boolean {
  return systemInfoLoadedSignal()
}

// isCaptchaEnabled returns whether the hub requires ALTCHA proof-of-work on
// Login/SignUp/CompleteOAuthSignup. Backed by a signal, so a `<Show>` that
// reads it re-evaluates when the system info (re)loads — including the
// denial-driven refresh after an admin toggles captcha at runtime.
export function isCaptchaEnabled(): boolean {
  return captchaEnabledSignal()
}

// getCaptchaAlgorithm returns the hub's active ALTCHA algorithm name
// (informational; the authoritative parameters arrive with each challenge).
export function getCaptchaAlgorithm(): string {
  return captchaAlgorithmSignal()
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

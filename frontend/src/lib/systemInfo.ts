import type { OAuthProviderInfo } from '~/generated/leapmux/v1/auth_pb'
import type { BuildInfo } from '~/lib/buildEnv'
import { createSignal } from 'solid-js'
import { authClient } from '~/api/clients'
import { getCapabilities, isTauriApp } from '~/api/platformBridge'
import { CaptchaProvider as GenCaptchaProvider } from '~/generated/leapmux/v1/auth_pb'
import { frontendBuildInfo } from '~/lib/buildEnv'
import { formatLocalDateTime } from './dateFormat'

export type CaptchaProvider = GenCaptchaProvider

// One snapshot of the hub's GetSystemInfo answer, held in a single
// signal. Every getter derives from it, so every consumer — a `<Show>`
// gate, a createEffect, a createMemo — re-evaluates when the system info
// (re)loads, including the denial-driven forced refresh after an admin
// toggles captcha or signup at runtime. Before the first successful
// load the snapshot is null and the getters answer fabricated defaults
// (see DEFAULTS below); isSystemInfoLoaded() distinguishes the two
// states.
interface SystemInfoSnapshot {
  soloMode: boolean
  signupEnabled: boolean
  setupRequired: boolean
  workerHubUrl: string
  emailEnabled: boolean
  captchaEnabled: boolean
  captchaProvider: GenCaptchaProvider
  captchaSiteKey: string
  altchaAlgorithm: string
  backendBuildInfo: BuildInfo
}

// The pre-fetch answers: every value is the safest "feature off" guess,
// so a consumer that reads before the first load fails closed rather
// than flashing an affordance the hub may not back.
const DEFAULTS: SystemInfoSnapshot = {
  soloMode: false,
  signupEnabled: false,
  setupRequired: false,
  workerHubUrl: '',
  emailEnabled: false,
  captchaEnabled: false,
  captchaProvider: GenCaptchaProvider.ALTCHA,
  captchaSiteKey: '',
  altchaAlgorithm: '',
  backendBuildInfo: { version: '', commitHash: '', commitTime: '', buildTime: '', branch: '' },
}

// The hub's degraded reporting path sends UNSPECIFIED, and an enum value
// this build does not know can arrive after a downgrade — both narrow to
// ALTCHA, the hub's own fallback.
function parseCaptchaProvider(raw: GenCaptchaProvider): GenCaptchaProvider {
  return raw === GenCaptchaProvider.RECAPTCHA_V3 || raw === GenCaptchaProvider.TURNSTILE
    ? raw
    : GenCaptchaProvider.ALTCHA
}

const [snapshot, setSnapshot] = createSignal<SystemInfoSnapshot | null>(null)

function current(): SystemInfoSnapshot {
  return snapshot() ?? DEFAULTS
}

// loadSystemInfo fetches the hub's system info and caches it (unless `force`).
//
// A failure REJECTS rather than being swallowed. The defaults above
// (notably `soloMode = false`) are fabrications until a call succeeds, and a
// caller that cannot tell "the hub said non-solo" from "we never asked" makes
// unrecoverable decisions on them: on a solo hub the app would render a "Log
// out" button whose one click sends the user to a login form no credentials
// can satisfy. The snapshot still lands only on success, so a failed load
// is retried by the next unforced call.
//
// `force` re-fetches and rewrites the snapshot, so a caller that suspects its
// view is stale (a login denied as "captcha verification failed" after
// the admin toggled captcha at runtime) can converge on the hub's current
// state without a page reload.
export async function loadSystemInfo(force = false): Promise<void> {
  if (snapshot() !== null && !force)
    return
  const resp = await authClient.getSystemInfo({})
  setSnapshot({
    soloMode: resp.soloMode,
    signupEnabled: resp.signupEnabled,
    setupRequired: resp.setupRequired,
    workerHubUrl: resp.workerHubUrl,
    emailEnabled: resp.emailEnabled,
    captchaEnabled: resp.captchaEnabled,
    captchaProvider: parseCaptchaProvider(resp.captchaProvider ?? GenCaptchaProvider.UNSPECIFIED),
    captchaSiteKey: resp.captchaSiteKey,
    altchaAlgorithm: resp.altchaAlgorithm,
    backendBuildInfo: {
      version: resp.version,
      commitHash: resp.commitHash,
      commitTime: resp.commitTime,
      buildTime: resp.buildTime,
      branch: resp.branch,
    },
  })
}

export function isSoloMode(): boolean {
  return current().soloMode
}

// refreshDedupeWindow limits how often refreshSnapshot may issue a fetch.
// The failure sites that suspect a stale snapshot (a captcha denial, a
// challenge-fetch error, a widget that cannot arm) can fire within
// milliseconds of each other for the same underlying event; the window
// collapses them into one round trip while still letting a genuinely new
// failure retry soon after.
const refreshDedupeWindow = 3000
let lastRefreshAt = 0

// refreshSnapshot is the ONE convergence primitive for "the snapshot may
// be stale": a deduped forced loadSystemInfo. Every captcha-failure site
// routes through it (never through a bare loadSystemInfo(true)), so one
// provider-switch event costs one extra fetch no matter how many sites
// noticed it, and a new failure site gets the same guarantee by calling
// this instead of inventing its own trigger.
export function refreshSnapshot(): void {
  const now = Date.now()
  if (now - lastRefreshAt < refreshDedupeWindow)
    return
  lastRefreshAt = now
  loadSystemInfo(true).catch(() => {
    // A failed refresh keeps the old snapshot; the next call outside the
    // dedupe window retries.
  })
}

export function isSignupEnabled(): boolean {
  return current().signupEnabled
}

export function isSetupRequired(): boolean {
  return current().setupRequired
}

// isEmailEnabled returns whether the hub has SMTP configured. Components
// gate optional email affordances (e.g. the "Send email" button on the
// worker registration dialog) on this flag — the corresponding RPC
// returns FailedPrecondition without SMTP, so showing a button that
// can't possibly work would mislead users.
export function isEmailEnabled(): boolean {
  return current().emailEnabled
}

// isSystemInfoLoaded reports whether a system-info answer has arrived.
// Read any other getter only after this flips, or you read the fabricated
// pre-fetch default.
export function isSystemInfoLoaded(): boolean {
  return snapshot() !== null
}

// isCaptchaEnabled returns whether the hub requires a captcha token on
// Login/SignUp/CompleteOAuthSignup. Derived from the snapshot signal, so
// a `<Show>` that reads it re-evaluates when the system info (re)loads —
// including the denial-driven refresh after an admin toggles captcha at
// runtime.
export function isCaptchaEnabled(): boolean {
  return current().captchaEnabled
}

// getCaptchaProvider returns the active captcha provider (the generated
// CaptchaProvider enum: ALTCHA, RECAPTCHA_V3, TURNSTILE — never
// UNSPECIFIED). The widget layer switches on this to mount the right
// field; a provider switch reaches the forms on the next system-info
// reload without a page refresh.
export function getCaptchaProvider(): CaptchaProvider {
  return current().captchaProvider
}

// getCaptchaSiteKey returns the public site key for external providers
// (recaptcha_v3 / turnstile); empty for altcha, whose challenge arrives
// per submission via GetAltchaChallenge instead.
export function getCaptchaSiteKey(): string {
  return current().captchaSiteKey
}

// getAltchaAlgorithm returns the hub's active ALTCHA algorithm name
// (informational; the authoritative parameters arrive with each challenge).
// Empty when another provider is selected.
export function getAltchaAlgorithm(): string {
  return current().altchaAlgorithm
}

// getWorkerHubUrl returns the URL workers should target when registering.
// Populated when the hub has an explicit --public-url configured (e.g. behind
// a reverse proxy) or when TCP is disabled (desktop app's local-only mode,
// where the browser origin resolves to `tauri://localhost` and the only
// viable URL is the unix-socket / named-pipe address). Empty otherwise — the
// caller should fall back to `window.location.origin`, which already reflects
// the public-facing URL the user is connecting through.
export function getWorkerHubUrl(): string {
  return current().workerHubUrl
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
  return current().backendBuildInfo
}

export function getFrontendBuildInfo(): BuildInfo {
  return frontendBuildInfo
}

const logoColor = '#0D9488'

// FIGURE SPACE (U+2007) keeps each glyph column at a fixed digit-width
// advance in proportional console fonts, so the ASCII logo stays aligned.
// Written as an explicit escape so an editor cannot silently normalize it
// back to a plain space (which made the substitution a no-op once
// before).
const FIGURE_SPACE = '\u2007'

const logoArt = [
  '█   █▀▀ █▀█ █▀█ █▄ ▄█ █ █ █ █',
  '█   █▀  █▀█ █▀▀ █ ▀ █ █ █ ▄▀▄',
  '▀▀▀ ▀▀▀ ▀ ▀ ▀   ▀   ▀ ▀▀▀ ▀ ▀',
].map(l => l.replaceAll(' ', FIGURE_SPACE))

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

  const backend = getBackendBuildInfo()
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

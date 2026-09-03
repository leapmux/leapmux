import type { OAuthProviderInfo } from '~/generated/proto/leapmux/v1/auth_pb'
import type { BuildInfo } from '~/lib/buildEnv'
import { createSignal } from 'solid-js'
import { authClient } from '~/api/clients'
import { getCapabilities, isTauriApp } from '~/api/platformBridge'
import { CaptchaProvider as GenCaptchaProvider } from '~/generated/proto/leapmux/v1/auth_pb'
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
  autoAuthenticated: boolean
  passwordSetupRequired: boolean
  soloPasswordSet: boolean
  signupEnabled: boolean
  setupRequired: boolean
  workerHubUrl: string
  emailEnabled: boolean
  passkeyEnabled: boolean
  captchaEnabled: boolean
  captchaProvider: GenCaptchaProvider
  captchaSiteKey: string
  altchaAlgorithm: string
  backendBuildInfo: BuildInfo
}

// The pre-fetch answers: every value is the safest "feature off" guess,
// so a consumer that reads before the first load fails closed rather
// than flashing an affordance the hub may not support.
const DEFAULTS: SystemInfoSnapshot = {
  soloMode: false,
  autoAuthenticated: false,
  passwordSetupRequired: false,
  soloPasswordSet: false,
  signupEnabled: false,
  setupRequired: false,
  workerHubUrl: '',
  emailEnabled: false,
  passkeyEnabled: false,
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
// A failure REJECTS; this function never discards it. The defaults above
// (notably `soloMode = false`) are fabrications until a call succeeds, and a
// caller that cannot tell "the hub said non-solo" from "nobody asked yet"
// makes unrecoverable decisions on them: on a solo hub the app would render a
// "Log out" button whose one click sends the user to a login form no
// credentials can satisfy. The snapshot still lands only on success, so the
// next unforced call retries a failed load.
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
    autoAuthenticated: resp.autoAuthenticated,
    passwordSetupRequired: resp.passwordSetupRequired,
    soloPasswordSet: resp.soloPasswordSet,
    signupEnabled: resp.signupEnabled,
    setupRequired: resp.setupRequired,
    workerHubUrl: resp.workerHubUrl,
    emailEnabled: resp.emailEnabled,
    passkeyEnabled: resp.passkeyEnabled,
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

/**
 * Whether the hub authenticates THIS connection with no credentials.
 *
 * Not a synonym for `isSoloMode`, and the two must not be swapped. `isSoloMode`
 * is a property of the HUB — one account, no sign-up — and decides which
 * account settings exist at all. This is a property of the CONNECTION, and
 * decides whether the app falls back to the login form: a solo hub reached over
 * the desktop app's local socket needs no sign-in, and the same hub reached at
 * a network address does, once its account holds a password.
 *
 * The fabricated default is `false`, which sends an unloaded app to the login
 * form rather than leaving it waiting for a session that never arrives.
 */
export function isAutoAuthenticated(): boolean {
  return current().autoAuthenticated
}

/**
 * Whether the app must block itself with a password-setup screen.
 *
 * True only when the hub answers on an address another machine can reach AND
 * its one account has no password. In that state every affordance the app
 * offers is offered to whoever reaches the port, so the one useful thing left
 * is to ask for a password.
 */
export function passwordSetupRequired(): boolean {
  return current().passwordSetupRequired
}

/**
 * Whether the solo hub's single account holds a password.
 *
 * The HUB's rule, which is what the Network access panel asks: publishing an
 * address demands a password, so that panel offers the field while this is
 * false and points at Account → Password once it is true. Always false on a
 * multi-user hub, where each account answers for itself.
 *
 * An account's own password is `auth.user()?.passwordSet`, and Account →
 * Password reads that one. Both answers come from the stored hash, so they
 * agree; they differ in what they describe.
 */
export function soloPasswordSet(): boolean {
  return current().soloPasswordSet
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
// show optional email affordances (e.g. the "Send email" button on the
// worker registration dialog) only when this flag is true — the
// corresponding RPC returns FailedPrecondition without SMTP, so showing a
// button that can't possibly work would mislead users.
export function isEmailEnabled(): boolean {
  return current().emailEnabled
}

/** Why this page cannot run a passkey ceremony. See `passkeyBlocker`. */
export type PasskeyBlocker = 'insecure-context' | 'no-webauthn' | 'origin-not-allowed'

// browserRunsWebAuthn reports whether this browser exposes the WebAuthn API
// on this page. A browser exposes it in a secure context only: HTTPS, or
// plain HTTP at a loopback address.
//
// It is the SAME test @simplewebauthn/browser applies before it touches
// navigator.credentials, and it must stay the same test. A surface that
// offers a ceremony the library then refuses shows the library's own
// message, "WebAuthn is not supported in this browser" -- which identifies
// the wrong cause nearly every time it appears. The browser supports
// passkeys. The page is not secure, so the browser exposes nothing to call.
//
// The browser is the only authority on this, which is why the hub does not
// answer it. A user can configure a user agent to trust an extra origin
// (Chromium's --unsafely-treat-insecure-origin-as-secure), and a hub that
// inferred the answer from the request Origin would refuse a page whose
// ceremonies work.
function browserRunsWebAuthn(): boolean {
  return typeof globalThis.PublicKeyCredential === 'function'
}

/**
 * Why this page cannot run a passkey ceremony, or null when it can.
 *
 * TWO parties must agree, and each one answers for itself. The hub says
 * whether it runs ceremonies for the origin this page is on -- that is what
 * GetSystemInfo's `passkey_enabled` carries, per request origin. The browser
 * says whether it exposes WebAuthn here at all. Either party alone lets a
 * surface offer a passkey affordance that can only fail.
 *
 * The BROWSER's reason comes first, and the order is deliberate. Its reason
 * is the more fundamental repair, and the hub's reason is wrong advice under
 * it: an operator who published http://hub.example:4327 and opened exactly
 * that address reads "open the hub through its configured URL", which is
 * what they already did.
 *
 * Every passkey affordance reads this, never the hub's half alone. That half
 * is not exported, so the mistake cannot be made.
 */
export function passkeyBlocker(): PasskeyBlocker | null {
  if (!browserRunsWebAuthn()) {
    // An explicit false, the same rule the captcha gate and the clipboard
    // hold: jsdom leaves isSecureContext undefined, and an unknown context
    // must not be reported as an insecure one.
    return typeof window !== 'undefined' && window.isSecureContext === false
      ? 'insecure-context'
      : 'no-webauthn'
  }
  return current().passkeyEnabled ? null : 'origin-not-allowed'
}

/** True when a passkey ceremony can run on this page. */
export function passkeysUsableHere(): boolean {
  return passkeyBlocker() === null
}

// isSystemInfoLoaded reports whether a system-info answer arrived.
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
//
// It reports the HUB's answer and nothing else. Whether THIS page can mount
// a widget is a second, unrelated question, and folding it in here made the
// name say one thing and the value mean another: a caller that asked whether
// captcha is enabled read no while the hub was about to say yes.
// isCaptchaUnsolvableHere below answers that second question, and
// createCaptchaForm is the one place that combines them.
export function isCaptchaEnabled(): boolean {
  return current().captchaEnabled
}

// isCaptchaUnsolvableHere reports the one state the two gates can disagree
// on: the hub requires ALTCHA, and THIS page is not a secure context, so
// no widget can mount and nothing can solve the challenge.
//
// It exists because the disagreement is reachable and silent. The hub
// decides from its own configuration (it must, or a request header could
// switch the bot check off), and it requires ALTCHA only for a published
// HTTPS address. So a browser that reaches the hub by SOME OTHER address —
// the LAN IP behind the TLS-terminating proxy, say — gets "captcha
// required" from the hub and "no secure context" from itself. Standing down locally and submitting an empty
// payload turns that into a permanent, undiagnosable "verification failed"
// loop: the hub denies, the form refreshes the snapshot, the hub answers
// the same, forever.
//
// The forms therefore BLOCK on this and say why, which specifies the fix:
// reach the hub at the published HTTPS address, or publish the address
// users really type.
// Require an explicit false: jsdom leaves isSecureContext undefined, and
// treating that as insecure would BLOCK submission on every unit-test ALTCHA
// form. (Under the predicate this replaced the same relaxation stood the form
// down instead; the polarity inverted when the two questions split.)
export function isCaptchaUnsolvableHere(): boolean {
  return current().captchaEnabled
    && captchaProviderNeedsSecureContext(current().captchaProvider)
    && typeof window !== 'undefined'
    && window.isSecureContext === false
}

// captchaProviderNeedsSecureContext reports whether this provider's widget
// needs a secure context to mount. Only ALTCHA does: its proof of work calls
// SubtleCrypto, which a page holds only in a secure context. Turnstile and
// reCAPTCHA v3 both run on a plain-HTTP page.
//
// A NAMED predicate rather than a comparison spelled at the one call site,
// because the backend spells the same rule the same way -- see
// providerRequiresSecureContext in internal/hub/captcha/secure_context.go, the
// gate that stands ALTCHA down on a hub that cannot serve one. The two sides
// then read alike, and a fourth provider gets one entry here instead of a
// silent `false` in front of a widget that cannot mount.
export function captchaProviderNeedsSecureContext(provider: CaptchaProvider): boolean {
  return provider === GenCaptchaProvider.ALTCHA
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
// the public-facing URL the user connects through.
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
// This function shows the branch verbatim when present and non-main. Detached HEAD
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

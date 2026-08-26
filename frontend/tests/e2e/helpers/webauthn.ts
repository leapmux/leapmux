import type { CDPSession, Page } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'

import { solveCaptchaViaAPI } from './altcha'
import { elevateSessionViaAPI } from './api'
import { loginViaToken, readSessionCookie } from './ui'

const SIMPLEWEBAUTHN_BUNDLE = readFileSync(
  join(process.cwd(), 'node_modules/@simplewebauthn/browser/dist/bundle/index.umd.min.js'),
  'utf8',
)

const installedPages = new WeakSet<Page>()

export interface VirtualAuthenticator {
  cdp: CDPSession
  authenticatorId: string
}

/** go-webauthn wraps options in `{ publicKey: … }`; SimpleWebAuthn expects the inner object. */
function unwrapWebAuthnOptionsScript(): string {
  return `
    globalThis.__unwrapWebAuthnOptions = (optionsJson) => {
      const parsed = JSON.parse(optionsJson);
      if (parsed && typeof parsed === 'object' && parsed.publicKey)
        return parsed.publicKey;
      return parsed;
    };
  `
}

async function installBrowserWebAuthn(page: Page): Promise<void> {
  if (installedPages.has(page))
    return
  await page.addInitScript({ content: SIMPLEWEBAUTHN_BUNDLE + unwrapWebAuthnOptionsScript() })
  installedPages.add(page)
}

/**
 * Enable Playwright's CDP WebAuthn virtual authenticator on `page`.
 *
 * Must run before any navigation that triggers a WebAuthn ceremony.
 */
export async function enableVirtualAuthenticator(page: Page): Promise<VirtualAuthenticator> {
  await installBrowserWebAuthn(page)
  const cdp = await page.context().newCDPSession(page)
  // enableUI must stay false so the virtual authenticator auto-completes
  // ceremonies without a native picker that headless Chromium never dismisses.
  await cdp.send('WebAuthn.enable', { enableUI: false })
  const { authenticatorId } = await cdp.send('WebAuthn.addVirtualAuthenticator', {
    options: {
      protocol: 'ctap2',
      ctap2Version: 'ctap2_1',
      transport: 'usb',
      hasResidentKey: true,
      hasUserVerification: true,
      isUserVerified: true,
      automaticPresenceSimulation: true,
    },
  })
  return { cdp, authenticatorId }
}

type CeremonyMethod = 'startRegistration' | 'startAuthentication'

/**
 * Run one Begin → browser ceremony → Finish chain inside `page` against the
 * raw Connect API. `procedure` values carry the service prefix (for example
 * `AuthService/BeginPasskeyLogin`). The Finish body merges
 * `{ sessionId, credentialJson }` with `finishExtra`. Returns the parsed
 * Finish response JSON.
 */
async function runCeremonyInPage(
  page: Page,
  hubUrl: string,
  spec: {
    beginProcedure: string
    beginBody: Record<string, unknown>
    ceremonyMethod: CeremonyMethod
    finishProcedure: string
    finishExtra?: Record<string, unknown>
  },
): Promise<Record<string, unknown>> {
  return await page.evaluate(async ({ hubUrl, spec }: {
    hubUrl: string
    spec: {
      beginProcedure: string
      beginBody: Record<string, unknown>
      ceremonyMethod: 'startRegistration' | 'startAuthentication'
      finishProcedure: string
      finishExtra?: Record<string, unknown>
    }
  }) => {
    const SWB = (globalThis as typeof globalThis & {
      SimpleWebAuthnBrowser?: Record<'startRegistration' | 'startAuthentication', (args: { optionsJSON: unknown }) => Promise<unknown>>
    }).SimpleWebAuthnBrowser
    const unwrap = (globalThis as typeof globalThis & { __unwrapWebAuthnOptions?: (json: string) => unknown }).__unwrapWebAuthnOptions
    const ceremony = SWB?.[spec.ceremonyMethod]
    if (!ceremony || !unwrap)
      throw new Error('SimpleWebAuthnBrowser is not installed on this page')

    const beginRes = await fetch(`${hubUrl}/leapmux.v1.${spec.beginProcedure}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'include',
      body: JSON.stringify(spec.beginBody),
    })
    if (!beginRes.ok)
      throw new Error(`${spec.beginProcedure} failed: ${beginRes.status} ${await beginRes.text()}`)
    const begin = await beginRes.json() as { sessionId?: string, optionsJson?: string }
    if (!begin.sessionId || !begin.optionsJson)
      throw new Error(`${spec.beginProcedure} returned incomplete payload`)

    const credential = await ceremony({ optionsJSON: unwrap(begin.optionsJson) })
    const finishRes = await fetch(`${hubUrl}/leapmux.v1.${spec.finishProcedure}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'include',
      body: JSON.stringify({
        sessionId: begin.sessionId,
        credentialJson: JSON.stringify(credential),
        ...spec.finishExtra,
      }),
    })
    if (!finishRes.ok)
      throw new Error(`${spec.finishProcedure} failed: ${finishRes.status} ${await finishRes.text()}`)
    return await finishRes.json() as Record<string, unknown>
  }, { hubUrl, spec })
}

/** POST one Connect procedure inside `page` and return the parsed response. */
async function postConnectInPage(
  page: Page,
  hubUrl: string,
  procedure: string,
  body: Record<string, unknown>,
): Promise<Record<string, unknown>> {
  return await page.evaluate(async ({ hubUrl, procedure, body }: {
    hubUrl: string
    procedure: string
    body: Record<string, unknown>
  }) => {
    const res = await fetch(`${hubUrl}/leapmux.v1.${procedure}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'include',
      body: JSON.stringify(body),
    })
    if (!res.ok)
      throw new Error(`${procedure} failed: ${res.status} ${await res.text()}`)
    return await res.json() as Record<string, unknown>
  }, { hubUrl, procedure, body })
}

/**
 * Complete a passkey sign-up through the Connect API while running the
 * WebAuthn ceremony inside `page` (virtual authenticator required).
 */
export async function signUpWithPasskeyViaAPIInBrowser(
  page: Page,
  hubUrl: string,
  username: string,
  email: string,
  displayName = '',
): Promise<string> {
  const captcha = await solveCaptchaViaAPI(hubUrl)
  await page.goto(`${hubUrl}/login`)
  await runCeremonyInPage(page, hubUrl, {
    beginProcedure: 'AuthService/BeginPasskeySignUp',
    beginBody: { username, displayName, email, captchaPayload: captcha.captchaPayload, honeypot: captcha.honeypot },
    ceremonyMethod: 'startRegistration',
    finishProcedure: 'AuthService/FinishPasskeySignUp',
  })
  return readSessionCookie(page, 'FinishPasskeySignUp')
}

/** Log in with a passkey via API while running WebAuthn inside `page`. */
export async function loginWithPasskeyViaAPIInBrowser(
  page: Page,
  hubUrl: string,
  username: string,
): Promise<string> {
  const captcha = await solveCaptchaViaAPI(hubUrl)
  await page.goto(`${hubUrl}/login`)
  await runCeremonyInPage(page, hubUrl, {
    beginProcedure: 'AuthService/BeginPasskeyLogin',
    beginBody: { username, captchaPayload: captcha.captchaPayload, honeypot: captcha.honeypot },
    ceremonyMethod: 'startAuthentication',
    finishProcedure: 'AuthService/FinishPasskeyLogin',
  })
  return readSessionCookie(page, 'FinishPasskeyLogin')
}

/**
 * Elevate the session inside `page` with a passkey ceremony.
 *
 * This is the arm a passkey-only account uses: it holds no password, so the
 * assertion IS the factor. Virtual authenticator must already be enabled.
 */
export async function elevateWithPasskeyViaAPIInBrowser(
  page: Page,
  hubUrl: string,
  cookie: string,
): Promise<void> {
  await loginViaToken(page, cookie)
  await page.goto(`${hubUrl}/`)
  await runCeremonyInPage(page, hubUrl, {
    beginProcedure: 'UserService/BeginPasskeyElevation',
    beginBody: {},
    ceremonyMethod: 'startAuthentication',
    finishProcedure: 'UserService/FinishPasskeyElevation',
  })
}

/**
 * Register an additional passkey for the authenticated session inside `page`.
 *
 * The session is elevated FIRST, because passkey management now needs a
 * proven factor rather than a secret on each request. With a password, the
 * password arm; without one, the passkey arm.
 */
export async function addPasskeyViaAPIInBrowser(
  page: Page,
  hubUrl: string,
  cookie: string,
  currentPassword = '',
): Promise<void> {
  if (currentPassword)
    await elevateSessionViaAPI(hubUrl, cookie, currentPassword)
  else
    await elevateWithPasskeyViaAPIInBrowser(page, hubUrl, cookie)
  await loginViaToken(page, cookie)
  await page.goto(`${hubUrl}/`)
  await runCeremonyInPage(page, hubUrl, {
    beginProcedure: 'UserService/BeginPasskeyRegistration',
    beginBody: {},
    ceremonyMethod: 'startRegistration',
    finishProcedure: 'UserService/FinishPasskeyRegistration',
    finishExtra: { friendlyName: 'E2E Passkey' },
  })
}

/**
 * Deactivate all passkeys on a passkey-only account: elevate with a passkey,
 * then set the replacement password the account must retain.
 * Virtual authenticator must already be enabled on `page`.
 */
export async function deactivatePasskeyAuthViaAPIInBrowser(
  page: Page,
  hubUrl: string,
  cookie: string,
  newPassword: string,
): Promise<void> {
  await elevateWithPasskeyViaAPIInBrowser(page, hubUrl, cookie)
  await postConnectInPage(page, hubUrl, 'UserService/DeactivatePasskeyAuth', {
    newPassword,
  })
}

/** Remove a virtual authenticator installed by {@link enableVirtualAuthenticator}. */
export async function removeVirtualAuthenticator(auth: VirtualAuthenticator): Promise<void> {
  await auth.cdp.send('WebAuthn.removeVirtualAuthenticator', { authenticatorId: auth.authenticatorId })
}

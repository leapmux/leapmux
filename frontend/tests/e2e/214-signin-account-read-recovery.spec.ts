import type { Page } from '@playwright/test'
import { Buffer } from 'node:buffer'
import { fromBinary, fromJson, toBinary, toJson } from '@bufbuild/protobuf'
import { expect } from '@playwright/test'
import { SignUpResponseSchema } from '../../src/generated/proto/leapmux/v1/auth_pb'
import { test } from './fixtures'
import { withCaptureSmtp } from './helpers/mail'
import { signUpViaUI } from './helpers/ui'

/** One GetCurrentUser request the route saw, in order. */
interface AccountRead {
  /** True when the route answered with the injected `unavailable` error. */
  injected: boolean
  /** The hub's status, for a read the route passed through. */
  status?: number
}

/**
 * Make the next sign-up report no account, and fail the first account read
 * that follows it.
 *
 * The page's own bootstrap restore issues a GetCurrentUser on `/signup` before
 * the form is submitted, so the failure is armed only once the SignUp response
 * is in hand: arming it earlier would fail the bootstrap read instead.
 */
async function injectAccountReadFailure(page: Page) {
  const reads: AccountRead[] = []
  let failNext = false
  let signUps = 0
  let readsBeforeArming = -1

  await page.route('**/leapmux.v1.AuthService/GetCurrentUser', async (route) => {
    if (failNext) {
      failNext = false
      reads.push({ injected: true })
      await route.fulfill({
        status: 503,
        contentType: 'application/json',
        body: JSON.stringify({ code: 'unavailable', message: 'injected account read failure' }),
      })
      return
    }
    const response = await route.fetch()
    reads.push({
      injected: false,
      status: response.status(),
    })
    await route.fulfill({ response })
  })

  await page.route('**/leapmux.v1.AuthService/SignUp', async (route) => {
    signUps++
    const response = await route.fetch()
    // The sign-up really happened on the hub: its account and session exist.
    // Only the reply loses the account, which is what a hub reports when it
    // could not read the account back.
    const contentType = response.headers()['content-type'] ?? ''
    let body: string | Buffer
    if (contentType.includes('application/proto')) {
      const reply = fromBinary(SignUpResponseSchema, await response.body())
      if (reply.sessionReport)
        reply.sessionReport.user = undefined
      body = Buffer.from(toBinary(SignUpResponseSchema, reply))
    }
    else {
      const reply = fromJson(SignUpResponseSchema, await response.json())
      if (reply.sessionReport)
        reply.sessionReport.user = undefined
      body = JSON.stringify(toJson(SignUpResponseSchema, reply))
    }
    readsBeforeArming = reads.length
    failNext = true
    await route.fulfill({ response, body })
  })

  return {
    reads,
    signUps: () => signUps,
    readsBeforeArming: () => readsBeforeArming,
  }
}

/** The assertions both cases share, once the page reached its destination. */
function expectOneInjectedFailureThenARecoveredRead(injection: Awaited<ReturnType<typeof injectAccountReadFailure>>) {
  const armedAt = injection.readsBeforeArming()
  // The bootstrap restore completed before the failure was armed, and the
  // route passed it through rather than failing it.
  expect(armedAt).toBeGreaterThanOrEqual(1)
  expect(injection.reads.slice(0, armedAt).every(read => !read.injected)).toBe(true)
  // Exactly one read after the sign-up received the injected failure: the
  // first one recovery made.
  expect(injection.reads.filter(read => read.injected)).toHaveLength(1)
  expect(injection.reads[armedAt]?.injected).toBe(true)
  // Retry issued another read, and the hub answered it.
  expect(injection.reads.slice(armedAt + 1).some(read => read.status === 200)).toBe(true)
  // The page never submitted its sign-up a second time.
  expect(injection.signUps()).toBe(1)
}

test.describe('sign-in account read recovery', () => {
  test('recovers a sign-up whose account read fails, and lands on the shell', async ({ page }) => {
    const injection = await injectAccountReadFailure(page)

    await signUpViaUI(page, `recover-${Date.now()}`, 'password123')

    const panel = page.getByTestId('signin-recovery')
    await expect(panel).toBeVisible()
    await expect(panel).toContainText('You are signed in, but your account could not be loaded.')
    await panel.getByRole('button', { name: 'Retry' }).click()

    await expect(page).toHaveURL('/')
    await expect(panel).toBeHidden()
    expectOneInjectedFailureThenARecoveredRead(injection)
  })

  test('recovers to /verify-email when the account must verify its address', async ({ page, leapmuxServer }) => {
    await withCaptureSmtp(leapmuxServer, async () => {
      const injection = await injectAccountReadFailure(page)
      const username = `recover-verify-${Date.now()}`

      await signUpViaUI(page, username, 'password123', 'Recover Verify', `${username}@test.local`)

      const panel = page.getByTestId('signin-recovery')
      await expect(panel).toBeVisible()
      await panel.getByRole('button', { name: 'Retry' }).click()

      await expect(page).toHaveURL(/\/verify-email/)
      await expect(panel).toBeHidden()
      expectOneInjectedFailureThenARecoveredRead(injection)
    })
  })
})

import type { DevServerHandle } from './helpers/devServer'
import { test as base, expect } from '@playwright/test'
import { fetchAltchaChallenge } from './helpers/altcha'
import { mintCLITokenForAdmin, runCLI } from './helpers/cli'
import { startDevServer, stopDevServer } from './helpers/devServer'
import { loginViaUI } from './helpers/ui'

/**
 * Exercises the dynamically loaded WASM solvers for the memory-hard ALTCHA
 * algorithms: SCRYPT and ARGON2ID are NOT pre-registered by the altcha
 * widget build — the frontend must fetch the hub's advertised algorithm,
 * lazily import the matching worker chunk, register it in the widget's
 * solver registry, and only then solve. A dedicated server per case keeps
 * the algorithm switch from leaking into the shared fixture's hub.
 *
 * The hub caches the captcha config for ~30s, and seeding the admin (via
 * the default algorithm) primes that cache, so the switch is only
 * confirmed once the challenge endpoint actually issues the target
 * algorithm — everything after that is guaranteed to exercise the WASM
 * worker path rather than a stale default challenge.
 */
async function waitForChallengeAlgorithm(hubUrl: string, algorithm: string, timeoutMs = 90_000): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    try {
      const challenge = await fetchAltchaChallenge(hubUrl)
      if (challenge?.parameters.algorithm === algorithm) {
        return
      }
    }
    catch {
      // Transient startup failures retry on the next poll.
    }
    await new Promise(r => setTimeout(r, 1000))
  }
  throw new Error(`hub did not issue ${algorithm} challenges within ${timeoutMs}ms`)
}

async function setupServerWithAlgorithm(
  // eslint-disable-next-line no-empty-pattern
  {}: object,
  use: (server: DevServerHandle) => Promise<void>,
  algorithm: string,
  args: string[],
): Promise<void> {
  const server = await startDevServer({ dataDirPrefix: 'leapmux-e2e-captcha-wasm' })
  let cliConfigDir: string | undefined
  try {
    // Captcha configuration is an ONLINE admin RPC against the running
    // hub; the offline captcha verb no longer exists.
    const cfg = await mintCLITokenForAdmin(server)
    cliConfigDir = cfg.path
    await runCLI(cfg, ['admin', 'captcha', 'set', ...args])
    await waitForChallengeAlgorithm(server.hubUrl, algorithm)
    await use(server)
  }
  finally {
    await stopDevServer(server, cliConfigDir ? [cliConfigDir] : [])
  }
}

const cases = [
  {
    algorithm: 'SCRYPT',
    // N=1024/r=8 keeps each derivation ~1ms so the solve finishes fast;
    // the test's subject is the worker-loading path, not PoW difficulty.
    args: ['--algorithm', 'SCRYPT', '--cost', '1024', '--memory-cost', '8'],
  },
  {
    algorithm: 'ARGON2ID',
    // t=1/m=8192KiB: same idea — minimal viable memory-hard params.
    args: ['--algorithm', 'ARGON2ID', '--cost', '1', '--memory-cost', '8192'],
  },
] as const

for (const { algorithm, args } of cases) {
  const test = base.extend<{ server: DevServerHandle }>({
    // eslint-disable-next-line no-empty-pattern
    server: async ({}, use) => setupServerWithAlgorithm({}, use, algorithm, [...args]),
    baseURL: async ({ server }, use) => {
      await use(server.hubUrl)
    },
  })

  test.describe(`captcha WASM solvers (${algorithm})`, () => {
    test(`login solves a ${algorithm} challenge through the dynamically loaded worker`, async ({ page }) => {
      await loginViaUI(page)
      await expect(page).toHaveURL(/\/$/)
    })

    test(`the ${algorithm} solver is registered in the widget's algorithm registry`, async ({ page }) => {
      await page.goto('/login')
      // The CaptchaField pre-warms the solver for the algorithm the hub
      // advertises; the registered factory must exist by the time the
      // checkbox is clickable.
      await page.waitForFunction(
        algorithm => (window as { $altcha?: { algorithms?: Map<string, unknown> } }).$altcha?.algorithms?.has(algorithm) === true,
        algorithm,
      )
      await expect(page.locator('altcha-widget')).toBeVisible()
    })
  })
}

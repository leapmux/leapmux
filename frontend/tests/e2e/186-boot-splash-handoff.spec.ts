import { expect, test } from './fixtures'
import { appMenuTrigger, loginViaToken } from './helpers/ui'

/**
 * The static splash in the served document must yield to the client app.
 *
 * With `ssr: false`, the build aliases `@solidjs/start/client` to its /spa
 * variant. Its `mount()` is solid's plain `render()`: it appends into `#app`
 * and removes no existing child. So the client entry removes the static
 * splash itself, right after `mount()` returns. Before that call existed,
 * the booted app stayed below the 100%-height splash, and the boot watchdog
 * failed every load at 45s with "The app did not start in time."
 *
 * A visibility check alone cannot catch this bug. Playwright counts a
 * below-the-fold shell as visible. So this spec pins the DOM handoff: the
 * served document ships the splash, the entry removes it, and the shell
 * becomes reachable.
 */
test.describe('static boot splash handoff', () => {
  test('the entry removes the static splash and shows the shell', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)

    // The served document ships the splash. The check reads the response
    // BODY — raw HTML. The client may remove the node before Playwright can
    // observe the DOM, but the body keeps the original bytes. So the spec
    // cannot pass vacuously when the document stops embedding a splash.
    const response = await page.goto('/')
    expect(response, 'goto(/) must return the document response').not.toBeNull()
    expect(await response!.text()).toContain('id="boot-splash"')

    // The handoff: the entry removes the static splash after mount. A
    // regression leaves the node in `#app` forever, so this assertion fails
    // by timeout instead of flaking.
    await expect(page.locator('#boot-splash')).toHaveCount(0)

    // The shared shell oracle — the same locator `loginViaUI` waits on, in
    // whichever shell is mounted.
    await expect(appMenuTrigger(page)).toBeVisible()
  })
})

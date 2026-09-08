import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { clickTerminalText, printTerminalHyperlink, waitForTerminalReady, waitForTerminalText } from './helpers/terminal'
import { openAboutDialog, openTerminalViaUI, sendMessage, userBubbles } from './helpers/ui'

/**
 * Record what the app tries to open instead of opening it.
 *
 * `openExternalUrl` takes the browser branch here -- there is no desktop shell
 * behind a Playwright page -- and a real `window.open` would put a request on
 * the network for an address that does not exist. Patched live rather than
 * through `addInitScript`, because the workspace fixture has already navigated
 * and the app reads `window.open` at call time.
 */
async function recordOpenedUrls(page: Page): Promise<void> {
  await page.evaluate(() => {
    const opened: string[] = [];
    (window as any).__openedUrls = opened
    window.open = ((url?: string | URL) => {
      opened.push(String(url))
      return null
    }) as typeof window.open
  })
}

/**
 * Record every anchor click, and whether the app took it over.
 *
 * Registered on `window` in the BUBBLE phase, so the app's own capture-phase
 * listener has already decided by the time this runs: `defaultPrevented` is
 * exactly the answer to "did the prompt claim this link". It then stops the
 * navigation itself, because these addresses are outside the test.
 */
async function recordAnchorClicks(page: Page): Promise<void> {
  await page.evaluate(() => {
    const clicks: { href: string, defaultPrevented: boolean }[] = [];
    (window as any).__anchorClicks = clicks
    window.addEventListener('click', (event) => {
      const anchor = event.composedPath().find(node => node instanceof HTMLAnchorElement)
      if (!anchor)
        return
      clicks.push({ href: (anchor as HTMLAnchorElement).href, defaultPrevented: event.defaultPrevented })
      event.preventDefault()
    })
  })
}

function anchorClicks(page: Page): Promise<{ href: string, defaultPrevented: boolean }[]> {
  return page.evaluate(() => ((window as any).__anchorClicks ?? []) as { href: string, defaultPrevented: boolean }[])
}

function openedUrls(page: Page): Promise<string[]> {
  return page.evaluate(() => ((window as any).__openedUrls ?? []) as string[])
}

const dialog = (page: Page) => page.locator('dialog[data-testid="untrusted-link-dialog"]')

/**
 * A terminal hyperlink and an agent-written markdown link carry the same risk:
 * the text states one address and the link opens another. Both reach the same
 * prompt, and these are the only tests that prove the whole path -- a real
 * OSC 8 sequence from a real shell, xterm's own hit testing, the app's policy,
 * and the dialog.
 *
 * The unit suites own the policy itself (`src/lib/untrustedLinks.test.ts`, 40+
 * cases over schemes, wrapped labels and loopback). What only a browser can
 * show is that the link is clickable at all.
 */
test.describe('Untrusted link prompt', () => {
  test('warns before opening a terminal hyperlink whose text names a different address', async ({ page, authenticatedWorkspace }) => {
    await openTerminalViaUI(page)
    await expect(page.locator('.xterm')).toBeVisible()
    await waitForTerminalReady(page)
    await recordOpenedUrls(page)

    await printTerminalHyperlink(page, 'https://evil.example/steal', 'https://good.example')
    await waitForTerminalText(page, 'https://good.example')
    await clickTerminalText(page, 'https://good.example')

    // Both strings, side by side: comparing them is the whole point.
    await expect(dialog(page)).toBeVisible()
    await expect(dialog(page).getByTestId('untrusted-link-shown')).toHaveText('https://good.example')
    await expect(dialog(page).getByTestId('untrusted-link-target')).toHaveText('https://evil.example/steal')

    // Cancel opens nothing. A prompt that navigated anyway would be worse than
    // no prompt, because the reader would believe they had refused.
    await dialog(page).getByTestId('untrusted-link-cancel').click()
    await expect(dialog(page)).toBeHidden()
    expect(await openedUrls(page)).toEqual([])

    // Then the override. The text reads as an address of its own, so the
    // primary is a two-click ConfirmButton.
    await clickTerminalText(page, 'https://good.example')
    await expect(dialog(page)).toBeVisible()
    await dialog(page).getByTestId('untrusted-link-open').click()
    await page.getByRole('button', { name: 'Confirm?' }).click()

    await expect(dialog(page)).toBeHidden()
    await expect.poll(() => openedUrls(page)).toEqual(['https://evil.example/steal'])
  })

  // The mirror. A prompt on every link would pass the test above and teach the
  // reader to confirm without reading.
  test('opens a terminal hyperlink that spells its own address, with no prompt', async ({ page, authenticatedWorkspace }) => {
    await openTerminalViaUI(page)
    await expect(page.locator('.xterm')).toBeVisible()
    await waitForTerminalReady(page)
    await recordOpenedUrls(page)

    await printTerminalHyperlink(page, 'https://example.test/docs', 'https://example.test/docs')
    await waitForTerminalText(page, 'https://example.test/docs')
    await clickTerminalText(page, 'https://example.test/docs')

    await expect.poll(() => openedUrls(page)).toEqual(['https://example.test/docs'])
    await expect(dialog(page)).toBeHidden()
  })

  test('warns before opening a markdown link whose text names a different address', async ({ page, authenticatedWorkspace }) => {
    await recordOpenedUrls(page)
    // Rendered through the same pipeline an agent's reply takes, so the anchor
    // carries the mark `rehypeExternalLinks` puts on every link it hardens.
    await sendMessage(page, '[https://good.example](https://evil.example/steal)')

    const link = userBubbles(page).first().getByRole('link', { name: 'https://good.example' })
    await expect(link).toBeVisible()
    await link.click()

    await expect(dialog(page)).toBeVisible()
    await expect(dialog(page).getByTestId('untrusted-link-target')).toHaveText('https://evil.example/steal')
    expect(await openedUrls(page)).toEqual([])

    await dialog(page).getByTestId('untrusted-link-open').click()
    await page.getByRole('button', { name: 'Confirm?' }).click()
    await expect.poll(() => openedUrls(page)).toEqual(['https://evil.example/steal'])
  })

  /**
   * The app's OWN links must not prompt.
   *
   * The mark that routes a link to the prompt is opt-IN, and this dialog is
   * why. Its licence link reads "Functional Source License, Version 1.1, ALv2
   * Future License" over a leapmux.dev address -- honest copy the app wrote,
   * and a text/address mismatch at the same time. Under an opt-OUT default it
   * would raise the prompt every time, over first-party text that carries no
   * deception risk at all.
   */
  test('opens the About dialog links with no prompt, first-party text included', async ({ page, authenticatedWorkspace }) => {
    await recordOpenedUrls(page)
    await recordAnchorClicks(page)
    const about = await openAboutDialog(page)

    // The licence link first: prose label, mismatched address, and the whole
    // reason the default runs this way round.
    await about.getByRole('link', { name: /Functional Source License/ }).click()
    await expect(dialog(page)).toBeHidden()

    // Then the plain ones, and NOTICE.html, which is same-origin and relative.
    await about.getByRole('link', { name: 'leapmux.dev' }).click()
    await about.getByRole('link', { name: 'github.com/leapmux/leapmux' }).click()
    await about.getByRole('link', { name: 'NOTICE.html' }).click()

    await expect(dialog(page)).toBeHidden()
    // Untouched by the app: every one reached the browser's own handling, and
    // nothing was routed through `openExternalUrl`.
    const clicks = await anchorClicks(page)
    expect(clicks.map(c => c.defaultPrevented)).toEqual([false, false, false, false])
    expect(clicks.map(c => c.href)).toEqual([
      'https://leapmux.dev/docs/reference/legal/',
      'https://leapmux.dev/',
      'https://github.com/leapmux/leapmux',
      `${new URL(page.url()).origin}/NOTICE.html`,
    ])
    expect(await openedUrls(page)).toEqual([])
  })
})

import { expect, test } from './fixtures'

/**
 * The phone-band creation dialog: one scroll for the whole body.
 *
 * Below `breakpoints.sm` the dialog fills the viewport (Oat's 85vh max-height
 * is raised there), the stacked columns stop scrolling in their own slices,
 * and the section becomes the single scroll container with the title header
 * and the Cancel/Create footer pinned outside it. The scrollbar must also
 * draw at the dialog's edge, not inset by the body's padding — the section
 * bleeds out to the body's edges and repaints the inset itself.
 *
 * jsdom cannot see any of this: resolved CSS is e2e territory (see
 * ~/styles/popover.css.test.ts for the same boundary). The geometry below was
 * settled by measuring the running app; each assertion names the bug it
 * guards against, all of which shipped at least once.
 */
interface DialogGeometry {
  viewportH: number
  dialog: { top: number, bottom: number, left: number, height: number, right: number }
  bodyPaddingLeft: number
  section: { top: number, bottom: number, right: number, overflowY: string, scrollH: number, clientH: number }
  vstackLeft: number
  footer: { top: number, bottom: number }
  leftPanelBottom: number
  rightPanelTop: number
}

test.describe('creation dialog on a phone', () => {
  test.use({ viewport: { width: 390, height: 667 } })

  test('body scrolls as one container; header and footer stay pinned; the scrollbar sits at the dialog edge', async ({ page, authenticatedWorkspace }) => {
    // The sidebar's new-workspace button lives behind the workspaces drawer on
    // a phone; open the drawer first.
    await page.getByRole('button', { name: 'Toggle workspaces' }).click()
    await page.locator('[data-testid="sidebar-new-workspace"]').click()
    await expect(page.getByRole('heading', { name: 'New workspace', level: 2 })).toBeVisible()

    // The dialog's directory tree makes the body genuinely taller than the
    // viewport; the scroll assertions below need that real overflow. Scoped
    // to the dialog: the Files sidebar's own tree is also in the DOM behind
    // the drawers, carrying the same testid.
    await expect(page.locator('dialog[open]').getByTestId('tree-root-node')).toBeVisible()

    const m = await page.evaluate((): DialogGeometry | null => {
      const dlg = document.querySelector('dialog[open]')
      const body = dlg?.querySelector(':scope > div')
      const form = body?.querySelector(':scope > form')
      const section = form?.querySelector(':scope > section')
      const footer = form?.querySelector(':scope > footer')
      const vstack = section?.firstElementChild
      const columns = vstack?.lastElementChild
      const leftPanel = columns?.firstElementChild
      const rightPanel = columns?.children[1]
      if (!(dlg && body && form && section && footer && vstack && columns && leftPanel && rightPanel))
        return null
      const rect = (el: Element) => {
        const r = el.getBoundingClientRect()
        return { top: r.top, bottom: r.bottom, left: r.left, right: r.right, height: r.height }
      }
      return {
        viewportH: window.innerHeight,
        dialog: rect(dlg),
        bodyPaddingLeft: Number.parseFloat(getComputedStyle(body).paddingLeft),
        section: {
          ...rect(section),
          overflowY: getComputedStyle(section).overflowY,
          scrollH: section.scrollHeight,
          clientH: section.clientHeight,
        },
        vstackLeft: rect(vstack).left,
        footer: rect(footer),
        leftPanelBottom: rect(leftPanel).bottom,
        rightPanelTop: rect(rightPanel).top,
      }
    })
    expect(m, 'the creation dialog\'s form/section/panels structure').not.toBeNull()
    const g = m!

    // Full-viewport dialog: Oat caps every dialog at max-height 85vh, and the
    // phone band raises that cap — without it the "fullscreen" dialog rendered
    // at 85vh with strips of backdrop above and below. Chromium rounds 100dvh a
    // few pixels off the layout viewport and margin-auto centers the slack, so
    // assert the share of the viewport, not the pixel: 85vh sits at 85%.
    // Safe-area insets are 0 in this desktop-engine phone viewport, so the
    // panel still fills the layout viewport; on a real notched PWA the same
    // rules inset the panel (see Dialog.css.ts :modal safe-area comment).
    expect(g.dialog.top).toBeGreaterThanOrEqual(-1)
    expect(g.dialog.height).toBeGreaterThanOrEqual(g.viewportH * 0.95)

    // The top-layer panel must declare safe-area insets itself — body's
    // padding-top does not reach the top layer. Chromium reports 0 here, so
    // we assert the shipped CSS rather than geometry.
    const shipsSafeArea = await page.evaluate(() => {
      const needles = [
        'safe-area-inset-top',
        'safe-area-inset-right',
        'safe-area-inset-bottom',
        'safe-area-inset-left',
      ]
      for (const sheet of Array.from(document.styleSheets)) {
        let rules: CSSRuleList
        try {
          rules = sheet.cssRules
        }
        catch {
          continue
        }
        for (const rule of Array.from(rules)) {
          const text = rule.cssText
          if (needles.every(n => text.includes(n)))
            return true
        }
      }
      return false
    })
    expect(shipsSafeArea, 'dialog CSS must include all four safe-area insets').toBe(true)

    // The section is the ONE scroll container and actually scrolls. Before the
    // form-wrapped shape learned overflow-y, its content spilled visible under
    // the footer and no scrollbar appeared anywhere.
    expect(g.section.overflowY).toBe('auto')
    expect(g.section.scrollH).toBeGreaterThan(g.section.clientH)

    // Nothing bleeds under the pinned footer.
    expect(g.section.bottom).toBeLessThanOrEqual(g.footer.top + 1)
    expect(g.footer.bottom).toBeLessThanOrEqual(g.dialog.bottom + 1)

    // The stacked columns don't overlap. The first phone-band attempt kept the
    // desktop fill chain, whose basis-zero children made the left row mis-size
    // and the tree paint over the git options.
    expect(g.leftPanelBottom).toBeLessThanOrEqual(g.rightPanelTop + 1)

    // The scrollbar draws at the dialog's edge (the section bleeds out past
    // the body's padding), while the content keeps that inset — within a
    // pixel, like the edge above: the dialog's centered width composes to a
    // fractional device pixel, so the inset lands a fraction off the token.
    expect(Math.abs(g.section.right - g.dialog.right)).toBeLessThanOrEqual(1)
    expect(Math.abs((g.vstackLeft - g.dialog.left) - g.bodyPaddingLeft)).toBeLessThanOrEqual(1)
  })
})

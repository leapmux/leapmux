import type { Page } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import {
  BOOT_SPLASH_CORE_PHASES,
  BOOT_SPLASH_ICON_HEIGHT,
  BOOT_SPLASH_ICON_WIDTH,
  BOOT_SPLASH_LABEL,
  BOOT_SPLASH_PHASES,
  BOOT_SPLASH_SHELL_ATTRIBUTE,
  BOOT_SPLASH_SHELL_PHASES,
  BOOT_SPLASH_SHELL_ROW_CLASS,
  BOOT_SPLASH_STATIC_ID,
  BOOT_SPLASH_TEST_ID,
  bootSplashDocumentCss,
} from '../../src/lib/bootSplashTheme'
import { expect, test } from './fixtures'
import { appMenuTrigger, loginViaToken } from './helpers/ui'

// The app stylesheet the splash must survive, read from the exact artifact
// `src/app.tsx` imports. Shared by the geometry describes below.
const here = dirname(fileURLToPath(import.meta.url))
const oatCss = readFileSync(resolve(here, '../../node_modules/@knadh/oat/oat.min.css'), 'utf8')

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
    const body = await response!.text()
    expect(body).toContain('id="boot-splash"')
    // The shipped document must also carry the checklist rows and the phase
    // script — the splash the user first sees is this markup, before any
    // module has run.
    for (const phase of BOOT_SPLASH_PHASES)
      expect(body, `the document must ship the ${phase.key} row`).toContain(`boot-splash-row-${phase.key}`)
    expect(body).toContain('boot-splash-progress-check')
    expect(body).toContain('data-boot-phase')

    // The handoff: the entry removes the static splash after mount. A
    // regression leaves the node in `#app` forever, so this assertion fails
    // by timeout instead of flaking.
    await expect(page.locator('#boot-splash')).toHaveCount(0)

    // The shared shell oracle — the same locator `loginViaUI` waits on, in
    // whichever shell is mounted.
    await expect(appMenuTrigger(page)).toBeVisible()
  })
})

/**
 * The splash must not move while it is on screen.
 *
 * `bootSplashDocumentCss` paints the splash before the app stylesheet exists,
 * so every property it leaves unstated is answered twice: by the UA first, and
 * by oat second. An INHERITED property is the trap, because the splash
 * stylesheet outranks nothing while it stays silent — it is unlayered, but an
 * unlayered declaration only beats a layered one for a property it declares.
 * `line-height` was unstated, so the paragraph inherited `normal` from `body`
 * and then oat's `1.5`: the line box grew 4.8px, half of the new leading landed
 * above the glyphs, and the centred column rose 2.4px part way through boot.
 *
 * The unit suite pins the literal against the oat stylesheet that ships
 * (`src/lib/bootSplashTheme.test.ts`). Only a real layout engine can check the
 * property those two agree about, which is that the geometry does not change.
 *
 * This spec needs no server: it renders the shipped stylesheet against the
 * shipped markup and adds the shipped oat sheet, which is the whole transition.
 */
test.describe('boot splash layout across the app stylesheet', () => {
  // Geometry only: a rect, not the artwork. `BootSplashIcon`'s paths are held
  // against `public/icons/leapmux-icon.svg` in the unit suite.
  const icon = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" width="${BOOT_SPLASH_ICON_WIDTH}" height="${BOOT_SPLASH_ICON_HEIGHT}" aria-hidden="true"><rect width="64" height="64" rx="14" fill="#0D9488" /></svg>`

  // Both trees, because the two carry different selectors and different
  // nesting: the static node matches on `#boot-splash` and wraps its children
  // in `.boot-splash-loading`, and Solid's matches on `data-testid` alone.
  // A rule that reaches only one of them is the drift this file exists to stop.
  const TREES: Record<string, string> = {
    static: `<div id="${BOOT_SPLASH_STATIC_ID}" data-testid="${BOOT_SPLASH_TEST_ID}" role="status">
      <div class="boot-splash-loading">${icon}<p>${BOOT_SPLASH_LABEL}</p></div>
      <div class="boot-splash-error" hidden>
        <p data-boot-fail-title>Could not start LeapMux</p>
        <pre data-boot-fail-detail>Failed to load</pre>
        <button type="button" data-boot-reload>Reload</button>
      </div>
    </div>`,
    solid: `<div data-testid="${BOOT_SPLASH_TEST_ID}" role="status">${icon}<p>${BOOT_SPLASH_LABEL}</p></div>`,
  }

  interface Geometry {
    /** Icon bottom to text-box top: the flex gap, and what visibly grew. */
    gap: number
    /** Line box of the label. The inherited `line-height` moves this one. */
    textHeight: number
    /** Whole column, so a shift that cancels out in `gap` still shows. */
    columnHeight: number
  }

  async function measure(page: Page, markup: string, withAppStylesheet: boolean): Promise<Geometry> {
    await page.setContent(`<style>${bootSplashDocumentCss()}</style><div id="app">${markup}</div>`)
    if (withAppStylesheet)
      await page.addStyleTag({ content: oatCss })

    return page.evaluate(() => {
      const round = (n: number) => Math.round(n * 100) / 100
      const splash = document.querySelector('[data-testid="boot-splash"]')!
      const iconRect = splash.querySelector('svg')!.getBoundingClientRect()
      const textRect = splash.querySelector('p')!.getBoundingClientRect()
      return {
        gap: round(textRect.top - iconRect.bottom),
        textHeight: round(textRect.height),
        columnHeight: round(textRect.bottom - iconRect.top),
      }
    })
  }

  for (const [name, markup] of Object.entries(TREES)) {
    test(`the ${name} splash keeps its geometry when the app stylesheet lands`, async ({ page }) => {
      const beforeAppCss = await measure(page, markup, false)
      const afterAppCss = await measure(page, markup, true)

      expect(afterAppCss).toEqual(beforeAppCss)

      // Not a comparison of two identical wrong answers: the gap is the
      // `--space-4` seed at the 16px root font size, whatever `system-ui`
      // resolves to on the machine running this.
      expect(beforeAppCss.gap).toBe(16)
      expect(beforeAppCss.textHeight).toBeGreaterThan(0)
    })
  }

  // The failure panel inherits the same answer. It is why `line-height` sits on
  // the splash container and not on the `p` rule: `pre` sets its own `.85rem`
  // and the Reload button takes `font:inherit`, so a rule that named only the
  // paragraph would leave both to move when oat arrived.
  test('the failure panel keeps its geometry when the app stylesheet lands', async ({ page }) => {
    const failed = TREES.static!
      .replace(`id="${BOOT_SPLASH_STATIC_ID}"`, `id="${BOOT_SPLASH_STATIC_ID}" data-boot-failed="true"`)
      .replace(' hidden', '')

    const read = () => page.evaluate(() => {
      const round = (n: number) => Math.round(n * 100) / 100
      const box = (el: Element) => {
        const rect = el.getBoundingClientRect()
        return { w: round(rect.width), h: round(rect.height) }
      }
      const panel = document.querySelector('.boot-splash-error')!
      return {
        panel: box(panel),
        title: box(panel.querySelector('[data-boot-fail-title]')!),
        // Oat's `pre` rule is the one that landed here: padding, a background,
        // a radius and the mono family, none of which the splash declared.
        detail: box(panel.querySelector('pre')!),
        button: box(panel.querySelector('button')!),
      }
    })

    await page.setContent(`<style>${bootSplashDocumentCss()}</style><div id="app">${failed}</div>`)
    await expect(page.locator('.boot-splash-error')).toBeVisible()
    const beforeAppCss = await read()

    await page.addStyleTag({ content: oatCss })
    expect(await read()).toEqual(beforeAppCss)
    expect(beforeAppCss.detail.h).toBeGreaterThan(0)
  })
})

/**
 * The progress checklist advances by one attribute on <html>; the stylesheet
 * does the rest. Two things only a layout engine can check:
 *
 * - the logo and the label hold STILL while the checklist advances (rows
 *   change state by opacity alone, and `ready` keeps the list in place rather
 *   than collapsing), measured with oat's sheet present so the list element
 *   rules the splash must neutralize are in play;
 * - the attribute actually maps to done checks, an active row, and a visible
 *   finished checklist on `ready`.
 */
test.describe('boot splash progress checklist', () => {
  const check = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" class="boot-splash-progress-check"><polyline points="20 6 9 17 4 12"/></svg>`
  const dots = '<span class="boot-splash-progress-dots">'
    + '<span class="boot-splash-progress-dot"></span>'
    + '<span class="boot-splash-progress-dot"></span>'
    + '<span class="boot-splash-progress-dot"></span>'
    + '</span>'
  const rowMarkup = (phase: { key: string, label: string }, shell: boolean) =>
    `<li class="boot-splash-progress-row boot-splash-row-${phase.key}`
    + `${shell ? ` ${BOOT_SPLASH_SHELL_ROW_CLASS}` : ''}">`
    + `<span class="boot-splash-progress-label">${phase.label}</span>`
    + `<span class="boot-splash-progress-status">${check}${dots}</span>`
    + '</li>'
  const rows = [
    ...BOOT_SPLASH_CORE_PHASES.map(phase => rowMarkup(phase, false)),
    ...BOOT_SPLASH_SHELL_PHASES.map(phase => rowMarkup(phase, true)),
  ].join('')
  const markup = `<div id="${BOOT_SPLASH_STATIC_ID}" data-testid="${BOOT_SPLASH_TEST_ID}" role="status">`
    + '<div class="boot-splash-loading">'
    + `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" width="${BOOT_SPLASH_ICON_WIDTH}" height="${BOOT_SPLASH_ICON_HEIGHT}" aria-hidden="true"><rect width="64" height="64" rx="14" fill="#0D9488" /></svg>`
    + `<p class="boot-splash-label">${BOOT_SPLASH_LABEL}</p>`
    + `<ul class="boot-splash-progress">${rows}</ul>`
    + '</div></div>'

  test('the logo and the label hold still across every phase, with oat present', async ({ page }) => {
    await page.setContent(`<style>${bootSplashDocumentCss()}</style><div id="app">${markup}</div>`)
    await page.addStyleTag({ content: oatCss })

    const read = () => page.evaluate(() => {
      const round = (n: number) => Math.round(n * 100) / 100
      const splash = document.querySelector('[data-testid="boot-splash"]')!
      const iconRect = splash.querySelector('svg')!.getBoundingClientRect()
      const labelRect = splash.querySelector('p')!.getBoundingClientRect()
      return {
        iconTop: round(iconRect.top),
        iconLeft: round(iconRect.left),
        gap: round(labelRect.top - iconRect.bottom),
        labelCenterX: round(labelRect.left + labelRect.width / 2),
        columnHeight: round(splash.getBoundingClientRect().height),
      }
    })

    // No attribute yet: `initializing` is the active row. Shell rows stay
    // display:none without data-boot-shell, so phase advances do not grow the
    // column — the same contract login/signup see.
    const baseline = await read()
    expect(baseline.gap).toBe(16)
    for (const key of [...BOOT_SPLASH_PHASES.map(p => p.key), 'ready']) {
      await page.evaluate((phase) => {
        document.documentElement.setAttribute('data-boot-phase', phase)
      }, key)
      expect(await read(), `phase ${key}`).toEqual(baseline)
    }
  })

  test('maps the attribute to done checks, one active row, and a visible ready list', async ({ page }) => {
    // Opacity transitions would make every read a race with the animation;
    // reduced motion switches them off, which is exactly the steady state.
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await page.setContent(`<style>${bootSplashDocumentCss()}</style><div id="app">${markup}</div>`)

    // `mounting` sits mid-list, so all three row states exist at once.
    await page.evaluate(() => {
      document.documentElement.setAttribute('data-boot-phase', 'mounting')
    })

    const opacity = (selector: string) =>
      page.locator(selector).evaluate(el => getComputedStyle(el).opacity)

    // Rows above the phase: check on, dots off, label half-muted.
    await expect.poll(() => opacity('.boot-splash-row-initializing .boot-splash-progress-check')).toBe('1')
    await expect.poll(() => opacity('.boot-splash-row-loading-bundles .boot-splash-progress-check')).toBe('1')
    expect(await opacity('.boot-splash-row-initializing .boot-splash-progress-dots')).toBe('0')
    expect(await opacity('.boot-splash-row-initializing .boot-splash-progress-label')).toBe('0.6')
    // The phase's own row: dots on, check off, label at full strength.
    expect(await opacity('.boot-splash-row-mounting .boot-splash-progress-dots')).toBe('1')
    expect(await opacity('.boot-splash-row-mounting .boot-splash-progress-check')).toBe('0')
    expect(await opacity('.boot-splash-row-mounting .boot-splash-progress-label')).toBe('1')
    // Rows below: nothing yet — dimmest label, no check, no dots.
    expect(await opacity('.boot-splash-row-system-info .boot-splash-progress-label')).toBe('0.45')
    expect(await opacity('.boot-splash-row-session .boot-splash-progress-check')).toBe('0')
    expect(await opacity('.boot-splash-row-session .boot-splash-progress-dots')).toBe('0')

    await page.evaluate(() => {
      document.documentElement.setAttribute('data-boot-phase', 'ready')
    })
    // ready: every row checked, list stays visible (AppShell keeps this splash
    // up until tabs land).
    await expect.poll(() => opacity('.boot-splash-row-session .boot-splash-progress-check')).toBe('1')
    await expect.poll(() => opacity('.boot-splash-row-initializing .boot-splash-progress-check')).toBe('1')
    expect(await opacity('.boot-splash-progress')).toBe('1')
  })

  test('keeps shell rows hidden off the shell path, and shows them with data-boot-shell', async ({ page }) => {
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await page.setContent(`<style>${bootSplashDocumentCss()}</style><div id="app">${markup}</div>`)

    const displayOf = (selector: string) =>
      page.locator(selector).evaluate(el => getComputedStyle(el).display)

    expect(await displayOf('.boot-splash-row-workspaces')).toBe('none')
    expect(await displayOf('.boot-splash-row-tabs')).toBe('none')
    expect(await displayOf('.boot-splash-row-session')).toBe('grid')

    await page.evaluate((attr) => {
      document.documentElement.setAttribute(attr, '')
      document.documentElement.setAttribute('data-boot-phase', 'workspaces')
    }, BOOT_SPLASH_SHELL_ATTRIBUTE)

    expect(await displayOf('.boot-splash-row-workspaces')).toBe('grid')
    expect(await displayOf('.boot-splash-row-tabs')).toBe('grid')
    await expect.poll(() =>
      page.locator('.boot-splash-row-session .boot-splash-progress-check')
        .evaluate(el => getComputedStyle(el).opacity),
    ).toBe('1')
    expect(await page.locator('.boot-splash-row-workspaces .boot-splash-progress-dots')
      .evaluate(el => getComputedStyle(el).opacity)).toBe('1')
  })

  test('fades the splash in on first paint, and skips the entrance under reduced motion', async ({ page }) => {
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await page.setContent(`<style>${bootSplashDocumentCss()}</style><div id="app">${markup}</div>`)
    const opacity = await page.locator(`#${BOOT_SPLASH_STATIC_ID}`).evaluate(el => getComputedStyle(el).opacity)
    expect(opacity).toBe('1')
  })
})

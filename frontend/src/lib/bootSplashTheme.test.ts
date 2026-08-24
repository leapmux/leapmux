import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  BOOT_SPLASH_FAIL_TIMEOUT_DETAIL,
  BOOT_SPLASH_FAIL_TIMEOUT_MS,
  BOOT_SPLASH_FAIL_TITLE,
  BOOT_SPLASH_GAP,
  BOOT_SPLASH_LABEL,
  BOOT_SPLASH_LINE_HEIGHT,
  BOOT_SPLASH_RELOAD_LABEL,
  BOOT_SPLASH_SPACE_4,
  BOOT_SPLASH_STATIC_ID,
  BOOT_SPLASH_TEST_ID,
  bootFailureWatchdogScript,
  bootSplashDark,
  bootSplashDocumentCss,
  bootSplashLight,
  bootThemeScript,
  removeStaticBootSplash,
} from './bootSplashTheme'

describe('boot splash palette', () => {
  it('uses Default theme backgrounds that disagree by polarity', () => {
    expect(bootSplashLight.background).not.toBe(bootSplashDark.background)
    expect(bootSplashLight.foreground).not.toBe(bootSplashDark.foreground)
  })
})

describe('bootSplashDocumentCss', () => {
  it('covers html/body/#app fill, safe-area body lock, and the shared gap token', () => {
    const css = bootSplashDocumentCss()
    // The gap token is seeded on the SPLASH, never on `:root`. This seed is
    // unlayered and oat's `:root` sits in `@layer theme`, so a `:root` seed
    // pins `--space-4` for the whole app, permanently, from a stylesheet that
    // exists to paint one splash.
    expect(css).toContain(`--space-4:${BOOT_SPLASH_SPACE_4}`)
    expect(css).not.toContain(`:root{--space-4`)
    expect(css).toContain(`gap:${BOOT_SPLASH_GAP}`)
    // Inherited properties the splash must state itself, or it reflows when
    // oat's `body` rule arrives. See BOOT_SPLASH_LINE_HEIGHT.
    expect(css).toContain(`line-height:${BOOT_SPLASH_LINE_HEIGHT}`)
    expect(css).toContain(bootSplashLight.background)
    expect(css).toContain(bootSplashDark.background)
    expect(css).toContain('html,body,#app{margin:0;height:100%;width:100%;overflow:hidden}')
    expect(css).toContain('padding-top:env(safe-area-inset-top,0px)')
    expect(css).toContain('position:fixed')
    expect(css).toContain(`#${BOOT_SPLASH_STATIC_ID}{min-height:100%}`)
    expect(css).toContain(
      `[data-testid="${BOOT_SPLASH_TEST_ID}"]:not(#${BOOT_SPLASH_STATIC_ID}){min-height:100dvh}`,
    )
    // Static splash must not pick up the Solid-only 100dvh floor.
    expect(css).not.toContain(`#${BOOT_SPLASH_STATIC_ID}{min-height:100dvh}`)
    expect(css).toContain(`html[data-theme="dark"]`)
    expect(css).toContain(`[data-testid="${BOOT_SPLASH_TEST_ID}"]`)
    expect(css).toContain(`#${BOOT_SPLASH_STATIC_ID}[data-boot-failed]`)
    expect(css).toContain('width:max-content')
    expect(css).toContain('max-width:min(100%,20rem)')
    expect(css).toContain('.boot-splash-error pre{margin:0 auto')
    expect(css).not.toContain('gap:1rem')
    // The pre-hydration polarity, as a RULE rather than an inline style, and
    // scoped to the window before the app answers. `html[data-theme]` is
    // (0,1,1), above `global.css.ts`'s unlayered `html { color-scheme: light }`
    // at (0,0,1) -- dark form controls and scrollbars from first paint on a dark
    // OS. `:not([data-ui-theme])` is what makes it YIELD: `themeStore` writes
    // that attribute with the palette, and without the clause this rule would
    // outrank `lightVariantSelector`'s `[data-ui-light="X"]` self match at
    // (0,1,0) for the rest of the session.
    expect(css).toContain('html[data-theme="light"]:not([data-ui-theme]){color-scheme:light}')
    expect(css).toContain('html[data-theme="dark"]:not([data-ui-theme]){color-scheme:dark}')
    // The clause is not optional: an unscoped rule is the bug above.
    expect(css).not.toContain('html[data-theme="light"]{color-scheme')
    expect(css).not.toContain('html[data-theme="dark"]{color-scheme')
  })
})

describe('bootThemeScript', () => {
  afterEach(() => {
    document.documentElement.removeAttribute('data-theme')
    document.documentElement.style.colorScheme = ''
    document.documentElement.style.backgroundColor = ''
    localStorage.removeItem('leapmux:browser-prefs')
    document.head.querySelectorAll('meta[name="theme-color"]').forEach(m => m.remove())
    vi.unstubAllGlobals()
  })

  function installThemeColorMetas(): void {
    for (const [media, content] of [
      ['(prefers-color-scheme: light)', bootSplashLight.background],
      ['(prefers-color-scheme: dark)', bootSplashDark.background],
      [null, bootSplashLight.background],
    ] as const) {
      const meta = document.createElement('meta')
      meta.setAttribute('name', 'theme-color')
      if (media)
        meta.setAttribute('media', media)
      meta.setAttribute('content', content)
      document.head.appendChild(meta)
    }
  }

  function runBootScript(systemDark: boolean): void {
    vi.stubGlobal('matchMedia', (query: string) => ({
      matches: systemDark && query.includes('prefers-color-scheme: dark'),
      media: query,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    }))
    // eslint-disable-next-line no-new-func -- evaluate the same string the document ships
    new Function(bootThemeScript())()
  }

  // IT READS NO STORAGE, and the spy is the assertion. Asserting only on the
  // painted polarity would also pass for a script that read a key and found
  // nothing -- and this script must not read at all: it is inlined into static
  // HTML that runs before any module and before any identity, so there is no
  // account whose theme it could legitimately read.
  it('paints the OS polarity and reads no storage', () => {
    installThemeColorMetas()
    const getItem = vi.spyOn(Storage.prototype, 'getItem')

    runBootScript(true)

    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
    expect(getItem).not.toHaveBeenCalled()
    getItem.mockRestore()
  })

  it('paints light under a light OS', () => {
    installThemeColorMetas()
    runBootScript(false)
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
  })

  // The loud failure if anyone re-adds the read: a document left by an earlier
  // build, or by another account, must not reach the first paint.
  it('ignores a stored theme document left behind by an earlier build', () => {
    installThemeColorMetas()
    localStorage.setItem(
      'leapmux:browser-prefs',
      JSON.stringify({ v: { theme: { mode: 'dark' } }, e: Date.now() + 60_000 }),
    )

    runBootScript(false)

    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
  })

  // The polarity is stated as a RULE, in `bootSplashDocumentCss`, never as an
  // inline style here. An inline declaration outranks every author rule, so an
  // inline `color-scheme` could not be overridden by the palette rule that
  // carries the app's own polarity: a dark app under a light OS kept
  // `color-scheme: light`, and `light-dark()` took the light branch inside a
  // dark palette.
  it('writes no inline style, so the palette rule can still win', () => {
    installThemeColorMetas()
    runBootScript(true)

    expect(document.documentElement.style.colorScheme).toBe('')
    expect(document.documentElement.style.backgroundColor).toBe('')
  })

  it('keeps the media theme-color metas and rewrites only the fallback', () => {
    installThemeColorMetas()

    runBootScript(true)

    const withMedia = document.querySelectorAll('meta[name="theme-color"][media]')
    expect(withMedia.length).toBe(2)
    expect(withMedia[0]!.getAttribute('content')).toBe(bootSplashLight.background)
    expect(withMedia[1]!.getAttribute('content')).toBe(bootSplashDark.background)
    const fallback = document.querySelector('meta[name="theme-color"]:not([media])')
    expect(fallback?.getAttribute('content')).toBe(bootSplashDark.background)
  })
})

// RESTORED. These three suites cover code that this change did not touch:
// `bootFailureWatchdogScript` and `removeStaticBootSplash` still ship
// unchanged, and `entry-server.tsx` / `entry-client.tsx` still call them. They
// went out with the storage-reading tests around them, which left the whole
// cold-start failure path -- the tombstone, the Reload button, the 45s timeout,
// the observer teardown -- with no coverage at all. A watchdog that stopped
// answering would leave a user on a slow connection reading "Loading LeapMux…"
// for ever, with a green suite.
describe('bootFailureWatchdogScript', () => {
  afterEach(() => {
    document.body.replaceChildren()
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  function mountStaticSplash(): HTMLElement {
    document.body.innerHTML = `
      <div id="app">
        <div id="${BOOT_SPLASH_STATIC_ID}" data-testid="${BOOT_SPLASH_TEST_ID}" role="status" aria-live="polite">
          <div class="boot-splash-loading"><p>${BOOT_SPLASH_LABEL}</p></div>
          <div class="boot-splash-error" hidden>
            <p data-boot-fail-title></p>
            <pre data-boot-fail-detail></pre>
            <button type="button" data-boot-reload></button>
          </div>
        </div>
      </div>
    `
    return document.getElementById(BOOT_SPLASH_STATIC_ID)!
  }

  function runWatchdog(): void {
    // eslint-disable-next-line no-new-func -- evaluate the same string the document ships
    new Function(bootFailureWatchdogScript())()
  }

  it('rewrites the static splash when a script resource fails to load', () => {
    const splash = mountStaticSplash()
    runWatchdog()

    const script = document.createElement('script')
    script.src = 'https://example.test/entry.js'
    document.body.appendChild(script)
    script.dispatchEvent(new Event('error', { bubbles: true }))

    expect(splash.getAttribute('data-boot-failed')).toBe('true')
    expect(splash.getAttribute('role')).toBe('alert')
    expect(splash.querySelector('[data-boot-fail-title]')?.textContent).toBe(BOOT_SPLASH_FAIL_TITLE)
    expect(splash.querySelector('[data-boot-fail-detail]')?.textContent).toBe(
      'Failed to load\nhttps://example.test/entry.js',
    )
    expect(splash.querySelector('[data-boot-reload]')?.textContent).toBe(BOOT_SPLASH_RELOAD_LABEL)
    expect(splash.querySelector('.boot-splash-loading')?.hasAttribute('hidden')).toBe(true)
    expect(splash.querySelector('.boot-splash-error')?.hasAttribute('hidden')).toBe(false)
  })

  it('ignores favicon and manifest link load errors', () => {
    const splash = mountStaticSplash()
    runWatchdog()

    for (const [rel, href] of [
      ['icon', 'https://example.test/icons/leapmux-icon.ico'],
      ['manifest', 'https://example.test/manifest.webmanifest'],
      ['stylesheet', 'https://example.test/assets/app.css'],
    ] as const) {
      const link = document.createElement('link')
      link.rel = rel
      link.href = href
      document.head.appendChild(link)
      link.dispatchEvent(new Event('error', { bubbles: true }))
    }

    expect(splash.getAttribute('data-boot-failed')).toBeNull()
    expect(splash.querySelector('.boot-splash-loading')?.hasAttribute('hidden')).toBe(false)
  })

  it('wires Reload to location.reload', () => {
    const splash = mountStaticSplash()
    runWatchdog()
    const reload = vi.fn()
    vi.stubGlobal('location', { ...window.location, reload })

    const script = document.createElement('script')
    script.src = 'https://example.test/entry.js'
    document.body.appendChild(script)
    script.dispatchEvent(new Event('error', { bubbles: true }))

    const button = splash.querySelector<HTMLButtonElement>('[data-boot-reload]')
    expect(button).not.toBeNull()
    button!.click()
    expect(reload).toHaveBeenCalledTimes(1)
  })

  it('fires on the timeout only while the static splash node remains', () => {
    vi.useFakeTimers()
    const splash = mountStaticSplash()
    runWatchdog()

    vi.advanceTimersByTime(BOOT_SPLASH_FAIL_TIMEOUT_MS - 1)
    expect(splash.getAttribute('data-boot-failed')).toBeNull()

    vi.advanceTimersByTime(1)
    expect(splash.getAttribute('data-boot-failed')).toBe('true')
    expect(splash.querySelector('[data-boot-fail-detail]')?.textContent).toBe(BOOT_SPLASH_FAIL_TIMEOUT_DETAIL)
  })

  it('does not treat a Solid BootSplash (testid only) as a failed static boot', () => {
    vi.useFakeTimers()
    // Static id is gone (in production the entry removes it); the Suspense
    // splash keeps only the testid.
    document.body.innerHTML = `<div id="app"><div data-testid="${BOOT_SPLASH_TEST_ID}" role="status"><p>${BOOT_SPLASH_LABEL}</p></div></div>`
    runWatchdog()

    vi.advanceTimersByTime(BOOT_SPLASH_FAIL_TIMEOUT_MS + 1)
    expect(document.querySelector('[data-boot-failed]')).toBeNull()
    expect(document.querySelector('[data-testid="boot-splash"]')).toBeInTheDocument()
  })

  it('finishes when the static splash is removed before the timeout', async () => {
    vi.useFakeTimers()
    mountStaticSplash()
    runWatchdog()

    document.getElementById('app')!.replaceChildren(
      document.createElement('div'),
    )
    // MutationObserver delivers on a microtask in jsdom.
    await Promise.resolve()

    // Before the timeout: capture listener must already be gone.
    const orphan = document.createElement('div')
    orphan.id = BOOT_SPLASH_STATIC_ID
    orphan.innerHTML = `
      <div class="boot-splash-loading"></div>
      <div class="boot-splash-error" hidden>
        <p data-boot-fail-title></p>
        <pre data-boot-fail-detail></pre>
        <button type="button" data-boot-reload></button>
      </div>
    `
    document.body.appendChild(orphan)
    const script = document.createElement('script')
    script.src = 'https://example.test/late.js'
    document.body.appendChild(script)
    script.dispatchEvent(new Event('error', { bubbles: true }))
    expect(orphan.getAttribute('data-boot-failed')).toBeNull()

    vi.advanceTimersByTime(BOOT_SPLASH_FAIL_TIMEOUT_MS + 1)
    expect(document.querySelector('[data-boot-failed]')).toBeNull()
  })
})

describe('removeStaticBootSplash', () => {
  afterEach(() => {
    document.body.replaceChildren()
  })

  it('removes the static splash and leaves the mounted tree alone', () => {
    document.body.innerHTML = `<div id="app"><div id="${BOOT_SPLASH_STATIC_ID}" data-testid="${BOOT_SPLASH_TEST_ID}"><p>${BOOT_SPLASH_LABEL}</p></div><div data-shell></div></div>`
    removeStaticBootSplash()
    expect(document.getElementById(BOOT_SPLASH_STATIC_ID)).toBeNull()
    expect(document.querySelector('[data-testid="boot-splash"]')).toBeNull()
    expect(document.querySelector('[data-shell]')).toBeInTheDocument()
  })

  it('is a no-op when the document shipped no static splash', () => {
    // A document that never carried the static node must not throw.
    document.body.innerHTML = `<div id="app"><div data-testid="${BOOT_SPLASH_TEST_ID}"><p>${BOOT_SPLASH_LABEL}</p></div></div>`
    expect(() => removeStaticBootSplash()).not.toThrow()
    expect(document.getElementById('app')!.childElementCount).toBe(1)
  })
})

// The static splash and the Solid one must stay one design. They are two
// separate sources -- inline HTML in `entry-server.tsx` and a component -- and
// the handoff between them is invisible only while they agree.
//
// The entry-server side is asserted in `entry-server.fonts.test.ts`; this is
// the BootSplash side and the shared constants, which nothing else covers.
describe('boot splash lockstep sources', () => {
  const here = dirname(fileURLToPath(import.meta.url))

  /** Value oat declares for a custom property, or a failure naming the token. */
  function oatToken(css: string, name: string): string {
    const match = new RegExp(`${name}\\s*:\\s*([^;}]+)`).exec(css)
    expect(match, `oat no longer declares ${name}`).not.toBeNull()
    return match![1]!.trim()
  }

  // The two values the splash states for itself before oat's stylesheet exists.
  // Nothing else catches this drift. Both reach the splash by INHERITANCE from
  // `body`, so a mismatch breaks no rule and fails no assertion elsewhere -- it
  // reflows the splash part way through boot, which only a reader watching a
  // cold start ever sees.
  //
  // `oat.min.css` is the exact artifact `src/app.tsx` imports, so this reads
  // what ships rather than a source file the package may stop publishing.
  it('keeps the splash literals in lockstep with the oat stylesheet it ships', () => {
    const oat = readFileSync(resolve(here, '../../node_modules/@knadh/oat/oat.min.css'), 'utf8')

    expect(oatToken(oat, '--space-4')).toBe(BOOT_SPLASH_SPACE_4)
    expect(oatToken(oat, '--leading-normal')).toBe(BOOT_SPLASH_LINE_HEIGHT)
    // The rule that makes line height an INHERITED answer rather than the
    // splash's own. If oat stops stating it on `body`, the pinned literal
    // compensates for nothing and this assertion reports that.
    expect(oat).toContain('line-height:var(--leading-normal)')
  })

  it('keeps BootSplash on the shared module, with no twin stylesheet', () => {
    const splash = readFileSync(resolve(here, '../components/common/BootSplash.tsx'), 'utf8')

    expect(splash).toContain('BOOT_SPLASH_LABEL')
    expect(splash).toContain('BOOT_SPLASH_TEST_ID')
    expect(splash).toContain('from \'~/lib/bootSplashTheme\'')
    expect(splash).toContain('data-boot-splash-icon')
    expect(splash).toContain('viewBox="0 0 64 64"')
    expect(splash).not.toContain('BootSplash.css')
    // The static node owns the id; a second element carrying it would make
    // `removeStaticBootSplash` delete the mounted splash instead.
    expect(splash).not.toMatch(/\bid=["']boot-splash["']/)
    // Inline, never an external URL: the icon has to paint on a connection
    // that has not delivered a second resource yet.
    expect(splash).not.toMatch(/src=["']\/icons\/leapmux-icon\.svg["']/)
    expect(BOOT_SPLASH_TEST_ID).toBe('boot-splash')
    expect(BOOT_SPLASH_STATIC_ID).toBe(BOOT_SPLASH_TEST_ID)
    expect(BOOT_SPLASH_LABEL).toBe('Loading LeapMux…')
  })

  it('keeps BootSplashIcon geometry aligned with the public icon file', () => {
    const splash = readFileSync(resolve(here, '../components/common/BootSplash.tsx'), 'utf8')
    const file = readFileSync(resolve(here, '../../public/icons/leapmux-icon.svg'), 'utf8')
    const fileCore = file.replace(/\s+/g, ' ').trim().replace(/^<\?xml[^>]*>\s*/i, '')
    expect(splash).toContain('viewBox="0 0 64 64"')
    expect(splash).toContain('fill="#0D9488"')
    expect(splash).toContain('fill="#F59E0B"')
    expect(fileCore).toContain('viewBox="0 0 64 64"')
    for (const path of [
      'M16 20 L30 32 L16 44',
      'M44 17 L48 28 L58 32 L48 36 L44 47 L40 36 L30 32 L40 28 Z',
    ]) {
      expect(splash).toContain(path)
      expect(fileCore).toContain(path)
    }
  })

  it('does not ship a BootSplash.css.ts twin', () => {
    const path = resolve(here, '../components/common/BootSplash.css.ts')
    expect(() => readFileSync(path, 'utf8')).toThrow()
  })
})

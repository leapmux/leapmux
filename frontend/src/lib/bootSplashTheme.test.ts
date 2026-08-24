import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { KEY_BROWSER_PREFS } from '~/lib/browserStorage'
import {
  BOOT_SPLASH_FAIL_TIMEOUT_DETAIL,
  BOOT_SPLASH_FAIL_TIMEOUT_MS,
  BOOT_SPLASH_FAIL_TITLE,
  BOOT_SPLASH_GAP,
  BOOT_SPLASH_LABEL,
  BOOT_SPLASH_RELOAD_LABEL,
  BOOT_SPLASH_SPACE_4,
  BOOT_SPLASH_STATIC_ID,
  BOOT_SPLASH_TEST_ID,
  bootFailureWatchdogScript,
  bootSplashDark,
  bootSplashDocumentCss,
  bootSplashLight,
  bootThemeScript,
  parseBootPrefsThemeMode,
  removeStaticBootSplash,
  resolveBootPolarity,
} from './bootSplashTheme'

describe('resolveBootPolarity', () => {
  it('honours an explicit light or dark pin', () => {
    expect(resolveBootPolarity('light', true)).toBe('light')
    expect(resolveBootPolarity('dark', false)).toBe('dark')
  })

  it('follows the OS when mode is system or absent', () => {
    expect(resolveBootPolarity('system', true)).toBe('dark')
    expect(resolveBootPolarity('system', false)).toBe('light')
    expect(resolveBootPolarity(undefined, true)).toBe('dark')
    expect(resolveBootPolarity('nope', false)).toBe('light')
  })
})

describe('boot splash palette', () => {
  it('uses Default theme backgrounds that disagree by polarity', () => {
    expect(bootSplashLight.background).not.toBe(bootSplashDark.background)
    expect(bootSplashLight.foreground).not.toBe(bootSplashDark.foreground)
  })
})

describe('parseBootPrefsThemeMode', () => {
  const now = 1_700_000_000_000

  it('defaults to system when raw is null or empty', () => {
    expect(parseBootPrefsThemeMode(null, now)).toBe('system')
    expect(parseBootPrefsThemeMode('', now)).toBe('system')
  })

  it('reads theme.mode from a valid TTL envelope', () => {
    const raw = JSON.stringify({ v: { theme: { name: 'default', mode: 'dark' } }, e: now + 60_000 })
    expect(parseBootPrefsThemeMode(raw, now)).toBe('dark')
  })

  it('ignores an expired envelope', () => {
    const raw = JSON.stringify({ v: { theme: { mode: 'dark' } }, e: now - 1 })
    expect(parseBootPrefsThemeMode(raw, now)).toBe('system')
  })

  it('ignores malformed JSON and a missing mode string', () => {
    expect(parseBootPrefsThemeMode('{', now)).toBe('system')
    expect(parseBootPrefsThemeMode(JSON.stringify({ v: {}, e: now + 1 }), now)).toBe('system')
    expect(parseBootPrefsThemeMode(JSON.stringify({ v: { theme: { mode: 1 } }, e: now + 1 }), now)).toBe('system')
  })
})

describe('bootSplashDocumentCss', () => {
  it('covers html/body/#app fill, safe-area body lock, and the shared gap token', () => {
    const css = bootSplashDocumentCss()
    expect(css).toContain(`:root{--space-4:${BOOT_SPLASH_SPACE_4}}`)
    expect(css).toContain(`gap:${BOOT_SPLASH_GAP}`)
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
  })
})

describe('bootThemeScript', () => {
  afterEach(() => {
    document.documentElement.removeAttribute('data-theme')
    document.documentElement.style.colorScheme = ''
    document.documentElement.style.backgroundColor = ''
    localStorage.removeItem(KEY_BROWSER_PREFS)
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

  function expectBackground(hex: string): void {
    const probe = document.createElement('div')
    probe.style.backgroundColor = hex
    expect(document.documentElement.style.backgroundColor).toBe(probe.style.backgroundColor)
  }

  it('applies a dark device pin over a light OS and strips theme-color media', () => {
    installThemeColorMetas()
    localStorage.setItem(
      KEY_BROWSER_PREFS,
      JSON.stringify({ v: { theme: { mode: 'dark' } }, e: Date.now() + 60_000 }),
    )
    runBootScript(false)

    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
    expectBackground(bootSplashDark.background)
    const metas = [...document.querySelectorAll('meta[name="theme-color"]')]
    expect(metas.length).toBeGreaterThan(0)
    for (const meta of metas) {
      expect(meta.getAttribute('content')).toBe(bootSplashDark.background)
      expect(meta.hasAttribute('media')).toBe(false)
    }
  })

  it('applies a light device pin over a dark OS and strips theme-color media', () => {
    installThemeColorMetas()
    localStorage.setItem(
      KEY_BROWSER_PREFS,
      JSON.stringify({ v: { theme: { mode: 'light' } }, e: Date.now() + 60_000 }),
    )
    runBootScript(true)

    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
    expectBackground(bootSplashLight.background)
    const metas = [...document.querySelectorAll('meta[name="theme-color"]')]
    expect(metas.length).toBeGreaterThan(0)
    for (const meta of metas) {
      expect(meta.getAttribute('content')).toBe(bootSplashLight.background)
      expect(meta.hasAttribute('media')).toBe(false)
    }
  })

  it('keeps media theme-color metas under system mode', () => {
    installThemeColorMetas()
    localStorage.setItem(
      KEY_BROWSER_PREFS,
      JSON.stringify({ v: { theme: { mode: 'system' } }, e: Date.now() + 60_000 }),
    )
    runBootScript(true)

    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
    const withMedia = document.querySelectorAll('meta[name="theme-color"][media]')
    expect(withMedia.length).toBe(2)
    expect(withMedia[0]!.getAttribute('content')).toBe(bootSplashLight.background)
    expect(withMedia[1]!.getAttribute('content')).toBe(bootSplashDark.background)
    const fallback = document.querySelector('meta[name="theme-color"]:not([media])')
    expect(fallback?.getAttribute('content')).toBe(bootSplashDark.background)
  })

  /**
   * The head script cannot import `parseBootPrefsThemeMode`. This matrix is
   * the contract that keeps the inlined parse aligned with the TypeScript
   * helper + `resolveBootPolarity`.
   */
  it('matches parseBootPrefsThemeMode + resolveBootPolarity for every envelope × OS case', () => {
    const now = Date.now()
    const cases: Array<{
      label: string
      raw: string | null
      systemDark: boolean
    }> = [
      { label: 'null prefs, light OS', raw: null, systemDark: false },
      { label: 'null prefs, dark OS', raw: null, systemDark: true },
      {
        label: 'dark pin, light OS',
        raw: JSON.stringify({ v: { theme: { mode: 'dark' } }, e: now + 60_000 }),
        systemDark: false,
      },
      {
        label: 'light pin, dark OS',
        raw: JSON.stringify({ v: { theme: { mode: 'light' } }, e: now + 60_000 }),
        systemDark: true,
      },
      {
        label: 'system mode, dark OS',
        raw: JSON.stringify({ v: { theme: { mode: 'system' } }, e: now + 60_000 }),
        systemDark: true,
      },
      {
        label: 'system mode, light OS',
        raw: JSON.stringify({ v: { theme: { mode: 'system' } }, e: now + 60_000 }),
        systemDark: false,
      },
      {
        label: 'expired dark pin, dark OS',
        raw: JSON.stringify({ v: { theme: { mode: 'dark' } }, e: now - 1 }),
        systemDark: true,
      },
      {
        label: 'expired dark pin, light OS',
        raw: JSON.stringify({ v: { theme: { mode: 'dark' } }, e: now - 1 }),
        systemDark: false,
      },
      {
        label: 'malformed JSON, dark OS',
        raw: '{',
        systemDark: true,
      },
      {
        label: 'missing mode, light OS',
        raw: JSON.stringify({ v: { theme: {} }, e: now + 60_000 }),
        systemDark: false,
      },
    ]

    for (const c of cases) {
      document.documentElement.removeAttribute('data-theme')
      document.documentElement.style.colorScheme = ''
      document.documentElement.style.backgroundColor = ''
      localStorage.removeItem(KEY_BROWSER_PREFS)
      if (c.raw !== null)
        localStorage.setItem(KEY_BROWSER_PREFS, c.raw)

      const expected = resolveBootPolarity(
        parseBootPrefsThemeMode(c.raw, now),
        c.systemDark,
      )
      runBootScript(c.systemDark)

      expect(
        document.documentElement.getAttribute('data-theme'),
        c.label,
      ).toBe(expected)
    }
  })
})

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

describe('boot splash lockstep sources', () => {
  const here = dirname(fileURLToPath(import.meta.url))

  it('keeps entry-server and BootSplash on the shared module; no twin stylesheet', () => {
    const entry = readFileSync(resolve(here, '../entry-server.tsx'), 'utf8')
    const splash = readFileSync(resolve(here, '../components/common/BootSplash.tsx'), 'utf8')

    for (const src of [entry, splash]) {
      expect(src).toContain('BOOT_SPLASH_LABEL')
      expect(src).toContain('BOOT_SPLASH_TEST_ID')
      expect(src).toContain('from \'~/lib/bootSplashTheme\'')
    }
    expect(entry).toContain('bootSplashDocumentCss')
    expect(entry).toContain('bootThemeScript')
    expect(entry).toContain('bootFailureWatchdogScript')
    expect(entry).toContain('BOOT_SPLASH_STATIC_ID')
    expect(entry).toContain('BootSplashIcon')
    expect(splash).toContain('data-boot-splash-icon')
    expect(splash).toContain('viewBox="0 0 64 64"')
    expect(splash).not.toContain('BootSplash.css')
    expect(splash).not.toMatch(/\bid=["']boot-splash["']/)
    expect(splash).not.toMatch(/src=["']\/icons\/leapmux-icon\.svg["']/)
    expect(entry).not.toMatch(/\bsrc=\{?BOOT_SPLASH_ICON/)
    expect(entry).not.toMatch(/<img[\s\S]*leapmux-icon\.svg/)
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

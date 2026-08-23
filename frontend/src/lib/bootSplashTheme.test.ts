import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { KEY_BROWSER_PREFS } from '~/lib/browserStorage'
import {
  BOOT_SPLASH_GAP,
  BOOT_SPLASH_ICON_SRC,
  BOOT_SPLASH_LABEL,
  BOOT_SPLASH_SPACE_4,
  BOOT_SPLASH_TEST_ID,
  bootSplashDark,
  bootSplashDocumentCss,
  bootSplashLight,
  bootThemeScript,
  parseBootPrefsThemeMode,
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
  it('covers html/body fill, splash polarity, and the shared gap token', () => {
    const css = bootSplashDocumentCss()
    expect(css).toContain(`:root{--space-4:${BOOT_SPLASH_SPACE_4}}`)
    expect(css).toContain(`gap:${BOOT_SPLASH_GAP}`)
    expect(css).toContain(bootSplashLight.background)
    expect(css).toContain(bootSplashDark.background)
    expect(css).toContain(`html,body{margin:0;background:${bootSplashLight.background}}`)
    expect(css).toContain(`html[data-theme="dark"]`)
    expect(css).toContain(`[data-testid="${BOOT_SPLASH_TEST_ID}"]`)
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

describe('boot splash lockstep sources', () => {
  const here = dirname(fileURLToPath(import.meta.url))

  it('keeps entry-server and BootSplash on the shared module; no twin stylesheet', () => {
    const entry = readFileSync(resolve(here, '../entry-server.tsx'), 'utf8')
    const splash = readFileSync(resolve(here, '../components/common/BootSplash.tsx'), 'utf8')

    for (const src of [entry, splash]) {
      expect(src).toContain('BOOT_SPLASH_LABEL')
      expect(src).toContain('BOOT_SPLASH_TEST_ID')
      expect(src).toContain('BOOT_SPLASH_ICON_SRC')
      expect(src).toContain('from \'~/lib/bootSplashTheme\'')
    }
    expect(entry).toContain('bootSplashDocumentCss')
    expect(entry).toContain('bootThemeScript')
    expect(splash).not.toContain('BootSplash.css')
    expect(BOOT_SPLASH_ICON_SRC).toBe('/icons/leapmux-icon.svg')
    expect(BOOT_SPLASH_TEST_ID).toBe('boot-splash')
    expect(BOOT_SPLASH_LABEL).toBe('Loading LeapMux…')
  })

  it('does not ship a BootSplash.css.ts twin', () => {
    const path = resolve(here, '../components/common/BootSplash.css.ts')
    expect(() => readFileSync(path, 'utf8')).toThrow()
  })
})

import type { Locator, Page } from '@playwright/test'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { openPreferencesAs, overrideThemeOnThisDevice, pickThemeMode, setPreferenceScope, themeModeRadio } from './preferences'
import { openAppAs, openSettingsAt, pickTheme } from './ui'

vi.mock('./ui', () => ({ openAppAs: vi.fn(), openSettingsAt: vi.fn(), pickTheme: vi.fn() }))

interface FakeTree {
  page: Page
  log: string[]
  node: (path: string) => Locator
}

/**
 * A fake page whose locators record each action in `log`, with the path that built them. Each assertion records its
 * expression and path, and `answer` decides it from the path.
 */
function fakeTree(answer: (expression: string, path: string) => boolean = () => true): FakeTree {
  const log: string[] = []
  class FakeLocator {
    readonly _apiName = 'Locator'
    constructor(readonly path: string) {}
    getByTestId(id: string) {
      return new FakeLocator(`${this.path} >> testid=${id}`)
    }

    getByRole(role: string, options: { name: string }) {
      return new FakeLocator(`${this.path} >> ${role}[${options.name}]`)
    }

    locator(selector: string) {
      return new FakeLocator(`${this.path} >> ${selector}`)
    }

    async click() {
      log.push(`click ${this.path}`)
    }

    get keyboard() {
      return { press: async (key: string) => {
        log.push(`press ${key}`)
      } }
    }

    async _expect(expression: string, options: { isNot: boolean }) {
      log.push(`${options.isNot ? 'not ' : ''}${expression} ${this.path}`)
      const matches = answer(expression, this.path)
      return { matches, received: matches ? 'expected' : 'other', log: [], timedOut: false }
    }
  }
  return { page: new FakeLocator('page') as unknown as Page, log, node: path => new FakeLocator(path) as unknown as Locator }
}

beforeEach(() => {
  vi.mocked(openAppAs).mockReset().mockResolvedValue(undefined)
  vi.mocked(openSettingsAt).mockReset()
  vi.mocked(pickTheme).mockReset().mockResolvedValue(undefined)
})

describe('openPreferencesAs', () => {
  it('opens the app as the session, then the dialog at the category, and returns the dialog', async () => {
    const { page, node } = fakeTree()
    const dialog = node('dialog')
    vi.mocked(openSettingsAt).mockResolvedValue(dialog)
    await expect(openPreferencesAs(page, 'leapmux-session=a', 'appearance')).resolves.toBe(dialog)
    expect(openAppAs).toHaveBeenCalledWith(page, 'leapmux-session=a')
    expect(openSettingsAt).toHaveBeenCalledWith(page, 'appearance')
    expect(vi.mocked(openAppAs).mock.invocationCallOrder[0]).toBeLessThan(vi.mocked(openSettingsAt).mock.invocationCallOrder[0]!)
  })
})

describe('setPreferenceScope', () => {
  const chip = 'page >> testid=scope-chip-appearance.theme'

  it.each([
    { scope: 'device' as const, item: 'Override on this device' },
    { scope: 'account' as const, item: 'Use account default' },
  ])('selects the $scope tier through the chip and requires the chip to show it', async ({ scope, item }) => {
    const { page, log } = fakeTree()
    await setPreferenceScope(page, 'appearance.theme', scope)
    expect(log).toEqual([`click ${chip}`, `click page >> menuitemradio[${item}]`, `to.have.text ${chip}`])
  })

  it('fails with the setting and the tier when the chip does not follow', async () => {
    const { page } = fakeTree((expression, path) => !(expression === 'to.have.text' && path === chip))
    await expect(setPreferenceScope(page, 'appearance.theme', 'device')).rejects.toThrow('the appearance.theme row edits the device tier')
  })
})

describe('pickThemeMode', () => {
  it('clicks the mode in the named group of the row', async () => {
    const { node, log } = fakeTree()
    const row = node('row')
    expect((themeModeRadio(row, 'Terminal theme mode', 'Dark') as unknown as { path: string }).path)
      .toBe('row >> radiogroup[Terminal theme mode] >> radio[Dark]')
    await pickThemeMode(row, 'Theme mode', 'Light')
    expect(log).toEqual(['click row >> radiogroup[Theme mode] >> radio[Light]'])
  })
})

describe('overrideThemeOnThisDevice', () => {
  it('pins the theme to this device, picks the palette, requires the paint, and closes the dialog', async () => {
    const { page, log, node } = fakeTree()
    const dialog = node('dialog')
    vi.mocked(openSettingsAt).mockResolvedValue(dialog)
    vi.mocked(pickTheme).mockImplementation(async (row, palette) => {
      log.push(`pick ${palette} in ${(row as unknown as { path: string }).path}`)
    })
    await overrideThemeOnThisDevice(page, 'nord')
    expect(openSettingsAt).toHaveBeenCalledWith(page, 'appearance')
    expect(log).toEqual([
      'click page >> testid=scope-chip-appearance.theme',
      'click page >> menuitemradio[Override on this device]',
      'to.have.text page >> testid=scope-chip-appearance.theme',
      'pick nord in dialog >> [data-setting-id="appearance.theme"]',
      'to.have.attribute.value page >> html',
      'press Escape',
      'to.be.hidden dialog',
    ])
  })

  it('fails when the page does not paint the palette, and leaves the dialog open', async () => {
    const { page, log, node } = fakeTree((expression, path) => !(expression === 'to.have.attribute.value' && path === 'page >> html'))
    vi.mocked(openSettingsAt).mockResolvedValue(node('dialog'))
    await expect(overrideThemeOnThisDevice(page, 'nord')).rejects.toThrow('the page paints the palette of the override')
    expect(log).not.toContain('press Escape')
  })
})

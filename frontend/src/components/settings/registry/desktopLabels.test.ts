import { describe, expect, it, vi } from 'vitest'

// Linux and Windows: a TRAY and a TASKBAR. The macOS twin of this file
// (./desktopLabels.mac.test.ts) pins the other wording. Two files rather than
// one, because the labels resolve at MODULE scope -- `getPlatform` caches its
// user-agent read, so a per-test override would not reach a constant the
// module already computed. `CustomTitlebar.test.tsx` / `.mac.test.tsx` split
// for the same reason.
vi.mock('~/lib/shortcuts/platform', () => ({
  isMac: () => false,
  getPlatform: () => 'linux',
}))

const { browserSettings } = await import('./settings')

/**
 * One Desktop row, narrowed to the account-backed shape.
 *
 * `browserSettings` is a union, and only the account half carries
 * `optionLabels` -- so the narrowing is also an assertion that these five rows
 * really are dual-tier, which is the whole point of them.
 */
function row(id: string) {
  const found = browserSettings.find(entry => entry.id === id)
  expect(found, `${id} must exist`).toBeDefined()
  if (found!.protoKey === undefined)
    throw new Error(`${id} must be an account-backed (dual) row`)
  return found as Extract<typeof found, { protoKey: string }>
}

describe('desktop row labels off macOS', () => {
  it('names the surface a tray', () => {
    expect(row('desktop.trayEnabled').label).toBe('Tray icon')
    expect(row('desktop.trayEnabled').help).toContain('tray')
    expect(row('desktop.trayEnabled').help).not.toContain('menu bar')
  })

  it('offers the tray and the taskbar as the two window destinations', () => {
    expect(row('desktop.trayOnClose').optionLabels).toEqual({
      tray: 'Hide to the tray',
      quit: 'Quit LeapMux',
    })
    expect(row('desktop.trayOnMinimize').optionLabels).toEqual({
      tray: 'Hide to the tray',
      taskbar: 'Keep in the taskbar',
    })
  })

  it('describes the login launch in the same words', () => {
    expect(row('desktop.startMinimized').help).toContain('tray icon is on')
  })

  // Findability must not depend on the platform: somebody who learned the
  // feature on the other operating system types the other word.
  it('indexes both platform words whichever it displays', () => {
    for (const id of ['desktop.trayEnabled', 'desktop.trayOnClose', 'desktop.trayOnMinimize']) {
      expect(row(id).keywords, id).toContain('tray')
      expect(row(id).keywords, id).toContain('menu bar')
    }
  })
})

import { describe, expect, it, vi } from 'vitest'

// macOS: a MENU BAR and a DOCK. "Tray icon" would name something the platform
// does not have. See ./desktopLabels.test.ts for why this is a second file.
vi.mock('~/lib/shortcuts/platform', () => ({
  isMac: () => true,
  getPlatform: () => 'mac',
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

describe('desktop row labels on macOS', () => {
  it('names the surface a menu bar', () => {
    expect(row('desktop.trayEnabled').label).toBe('Menu bar icon')
    expect(row('desktop.trayEnabled').help).toContain('menu bar')
  })

  it('offers the menu bar and the Dock as the two window destinations', () => {
    expect(row('desktop.trayOnClose').optionLabels).toEqual({
      tray: 'Hide to the menu bar',
      quit: 'Quit LeapMux',
    })
    expect(row('desktop.trayOnMinimize').optionLabels).toEqual({
      tray: 'Hide to the menu bar',
      taskbar: 'Keep in the Dock',
    })
  })

  it('describes the login launch in the same words', () => {
    expect(row('desktop.startMinimized').help).toContain('menu bar icon is on')
  })

  // The two rows that name no surface stay identical on every platform, so a
  // reader of the docs sees one wording for them.
  it('leaves the platform-neutral labels alone', () => {
    expect(row('desktop.trayOnClose').label).toBe('When you close the window')
    expect(row('desktop.trayOnMinimize').label).toBe('When you minimize the window')
    expect(row('desktop.startOnLogin').label).toBe('Start at login')
    expect(row('desktop.startMinimized').label).toBe('Window at login')
  })

  it('indexes both platform words whichever it displays', () => {
    for (const id of ['desktop.trayEnabled', 'desktop.trayOnClose', 'desktop.trayOnMinimize']) {
      expect(row(id).keywords, id).toContain('tray')
      expect(row(id).keywords, id).toContain('menu bar')
    }
  })
})

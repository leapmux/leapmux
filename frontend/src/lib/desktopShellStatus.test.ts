import { beforeEach, describe, expect, it } from 'vitest'
import { desktopShellRefusal, reportDesktopShellRefusal, resetDesktopShellStatusForTests } from './desktopShellStatus'

beforeEach(() => {
  resetDesktopShellStatusForTests()
})

describe('desktopShellStatus', () => {
  it('holds nothing until the shell refuses something', () => {
    expect(desktopShellRefusal()).toBeNull()
  })

  // The row ids are what the Preferences panel matches against, so a wrong one
  // hides the message entirely rather than showing it in the wrong place.
  it('addresses each refusable choice to the row that owns it', () => {
    reportDesktopShellRefusal({ setting: 'trayEnabled', message: 'no status-icon library' })
    expect(desktopShellRefusal()).toEqual({
      key: 'desktop.trayEnabled',
      message: 'no status-icon library',
    })

    reportDesktopShellRefusal({ setting: 'startOnLogin', message: 'the system declined' })
    expect(desktopShellRefusal()).toEqual({
      key: 'desktop.startOnLogin',
      message: 'the system declined',
    })
  })

  // A message left over from a failure the user has since repaired reads as
  // the repair having failed too.
  it('clears on the push that succeeds', () => {
    reportDesktopShellRefusal({ setting: 'trayEnabled', message: 'no status-icon library' })
    reportDesktopShellRefusal(null)
    expect(desktopShellRefusal()).toBeNull()
  })
})

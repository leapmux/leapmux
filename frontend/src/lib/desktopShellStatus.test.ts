import { beforeEach, describe, expect, it } from 'vitest'
import { desktopShellRefusals, reportDesktopShellRefusals, resetDesktopShellStatusForTests } from './desktopShellStatus'

beforeEach(() => {
  resetDesktopShellStatusForTests()
})

describe('desktopShellStatus', () => {
  it('holds nothing until the shell refuses something', () => {
    expect(desktopShellRefusals()).toEqual([])
  })

  // The row ids are what the Preferences panel matches against, so a wrong one
  // hides the message entirely rather than showing it in the wrong place.
  it('addresses each refusable choice to the row that owns it', () => {
    reportDesktopShellRefusals([{ setting: 'trayEnabled', message: 'no status-icon library' }])
    expect(desktopShellRefusals()).toEqual([{
      key: 'desktop.trayEnabled',
      message: 'no status-icon library',
    }])

    reportDesktopShellRefusals([{ setting: 'startOnLogin', message: 'the system declined' }])
    expect(desktopShellRefusals()).toEqual([{
      key: 'desktop.startOnLogin',
      message: 'the system declined',
    }])
  })

  // The two choices fail INDEPENDENTLY: a Linux desktop with no status-icon
  // library is also a plausible one for the system to decline a login item on.
  // Keeping one would leave the other toggle reading "on" with no explanation.
  it('keeps every refusal of one push', () => {
    reportDesktopShellRefusals([
      { setting: 'trayEnabled', message: 'no status-icon library' },
      { setting: 'startOnLogin', message: 'the system declined' },
    ])
    expect(desktopShellRefusals()).toEqual([
      { key: 'desktop.trayEnabled', message: 'no status-icon library' },
      { key: 'desktop.startOnLogin', message: 'the system declined' },
    ])
  })

  // A message left over from a failure the user has since repaired reads as
  // the repair having failed too.
  it('clears on the push that succeeds', () => {
    reportDesktopShellRefusals([{ setting: 'trayEnabled', message: 'no status-icon library' }])
    reportDesktopShellRefusals([])
    expect(desktopShellRefusals()).toEqual([])
  })

  // A push that repairs ONE of two failures must drop the message for the one
  // it fixed. Merging into the previous list would leave a stale message
  // beside a control the user just corrected.
  it('replaces the previous refusals rather than adding to them', () => {
    reportDesktopShellRefusals([
      { setting: 'trayEnabled', message: 'no status-icon library' },
      { setting: 'startOnLogin', message: 'the system declined' },
    ])
    reportDesktopShellRefusals([{ setting: 'startOnLogin', message: 'the system declined' }])
    expect(desktopShellRefusals()).toEqual([{
      key: 'desktop.startOnLogin',
      message: 'the system declined',
    }])
  })
})

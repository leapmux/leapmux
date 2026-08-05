import { describe, expect, it } from 'vitest'
import { fatalCloseMessage } from './fatalCloseMessage'
import {
  CLOSE_REASON_CONTROL_FLOOD,
  CLOSE_REASON_FORBIDDEN,
  CLOSE_REASON_SNAPSHOT_TOO_LARGE,
  CLOSE_REASON_TOO_MANY_CONNECTIONS,
} from './wsCloseCodes'

describe('fatalCloseMessage', () => {
  it('tells a capped user to close something, not to reload', () => {
    const message = fatalCloseMessage({ code: 1008, reason: CLOSE_REASON_TOO_MANY_CONNECTIONS })

    expect(message).toContain('Close another tab')
    // Reloading is the ONE piece of advice that cannot work here: the reload
    // opens another connection and is refused for the same reason.
    expect(message).not.toContain('Reload the page to reconnect')
  })

  it('keeps the generic advice for every other terminal close', () => {
    const generic = 'Live updates disconnected. Reload the page to reconnect.'

    // 1008 is also how the hub reports a revoked or expired credential, and
    // there a reload genuinely is the fix.
    expect(fatalCloseMessage({ code: 1008, reason: 'credential' })).toBe(generic)
    expect(fatalCloseMessage({ code: 1002, reason: '' })).toBe(generic)
    expect(fatalCloseMessage({ code: 1008, reason: 'something_new' })).toBe(generic)
  })

  // An ACL refusal is not a credential problem, so sending the user to sign in
  // again -- or to reload, which asks for the same thing -- is the wrong advice.
  // This used to fall through to the generic copy because the hub sent the
  // reason as unpinned prose that no client branched on.
  it('tells a forbidden user to ask for access, not to re-authenticate', () => {
    const message = fatalCloseMessage({ code: 1008, reason: CLOSE_REASON_FORBIDDEN })

    expect(message).toContain('do not have access')
    expect(message).not.toBe('Live updates disconnected. Reload the page to reconnect.')
  })

  // The one terminal reason where a plain reload really is the fix, because the
  // new socket starts with a full control-frame allowance. Saying nothing about
  // the account matters: nothing is wrong with it.
  it('tells a flooding client to reload, without blaming the account', () => {
    const message = fatalCloseMessage({ code: 1008, reason: CLOSE_REASON_CONTROL_FLOOD })

    expect(message).toContain('Reload the page')
    expect(message).not.toContain('administrator')
    expect(message).not.toContain('Close another tab')
  })

  it('matches the reason exactly rather than by substring', () => {
    // A substring match would fire on any reason that happens to contain the
    // token, and -- worse -- would miss nothing while quietly widening what
    // claims to be a connection-cap refusal.
    expect(fatalCloseMessage({ code: 1008, reason: `not_${CLOSE_REASON_TOO_MANY_CONNECTIONS}` }))
      .toBe('Live updates disconnected. Reload the page to reconnect.')
  })

  // A third terminal cause, and the one where "reload" and "close a tab" are
  // BOTH wrong: the snapshot is exactly as large either way. Only an operator
  // can move this, so the copy has to say so rather than hand the user a ritual.
  it('tells an oversized workspace to ask an administrator, not to reload', () => {
    const message = fatalCloseMessage({ code: 1008, reason: CLOSE_REASON_SNAPSHOT_TOO_LARGE })

    expect(message).toContain('administrator')
    expect(message).not.toContain('Reload the page to reconnect')
    expect(message).not.toContain('Close another tab')
  })
})

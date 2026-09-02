import { describe, expect, it } from 'vitest'
import { DEFAULT_NAV_GROUP_ID, NAV_GROUPS } from './navGroups'

describe('the section order (NAV_GROUPS)', () => {
  // Account leads. It is the group a user opens the dialog for deliberately --
  // a password, a passkey, an address -- where the rest are preferences they
  // adjust while they are already here.
  it('puts Account first', () => {
    expect(NAV_GROUPS[0]!.id).toBe('account')
  })

  // The two halves stay whole: the navigation draws one PREFERENCES rule and
  // one ADMINISTRATION rule, so an admin group between two user groups would
  // draw a third.
  it('keeps every user group before every admin group', () => {
    const firstAdmin = NAV_GROUPS.findIndex(g => g.admin)
    expect(firstAdmin).toBeGreaterThan(0)
    expect(NAV_GROUPS.slice(firstAdmin).every(g => g.admin)).toBe(true)
  })

  // Straight after Sign-up & Access, because it answers the same question one
  // step earlier: that section decides who may hold an account, this one
  // decides which addresses they can reach the hub at.
  it('puts Network access directly after Sign-up & Access', () => {
    const ids = NAV_GROUPS.map(g => g.id)
    expect(ids.indexOf('admin-network')).toBe(ids.indexOf('admin-signup') + 1)
  })

  it('gives every group a distinct id', () => {
    expect(new Set(NAV_GROUPS.map(g => g.id)).size).toBe(NAV_GROUPS.length)
  })

  // Desktop sits between Terminal and Files & Editors. The docs page lists the
  // categories in navigation order (site/content/docs/using/settings.md), and
  // nothing derives that page from this list, so a reorder here would leave it
  // describing a dialog that no longer matches.
  it('places Desktop between Terminal and Files & Editors', () => {
    const ids = NAV_GROUPS.map(g => g.id)
    expect(ids.slice(ids.indexOf('terminal'), ids.indexOf('files') + 1))
      .toEqual(['terminal', 'desktop', 'files'])
  })
})

describe('the section the dialog opens on (DEFAULT_NAV_GROUP_ID)', () => {
  // DERIVED, so reordering the list moves the landing section with it. Every
  // entry point used to spell 'appearance' out, which is how the list and the
  // section the dialog opened on came to disagree.
  it('is the first section, whatever that is', () => {
    expect(DEFAULT_NAV_GROUP_ID).toBe(NAV_GROUPS[0]!.id)
  })
})

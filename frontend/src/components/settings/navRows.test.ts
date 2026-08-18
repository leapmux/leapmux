import type { SettingDescriptor, SettingRowModel } from './types'
import { describe, expect, it } from 'vitest'
import { NAV_GROUPS } from './navGroups'
import { groupRowsByNav, occupiedNavGroups } from './navRows'

function descriptor(overrides: Partial<SettingDescriptor>): SettingDescriptor {
  return {
    id: 'x',
    category: 'appearance',
    label: 'X',
    scope: 'browser',
    control: { kind: 'toggle' },
    ...overrides,
  }
}

function row(overrides: Partial<SettingDescriptor>): SettingRowModel {
  return {
    descriptor: descriptor(overrides),
    binding: { value: () => null, set: () => {} },
  }
}

function hubRow(overrides: Partial<SettingDescriptor>): SettingRowModel {
  return row({ scope: 'hub', ...overrides })
}

function group(args: {
  isAdmin?: boolean
  browserRows?: SettingRowModel[]
  adminRows?: SettingRowModel[]
}) {
  return groupRowsByNav(NAV_GROUPS, {
    isAdmin: args.isAdmin ?? false,
    browserRows: args.browserRows ?? [],
    adminRows: args.adminRows ?? [],
  })
}

describe('groupRowsByNav', () => {
  it('gives every navigation group an entry, empty ones included', () => {
    const byNav = group({ browserRows: [row({ category: 'appearance' })] })
    expect([...byNav.keys()]).toEqual(NAV_GROUPS.map(g => g.id))
    expect(byNav.get('terminal')).toEqual([])
  })

  it('places a browser row in the matching user group only', () => {
    const theme = row({ id: 'appearance.theme', category: 'appearance' })
    const byNav = group({ browserRows: [theme] })
    expect(byNav.get('appearance')).toEqual([theme])
    expect(byNav.get('notifications')).toEqual([])
  })

  // The two sources meet in ONE list, in the order the dialog renders them:
  // browser rows for a user group, hub rows for an admin group. Six
  // consumers used to re-derive that split for themselves.
  it('keeps a group whose rows come from both sources apart by admin flag', () => {
    const browser = row({ id: 'advanced.debugLogging', category: 'advanced' })
    const hub = hubRow({ id: 'queue_budget.relay_bytes', category: 'advanced' })
    const byNav = group({ isAdmin: true, browserRows: [browser], adminRows: [hub] })
    expect(byNav.get('advanced')).toEqual([browser])
    expect(byNav.get('admin-advanced')).toEqual([hub])
  })

  it('drops a hidden row from its group', () => {
    const byNav = group({
      browserRows: [
        row({ id: 'visible', category: 'appearance' }),
        row({ id: 'gone', category: 'appearance', hidden: () => true }),
      ],
    })
    expect(byNav.get('appearance')?.map(r => r.descriptor.id)).toEqual(['visible'])
  })

  it('gives a non-admin session no admin rows at all', () => {
    const byNav = group({ isAdmin: false, adminRows: [hubRow({ category: 'captcha' })] })
    expect(byNav.get('admin-captcha')).toEqual([])
  })

  it('re-reads `hidden` on every call, so a rule that reads a preference tracks it', () => {
    let enabled = false
    const stack = row({ id: 'appearance.uiFontStack', category: 'appearance', hidden: () => !enabled })
    expect(group({ browserRows: [stack] }).get('appearance')).toEqual([])
    enabled = true
    expect(group({ browserRows: [stack] }).get('appearance')).toEqual([stack])
  })
})

describe('occupiedNavGroups', () => {
  it('keeps a user group that has a visible browser row', () => {
    const groups = occupiedNavGroups(NAV_GROUPS, group({
      browserRows: [row({ category: 'appearance' })],
    }))
    expect(groups.map(g => g.id)).toContain('appearance')
    expect(groups.some(g => g.admin)).toBe(false)
  })

  it('drops a user group whose only row is hidden', () => {
    const groups = occupiedNavGroups(NAV_GROUPS, group({
      browserRows: [row({ category: 'account', hidden: () => true })],
    }))
    expect(groups.map(g => g.id)).not.toContain('account')
  })

  it('drops admin groups when the session is not an administrator', () => {
    const groups = occupiedNavGroups(NAV_GROUPS, group({
      isAdmin: false,
      browserRows: [row({ category: 'appearance' })],
      adminRows: [hubRow({ category: 'captcha' })],
    }))
    expect(groups.map(g => g.id)).not.toContain('admin-captcha')
  })

  it('drops an admin group whose every row is hidden (solo captcha / rate-limits)', () => {
    const groups = occupiedNavGroups(NAV_GROUPS, group({
      isAdmin: true,
      browserRows: [row({ category: 'appearance' })],
      adminRows: [
        hubRow({ category: 'general', id: 'session' }),
        hubRow({ category: 'captcha', hidden: () => true }),
        hubRow({ category: 'rate-limits', hidden: () => true }),
      ],
    }))
    expect(groups.map(g => g.id)).toContain('admin-general')
    expect(groups.map(g => g.id)).not.toContain('admin-captcha')
    expect(groups.map(g => g.id)).not.toContain('admin-rate-limits')
  })

  it('keeps an admin group that has a visible hub row', () => {
    const groups = occupiedNavGroups(NAV_GROUPS, group({
      isAdmin: true,
      adminRows: [hubRow({ category: 'captcha' })],
    }))
    expect(groups.map(g => g.id)).toContain('admin-captcha')
  })

  it('lists the occupied groups in navigation order', () => {
    const groups = occupiedNavGroups(NAV_GROUPS, group({
      isAdmin: true,
      browserRows: [row({ category: 'advanced' }), row({ category: 'appearance' })],
      adminRows: [hubRow({ category: 'general' })],
    }))
    expect(groups.map(g => g.id)).toEqual(['appearance', 'advanced', 'admin-general'])
  })
})

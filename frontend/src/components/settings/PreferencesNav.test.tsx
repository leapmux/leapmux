import type { NavGroup } from './navGroups'
import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { NAV_GROUPS } from './navGroups'
import { PreferencesNav } from './PreferencesNav'

vi.mock('~/components/common/DropdownMenu', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/components/common/DropdownMenu')>()
  return {
    ...actual,
    DropdownMenu: (props: any) => (
      <>
        {props.trigger({
          'aria-expanded': false,
          'ref': () => {},
          'onPointerDown': () => {},
          'onClick': () => {},
        })}
        <div>{props.children}</div>
      </>
    ),
  }
})

afterEach(() => {
  cleanup()
})

const userGroups = NAV_GROUPS.filter(g => !g.admin)
const withAdmin = NAV_GROUPS
const adminOnly = NAV_GROUPS.filter(g => g.admin)

/**
 * The nav takes the RESOLVED group, not its id: the dialog owns the
 * fallback rule for a deep link whose group is hidden, and stating it a
 * second time here is what let the two disagree.
 */
function group(id: string): NavGroup {
  const found = NAV_GROUPS.find(g => g.id === id)
  if (!found)
    throw new Error(`no nav group ${id}`)
  return found
}

describe('preferencesNav desktop', () => {
  it('renders a tab per group and selects on click', () => {
    const onSelect = vi.fn()
    render(() => (
      <PreferencesNav
        groups={userGroups}
        active={group('appearance')}
        onSelect={onSelect}
        restartGroups={() => new Set()}
        compact={false}
      />
    ))
    expect(screen.getByRole('tablist', { name: 'Preferences sections' })).toBeTruthy()
    fireEvent.click(screen.getByTestId('preferences-nav-notifications'))
    expect(onSelect).toHaveBeenCalledWith('notifications')
  })

  it('labels the user sections PREFERENCES and admin sections ADMINISTRATION', () => {
    render(() => (
      <PreferencesNav
        groups={withAdmin}
        active={group('appearance')}
        onSelect={() => {}}
        restartGroups={() => new Set()}
        compact={false}
      />
    ))
    expect(screen.getByRole('separator', { name: 'Preferences' }).textContent).toBe('PREFERENCES')
    expect(screen.getByRole('separator', { name: 'Administration' }).textContent).toBe('ADMINISTRATION')
  })

  it('omits PREFERENCES when only admin groups are visible', () => {
    render(() => (
      <PreferencesNav
        groups={adminOnly}
        active={group('admin-general')}
        onSelect={() => {}}
        restartGroups={() => new Set()}
        compact={false}
      />
    ))
    expect(screen.queryByRole('separator', { name: 'Preferences' })).toBeNull()
    expect(screen.getByRole('separator', { name: 'Administration' })).toBeTruthy()
  })

  it('rules off ADMINISTRATION, and leaves the leading PREFERENCES header flush', () => {
    render(() => (
      <PreferencesNav
        groups={withAdmin}
        active={group('appearance')}
        onSelect={() => {}}
        restartGroups={() => new Set()}
        compact={false}
      />
    ))
    const nav = screen.getByRole('tablist', { name: 'Preferences sections' })
    const rules = nav.querySelectorAll('hr')
    expect(rules.length).toBe(1)
    // The rule opens the admin half, so the header it precedes is the one
    // the divider belongs to.
    expect(rules[0]!.nextElementSibling?.textContent).toBe('ADMINISTRATION')
    // Nothing precedes PREFERENCES: its own padding seats it under the
    // search box.
    expect(nav.firstElementChild?.textContent).toBe('PREFERENCES')
  })

  it('hides the rule from the accessibility tree', () => {
    render(() => (
      <PreferencesNav
        groups={withAdmin}
        active={group('appearance')}
        onSelect={() => {}}
        restartGroups={() => new Set()}
        compact={false}
      />
    ))
    // An <hr> is a separator by default. Both separators a reader reaches
    // must be the LABELLED headers, never the decorative rule between
    // them.
    const separators = screen.getAllByRole('separator')
    expect(separators.map(el => el.textContent)).toEqual(['PREFERENCES', 'ADMINISTRATION'])
  })

  it('draws no rule when only admin groups are visible', () => {
    render(() => (
      <PreferencesNav
        groups={adminOnly}
        active={group('admin-general')}
        onSelect={() => {}}
        restartGroups={() => new Set()}
        compact={false}
      />
    ))
    // ADMINISTRATION leads the list here, so there is nothing to rule off
    // -- a rule above the first header would hang under the search box.
    const nav = screen.getByRole('tablist', { name: 'Preferences sections' })
    expect(nav.querySelectorAll('hr').length).toBe(0)
    expect(nav.firstElementChild?.textContent).toBe('ADMINISTRATION')
  })

  it('marks restart groups with a decorative warning', () => {
    render(() => (
      <PreferencesNav
        groups={withAdmin}
        active={group('admin-advanced')}
        onSelect={() => {}}
        restartGroups={() => new Set(['admin-advanced'])}
        compact={false}
      />
    ))
    expect(screen.getByTestId('preferences-nav-admin-advanced').textContent).toContain('\u26A0')
  })
})

describe('preferencesNav compact', () => {
  it('renders an oat-styled dropdown instead of a native select or tab list', () => {
    const onSelect = vi.fn()
    render(() => (
      <PreferencesNav
        groups={userGroups}
        active={group('appearance')}
        onSelect={onSelect}
        restartGroups={() => new Set()}
        compact={true}
      />
    ))
    expect(screen.queryByRole('tablist')).toBeNull()
    expect(screen.queryByRole('combobox')).toBeNull()
    const trigger = screen.getByTestId('preferences-nav')
    expect(trigger.tagName).toBe('BUTTON')
    expect(trigger).toHaveTextContent('Appearance')

    fireEvent.click(trigger)
    fireEvent.click(screen.getByTestId('preferences-nav-terminal'))
    expect(onSelect).toHaveBeenCalledWith('terminal')
  })

  it('groups user and admin sections under matching headers', () => {
    render(() => (
      <PreferencesNav
        groups={withAdmin}
        active={group('appearance')}
        onSelect={() => {}}
        restartGroups={() => new Set()}
        compact={true}
      />
    ))
    fireEvent.click(screen.getByTestId('preferences-nav'))
    expect(screen.getByText('PREFERENCES')).toBeTruthy()
    expect(screen.getByText('ADMINISTRATION')).toBeTruthy()
    const items = screen.getAllByRole('menuitemradio')
    expect(items.map(el => el.getAttribute('data-testid'))).toEqual(
      withAdmin.map(g => `preferences-nav-${g.id}`),
    )
  })

  it('omits the ADMINISTRATION header when no admin groups are visible', () => {
    render(() => (
      <PreferencesNav
        groups={userGroups}
        active={group('appearance')}
        onSelect={() => {}}
        restartGroups={() => new Set()}
        compact={true}
      />
    ))
    fireEvent.click(screen.getByTestId('preferences-nav'))
    expect(screen.getByText('PREFERENCES')).toBeTruthy()
    expect(screen.queryByText('ADMINISTRATION')).toBeNull()
  })

  it('omits the PREFERENCES header when only admin groups are visible', () => {
    render(() => (
      <PreferencesNav
        groups={adminOnly}
        active={group('admin-general')}
        onSelect={() => {}}
        restartGroups={() => new Set()}
        compact={true}
      />
    ))
    fireEvent.click(screen.getByTestId('preferences-nav'))
    expect(screen.queryByText('PREFERENCES')).toBeNull()
    expect(screen.getByText('ADMINISTRATION')).toBeTruthy()
  })

  it('marks the active section and appends the restart mark to labels', () => {
    render(() => (
      <PreferencesNav
        groups={withAdmin}
        active={group('admin-advanced')}
        onSelect={() => {}}
        restartGroups={() => new Set(['admin-advanced'])}
        compact={true}
      />
    ))
    expect(screen.getByTestId('preferences-nav')).toHaveTextContent(/\u26A0/)
    fireEvent.click(screen.getByTestId('preferences-nav'))
    const item = screen.getByTestId('preferences-nav-admin-advanced')
    expect(item).toHaveAttribute('aria-checked', 'true')
    expect(item).toHaveTextContent('\u26A0')
  })
})

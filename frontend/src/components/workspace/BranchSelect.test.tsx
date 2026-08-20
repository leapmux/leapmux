/// <reference types="vitest/globals" />
import type { GitBranchEntry } from '~/generated/leapmux/v1/git_pb'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { menuOptions, menuTrigger, menuTriggerText, pickMenuValue } from '~/test-support/menu'
import { BranchSelect, partitionBranches } from './BranchSelect'

const MENU = 'branch-select-menu'

function branch(name: string, isRemote = false): GitBranchEntry {
  return { name, isRemote } as GitBranchEntry
}

function renderBranchSelect(overrides: Partial<Parameters<typeof BranchSelect>[0]> = {}) {
  const onChange = vi.fn()
  const props = {
    value: '',
    onChange,
    local: [] as GitBranchEntry[],
    remote: [] as GitBranchEntry[],
    ...overrides,
  }
  const result = render(() => <BranchSelect {...props} />)
  return { onChange, unmount: result.unmount }
}

describe('partitionBranches', () => {
  it('splits local from remote and preserves the order within each', () => {
    const { local, remote } = partitionBranches([
      branch('main'),
      branch('origin/main', true),
      branch('feature'),
      branch('origin/feature', true),
    ])
    expect(local.map(b => b.name)).toEqual(['main', 'feature'])
    expect(remote.map(b => b.name)).toEqual(['origin/main', 'origin/feature'])
  })

  it('returns two empty lists for no branches', () => {
    expect(partitionBranches([])).toEqual({ local: [], remote: [] })
  })
})

describe('branchSelect', () => {
  it('shows the loading sentinel while loading', () => {
    renderBranchSelect({ loading: true })
    expect(menuTrigger(MENU)).toBeDisabled()
    expect(menuTriggerText(MENU)).toContain('Loading branches...')
  })

  it('shows the empty sentinel when both lists are empty', () => {
    renderBranchSelect({ local: [], remote: [] })
    expect(menuTrigger(MENU)).toBeDisabled()
    expect(menuTriggerText(MENU)).toContain('No branches found')
  })

  it('lists local branches before remote ones', () => {
    renderBranchSelect({ local: [branch('main')], remote: [branch('origin/dev', true)] })
    expect(menuOptions(MENU)).toEqual(['main', 'origin/dev'])
  })

  it('heads each group the way optgroup used to', () => {
    // `<optgroup label>` became a heading the menu draws whenever the group
    // changes, so Local and Remote still read apart.
    renderBranchSelect({ local: [branch('main')], remote: [branch('origin/dev', true)] })
    expect(screen.getByText('Local')).toBeInTheDocument()
    expect(screen.getByText('Remote')).toBeInTheDocument()
  })

  it('omits a group heading when that list is empty', () => {
    renderBranchSelect({ local: [branch('main')], remote: [] })
    expect(screen.getByText('Local')).toBeInTheDocument()
    expect(screen.queryByText('Remote')).toBeNull()
  })

  it('marks the current branch only when asked', () => {
    const { unmount } = renderBranchSelect({
      local: [branch('main'), branch('dev')],
      currentBranch: 'main',
      showCurrent: true,
    })
    expect(menuOptions(MENU)).toEqual(['main (current)', 'dev'])
    unmount()

    renderBranchSelect({ local: [branch('main')], currentBranch: 'main', showCurrent: false })
    expect(menuOptions(MENU)).toEqual(['main'])
  })

  // The prompt is the TRIGGER's, not a row. It used to be injected into the
  // options as well, so the same string showed twice -- and the extra row made
  // the list one entry long in a repository with no branches, which is what
  // stopped `LoadingMenu` deriving its own empty state.
  it('shows the prompt on the trigger and never as an option', () => {
    renderBranchSelect({ local: [branch('main')], value: '' })
    expect(menuOptions(MENU)).toEqual(['main'])
    expect(menuTriggerText(MENU)).toContain('Select a branch...')
  })

  // With no branches at all the menu says so, rather than offering a prompt row
  // that picks nothing.
  it('says there are no branches when both lists are empty', () => {
    renderBranchSelect({ local: [], remote: [], value: '' })
    expect(menuOptions(MENU)).toEqual([])
    expect(menuTriggerText(MENU)).toContain('No branches found')
    expect(menuTrigger(MENU)).toBeDisabled()
  })

  it('fires onChange with the picked branch', () => {
    const { onChange } = renderBranchSelect({ local: [branch('main'), branch('dev')], value: 'main' })
    pickMenuValue(MENU, 'dev')
    expect(onChange).toHaveBeenCalledWith('dev')
  })

  it('honours the caller disable on top of its own', () => {
    renderBranchSelect({ local: [branch('main')], disabled: true })
    expect(menuTrigger(MENU)).toBeDisabled()
  })

  // A repository's branch list is unbounded, and a native `<select>` gave
  // type-ahead over it for free. The filter box is what buys that back, and it
  // is the reason this menu renders `as="div"` rather than `as="menu"`.
  describe('filter', () => {
    function renderMany() {
      return renderBranchSelect({
        local: [branch('main'), branch('feature/login'), branch('feature/signup')],
        remote: [branch('origin/main', true)],
      })
    }

    it('narrows the list to the matching branches', () => {
      renderMany()
      fireEvent.input(screen.getByTestId('loading-menu-filter'), { target: { value: 'feature' } })
      expect(menuOptions(MENU)).toEqual(['feature/login', 'feature/signup'])
    })

    it('matches without regard to case', () => {
      renderMany()
      fireEvent.input(screen.getByTestId('loading-menu-filter'), { target: { value: 'LOGIN' } })
      expect(menuOptions(MENU)).toEqual(['feature/login'])
    })

    it('says so rather than showing an empty menu', () => {
      renderMany()
      fireEvent.input(screen.getByTestId('loading-menu-filter'), { target: { value: 'nothing' } })
      expect(menuOptions(MENU)).toEqual([])
      expect(screen.getByText('No matches')).toBeInTheDocument()
    })

    it('still commits the branch the user picks out of a filtered list', () => {
      const { onChange } = renderMany()
      fireEvent.input(screen.getByTestId('loading-menu-filter'), { target: { value: 'signup' } })
      pickMenuValue(MENU, 'feature/signup')
      expect(onChange).toHaveBeenCalledWith('feature/signup')
    })
  })
})

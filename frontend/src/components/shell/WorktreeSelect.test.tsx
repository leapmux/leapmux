/// <reference types="vitest/globals" />
import type { WorktreeOption } from './WorktreeSelect'
import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { menuOptions, menuTrigger, menuTriggerText, pickMenuValue } from '~/test-support/menu'
import { WorktreeSelect } from './WorktreeSelect'

const MENU = 'worktree-select-menu'

function renderWorktreeSelect(overrides: Partial<Parameters<typeof WorktreeSelect>[0]> = {}) {
  const onChange = vi.fn()
  const props = {
    value: '',
    onChange,
    worktrees: [] as WorktreeOption[],
    loading: false,
    ...overrides,
  }
  render(() => <WorktreeSelect {...props} />)
  return { onChange }
}

describe('worktreeSelect', () => {
  it('shows the loading sentinel while loading', () => {
    renderWorktreeSelect({ loading: true })
    expect(menuTrigger(MENU)).toBeDisabled()
    expect(menuTriggerText(MENU)).toContain('Loading worktrees...')
  })

  it('shows the empty sentinel when not loading and no worktrees', () => {
    renderWorktreeSelect({ loading: false, worktrees: [] })
    expect(menuTrigger(MENU)).toBeDisabled()
    expect(menuTriggerText(MENU)).toContain('No worktrees found')
  })

  it('renders one option per worktree with the "branch — path" label', () => {
    renderWorktreeSelect({
      worktrees: [
        { path: '/home/u/wt/feature', branch: 'feature' },
        { path: '/home/u/wt/bare', branch: '' },
      ],
      homeDir: '/home/u',
    })
    expect(menuOptions(MENU)).toEqual(['feature — ~/wt/feature', '~/wt/bare'])
  })

  it('abbreviates the path against the home directory it is given', () => {
    renderWorktreeSelect({
      worktrees: [{ path: '/home/u/wt/x', branch: 'x' }],
      homeDir: '/elsewhere',
    })
    // No `~/` when the path sits outside the home directory.
    expect(menuOptions(MENU)).toEqual(['x — /home/u/wt/x'])
  })

  it('shows the prompt while nothing is chosen', () => {
    // The `<option value="">Select a worktree...</option>` this replaced. A
    // menu has no empty option, so the prompt lives on the trigger.
    renderWorktreeSelect({ worktrees: [{ path: '/a', branch: 'a' }], value: '' })
    expect(menuTriggerText(MENU)).toContain('Select a worktree...')
  })

  it('fires onChange with the picked path', () => {
    const { onChange } = renderWorktreeSelect({
      worktrees: [{ path: '/a', branch: 'a' }, { path: '/b', branch: 'b' }],
      value: '/a',
    })
    pickMenuValue(MENU, '/b')
    expect(onChange).toHaveBeenCalledWith('/b')
  })
})

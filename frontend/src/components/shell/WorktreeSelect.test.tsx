/// <reference types="vitest/globals" />
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { WorktreeSelect } from './WorktreeSelect'

function renderWorktreeSelect(overrides: Partial<Parameters<typeof WorktreeSelect>[0]> = {}) {
  const onChange = vi.fn()
  const props = {
    value: '',
    onChange,
    worktrees: [] as { path: string, branch: string }[],
    loading: false,
    ...overrides,
  }
  render(() => <WorktreeSelect {...props} />)
  const select = screen.getByRole('combobox') as HTMLSelectElement
  return { select, onChange }
}

describe('worktreeSelect', () => {
  it('shows the loading sentinel option while loading', () => {
    const { select } = renderWorktreeSelect({ loading: true })
    expect(select.disabled).toBe(true)
    expect(select.value).toBe('')
    expect(Array.from(select.options).map(o => o.textContent)).toContain('Loading worktrees...')
  })

  it('shows the empty sentinel option when not loading and no worktrees', () => {
    const { select } = renderWorktreeSelect({ loading: false, worktrees: [] })
    // When there are no worktrees, the picker has nothing to offer; the
    // sentinel "No worktrees found" option keeps the select visible but
    // the user can't pick anything meaningful.
    expect(Array.from(select.options).map(o => o.textContent)).toContain('No worktrees found')
  })

  it('renders one option per worktree with the "branch — path" label', () => {
    const { select } = renderWorktreeSelect({
      worktrees: [
        { path: '/tmp/repo-worktrees/feature', branch: 'feature' },
        { path: '/tmp/repo-worktrees/bugfix', branch: 'bugfix' },
      ],
    })
    expect(select.disabled).toBe(false)
    const labels = Array.from(select.options).map(o => o.textContent?.trim())
    // First option is the "Select a worktree..." placeholder.
    expect(labels[0]).toBe('Select a worktree...')
    expect(labels.slice(1)).toEqual([
      'feature — /tmp/repo-worktrees/feature',
      'bugfix — /tmp/repo-worktrees/bugfix',
    ])
  })

  it('omits the "branch — " prefix when branch is empty (detached worktree)', () => {
    const { select } = renderWorktreeSelect({
      worktrees: [{ path: '/tmp/repo-worktrees/detached', branch: '' }],
    })
    const labels = Array.from(select.options).map(o => o.textContent?.trim())
    expect(labels.slice(1)).toEqual(['/tmp/repo-worktrees/detached'])
  })

  it('abbreviates home-directory paths with ~/', () => {
    const { select } = renderWorktreeSelect({
      worktrees: [{ path: '/home/me/projects/repo-worktrees/feature', branch: 'feature' }],
      homeDir: '/home/me',
    })
    const labels = Array.from(select.options).map(o => o.textContent?.trim())
    expect(labels.slice(1)).toEqual(['feature — ~/projects/repo-worktrees/feature'])
  })

  it('fires onChange with the picked path when a worktree is selected', () => {
    const { select, onChange } = renderWorktreeSelect({
      worktrees: [
        { path: '/tmp/repo-worktrees/feature', branch: 'feature' },
        { path: '/tmp/repo-worktrees/bugfix', branch: 'bugfix' },
      ],
    })
    fireEvent.change(select, { target: { value: '/tmp/repo-worktrees/bugfix' } })
    expect(onChange).toHaveBeenCalledWith('/tmp/repo-worktrees/bugfix')
  })

  it('does NOT render any worktree options while loading even if the list is non-empty', () => {
    // Regression guard: if the loading sentinel were replaced rather
    // than gating the For, the user could see stale options during a
    // re-fetch. The component should hide them.
    const { select } = renderWorktreeSelect({
      loading: true,
      worktrees: [{ path: '/tmp/repo-worktrees/x', branch: 'x' }],
    })
    const labels = Array.from(select.options).map(o => o.textContent?.trim())
    expect(labels).toEqual(['Loading worktrees...'])
  })
})

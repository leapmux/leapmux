import type { JSX } from 'solid-js'
import type { DiffStats } from '~/stores/repoGit'
import { render, screen } from '@solidjs/testing-library'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { hoverForTooltip, stubClipped, stubFitting } from '~/test-support/clipStub'
import { RowLabelWithStats } from './gitStatusUtils'
import { labelWithStats } from './sharedTree.css'

const STATS: DiffStats = { added: 4, deleted: 2, untracked: 1 }

beforeAll(() => {
  // The tooltip enters the top layer when it opens.
  HTMLElement.prototype.showPopover = vi.fn()
  HTMLElement.prototype.hidePopover = vi.fn()
})

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
})

function renderRow(props: {
  label?: JSX.Element
  tooltipLabel?: string
  tooltipContent?: JSX.Element
  stats?: DiffStats | null
}) {
  const { container } = render(() => (
    <RowLabelWithStats
      label={props.label ?? 'feature/auth'}
      tooltipLabel={props.tooltipLabel}
      tooltipContent={props.tooltipContent}
      stats={props.stats ?? null}
    />
  ))
  return container.querySelector<HTMLElement>(`.${labelWithStats}`)!
}

describe('rowLabelWithStats', () => {
  // The pre-existing contract, and the one a shared component must not lose:
  // the repo group row, the directory tree and the files section all rely on a
  // tooltip that repeats the label ONLY when the label is truncated. Every one
  // of them passes no `tooltipContent`.
  describe('without tooltipContent', () => {
    it('opens on hover once the label is clipped', () => {
      const el = renderRow({})
      stubClipped(el)

      expect(hoverForTooltip(el)?.textContent).toBe('feature/auth')
    })

    it('stays shut while the label fits', () => {
      const el = renderRow({})
      stubFitting(el)

      expect(hoverForTooltip(el)).toBeNull()
    })

    it('shows the tooltipLabel instead of the visible label when the two differ', () => {
      // The repo group row's case: a short basename on screen, the full origin
      // URL in the tooltip.
      const el = renderRow({ label: 'leapmux', tooltipLabel: 'github.com/leapmux/leapmux' })
      stubClipped(el)

      expect(hoverForTooltip(el)?.textContent).toBe('github.com/leapmux/leapmux')
    })

    it('carries the diff badge into the tooltip body', () => {
      const el = renderRow({ stats: STATS })
      stubClipped(el)

      const tooltip = hoverForTooltip(el)
      expect(tooltip?.textContent).toContain('feature/auth')
      expect(tooltip?.querySelector('[data-testid="git-diff-stats"]')?.textContent).toContain('+4')
    })

    // A JSX label has no string to fall back on, so the body would be an empty
    // string. The row still renders, and the tooltip states nothing rather than
    // throwing.
    it('falls back to an empty body for a JSX label with no tooltipLabel', () => {
      const el = renderRow({ label: <em>styled</em> })
      stubClipped(el)

      expect(hoverForTooltip(el)?.textContent).toBe('')
    })
  })

  // The new mode. A caller passes a body that states facts the row does not
  // show, so waiting for the label to clip would hide them whenever the label
  // happened to fit.
  describe('with tooltipContent', () => {
    it('opens on hover even though the label fits', () => {
      const el = renderRow({ tooltipContent: <span>Worktree · ~/repos/wt</span> })
      stubFitting(el)

      expect(hoverForTooltip(el)?.textContent).toBe('Worktree · ~/repos/wt')
    })

    it('replaces the default label+stats body rather than adding to it', () => {
      const el = renderRow({ stats: STATS, tooltipContent: <span>Worktree</span> })
      stubFitting(el)

      const tooltip = hoverForTooltip(el)
      expect(tooltip?.textContent).toBe('Worktree')
      expect(tooltip?.textContent).not.toContain('feature/auth')
    })

    // The visible row is unchanged by the prop: only the tooltip body moves.
    it('leaves the visible label and badge alone', () => {
      const el = renderRow({ stats: STATS, tooltipContent: <span>Worktree</span> })

      expect(el.textContent).toContain('feature/auth')
      expect(screen.getByTestId('git-diff-stats').textContent).toContain('+4')
    })
  })
})

import type { Component, JSX } from 'solid-js'
import type { DiffStats } from '~/stores/repoGit'
import { render, screen } from '@solidjs/testing-library'
import { Show } from 'solid-js'
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
  showWhen?: 'always' | 'clipped'
  stats?: DiffStats | null
}) {
  const { container } = render(() => (
    <RowLabelWithStats
      label={props.label ?? 'feature/auth'}
      tooltipLabel={props.tooltipLabel}
      tooltipContent={props.tooltipContent}
      showWhen={props.showWhen}
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
  // show, and asks for `showWhen="always"` so the label happening to fit does
  // not hide them.
  describe('with tooltipContent', () => {
    it('opens on hover even though the label fits', () => {
      const el = renderRow({ tooltipContent: <span>Worktree · ~/repos/wt</span>, showWhen: 'always' })
      stubFitting(el)

      expect(hoverForTooltip(el)?.textContent).toBe('Worktree · ~/repos/wt')
    })

    it('replaces the default label+stats body rather than adding to it', () => {
      const el = renderRow({ stats: STATS, tooltipContent: <span>Worktree</span>, showWhen: 'always' })
      stubFitting(el)

      const tooltip = hoverForTooltip(el)
      expect(tooltip?.textContent).toBe('Worktree')
      expect(tooltip?.textContent).not.toContain('feature/auth')
    })

    // The visible row is unchanged by the prop: only the tooltip body moves.
    it('leaves the visible label and badge alone', () => {
      const el = renderRow({ stats: STATS, tooltipContent: <span>Worktree</span>, showWhen: 'always' })

      expect(el.textContent).toContain('feature/auth')
      expect(screen.getByTestId('git-diff-stats').textContent).toContain('+4')
    })

    // The two decisions are INDEPENDENT. `showWhen` was derived from whether
    // `tooltipContent` was passed, which fused them -- and, worse, read a lazy
    // JSX prop from inside `present()`'s unowned `setTimeout`, building the
    // whole body just to test its truthiness and leaking the computation.
    it('keeps a custom body clipped-only when the caller asks for that', () => {
      const el = renderRow({ tooltipContent: <span>Worktree</span> })
      stubFitting(el)

      expect(hoverForTooltip(el)).toBeNull()
    })

    /**
     * The body is built ONCE, however many times the tooltip reads it.
     *
     * `tooltipContent` has to sit in JSX PROP position here, not in a plain
     * options object: Solid compiles the former to a lazy getter that re-runs
     * per read, and evaluates the latter eagerly exactly once — so a helper
     * that takes the element as an argument cannot see this at all.
     *
     * Deriving `showWhen` from `tooltipContent` is what made the count grow:
     * `Tooltip.present()` reads `showWhen` from a bare `setTimeout`, outside
     * any owner, so each hover built the body again and left the computation
     * undisposed.
     */
    it('builds the tooltip body once per hover', () => {
      let builds = 0
      const Body: Component = () => {
        builds++
        return <span>Worktree</span>
      }
      const { container } = render(() => (
        <RowLabelWithStats
          label="feature/auth"
          tooltipContent={<Body />}
          showWhen="always"
          stats={null}
        />
      ))
      const el = container.querySelector<HTMLElement>(`.${labelWithStats}`)!
      stubFitting(el)

      hoverForTooltip(el)
      expect(builds).toBe(1)
    })

    /**
     * Solid warns when a computation is created with no owner, and never
     * disposes it.
     *
     * The body must CONTAIN a computation for this to be observable, which is
     * why it renders a `<Show>` — the real body (`WorkingTreeRows`) has one for
     * its diff badge. A plain element creates none, so a test with one could
     * never fail.
     *
     * The leak came from `present()`: it runs in a bare `setTimeout`, outside
     * the owner that `show`/`hide` re-enter, so anything it built there had no
     * parent. Reading `showWhen` used to build the body.
     */
    it('creates no undisposed computation when it opens', () => {
      const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
      try {
        const Body: Component = () => (
          <Show when={STATS}>{s => <span>{s().added}</span>}</Show>
        )
        const { container } = render(() => (
          <RowLabelWithStats
            label="feature/auth"
            tooltipContent={<Body />}
            showWhen="always"
            stats={null}
          />
        ))
        const el = container.querySelector<HTMLElement>(`.${labelWithStats}`)!
        stubFitting(el)
        warn.mockClear()

        hoverForTooltip(el)

        const leaks = warn.mock.calls.filter(c => String(c[0]).includes('will never be disposed'))
        expect(leaks).toEqual([])
      }
      finally {
        warn.mockRestore()
      }
    })
  })
})

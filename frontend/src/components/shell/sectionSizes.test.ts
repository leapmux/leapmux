import { describe, expect, it } from 'vitest'
import { distributeSectionSizes } from './sectionSizes'

/** The sum every result must reach, within floating-point noise. */
function total(sizes: Map<string, number>): number {
  return [...sizes.values()].reduce((sum, size) => sum + size, 0)
}

describe('distributeSectionSizes', () => {
  it('keeps a declared share when the declarations fit', () => {
    const sizes = distributeSectionSizes(
      ['files', 'todos', 'tasks'],
      new Map([['files', 0.6], ['todos', 0.15], ['tasks', 0.25]]),
    )
    expect(sizes.get('files')).toBeCloseTo(0.6)
    expect(sizes.get('todos')).toBeCloseTo(0.15)
    expect(sizes.get('tasks')).toBeCloseTo(0.25)
  })

  // The left sidebar's shape: one declared section beside the workspace
  // sections, which split the remainder.
  it('splits the remainder among the sections that declare nothing', () => {
    const sizes = distributeSectionSizes(
      ['workers', 'progress', 'archived'],
      new Map([['workers', 0.15]]),
    )
    expect(sizes.get('workers')).toBeCloseTo(0.15)
    expect(sizes.get('progress')).toBeCloseTo(0.425)
    expect(sizes.get('archived')).toBeCloseTo(0.425)
  })

  /**
   * The reachable bug this helper exists for. The three right-sidebar defaults
   * sum to exactly 1.0, and every section is draggable between the sidebars, so
   * one drop puts a workspace section beside them. Under the reserve-then-split
   * rule that section computed a share of zero and rendered with no height at
   * all, header included.
   */
  it('gives every section a positive size when the declarations claim it all', () => {
    const sizes = distributeSectionSizes(
      ['files', 'todos', 'tasks', 'dropped'],
      new Map([['files', 0.6], ['todos', 0.15], ['tasks', 0.25]]),
    )
    for (const [id, size] of sizes)
      expect(size, id).toBeGreaterThan(0)
    expect(total(sizes)).toBeCloseTo(1)
  })

  // Over 1.0 the remainder is negative, which used to produce a negative
  // flex-grow that the browser discards.
  it('gives every section a positive size when the declarations exceed the sidebar', () => {
    const sizes = distributeSectionSizes(
      ['files', 'todos', 'tasks', 'workers', 'dropped'],
      new Map([['files', 0.6], ['todos', 0.15], ['tasks', 0.25], ['workers', 0.15]]),
    )
    for (const [id, size] of sizes)
      expect(size, id).toBeGreaterThan(0)
    expect(total(sizes)).toBeCloseTo(1)
  })

  it('splits equally when nothing is declared', () => {
    const sizes = distributeSectionSizes(['a', 'b', 'c', 'd'], new Map())
    for (const size of sizes.values())
      expect(size).toBeCloseTo(0.25)
  })

  it('normalizes declarations that do not sum to one', () => {
    const sizes = distributeSectionSizes(['a', 'b'], new Map([['a', 0.2], ['b', 0.2]]))
    expect(sizes.get('a')).toBeCloseTo(0.5)
    expect(sizes.get('b')).toBeCloseTo(0.5)
    expect(total(sizes)).toBeCloseTo(1)
  })

  it('returns nothing for an empty sidebar', () => {
    expect(distributeSectionSizes([], new Map()).size).toBe(0)
  })

  it('gives a lone section the whole sidebar', () => {
    expect(distributeSectionSizes(['only'], new Map([['only', 0.15]])).get('only')).toBeCloseTo(1)
  })

  // A zero declaration would otherwise divide by zero on the normalize.
  it('splits equally when every declaration is zero', () => {
    const sizes = distributeSectionSizes(['a', 'b'], new Map([['a', 0], ['b', 0]]))
    expect(sizes.get('a')).toBeCloseTo(0.5)
    expect(sizes.get('b')).toBeCloseTo(0.5)
  })
})

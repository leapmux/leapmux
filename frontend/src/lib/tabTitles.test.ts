/// <reference types="vitest/globals" />
import { describe, expect, it } from 'vitest'
import { AGENT_TITLE_PREFIX, TAB_NAMES, TERMINAL_TITLE_PREFIX } from '~/generated/contracts/tab-names'
import { randomAgentTitle, randomTerminalTitle } from '~/lib/tabTitles'

// The shape the WORKER's plan-mode auto-rename accepts. A pre-filled title
// that stopped matching would silently stop being overwritable by a plan
// title, so this is asserted here and not only in the Go suite.
const AGENT_AUTO_TITLE = new RegExp(`^${AGENT_TITLE_PREFIX} [A-Z][A-Za-z]+$`)

describe('randomAgentTitle', () => {
  it('draws a name from the shared pool', () => {
    for (let i = 0; i < 50; i++) {
      const [prefix, ...rest] = randomAgentTitle().split(' ')
      expect(prefix).toBe(AGENT_TITLE_PREFIX)
      expect(rest).toHaveLength(1)
      expect(TAB_NAMES).toContain(rest[0])
    }
  })

  it('produces the shape the worker treats as auto-generated', () => {
    expect(randomAgentTitle()).toMatch(AGENT_AUTO_TITLE)
  })

  // A picker that collapsed to one name (a lost Math.random, an off-by-one on
  // the bound) still passes every shape assertion above.
  it('does not return the same name every time', () => {
    const drawn = new Set(Array.from({ length: 200 }, randomAgentTitle))
    expect(drawn.size).toBeGreaterThan(1)
  })

  // Math.floor(Math.random() * n) returns n only if Math.random() returns 1,
  // which it never does -- but an off-by-one written as `+ 1` would index past
  // the end and yield undefined, spelling the title "Agent undefined".
  it('never indexes past the end of the pool', () => {
    for (let i = 0; i < 500; i++)
      expect(randomAgentTitle()).not.toContain('undefined')
  })
})

describe('randomTerminalTitle', () => {
  it('draws a name from the shared pool', () => {
    for (let i = 0; i < 50; i++) {
      const [prefix, ...rest] = randomTerminalTitle().split(' ')
      expect(prefix).toBe(TERMINAL_TITLE_PREFIX)
      expect(rest).toHaveLength(1)
      expect(TAB_NAMES).toContain(rest[0])
    }
  })

  // A terminal title must not read as an auto-generated AGENT title, or the
  // worker's plan-mode rename would treat a terminal's name as overwritable.
  it('does not match the agent auto-title shape', () => {
    expect(randomTerminalTitle()).not.toMatch(AGENT_AUTO_TITLE)
  })
})

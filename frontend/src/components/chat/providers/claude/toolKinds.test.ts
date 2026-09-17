import { describe, expect, it } from 'vitest'
import { claudeToolIcon, claudeToolKind } from './toolKinds'
import { CLAUDE_TOOL_NAMES } from './toolNames'

describe('claudeToolIcon', () => {
  // The common case, and the reason the override table is short: a tool whose
  // kind already says what it does draws the kind's glyph, so a Bash call on
  // Claude and a shell call anywhere else look the same.
  it('states no hint for a tool its kind describes', () => {
    expect(claudeToolIcon(CLAUDE_TOOL_NAMES.READ)).toBeUndefined()
    expect(claudeToolIcon(CLAUDE_TOOL_NAMES.BASH)).toBeUndefined()
  })

  it('states no hint for a tool it does not know', () => {
    expect(claudeToolIcon('SomeToolNobodyShipped')).toBeUndefined()
  })

  it('overrides the kind where the tool is narrower than it', () => {
    expect(claudeToolIcon(CLAUDE_TOOL_NAMES.TASK_GET)).toBe('checklist')
    expect(claudeToolIcon(CLAUDE_TOOL_NAMES.TASK_STOP)).toBe('stop')
    expect(claudeToolIcon(CLAUDE_TOOL_NAMES.REMOTE_TRIGGER)).toBe('webhook')
    expect(claudeToolIcon(CLAUDE_TOOL_NAMES.STRUCTURED_OUTPUT)).toBe('json')
  })

  // Entering and leaving plan mode are opposite moves, and the pair of glyphs
  // is how a reader tells one row from the other at a glance.
  it('draws entering and leaving plan mode apart', () => {
    expect(claudeToolIcon(CLAUDE_TOOL_NAMES.ENTER_PLAN_MODE)).toBe('plan-enter')
    expect(claudeToolIcon(CLAUDE_TOOL_NAMES.EXIT_PLAN_MODE)).toBe('plan-exit')
  })

  // Both worktree moves are the same move, so they share one hint: the row's
  // own words state the direction.
  it('draws both worktree moves as a branch', () => {
    expect(claudeToolIcon(CLAUDE_TOOL_NAMES.ENTER_WORKTREE)).toBe('branch')
    expect(claudeToolIcon(CLAUDE_TOOL_NAMES.EXIT_WORKTREE)).toBe('branch')
  })

  // A hint exists for a tool whose KIND says something wider. A tool that
  // overrides the glyph of a kind that already fits it is a mistake in the
  // table, so the two `Task*` overrides must not have taken a `task` kind.
  it('overrides only where the kind says something else', () => {
    expect(claudeToolKind(CLAUDE_TOOL_NAMES.TASK_STOP)).not.toBe('')
    expect(claudeToolIcon(CLAUDE_TOOL_NAMES.TASK_STOP)).toBe('stop')
  })
})

import { describe, expect, it } from 'vitest'
import { OPENCODE_CHILD, openCodeChildReportRule } from './childScenario'

describe('OPENCODE_CHILD', () => {
  it('matches the native bootstrap and excludes a parent that quotes the task', () => {
    const task = 'Read the assigned file.'
    const pattern = OPENCODE_CHILD.childTask(task).user
    if (typeof pattern !== 'string')
      throw new TypeError('The native child matcher requires its user pattern.')
    const matches = new RegExp(pattern)
    expect(matches.test(`You are a subagent spawned by another session.\n${task}\n\nLEAPMUXE2ESCENARIO:test`)).toBe(true)
    expect(matches.test(`Spawn a child for ${task}`)).toBe(false)
  })
})

describe('openCodeChildReportRule', () => {
  it('handles only the native completion report of its own description', () => {
    const rule = openCodeChildReportRule('Read [sample]')
    if (typeof rule.when.user !== 'string')
      throw new TypeError('The native report rule requires its user pattern.')
    const matches = new RegExp(rule.when.user)
    expect(matches.test('<subagent sessionID="ses_child" state="completed" description="Read [sample]">\nPONG\n</subagent>')).toBe(true)
    expect(matches.test('<subagent sessionID="ses_child" state="cancelled" description="Read [sample]">\nSubagent cancelled\n</subagent>')).toBe(true)
    expect(matches.test('<subagent sessionID="ses_child" state="completed" description="Other child">\nPONG\n</subagent>')).toBe(false)
    expect(matches.test('Ask a child to Read [sample]')).toBe(false)
  })
})

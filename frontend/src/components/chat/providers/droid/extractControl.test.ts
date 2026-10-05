import { describe, expect, it } from 'vitest'
import { droidExtractControl } from './extractControl'

describe('droidExtractControl', () => {
  it('draws ExitSpecMode as a plan approval with the plan it carries', () => {
    expect(droidExtractControl({ payload: {
      type: 'permission_request',
      toolUse: { type: 'tool_use', id: 'exit-1', name: 'ExitSpecMode', input: { plan: '# Native plan' } },
      confirmationType: 'exit_spec_mode',
      details: { type: 'exit_spec_mode', plan: '# Native plan' },
    } })).toEqual({ kind: 'plan', text: '# Native plan' })
  })

  // Droid groups a mission proposal with the spec-mode exit by the confirmation
  // type, and the proposal rides `details.proposal` where the plan rides
  // `details.plan`.
  it('draws a mission proposal as a plan approval with the proposal', () => {
    expect(droidExtractControl({ payload: {
      type: 'permission_request',
      toolUse: { type: 'tool_use', id: 'mission-1', name: 'ProposeMission', input: {} },
      confirmationType: 'propose_mission',
      details: { type: 'propose_mission', proposal: '# Mission\n\n- Ship the feature.', title: 'Feature' },
    } })).toEqual({ kind: 'plan', text: '# Mission\n\n- Ship the feature.' })
  })

  // A spec review that carries no plan text keeps the plain approval card.
  it('draws a review with no document as a bare plan approval', () => {
    expect(droidExtractControl({ payload: {
      type: 'permission_request',
      toolUse: { type: 'tool_use', id: 'exit-2', name: 'ExitSpecMode', input: {} },
      confirmationType: 'exit_spec_mode',
      details: { type: 'exit_spec_mode', plan: '  ' },
    } })).toEqual({ kind: 'plan' })
  })

  // A Droid Shield refusal is a permission for an Execute call whose `details`
  // state why the call was blocked.
  it('states the reason of a Droid Shield refusal', () => {
    expect(droidExtractControl({ payload: {
      type: 'permission_request',
      toolUse: { type: 'tool_use', id: 'shield-1', name: 'Execute', input: { command: 'git push' } },
      confirmationType: 'droid_shield_violation',
      details: { type: 'droid_shield_violation', command: 'git push', reason: 'the diff may hold a secret' },
    } })).toEqual({ kind: 'permission', permission: {
      title: 'Execute',
      input: { command: 'git push' },
      command: 'git push',
      reason: 'the diff may hold a secret',
      options: [],
    } })
  })

  it('keeps an ordinary Edit as a permission', () => {
    expect(droidExtractControl({ payload: {
      type: 'permission_request',
      toolUse: { name: 'Edit', input: { file_path: '/work/note.txt' } },
    } })).toMatchObject({ kind: 'permission', permission: { title: 'Edit' } })
  })

  it('ignores a payload outside the permission channel', () => {
    expect(droidExtractControl({ payload: { type: 'settings_updated' } })).toBeNull()
  })
})

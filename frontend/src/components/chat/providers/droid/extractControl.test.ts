import { describe, expect, it } from 'vitest'
import { droidExtractControl } from './extractControl'

describe('droidExtractControl', () => {
  it('draws ExitSpecMode as a plan approval', () => {
    expect(droidExtractControl({ payload: {
      type: 'permission_request',
      toolUse: { type: 'tool_use', id: 'exit-1', name: 'ExitSpecMode', input: { plan: '# Native plan' } },
      confirmationType: 'exit_spec_mode',
      details: { type: 'exit_spec_mode', plan: '# Native plan' },
    } })).toEqual({ kind: 'plan' })
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

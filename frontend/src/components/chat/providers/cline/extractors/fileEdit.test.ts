import { describe, expect, it } from 'vitest'
import { clineEditRequest, clineEditResult } from './fileEdit'

const PATCH = '*** Begin Patch\n*** Update File: /w/a.ts\n@@\n-old\n+new\n*** End Patch'

describe('clineEditRequest', () => {
  it('reads a replacement of the editor as the two sides of its file', () => {
    expect(clineEditRequest('editor', { path: '/w/a.ts', old_text: 'old', new_text: 'new' }).changes).toEqual([
      { filePath: '/w/a.ts', structuredPatch: null, oldStr: 'old', newStr: 'new' },
    ])
  })

  // An insertion lands between two lines, so an `old_text` beside it is no before side.
  it('reads an insertion as a change with no before side', () => {
    for (const insertLine of [0, 3])
      expect(clineEditRequest('editor', { path: '/w/a.ts', old_text: 'ignored', new_text: 'new', insert_line: insertLine }).changes[0], String(insertLine)).toMatchObject({ oldStr: '', newStr: 'new' })
  })

  it('reads the files of a patch', () => {
    expect(clineEditRequest('apply_patch', { input: PATCH }).changes.map(change => [change.filePath, change.operation])).toEqual([['/w/a.ts', 'edit']])
  })

  it('states no change for a call that states no file, or a patch it cannot follow', () => {
    expect(clineEditRequest('editor', { old_text: 'a', new_text: 'b' })).toEqual({ changes: [] })
    expect(clineEditRequest('apply_patch', { input: 'not a patch' })).toEqual({ changes: [] })
    expect(clineEditRequest('apply_patch', {})).toEqual({ changes: [] })
  })
})

describe('clineEditResult', () => {
  const request = clineEditRequest('editor', { path: '/w/a.ts', old_text: 'old', new_text: 'new' })

  it('draws the change the call asked for once Cline confirms it', () => {
    expect(clineEditResult(request, { query: 'edit:/w/a.ts', result: 'Edited', success: true })).toEqual({ changes: request.changes })
    // Only `success: false` states a failure, so a record with no flag is a success.
    expect(clineEditResult(request, [{ query: 'edit:/w/a.ts', result: 'Edited' }])).toEqual({ changes: request.changes })
  })

  it('confirms nothing when any operation failed', () => {
    expect(clineEditResult(request, [
      { query: 'edit:/w/a.ts', result: 'Edited', success: true },
      { query: 'edit:/w/b.ts', result: '', error: 'No match', success: false },
    ])).toBeNull()
  })

  it('confirms nothing for a result with no record, or a request with no change', () => {
    expect(clineEditResult(request, 'Edited /w/a.ts')).toBeNull()
    expect(clineEditResult(request, [])).toBeNull()
    expect(clineEditResult({ changes: [] }, { query: 'edit', result: 'Edited', success: true })).toBeNull()
  })
})

import { describe, expect, it } from 'vitest'
import { geminiCommittedFileChange } from './fileChange'

describe('geminiCommittedFileChange', () => {
  it('preserves empty files and the explicit native operation', () => {
    expect(geminiCommittedFileChange({ resultDisplay: { filePath: '/native/empty.txt', originalContent: '', newContent: '', isNewFile: false } })).toMatchObject({ changes: [{ filePath: '/native/empty.txt', operation: 'edit', oldStr: '', newStr: '' }] })
    expect(geminiCommittedFileChange({ resultDisplay: { filePath: '/native/new.txt', originalContent: '', newContent: 'native\n', isNewFile: true } })).toMatchObject({ changes: [{ operation: 'add', oldStr: '', newStr: 'native\n' }] })
  })

  it('rejects missing malformed and non-string committed bytes', () => {
    for (const resultDisplay of [undefined, null, [], '', {}, { filePath: '', originalContent: '', newContent: 'native' }, { filePath: '/native/file', originalContent: null, newContent: 'native' }, { filePath: '/native/file', originalContent: 'old', newContent: 0 }])
      expect(geminiCommittedFileChange({ resultDisplay })).toBeNull()
  })

  it('preserves very large native committed content without a requested-byte fallback', () => {
    const before = 'old native line\n'.repeat(30_000)
    const after = 'new native line\n'.repeat(30_000)
    expect(geminiCommittedFileChange({ args: { old_string: 'requested old', new_string: 'requested new' }, resultDisplay: { filePath: '/native/large.txt', originalContent: before, newContent: after, isNewFile: false } })).toMatchObject({ changes: [{ oldStr: before, newStr: after, operation: 'edit' }] })
  })
})

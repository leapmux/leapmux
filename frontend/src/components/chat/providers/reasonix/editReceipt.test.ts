import { describe, expect, it } from 'vitest'
import { reasonixEditReceipt } from './editReceipt'

const marker = '…[replacement span truncated]…'
const receipt = (body: string, summary = 'edited /project/file.ts') => `${summary}\nActual replacement receipt after write:\n${body}`
const header = '@@ replacement 1 of 1 (1 occurrence(s)) @@\n'

describe('reasonix replacement receipts', () => {
  it('uses the actual matched text for a fuzzy replacement', () => {
    const sources = reasonixEditReceipt(receipt('@@ replacement 1 of 1 (1 occurrence(s), fuzzy match) @@\n-actual\n+after\n'), '/project/file.ts')
    expect(sources?.[0]).toMatchObject({ oldStr: 'actual', newStr: 'after', showLineNumbers: false, notice: 'Fuzzy match' })
  })

  it('preserves the replacement count and the sample limit', () => {
    const sources = reasonixEditReceipt(receipt('@@ replacement 1 of 1 (3 occurrence(s), fuzzy match, first matched sample shown) @@\n-actual\n+after\n'), '/project/file.ts')
    expect(sources?.[0]).toMatchObject({ notice: 'Fuzzy match; 3 replacements; first matched sample shown' })
  })

  it('keeps a truncation marker out of an applied diff', () => {
    expect(reasonixEditReceipt(receipt(`${header}-first\n-${marker}\n-last\n+after\n`), '/project/file.ts')).toEqual([])
  })

  it('recovers a truncated exact replacement from the matching request', () => {
    const sources = reasonixEditReceipt(receipt(`${header}-first\n-${marker}\n-last\n+after\n`), '/project/file.ts', { old_string: 'first\nrecovered middle\nlast', new_string: 'after' })
    expect(sources?.[0]).toMatchObject({ oldStr: 'first\nrecovered middle\nlast', newStr: 'after' })
  })

  it('does not substitute requested text for a truncated fuzzy match', () => {
    expect(reasonixEditReceipt(receipt(`@@ replacement 1 of 1 (1 occurrence(s), fuzzy match) @@\n-first\n-${marker}\n-last\n+after\n`), '/project/file.ts', { old_string: 'first\nunknown actual text\nlast', new_string: 'after' })).toEqual([])
  })

  it.each(['', '<empty>', '<empty>\n'])('resolves an empty marker from the replacement request: %j', (replacement) => {
    const sources = reasonixEditReceipt(receipt(`${header}-<empty>\n+<empty>\n`), '/project/file.ts', { old_string: '<empty>', new_string: replacement })
    expect(sources?.[0]).toMatchObject({ oldStr: '<empty>', newStr: replacement })
  })

  it('does not interpret an ambiguous empty marker without the request', () => {
    expect(reasonixEditReceipt(receipt(`${header}-before\n+<empty>\n`), '/project/file.ts')).toEqual([])
  })

  it('recovers omitted exact replacements from the complete batch request', () => {
    const sources = reasonixEditReceipt(receipt('@@ replacement 1 of 3 (1 occurrence(s)) @@\n-firstBefore\n+firstAfter\n\n…[1 intermediate replacement receipt(s) omitted]…\n\n@@ replacement 3 of 3 (1 occurrence(s)) @@\n-thirdBefore\n+thirdAfter\n', 'multi_edit /project/file.ts: 3 edits applied (3 total replacements)'), '/project/file.ts', {
      edits: [
        { old_string: 'firstBefore', new_string: 'firstAfter' },
        { old_string: 'secondBefore', new_string: 'secondAfter' },
        { old_string: 'thirdBefore', new_string: 'thirdAfter' },
      ],
    })
    expect(sources?.map(source => source.newStr)).toEqual(['firstAfter', 'secondAfter', 'thirdAfter'])
  })

  it.each([
    `${header}-before\n`,
    '@@ replacement 0 of 1 (1 occurrence(s)) @@\n-before\n+after\n',
    '@@ replacement 1 of 2 (1 occurrence(s)) @@\n-before\n+after\n',
    `${header}-before\n+after\n${header}-before\n+after\n`,
  ])('keeps incomplete or invalid receipts as raw output: %j', (body) => {
    expect(reasonixEditReceipt(receipt(body), '/project/file.ts')).toEqual([])
  })

  it('distinguishes a missing receipt from an unusable receipt', () => {
    expect(reasonixEditReceipt('Saved', '/project/file.ts')).toBeNull()
  })
})

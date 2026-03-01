import type { StructuredPatchHunk } from './diffUtils'
import { render } from '@solidjs/testing-library'
import { diffWordsWithSpace } from 'diff'
import { describe, expect, it } from 'vitest'
import { computeGapMap, DiffView, groupByHunk, rawDiffToHunks } from './diffUtils'

/**
 * Reconstruct one side of a word-diff using the same filtering logic
 * as renderTokenizedWordDiff: filter parts, then concatenate values.
 *
 * This mirrors what renderRemovedInline / renderAddedInline do when
 * there are no Shiki tokens (the plain-text path).
 */
function reconstructSide(
  oldLine: string,
  newLine: string,
  side: 'old' | 'new',
): string {
  const parts = diffWordsWithSpace(oldLine, newLine)
  const filterFn = side === 'old'
    ? (p: { added?: boolean }) => !p.added
    : (p: { removed?: boolean }) => !p.removed
  return parts.filter(filterFn).map(p => p.value).join('')
}

describe('diffWordsWithSpace preserves whitespace on both sides', () => {
  const cases: Array<{ name: string, oldLine: string, newLine: string }> = [
    { name: 'indent decrease', oldLine: '        return value;', newLine: '    return newValue;' },
    { name: 'indent increase', oldLine: '    const x = 1;', newLine: '        const y = 2;' },
    { name: 'old indented, new not', oldLine: '    return value', newLine: 'return newValue' },
    { name: 'old not indented, new indented', oldLine: 'return value', newLine: '    return newValue' },
    { name: 'tabs vs spaces', oldLine: '\t\tconst x = 1;', newLine: '    const y = 2;' },
    { name: 'same indentation', oldLine: '    const x = 1;', newLine: '    const y = 2;' },
  ]

  for (const { name, oldLine, newLine } of cases) {
    it(`old side: ${name}`, () => {
      expect(reconstructSide(oldLine, newLine, 'old')).toBe(oldLine)
    })
    it(`new side: ${name}`, () => {
      expect(reconstructSide(oldLine, newLine, 'new')).toBe(newLine)
    })
  }
})

describe('diffView rendering preserves whitespace', () => {
  /**
   * Render DiffView with crafted hunks containing paired removed+added
   * lines with different indentation, then verify the rendered text
   * content preserves whitespace faithfully.
   */

  function extractDiffTexts(container: HTMLElement, view: 'unified' | 'split'): { removedText: string, addedText: string } {
    let removedText = ''
    let addedText = ''
    const allDivs = container.querySelectorAll('div')

    for (const el of allDivs) {
      // In unified: spans are [lineNumOld, lineNumNew, prefix, content]
      // In split:   spans are [lineNum, prefix, content]
      const spans = el.querySelectorAll(':scope > span')
      if (spans.length < 3)
        continue

      const prefixIdx = view === 'unified' ? 2 : 1
      const contentIdx = view === 'unified' ? 3 : 2
      const prefix = spans[prefixIdx]?.textContent
      const content = spans[contentIdx]?.textContent ?? ''

      if (prefix === '-')
        removedText = content
      else if (prefix === '+')
        addedText = content
    }

    return { removedText, addedText }
  }

  it('unified view: preserves indentation when indent decreases', () => {
    const hunks = [{
      oldStart: 1,
      oldLines: 1,
      newStart: 1,
      newLines: 1,
      lines: ['-        return value;', '+    return newValue;'],
    }]

    const { container } = render(() => (
      <DiffView hunks={hunks} view="unified" />
    ))

    const { removedText, addedText } = extractDiffTexts(container, 'unified')
    expect(removedText).toBe('        return value;')
    expect(addedText).toBe('    return newValue;')
  })

  it('unified view: preserves indentation when indent increases', () => {
    const hunks = [{
      oldStart: 1,
      oldLines: 1,
      newStart: 1,
      newLines: 1,
      lines: ['-    const x = 1;', '+        const y = 2;'],
    }]

    const { container } = render(() => (
      <DiffView hunks={hunks} view="unified" />
    ))

    const { removedText, addedText } = extractDiffTexts(container, 'unified')
    expect(removedText).toBe('    const x = 1;')
    expect(addedText).toBe('        const y = 2;')
  })

  it('unified view: preserves indentation when old is indented and new is not', () => {
    const hunks = [{
      oldStart: 1,
      oldLines: 1,
      newStart: 1,
      newLines: 1,
      lines: ['-    return value', '+return newValue'],
    }]

    const { container } = render(() => (
      <DiffView hunks={hunks} view="unified" />
    ))

    const { removedText, addedText } = extractDiffTexts(container, 'unified')
    expect(removedText).toBe('    return value')
    expect(addedText).toBe('return newValue')
  })

  it('split view: preserves indentation when indent decreases', () => {
    const hunks = [{
      oldStart: 1,
      oldLines: 1,
      newStart: 1,
      newLines: 1,
      lines: ['-        return value;', '+    return newValue;'],
    }]

    const { container } = render(() => (
      <DiffView hunks={hunks} view="split" />
    ))

    const { removedText, addedText } = extractDiffTexts(container, 'split')
    expect(removedText).toBe('        return value;')
    expect(addedText).toBe('    return newValue;')
  })
})

describe('rawDiffToHunks', () => {
  it('produces correct hunk lines with prefixes', () => {
    const hunks = rawDiffToHunks(
      '    const x = 1;\n',
      '    const y = 2;\n',
    )
    expect(hunks).toHaveLength(1)
    expect(hunks[0].lines).toContain('-    const x = 1;')
    expect(hunks[0].lines).toContain('+    const y = 2;')
  })

  it('preserves leading whitespace in removed lines', () => {
    const hunks = rawDiffToHunks(
      '        return value;\n',
      '    return newValue;\n',
    )
    expect(hunks).toHaveLength(1)
    const removedLine = hunks[0].lines.find(l => l.startsWith('-'))
    expect(removedLine).toBe('-        return value;')
    const addedLine = hunks[0].lines.find(l => l.startsWith('+'))
    expect(addedLine).toBe('+    return newValue;')
  })
})

describe('computeGapMap', () => {
  const originalFileLines = [
    'line 1', // line 1
    'line 2', // line 2
    'line 3', // line 3
    'line 4', // line 4
    'line 5', // line 5
    'line 6', // line 6
    'line 7', // line 7
    'line 8', // line 8
    'line 9', // line 9
    'line 10', // line 10
  ]

  it('computes leading gap before first hunk', () => {
    const hunks: StructuredPatchHunk[] = [
      { oldStart: 4, oldLines: 2, newStart: 4, newLines: 2, lines: [' line 4', '-line 5', '+line 5 modified'] },
    ]
    const { gaps, trailing } = computeGapMap(hunks, originalFileLines)
    expect(gaps.size).toBe(1)
    const leadingGap = gaps.get(0)!
    expect(leadingGap.startLineNumber).toBe(1)
    expect(leadingGap.lines).toEqual(['line 1', 'line 2', 'line 3'])
    // Trailing gap: lines 6..10
    expect(trailing).not.toBeNull()
    expect(trailing!.startLineNumber).toBe(6)
    expect(trailing!.lines).toEqual(['line 6', 'line 7', 'line 8', 'line 9', 'line 10'])
  })

  it('computes inter-hunk gap', () => {
    const hunks: StructuredPatchHunk[] = [
      { oldStart: 1, oldLines: 2, newStart: 1, newLines: 2, lines: ['-line 1', '+line 1 mod', ' line 2'] },
      { oldStart: 7, oldLines: 2, newStart: 7, newLines: 2, lines: [' line 7', '-line 8', '+line 8 mod'] },
    ]
    const { gaps, trailing } = computeGapMap(hunks, originalFileLines)
    // No leading gap (hunk starts at line 1)
    expect(gaps.has(0)).toBe(false)
    // Inter-hunk gap: lines 3..6
    const interGap = gaps.get(1)!
    expect(interGap.startLineNumber).toBe(3)
    expect(interGap.lines).toEqual(['line 3', 'line 4', 'line 5', 'line 6'])
    // Trailing gap: lines 9..10
    expect(trailing).not.toBeNull()
    expect(trailing!.startLineNumber).toBe(9)
    expect(trailing!.lines).toEqual(['line 9', 'line 10'])
  })

  it('returns no trailing gap when last hunk reaches end of file', () => {
    const hunks: StructuredPatchHunk[] = [
      { oldStart: 9, oldLines: 2, newStart: 9, newLines: 2, lines: [' line 9', '-line 10', '+line 10 mod'] },
    ]
    const { trailing } = computeGapMap(hunks, originalFileLines)
    expect(trailing).toBeNull()
  })

  it('returns empty gaps for empty hunks array', () => {
    const { gaps, trailing } = computeGapMap([], originalFileLines)
    expect(gaps.size).toBe(0)
    expect(trailing).toBeNull()
  })

  it('handles single hunk spanning entire file', () => {
    const hunks: StructuredPatchHunk[] = [
      { oldStart: 1, oldLines: 10, newStart: 1, newLines: 10, lines: originalFileLines.map(l => ` ${l}`) },
    ]
    const { gaps, trailing } = computeGapMap(hunks, originalFileLines)
    expect(gaps.size).toBe(0)
    expect(trailing).toBeNull()
  })
})

describe('groupByHunk', () => {
  it('groups entries by hunkIndex', () => {
    const entries = [
      { hunkIndex: 0, value: 'a' },
      { hunkIndex: 0, value: 'b' },
      { hunkIndex: 1, value: 'c' },
      { hunkIndex: 1, value: 'd' },
      { hunkIndex: 1, value: 'e' },
      { hunkIndex: 2, value: 'f' },
    ]
    const groups = groupByHunk(entries)
    expect(groups).toHaveLength(3)
    expect(groups[0]).toEqual([{ hunkIndex: 0, value: 'a' }, { hunkIndex: 0, value: 'b' }])
    expect(groups[1]).toEqual([{ hunkIndex: 1, value: 'c' }, { hunkIndex: 1, value: 'd' }, { hunkIndex: 1, value: 'e' }])
    expect(groups[2]).toEqual([{ hunkIndex: 2, value: 'f' }])
  })

  it('returns empty array for empty input', () => {
    const groups = groupByHunk([])
    expect(groups).toEqual([])
  })

  it('handles single group', () => {
    const entries = [
      { hunkIndex: 0, value: 'a' },
      { hunkIndex: 0, value: 'b' },
    ]
    const groups = groupByHunk(entries)
    expect(groups).toHaveLength(1)
    expect(groups[0]).toHaveLength(2)
  })
})

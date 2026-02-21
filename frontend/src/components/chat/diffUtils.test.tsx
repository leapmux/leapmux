import { render } from '@solidjs/testing-library'
import { diffWordsWithSpace } from 'diff'
import { describe, expect, it } from 'vitest'
import { DiffView, rawDiffToHunks } from './diffUtils'

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

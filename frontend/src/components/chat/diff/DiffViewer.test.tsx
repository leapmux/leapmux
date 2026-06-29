import type { StructuredPatchHunk } from '.'
import type { RenderContext } from '../messageRenderers'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { diffWordsWithSpace } from 'diff'
import { createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { setCachedTokens } from '~/lib/tokenCache'
import { computeGapMap, computeSyntheticGapMap, DiffView, groupByHunk, rawDiffToHunks } from '.'
import { buildUnifiedLines } from './diffTokenRender'

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

  it('falls back to plain gap context when worker tokenization is unavailable', async () => {
    const originalWorkerDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'Worker')
    Object.defineProperty(globalThis, 'Worker', {
      configurable: true,
      writable: true,
      value: undefined,
    })
    try {
      const originalFile = Array.from({ length: 12 }, (_, i) => `line ${i + 1}`).join('\n')
      render(() => (
        <DiffView
          filePath="example.ts"
          originalFile={originalFile}
          hunks={[{
            oldStart: 10,
            oldLines: 1,
            newStart: 10,
            newLines: 1,
            lines: [' line 10'],
          }]}
          view="unified"
        />
      ))

      fireEvent.click(screen.getByText('9 lines hidden'))
      await Promise.resolve()

      expect(screen.getByText('line 1')).toBeInTheDocument()
      expect(screen.getByText('line 9')).toBeInTheDocument()
    }
    finally {
      if (originalWorkerDescriptor)
        Object.defineProperty(globalThis, 'Worker', originalWorkerDescriptor)
      else
        Reflect.deleteProperty(globalThis, 'Worker')
    }
  })

  it('invalidates cached revealed gap tokens when original gap text changes', async () => {
    const hunk = {
      oldStart: 10,
      oldLines: 1,
      newStart: 10,
      newLines: 1,
      lines: [' line 10'],
    }
    const oldLines = Array.from({ length: 10 }, (_, i) => i === 9 ? 'line 10' : `old line ${i + 1}`)
    const newLines = Array.from({ length: 10 }, (_, i) => i === 9 ? 'line 10' : `new line ${i + 1}`)
    const oldGapCode = oldLines.slice(0, 9).join('\n')
    const newGapCode = newLines.slice(0, 9).join('\n')
    setCachedTokens(
      'typescript',
      oldGapCode,
      oldLines.slice(0, 9).map(line => [{ content: `old-token:${line}`, htmlStyle: {} }]),
    )
    setCachedTokens(
      'typescript',
      newGapCode,
      newLines.slice(0, 9).map(line => [{ content: `new-token:${line}`, htmlStyle: {} }]),
    )
    const [originalFile, setOriginalFile] = createSignal(oldLines.join('\n'))

    render(() => (
      <DiffView
        filePath="example.ts"
        originalFile={originalFile()}
        hunks={[hunk]}
        view="unified"
      />
    ))

    fireEvent.click(screen.getByText('9 lines hidden'))
    await waitFor(() => {
      expect(screen.getByText('old-token:old line 1')).toBeInTheDocument()
    })

    setOriginalFile(newLines.join('\n'))

    await waitFor(() => {
      expect(screen.getByText('new-token:new line 1')).toBeInTheDocument()
    })
    expect(screen.queryByText('old-token:old line 1')).not.toBeInTheDocument()
  })

  it('maps tokens to the correct lines when both the top and bottom of a gap are revealed', async () => {
    // Reveal a NON-contiguous slice (top 10 + bottom 10) and confirm each revealed line
    // gets ITS token. The migrated gap tokenizes the joined revealed slice through the
    // shared useAsyncCodeTokens hook and indexes tokens by POSITION (top slice first, then
    // bottom slice), so this guards the top/bottom index mapping -- the old incremental
    // path keyed a per-line map by gap index, the new one maps position -> gap line, and
    // only expand-all (top-only) was covered before.
    const gapLines = Array.from({ length: 25 }, (_, i) => `line ${i + 1}`)
    // Hunk at line 26 -> lines 1..25 form one leading gap (25 hidden, > GAP_EXPAND_STEP).
    const hunk = { oldStart: 26, oldLines: 1, newStart: 26, newLines: 1, lines: [' line 26'] }
    const originalFile = [...gapLines, 'line 26'].join('\n')

    // "Expand down" reveals the top 10 (lines 1..10); "Expand up" reveals the bottom 10
    // (lines 16..25). The hook then tokenizes exactly this joined slice -- seed the cache
    // so it resolves synchronously (no Worker in jsdom).
    const revealed = [...gapLines.slice(0, 10), ...gapLines.slice(15, 25)]
    setCachedTokens('typescript', revealed.join('\n'), revealed.map(line => [{ content: `tok:${line}`, htmlStyle: {} }]))

    render(() => <DiffView filePath="example.ts" originalFile={originalFile} hunks={[hunk]} view="unified" />)

    fireEvent.click(screen.getByText('Expand down')) // top 10
    fireEvent.click(screen.getByText('Expand up')) // bottom 10

    await waitFor(() => {
      expect(screen.getByText('tok:line 1')).toBeInTheDocument() // first top line
      expect(screen.getByText('tok:line 10')).toBeInTheDocument() // last top line
      expect(screen.getByText('tok:line 16')).toBeInTheDocument() // first bottom line
      expect(screen.getByText('tok:line 25')).toBeInTheDocument() // last bottom line
    })
    // The still-hidden middle (lines 11..15) is never revealed, so it has no token span.
    expect(screen.queryByText('tok:line 13')).not.toBeInTheDocument()
  })
})

describe('diffView old/new-side syntax highlighting (useAsyncCodeTokens migration)', () => {
  // A context line is tokenized from the OLD side; a bare added line from the NEW side.
  // Pre-seed the shared token cache for both sides so the migrated useDiffTokens hook
  // takes its synchronous cache-hit path (no Worker needed), proving both sides flow
  // through the shared hook into buildUnifiedLines.
  const hunks: StructuredPatchHunk[] = [{
    oldStart: 1,
    oldLines: 1,
    newStart: 1,
    newLines: 2,
    lines: [' ctx', '+added'],
  }]

  function seedSides(): void {
    // extractSidesFromHunks: oldCode = 'ctx'; newCode = 'ctx\nadded'.
    setCachedTokens('typescript', 'ctx', [[{ content: 'CTXTOK', htmlStyle: {} }]])
    setCachedTokens('typescript', 'ctx\nadded', [
      [{ content: 'CTXTOK2', htmlStyle: {} }],
      [{ content: 'ADDEDTOK', htmlStyle: {} }],
    ])
  }

  it('tokenizes both sides via the shared hook when a filePath resolves a language', async () => {
    seedSides()
    render(() => <DiffView filePath="example.ts" hunks={hunks} view="unified" />)

    // The context line carries the OLD-side token; the added line the NEW-side token.
    await waitFor(() => {
      expect(screen.getByText('CTXTOK')).toBeInTheDocument()
      expect(screen.getByText('ADDEDTOK')).toBeInTheDocument()
    })
  })

  it('applies seeded cache tokens on a fresh mount even while paused (no second-view flash)', () => {
    // Switching the diff view (unified<->split) mounts a fresh subtree, and the toggle's
    // pointerdown pauses syntax highlighting for a scroll-idle beat -- so the new view mounts
    // with syntaxHighlightingPaused=true. A fresh mount's first paint is not a disruptive
    // text-node swap, so the already-cached tokens must seed THROUGH the hold gate and paint
    // on the first frame rather than flashing plain until the pause lifts. (The hold gate
    // still defers tokens for an IN-PLACE content change on an already-mounted diff.)
    seedSides()
    const context = { syntaxHighlightingPaused: () => true } as unknown as RenderContext
    render(() => <DiffView filePath="example.ts" hunks={hunks} view="unified" context={context} />)

    // Present synchronously on the first render (the seed), not after a deferred effect.
    expect(screen.getByText('CTXTOK')).toBeInTheDocument()
    expect(screen.getByText('ADDEDTOK')).toBeInTheDocument()
  })

  it('renders plain when no filePath resolves a language (ineligible)', () => {
    seedSides()
    render(() => <DiffView hunks={hunks} view="unified" />)

    // No filePath -> lang undefined -> the hook never tokenizes; raw text renders.
    expect(screen.getByText('ctx')).toBeInTheDocument()
    expect(screen.queryByText('CTXTOK')).not.toBeInTheDocument()
  })
})

describe('diffView ansi (.log) highlighting', () => {
  // The ANSI escape byte, built via fromCharCode so the source stays plain ASCII.
  const ESC = String.fromCharCode(27)

  // A `.log` file resolves to the `ansi` language, which the Oniguruma token WORKER
  // cannot tokenize (ansi is a Shiki special, not a bundled grammar). The diff sides /
  // gap-context lines must fall back to the shared synchronous ansi tokenizer instead of
  // degrading to plain -- these guard against the regression where only the Read view
  // carried that fallback and `.log` diffs lost their colors.

  it('colors a `.log` diff context line via the synchronous ansi tokenizer (no worker)', () => {
    // One unchanged context line carrying an ANSI color escape; jsdom has no Worker, so
    // the only way this highlights is the syncTokenize ansi path wired into useDiffTokens.
    const { container } = render(() => (
      <DiffView
        filePath="out.log"
        view="unified"
        hunks={[{ oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: [` ${ESC}[32mgreen${ESC}[0m tail`] }]}
      />
    ))

    // The escape sequences are consumed into per-token colors -- the visible text is clean.
    // Target the inline-styled token span (its diffContent parent has the same textContent
    // but no style), so this asserts a real Shiki token, not the wrapper.
    const greenSpan = [...container.querySelectorAll('span[style]')].find(s => s.textContent === 'green')
    expect(greenSpan).toBeTruthy()
    expect(greenSpan!.getAttribute('style')).toContain('--shiki-light')
    // The raw escape byte never reaches the DOM (it would if the line rendered plain).
    expect(container.textContent).not.toContain(ESC)
  })

  it('colors a revealed `.log` gap-context line via the synchronous ansi tokenizer', () => {
    // Leading gap of ansi-colored lines; expanding it must tokenize the revealed slice
    // through the same syncTokenize path (the gap hook was the other diff surface missing it).
    const originalFile = [
      `${ESC}[31mred-one${ESC}[0m`,
      `${ESC}[32mgreen-two${ESC}[0m`,
      'line 3',
    ].join('\n')
    render(() => (
      <DiffView
        filePath="out.log"
        view="unified"
        originalFile={originalFile}
        hunks={[{ oldStart: 3, oldLines: 1, newStart: 3, newLines: 1, lines: [' line 3'] }]}
      />
    ))

    fireEvent.click(screen.getByText('2 lines hidden'))

    // Revealed gap lines render colored (escapes stripped into token colors). Target the
    // inline-styled token span rather than its unstyled diffContent parent.
    const redSpan = [...document.querySelectorAll('span[style]')].find(s => s.textContent === 'red-one')
    expect(redSpan).toBeTruthy()
    expect(redSpan!.getAttribute('style')).toContain('--shiki-light')
  })
})

describe('diffView shared gap scaffold (unified + split)', () => {
  // Both views render through one DiffGapScaffold; these exercise the shared reveal path
  // in the split view and the trailing-gap branch, which the unified leading-gap tests
  // above don't cover -- guarding the unified/split -> scaffold extraction.

  it('expands a leading gap in SPLIT view through the shared scaffold', async () => {
    const originalFile = Array.from({ length: 12 }, (_, i) => `line ${i + 1}`).join('\n')
    render(() => (
      <DiffView
        originalFile={originalFile}
        hunks={[{ oldStart: 10, oldLines: 1, newStart: 10, newLines: 1, lines: [' line 10'] }]}
        view="split"
      />
    ))

    // Leading gap: lines 1..9 hidden. Reveal them via the shared scaffold's expand-all.
    fireEvent.click(screen.getByText('9 lines hidden'))
    await Promise.resolve()

    // Split renders each gap context line in BOTH gutters (left + right), so each hidden
    // line appears exactly twice -- guards GapContextLine's split branch emitting both rows.
    expect(screen.getAllByText('line 1')).toHaveLength(2)
    expect(screen.getAllByText('line 9')).toHaveLength(2)
  })

  it('reveals a trailing gap in UNIFIED view through the shared scaffold', async () => {
    // Hunk touches line 2 -> a 1-line leading gap (line 1) and a 9-line trailing gap
    // (lines 3..11). The trailing branch lives only in the scaffold's real-gap path.
    const originalFile = Array.from({ length: 11 }, (_, i) => `line ${i + 1}`).join('\n')
    render(() => (
      <DiffView
        originalFile={originalFile}
        hunks={[{ oldStart: 2, oldLines: 1, newStart: 2, newLines: 1, lines: [' line 2'] }]}
        view="unified"
      />
    ))

    // "9 lines hidden" is the trailing gap ("1 line hidden" is the leading one).
    fireEvent.click(screen.getByText('9 lines hidden'))
    await Promise.resolve()

    expect(screen.getByText('line 3')).toBeInTheDocument() // first trailing line
    expect(screen.getByText('line 11')).toBeInTheDocument() // last trailing line
  })

  it('reveals a trailing gap in SPLIT view through the shared scaffold', async () => {
    const originalFile = Array.from({ length: 11 }, (_, i) => `line ${i + 1}`).join('\n')
    render(() => (
      <DiffView
        originalFile={originalFile}
        hunks={[{ oldStart: 2, oldLines: 1, newStart: 2, newLines: 1, lines: [' line 2'] }]}
        view="split"
      />
    ))

    fireEvent.click(screen.getByText('9 lines hidden'))
    await Promise.resolve()

    // Both gutters render each revealed line (split view), so each appears exactly twice.
    expect(screen.getAllByText('line 3')).toHaveLength(2)
    expect(screen.getAllByText('line 11')).toHaveLength(2)
  })
})

describe('buildUnifiedLines line ordering', () => {
  it('places bare added lines before trailing context within a hunk', () => {
    // Hunk: 3 leading context lines, 16 bare additions, 3 trailing context lines.
    // Mirrors the third hunk of a real Edit result (oldStart=152, newStart=153)
    // that previously rendered the additions AFTER the trailing context in
    // the unified view.
    const hunks: StructuredPatchHunk[] = [
      {
        oldStart: 152,
        oldLines: 6,
        newStart: 153,
        newLines: 20,
        lines: [
          '   )',
          ' }',
          ' ',
          '+/**',
          '+ * The `ToolUseLayout`-forwarded subset of `ToolHeaderActions` props. Layout-',
          '+ * owned fields (timestamp, expanded, hasDiff, json copy) come from context',
          '+ * or sibling `ToolUseLayout` props, so they\'re excluded here.',
          '+ */',
          '+export interface ToolHeaderActionsForwardedProps {',
          '+  onCopyContent?: () => void',
          '+  contentCopied?: boolean',
          '+  copyContentLabel?: string',
          '+  onReply?: () => void',
          '+  onCopyMarkdown?: () => void',
          '+  markdownCopied?: boolean',
          '+}',
          '+',
          ' /** Actions area in tool header: Reply + Raw JSON copy + diff toggle + expand/collapse, all with tooltips. */',
          ' export function ToolHeaderActions(props: {',
          '   /** ISO timestamp for relative time display. */',
        ],
      },
    ]

    const lines = buildUnifiedLines(hunks, null, null)

    // Sequence of (type, oldNum, newNum) with hunk lines in source order.
    expect(lines.map(l => [l.type, l.oldNum, l.newNum])).toEqual([
      ['context', 152, 153],
      ['context', 153, 154],
      ['context', 154, 155],
      ['added', null, 156],
      ['added', null, 157],
      ['added', null, 158],
      ['added', null, 159],
      ['added', null, 160],
      ['added', null, 161],
      ['added', null, 162],
      ['added', null, 163],
      ['added', null, 164],
      ['added', null, 165],
      ['added', null, 166],
      ['added', null, 167],
      ['added', null, 168],
      ['added', null, 169],
      ['context', 155, 170],
      ['context', 156, 171],
      ['context', 157, 172],
    ])
  })

  it('keeps bare added before bare removed when both abut without context', () => {
    // Order in source: + then -. The unified output should preserve that
    // order; only within a `-` block do removals come before additions.
    const hunks: StructuredPatchHunk[] = [
      {
        oldStart: 1,
        oldLines: 2,
        newStart: 1,
        newLines: 2,
        lines: [
          ' a',
          '+x',
          '-b',
          '+y',
          ' c',
        ],
      },
    ]

    const lines = buildUnifiedLines(hunks, null, null)

    expect(lines.map(l => [l.type, l.prefix])).toEqual([
      ['context', ' '],
      ['added', '+'],
      ['removed', '-'],
      ['added', '+'],
      ['context', ' '],
    ])
  })

  it('emits all removals before all additions within a single -/+ block', () => {
    // The "all - then all +" reordering applies within a single block of
    // *consecutive* removals immediately followed by *consecutive* additions.
    const hunks: StructuredPatchHunk[] = [
      {
        oldStart: 1,
        oldLines: 3,
        newStart: 1,
        newLines: 3,
        lines: [
          '-a1',
          '-a2',
          '-a3',
          '+b1',
          '+b2',
          '+b3',
        ],
      },
    ]

    const lines = buildUnifiedLines(hunks, null, null)

    expect(lines.map(l => l.type)).toEqual([
      'removed',
      'removed',
      'removed',
      'added',
      'added',
      'added',
    ])
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

describe('computeSyntheticGapMap', () => {
  it('computes only inter-hunk gaps without original file content', () => {
    const hunks: StructuredPatchHunk[] = [
      { oldStart: 4, oldLines: 2, newStart: 4, newLines: 2, lines: [' line 4', '-line 5', '+line 5 mod'] },
      { oldStart: 9, oldLines: 1, newStart: 9, newLines: 1, lines: ['-line 9', '+line 9 mod'] },
    ]

    const gaps = computeSyntheticGapMap(hunks)

    expect(gaps.get(1)).toEqual({ startLineNumber: 6, lineCount: 3 })
    expect(gaps.size).toBe(1)
  })

  it('does not infer outer gaps without original file length', () => {
    const hunks: StructuredPatchHunk[] = [
      { oldStart: 2, oldLines: 1, newStart: 2, newLines: 1, lines: ['-line 2', '+line 2 mod'] },
    ]

    const gaps = computeSyntheticGapMap(hunks)

    expect(gaps.size).toBe(0)
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

const EXPAND_RE = /expand/i
const COLLAPSE_RE = /collapse/i

describe('diffView gap rendering without original file', () => {
  const hunks: StructuredPatchHunk[] = [
    { oldStart: 4, oldLines: 2, newStart: 4, newLines: 2, lines: [' line 4', '-line 5', '+line 5 mod'] },
    { oldStart: 9, oldLines: 1, newStart: 9, newLines: 1, lines: ['-line 9', '+line 9 mod'] },
  ]

  it('renders non-clickable hidden-line gaps in unified view', () => {
    const { getAllByText, queryByRole } = render(() => (
      <DiffView hunks={hunks} view="unified" />
    ))

    expect(getAllByText('3 lines hidden')).toHaveLength(1)
    expect(queryByRole('button', { name: EXPAND_RE })).not.toBeInTheDocument()
    expect(queryByRole('button', { name: COLLAPSE_RE })).not.toBeInTheDocument()
  })

  it('renders non-clickable hidden-line gaps in split view', () => {
    const { getAllByText, queryByRole } = render(() => (
      <DiffView hunks={hunks} view="split" />
    ))

    expect(getAllByText('3 lines hidden')).toHaveLength(1)
    expect(queryByRole('button', { name: EXPAND_RE })).not.toBeInTheDocument()
    expect(queryByRole('button', { name: COLLAPSE_RE })).not.toBeInTheDocument()
  })
})

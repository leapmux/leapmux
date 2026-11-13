import type { StructuredPatchHunk } from '.'
import type { MarkdownRenderContext } from '../renderContext'
import type { RenderReadinessScope } from '../renderReadiness'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { diffWordsWithSpace } from 'diff'
import { createRenderEffect, createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { setSliceSchedulerForTests } from '~/lib/idleSlices'
import { readInjectedShikiRules } from '~/lib/shikiStyleClass.testkit'
import { setCachedTokens } from '~/lib/tokenCache'
import { AnsweringWorker, settleWorkerReplies } from '~/test-support/fakeWorker'
import { installControlledSlices } from '~/test-support/sliceSchedulers'
import { computeGapMap, computeSyntheticGapMap, DiffView, groupByHunk } from '.'
import { createRenderReadinessScope } from '../renderReadiness'
import { DIFF_DRAWN_LINE_LIMIT } from './diffBuilder'
import { patchWithoutWordDiffs, replacedWholeHunks } from './diffCompute'
import { computeDiff } from './diffSearch'
import { diffAddedInline, diffContent, diffHideLineNumbers, diffLayoutIncoming, diffLine, diffRemovedInline, diffSplitContainer } from './diffStyles.css'
import { buildSplitRows, buildUnifiedLines, splitRowMarks } from './diffTokenRender'
import { resetLayoutEstimateForTests } from './progressiveDiffLines'

/**
 * How many times the drawn lines of the hunks drew their content, and how many
 * of those contents their lines disposed. A line draws its content once when it
 * mounts, and again for each change of its marks.
 */
const lineContents = vi.hoisted(() => ({ drawn: 0, disposed: 0 }))

// A pass-through spy on the line builders, so that a test can count the builds,
// and on the content of each drawn line, so that a test can count the lines
// that it disposes.
vi.mock('./diffTokenRender', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./diffTokenRender')>()
  const { onCleanup } = await import('solid-js')
  return {
    ...actual,
    buildUnifiedLines: vi.fn(actual.buildUnifiedLines),
    buildSplitRows: vi.fn(actual.buildSplitRows),
    renderMarkedLine: (...args: Parameters<typeof actual.renderMarkedLine>) => {
      lineContents.drawn += 1
      onCleanup(() => {
        lineContents.disposed += 1
      })
      return actual.renderMarkedLine(...args)
    },
  }
})

// The slices learn the layout time of a line, which jsdom measures at about
// nothing. Each test starts from the initial estimate.
beforeEach(() => {
  resetLayoutEstimateForTests()
  lineContents.drawn = 0
  lineContents.disposed = 0
})

/**
 * A computed diff of `hunks`, with the word diffs that the diff worker computes
 * for them, at the line limit `drawnLineLimit` (the default when absent).
 */
function patchDiff(hunks: StructuredPatchHunk[], drawnLineLimit?: number) {
  return computeDiff(drawnLineLimit === undefined
    ? { kind: 'hunks', hunks }
    : { kind: 'hunks', hunks, drawnLineLimit })
}

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

describe('DiffView rendering preserves whitespace', () => {
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

      const prefixIdx = view === 'unified'
        ? 2
        : 1
      const contentIdx = view === 'unified'
        ? 3
        : 2
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
    const hunks = [
      {
        oldStart: 1,
        oldLines: 1,
        newStart: 1,
        newLines: 1,
        lines: ['-        return value;', '+    return newValue;'],
      },
    ]

    const { container } = render(() => (
      <DiffView diff={patchDiff(hunks)} view="unified" />
    ))

    const { removedText, addedText } = extractDiffTexts(container, 'unified')
    expect(removedText).toBe('        return value;')
    expect(addedText).toBe('    return newValue;')
  })

  it('unified view: preserves indentation when indent increases', () => {
    const hunks = [
      {
        oldStart: 1,
        oldLines: 1,
        newStart: 1,
        newLines: 1,
        lines: ['-    const x = 1;', '+        const y = 2;'],
      },
    ]

    const { container } = render(() => (
      <DiffView diff={patchDiff(hunks)} view="unified" />
    ))

    const { removedText, addedText } = extractDiffTexts(container, 'unified')
    expect(removedText).toBe('    const x = 1;')
    expect(addedText).toBe('        const y = 2;')
  })

  it('unified view: preserves indentation when old is indented and new is not', () => {
    const hunks = [
      {
        oldStart: 1,
        oldLines: 1,
        newStart: 1,
        newLines: 1,
        lines: ['-    return value', '+return newValue'],
      },
    ]

    const { container } = render(() => (
      <DiffView diff={patchDiff(hunks)} view="unified" />
    ))

    const { removedText, addedText } = extractDiffTexts(container, 'unified')
    expect(removedText).toBe('    return value')
    expect(addedText).toBe('return newValue')
  })

  it('split view: preserves indentation when indent decreases', () => {
    const hunks = [
      {
        oldStart: 1,
        oldLines: 1,
        newStart: 1,
        newLines: 1,
        lines: ['-        return value;', '+    return newValue;'],
      },
    ]

    const { container } = render(() => (
      <DiffView diff={patchDiff(hunks)} view="split" />
    ))

    const { removedText, addedText } = extractDiffTexts(container, 'split')
    expect(removedText).toBe('        return value;')
    expect(addedText).toBe('    return newValue;')
  })

  it('falls back to plain gap context when worker tokenization is unavailable', async () => {
    vi.stubGlobal('Worker', undefined)
    try {
      const originalFile = Array.from({ length: 12 }, (_, i) => `line ${i + 1}`).join('\n')
      render(() => (
        <DiffView
          filePath="example.ts"
          originalFile={originalFile}
          diff={patchDiff([
            {
              oldStart: 10,
              oldLines: 1,
              newStart: 10,
              newLines: 1,
              lines: [' line 10'],
            },
          ])}
          view="unified"
        />
      ))

      fireEvent.click(screen.getByText('9 lines hidden'))
      await Promise.resolve()

      expect(screen.getByText('line 1')).toBeInTheDocument()
      expect(screen.getByText('line 9')).toBeInTheDocument()
    }
    finally {
      vi.unstubAllGlobals()
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
    const oldLines = Array.from({ length: 10 }, (_, i) => i === 9
      ? 'line 10'
      : `old line ${i + 1}`)
    const newLines = Array.from({ length: 10 }, (_, i) => i === 9
      ? 'line 10'
      : `new line ${i + 1}`)
    const oldGapCode = oldLines.slice(0, 9).join('\n')
    const newGapCode = newLines.slice(0, 9).join('\n')
    setCachedTokens(
      'typescript',
      oldGapCode,
      oldLines.slice(0, 9).map(line => [
        { content: `old-token:${line}`, htmlStyle: {} },
      ]),
    )
    setCachedTokens(
      'typescript',
      newGapCode,
      newLines.slice(0, 9).map(line => [
        { content: `new-token:${line}`, htmlStyle: {} },
      ]),
    )
    const [originalFile, setOriginalFile] = createSignal(oldLines.join('\n'))

    render(() => (
      <DiffView
        filePath="example.ts"
        originalFile={originalFile()}
        diff={patchDiff([hunk])}
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
    const hunk = {
      oldStart: 26,
      oldLines: 1,
      newStart: 26,
      newLines: 1,
      lines: [' line 26'],
    }
    const originalFile = [...gapLines, 'line 26'].join('\n')

    // "Expand down" reveals the top 10 (lines 1..10); "Expand up" reveals the bottom 10
    // (lines 16..25). The hook then tokenizes exactly this joined slice -- seed the cache
    // so it resolves synchronously, with no worker round trip.
    const revealed = [
      ...gapLines.slice(0, 10),
      ...gapLines.slice(15, 25),
    ]
    setCachedTokens('typescript', revealed.join('\n'), revealed.map(line => [
      { content: `tok:${line}`, htmlStyle: {} },
    ]))

    render(() => <DiffView filePath="example.ts" originalFile={originalFile} diff={patchDiff([hunk])} view="unified" />)

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

describe('DiffView old/new-side syntax highlighting (useAsyncCodeTokens migration)', () => {
  // A context line is tokenized from the OLD side; a bare added line from the NEW side.
  // Pre-seed the shared token cache for both sides so the migrated useDiffTokens hook
  // takes its synchronous cache-hit path (no Worker needed), proving both sides flow
  // through the shared hook into buildUnifiedLines.
  const hunks: StructuredPatchHunk[] = [
    {
      oldStart: 1,
      oldLines: 1,
      newStart: 1,
      newLines: 2,
      lines: [' ctx', '+added'],
    },
  ]

  function seedSides(): void {
    // extractSidesFromHunks: oldCode = 'ctx'; newCode = 'ctx\nadded'.
    setCachedTokens('typescript', 'ctx', [
      [
        { content: 'CTXTOK' },
      ],
    ])
    setCachedTokens('typescript', 'ctx\nadded', [
      [
        { content: 'CTXTOK2' },
      ],
      [
        { content: 'ADDEDTOK' },
      ],
    ])
  }

  it('tokenizes both sides via the shared hook when a filePath resolves a language', async () => {
    seedSides()
    render(() => <DiffView filePath="example.ts" diff={patchDiff(hunks)} view="unified" />)

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
    // DiffView takes the markdown capability alone, so the paused flag fixture
    // states it directly -- no orchestration-bag cast needed.
    const context: MarkdownRenderContext = {
      syntaxHighlightingPaused: () => true,
    }
    render(() => <DiffView filePath="example.ts" diff={patchDiff(hunks)} view="unified" context={context} />)

    // Present synchronously on the first render (the seed), not after a deferred effect.
    expect(screen.getByText('CTXTOK')).toBeInTheDocument()
    expect(screen.getByText('ADDEDTOK')).toBeInTheDocument()
  })

  it('renders plain when no filePath resolves a language (ineligible)', () => {
    seedSides()
    render(() => <DiffView diff={patchDiff(hunks)} view="unified" />)

    // No filePath -> lang undefined -> the hook never tokenizes; raw text renders.
    expect(screen.getByText('ctx')).toBeInTheDocument()
    expect(screen.queryByText('CTXTOK')).not.toBeInTheDocument()
  })
})

describe('DiffView ansi (.log) highlighting', () => {
  // The ANSI escape byte, built via fromCharCode so the source stays plain ASCII.
  const ESC = String.fromCharCode(27)

  // A `.log` file resolves to the `ansi` language, which the Oniguruma token WORKER
  // cannot tokenize (ansi is a Shiki special, not a bundled grammar). The diff sides /
  // gap-context lines must fall back to the shared synchronous ansi tokenizer instead of
  // degrading to plain -- these guard against the regression where only the Read view
  // carried that fallback and `.log` diffs lost their colors.

  it('colors a `.log` diff context line via the synchronous ansi tokenizer (no worker)', () => {
    // One unchanged context line carrying an ANSI color escape. The assertion runs before
    // any worker could answer, so the only way this highlights is the syncTokenize ansi
    // path wired into useDiffTokens.
    const { container } = render(() => (
      <DiffView
        filePath="out.log"
        view="unified"
        diff={patchDiff([
          {
            oldStart: 1,
            oldLines: 1,
            newStart: 1,
            newLines: 1,
            lines: [` ${ESC}[32mgreen${ESC}[0m tail`],
          },
        ])}
      />
    ))

    // The escape sequences are consumed into per-token colors -- the visible text is clean.
    // Target the marked token span (its diffContent parent has the same textContent but no
    // marker), so this asserts a real Shiki token, not the wrapper.
    const greenSpan = [
      ...container.querySelectorAll('span[data-shiki-token]'),
    ].find(s => s.textContent === 'green')
    expect(greenSpan).toBeTruthy()
    // The token's shared style class defines the dual-theme variables (shikiStyleClass).
    expect(greenSpan!.className).toMatch(/^sk-/)
    expect(readInjectedShikiRules())
      .toContain(`.${greenSpan!.className}{`)
    // The raw escape byte never reaches the DOM (it would if the line rendered plain).
    expect(container.textContent).not.toContain(ESC)
  })

  it('colors a revealed `.log` gap-context line via the synchronous ansi tokenizer', () => {
    // Leading gap of ansi-colored lines; expanding it must tokenize the revealed slice
    // through the same syncTokenize path (the gap hook was the other diff surface missing it).
    // The tokens land after the reveal mounts the lines, and the next slice applies them.
    const slices = installControlledSlices(100)
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
        diff={patchDiff([
          {
            oldStart: 3,
            oldLines: 1,
            newStart: 3,
            newLines: 1,
            lines: [' line 3'],
          },
        ])}
      />
    ))

    fireEvent.click(screen.getByText('2 lines hidden'))
    slices.runAllSlices()

    // Revealed gap lines render colored (escapes stripped into token colors). Target the
    // marked token span rather than its unmarked diffContent parent.
    const redSpan = [
      ...document.querySelectorAll('span[data-shiki-token]'),
    ].find(s => s.textContent === 'red-one')
    expect(redSpan).toBeTruthy()
    expect(redSpan!.className).toMatch(/^sk-/)
  })
})

describe('DiffView shared gap scaffold (unified + split)', () => {
  // Both views render through one DiffGapScaffold; these exercise the shared reveal path
  // in the split view and the trailing-gap branch, which the unified leading-gap tests
  // above don't cover -- guarding the unified/split -> scaffold extraction.

  it('expands a leading gap in SPLIT view through the shared scaffold', async () => {
    const originalFile = Array.from({ length: 12 }, (_, i) => `line ${i + 1}`).join('\n')
    render(() => (
      <DiffView
        originalFile={originalFile}
        diff={patchDiff([
          {
            oldStart: 10,
            oldLines: 1,
            newStart: 10,
            newLines: 1,
            lines: [' line 10'],
          },
        ])}
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
        diff={patchDiff([
          {
            oldStart: 2,
            oldLines: 1,
            newStart: 2,
            newLines: 1,
            lines: [' line 2'],
          },
        ])}
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
        diff={patchDiff([
          {
            oldStart: 2,
            oldLines: 1,
            newStart: 2,
            newLines: 1,
            lines: [' line 2'],
          },
        ])}
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

    const lines = buildUnifiedLines(hunks)

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

    const lines = buildUnifiedLines(hunks)

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

    const lines = buildUnifiedLines(hunks)

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

describe('buildSplitRows', () => {
  it('pairs each removal with the addition beside it, and draws an empty cell beside an unpaired line', () => {
    const hunks: StructuredPatchHunk[] = [
      {
        oldStart: 1,
        oldLines: 4,
        newStart: 1,
        newLines: 3,
        lines: [' a', '-b', '-c', '+B', ' d', '+e'],
      },
    ]
    const sources = { oldTokens: null, newTokens: null, wordDiffs: patchDiff(hunks).wordDiffs }

    const rows = buildSplitRows(hunks)

    expect(rows.map(row => [
      row.left.type,
      row.left.text,
      row.left.num,
      row.right.type,
      row.right.text,
      row.right.num,
    ])).toEqual([
      ['context', 'a', 1, 'context', 'a', 1],
      ['removed', 'b', 2, 'added', 'B', 2],
      ['removed', 'c', 3, 'empty', '', null],
      ['context', 'd', 4, 'context', 'd', 3],
      ['empty', '', null, 'added', 'e', 4],
    ])
    // Both sides of the paired row carry its word diff, and no other row has one.
    const marks = rows.map(row => splitRowMarks(row, sources))
    expect(marks[1]!.left.words).toBeDefined()
    expect(marks[1]!.right.words).toBe(marks[1]!.left.words)
    expect(marks.filter((_, i) => i !== 1).every(rowMarks => rowMarks.left.words === undefined && rowMarks.right.words === undefined)).toBe(true)
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
      {
        oldStart: 4,
        oldLines: 2,
        newStart: 4,
        newLines: 2,
        lines: [' line 4', '-line 5', '+line 5 modified'],
      },
    ]
    const { gaps, trailing } = computeGapMap(hunks, originalFileLines)
    expect(gaps.size).toBe(1)
    const leadingGap = gaps.get(0)!
    expect(leadingGap.startLineNumber).toBe(1)
    expect(leadingGap.lines).toEqual(['line 1', 'line 2', 'line 3'])
    // Trailing gap: lines 6..10
    expect(trailing).not.toBeNull()
    expect(trailing!.startLineNumber).toBe(6)
    expect(trailing!.lines).toEqual([
      'line 6',
      'line 7',
      'line 8',
      'line 9',
      'line 10',
    ])
  })

  it('computes inter-hunk gap', () => {
    const hunks: StructuredPatchHunk[] = [
      {
        oldStart: 1,
        oldLines: 2,
        newStart: 1,
        newLines: 2,
        lines: ['-line 1', '+line 1 mod', ' line 2'],
      },
      {
        oldStart: 7,
        oldLines: 2,
        newStart: 7,
        newLines: 2,
        lines: [' line 7', '-line 8', '+line 8 mod'],
      },
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
      {
        oldStart: 9,
        oldLines: 2,
        newStart: 9,
        newLines: 2,
        lines: [' line 9', '-line 10', '+line 10 mod'],
      },
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
      {
        oldStart: 1,
        oldLines: 10,
        newStart: 1,
        newLines: 10,
        lines: originalFileLines.map(l => ` ${l}`),
      },
    ]
    const { gaps, trailing } = computeGapMap(hunks, originalFileLines)
    expect(gaps.size).toBe(0)
    expect(trailing).toBeNull()
  })
})

describe('computeSyntheticGapMap', () => {
  it('computes only inter-hunk gaps without original file content', () => {
    const hunks: StructuredPatchHunk[] = [
      {
        oldStart: 4,
        oldLines: 2,
        newStart: 4,
        newLines: 2,
        lines: [' line 4', '-line 5', '+line 5 mod'],
      },
      {
        oldStart: 9,
        oldLines: 1,
        newStart: 9,
        newLines: 1,
        lines: ['-line 9', '+line 9 mod'],
      },
    ]

    const gaps = computeSyntheticGapMap(hunks)

    expect(gaps.get(1)).toEqual({ startLineNumber: 6, lineCount: 3 })
    expect(gaps.size).toBe(1)
  })

  it('does not infer outer gaps without original file length', () => {
    const hunks: StructuredPatchHunk[] = [
      {
        oldStart: 2,
        oldLines: 1,
        newStart: 2,
        newLines: 1,
        lines: ['-line 2', '+line 2 mod'],
      },
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
    expect(groups[0]).toEqual([
      { hunkIndex: 0, value: 'a' },
      { hunkIndex: 0, value: 'b' },
    ])
    expect(groups[1]).toEqual([
      { hunkIndex: 1, value: 'c' },
      { hunkIndex: 1, value: 'd' },
      { hunkIndex: 1, value: 'e' },
    ])
    expect(groups[2]).toEqual([
      { hunkIndex: 2, value: 'f' },
    ])
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

describe('DiffView gap rendering without original file', () => {
  const hunks: StructuredPatchHunk[] = [
    {
      oldStart: 4,
      oldLines: 2,
      newStart: 4,
      newLines: 2,
      lines: [' line 4', '-line 5', '+line 5 mod'],
    },
    {
      oldStart: 9,
      oldLines: 1,
      newStart: 9,
      newLines: 1,
      lines: ['-line 9', '+line 9 mod'],
    },
  ]

  it('renders non-clickable hidden-line gaps in unified view', () => {
    const { getAllByText, queryByRole } = render(() => (
      <DiffView diff={patchDiff(hunks)} view="unified" />
    ))

    expect(getAllByText('3 lines hidden')).toHaveLength(1)
    expect(queryByRole('button', { name: EXPAND_RE })).not.toBeInTheDocument()
    expect(queryByRole('button', { name: COLLAPSE_RE })).not.toBeInTheDocument()
  })

  it('renders non-clickable hidden-line gaps in split view', () => {
    const { getAllByText, queryByRole } = render(() => (
      <DiffView diff={patchDiff(hunks)} view="split" />
    ))

    expect(getAllByText('3 lines hidden')).toHaveLength(1)
    expect(queryByRole('button', { name: EXPAND_RE })).not.toBeInTheDocument()
    expect(queryByRole('button', { name: COLLAPSE_RE })).not.toBeInTheDocument()
  })
})

describe('DiffView line numbers', () => {
  const hunks: StructuredPatchHunk[] = [
    {
      oldStart: 1,
      oldLines: 1,
      newStart: 1,
      newLines: 1,
      lines: ['-old line', '+new line'],
    },
  ]

  it.each(['unified', 'split'] as const)('hides the line numbers when the caller passes showLineNumbers false, in the %s view', (view) => {
    const { container } = render(() => (
      <DiffView diff={patchDiff(hunks)} view={view} showLineNumbers={false} />
    ))

    expect(container.querySelector('[data-file-diff]')).toHaveClass(diffHideLineNumbers)
  })

  it.each([
    ['unified', undefined],
    ['unified', true],
    ['split', undefined],
    ['split', true],
  ] as const)('shows the line numbers in the %s view when showLineNumbers is %s', (view, showLineNumbers) => {
    const { container } = render(() => (
      <DiffView
        diff={patchDiff(hunks)}
        view={view}
        {...(showLineNumbers !== undefined
          ? { showLineNumbers }
          : {})}
      />
    ))

    expect(container.querySelector('[data-file-diff]')).not.toHaveClass(diffHideLineNumbers)
  })

  it('keeps the line numbers hidden in the layout that a switch between unified and split mounts', async () => {
    const [view, setView] = createSignal<'unified' | 'split'>('unified')
    const { container } = render(() => (
      <DiffView diff={patchDiff(hunks)} view={view()} showLineNumbers={false} />
    ))

    setView('split')
    await waitFor(() => expect(container.querySelector(`.${diffSplitContainer}`)).toBeInTheDocument())

    const layouts = [
      ...container.querySelectorAll('[data-file-diff]'),
    ]
    expect(layouts).toHaveLength(1)
    expect(layouts[0]).toHaveClass(diffSplitContainer)
    expect(layouts[0]).toHaveClass(diffHideLineNumbers)
  })
})

describe('dual-side tokenization dedup', () => {
  it('coalesces identical old/new side code into ONE worker dispatch shared by both hooks', async () => {
    // A context-only hunk makes extractSidesFromHunks produce byte-identical
    // oldCode/newCode. Both per-side useAsyncCodeTokens instances then resolve the
    // same `${lang}\0${code}` key, so the in-flight coalescer in tokenizeAsync must
    // collapse their concurrent dispatches into a single worker tokenization -- the
    // dedup lives in the key construction (shared lang memo + shared sides memo),
    // and this pins that the two sides cannot drift onto different keys.
    //
    // The token client keeps the first worker that it spawns for the whole file, so
    // the spy observes the posts of whichever stand-in it holds.
    const posted = vi.spyOn(AnsweringWorker.prototype, 'postMessage')
    try {
      // Unique content so earlier tests' token-cache seeds can't satisfy this key.
      const code = 'const dedupSideA = 1\nconst dedupSideB = 2'
      const hunks: StructuredPatchHunk[] = [
        {
          oldStart: 1,
          oldLines: 2,
          newStart: 1,
          newLines: 2,
          lines: [' const dedupSideA = 1', ' const dedupSideB = 2'],
        },
      ]
      const { container } = render(() => <DiffView filePath="dedup-sides.ts" diff={patchDiff(hunks)} view="split" />)
      await settleWorkerReplies()

      // Two hook instances (old side + new side), ONE dispatch.
      const dispatches = posted.mock.calls
        .map(([message]) => message as { lang?: string, code?: string })
        .filter(message => message.code === code)
      expect(dispatches).toEqual([
        expect.objectContaining({ lang: 'typescript', code }),
      ])
      // BOTH sides of the split view apply the shared tokens (each context line
      // renders left + right). The diff renderer marks token spans with
      // data-shiki-token; the diffContent wrapper shares the textContent, so
      // target the marker.
      const tokenSpans = [
        ...container.querySelectorAll('span[data-shiki-token]'),
      ]
        .filter(s => s.textContent?.includes('dedupSideA'))
      expect(tokenSpans).toHaveLength(2) // left column + right column
    }
    finally {
      posted.mockRestore()
    }
  })
})

describe('diff token span classes (shared style class + word-diff composition)', () => {
  it('composes the token style class with the word-diff highlight on changed fragments only', () => {
    // One whole-line token per side carrying a shared style class, so the
    // word-diff splitter must carry that class onto every fragment it cuts.
    setCachedTokens('typescript', 'const wdValue = 1', [
      [
        { content: 'const wdValue = 1', className: 'sk-wd-old' },
      ],
    ])
    setCachedTokens('typescript', 'const wdNewValue = 1', [
      [
        { content: 'const wdNewValue = 1', className: 'sk-wd-new' },
      ],
    ])
    const hunks: StructuredPatchHunk[] = [
      {
        oldStart: 1,
        oldLines: 1,
        newStart: 1,
        newLines: 1,
        lines: ['-const wdValue = 1', '+const wdNewValue = 1'],
      },
    ]
    const { container } = render(() => <DiffView filePath="wd.ts" diff={patchDiff(hunks)} view="unified" />)

    // The changed word carries BOTH the token's shared style class and the
    // word-diff highlight...
    const removed = [
      ...container.querySelectorAll('span[data-shiki-token]'),
    ].find(s => s.textContent === 'wdValue')!
    expect(removed).toBeTruthy()
    expect(removed.classList.contains('sk-wd-old')).toBe(true)
    expect(removed.classList.contains(diffRemovedInline)).toBe(true)
    const added = [
      ...container.querySelectorAll('span[data-shiki-token]'),
    ].find(s => s.textContent === 'wdNewValue')!
    expect(added).toBeTruthy()
    expect(added.classList.contains('sk-wd-new')).toBe(true)
    expect(added.classList.contains(diffAddedInline)).toBe(true)

    // ...while the unchanged fragments of the same tokens keep only the style class.
    const unchanged = [
      ...container.querySelectorAll('span[data-shiki-token]'),
    ].filter(s => s.textContent === 'const ')
    expect(unchanged.length).toBeGreaterThan(0)
    for (const fragment of unchanged) {
      expect(fragment.classList.contains('sk-wd-old') || fragment.classList.contains('sk-wd-new')).toBe(true)
      expect(fragment.classList.contains(diffRemovedInline)).toBe(false)
      expect(fragment.classList.contains(diffAddedInline)).toBe(false)
    }
  })

  it('never stamps an empty class attribute on unstyled token or plain fallback spans', () => {
    // Unstyled tokens (no className) exercise the fragment path...
    setCachedTokens('typescript', 'plain wd old line', [
      [
        { content: 'plain wd old line' },
      ],
    ])
    setCachedTokens('typescript', 'plain wd new line', [
      [
        { content: 'plain wd new line' },
      ],
    ])
    const hunks: StructuredPatchHunk[] = [
      {
        oldStart: 1,
        oldLines: 1,
        newStart: 1,
        newLines: 1,
        lines: ['-plain wd old line', '+plain wd new line'],
      },
    ]
    const withTokens = render(() => <DiffView filePath="empty-class.ts" diff={patchDiff(hunks)} view="unified" />)
    expect(withTokens.container.querySelector('[class=""]')).toBeNull()

    // ...and a token-less diff (no filePath -> no language) exercises the
    // plain word-diff fallback, whose unchanged parts must omit the class
    // attribute rather than stamp class="".
    const plain = render(() => <DiffView diff={patchDiff(hunks)} view="unified" />)
    expect(plain.container.querySelector('[class=""]')).toBeNull()
  })
})

describe('DiffView line limit', () => {
  /** One hunk at `oldStart` that adds `count` lines, `row 0` to `row <count - 1>`. */
  function addedRows(count: number, oldStart = 1): StructuredPatchHunk[] {
    return [
      {
        oldStart,
        oldLines: 0,
        newStart: oldStart,
        newLines: count,
        lines: Array.from({ length: count }, (_, i) => `+row ${i}`),
      },
    ]
  }

  it('draws every line of a diff at the limit, with no notice', () => {
    const { queryByTestId, getByText } = render(() => (
      <DiffView diff={patchDiff(addedRows(DIFF_DRAWN_LINE_LIMIT))} view="unified" />
    ))

    expect(getByText(`row ${DIFF_DRAWN_LINE_LIMIT - 1}`)).toBeInTheDocument()
    expect(queryByTestId('diff-truncation-notice')).not.toBeInTheDocument()
  })

  it('draws the first lines of a longer diff and states how many more it leaves out', () => {
    const { getByTestId, getByText, queryByText } = render(() => (
      <DiffView diff={patchDiff(addedRows(DIFF_DRAWN_LINE_LIMIT + 5))} view="unified" />
    ))

    expect(getByText(`row ${DIFF_DRAWN_LINE_LIMIT - 1}`)).toBeInTheDocument()
    expect(queryByText(`row ${DIFF_DRAWN_LINE_LIMIT}`)).not.toBeInTheDocument()
    expect(getByTestId('diff-truncation-notice')).toHaveTextContent('5 more lines of this diff are not shown.')
  })

  it('states one left-out line in the singular, in the split view too', () => {
    const { getByTestId } = render(() => (
      <DiffView diff={patchDiff(addedRows(DIFF_DRAWN_LINE_LIMIT + 1))} view="split" />
    ))

    expect(getByTestId('diff-truncation-notice')).toHaveTextContent('1 more line of this diff is not shown.')
  })

  it('draws at most the line limit of the diff', () => {
    const { getByTestId, getByText, queryByText } = render(() => (
      <DiffView diff={patchDiff(addedRows(15), 10)} view="unified" />
    ))

    expect(getByText('row 9')).toBeInTheDocument()
    expect(queryByText('row 10')).not.toBeInTheDocument()
    expect(getByTestId('diff-truncation-notice')).toHaveTextContent('5 more lines of this diff are not shown.')
  })

  it('draws every line of a long diff at an infinite line limit, in the split view too', () => {
    const { getByText, queryByTestId } = render(() => (
      <DiffView diff={patchDiff(addedRows(DIFF_DRAWN_LINE_LIMIT + 5), Number.POSITIVE_INFINITY)} view="split" />
    ))

    expect(getByText(`row ${DIFF_DRAWN_LINE_LIMIT + 4}`)).toBeInTheDocument()
    expect(queryByTestId('diff-truncation-notice')).not.toBeInTheDocument()
  })

  describe('with the original file', () => {
    // Lines 1 to 9 come before the first hunk, which changes line 10. Lines 11 to 19
    // come between the two hunks, and the second inserts its rows before line 20.
    // Lines 20 to 30 come after the second hunk.
    const originalFile = Array.from({ length: 30 }, (_, i) => `line ${i + 1}`).join('\n')
    function twoHunks(insertedRows: number): StructuredPatchHunk[] {
      return [
        {
          oldStart: 10,
          oldLines: 1,
          newStart: 10,
          newLines: 1,
          lines: ['-line 10', '+LINE 10'],
        },
        ...addedRows(insertedRows, 20),
      ]
    }

    it('offers each gap of a diff that fits', () => {
      const { getAllByText, getByText } = render(() => (
        <DiffView originalFile={originalFile} diff={patchDiff(twoHunks(3))} view="unified" />
      ))

      expect(getAllByText('9 lines hidden')).toHaveLength(2)
      expect(getByText('11 lines hidden')).toBeInTheDocument()
    })

    it('keeps the gaps before the cut, and offers no gap after it', async () => {
      const { getAllByText, getByText, queryByText } = render(() => (
        <DiffView originalFile={originalFile} diff={patchDiff(twoHunks(DIFF_DRAWN_LINE_LIMIT))} view="unified" />
      ))

      // The cut falls in the second hunk, so the lines after it would show its
      // left-out rows as unchanged lines.
      expect(queryByText('11 lines hidden')).not.toBeInTheDocument()
      const gaps = getAllByText('9 lines hidden')
      expect(gaps).toHaveLength(2)

      fireEvent.click(gaps[1]!)
      await Promise.resolve()

      expect(getByText('line 11')).toBeInTheDocument()
      expect(getByText('line 19')).toBeInTheDocument()
    })

    it('offers the gap after a long diff that the view draws whole', () => {
      const { getAllByText, getByText } = render(() => (
        <DiffView
          originalFile={originalFile}
          diff={patchDiff(twoHunks(DIFF_DRAWN_LINE_LIMIT), Number.POSITIVE_INFINITY)}
          view="unified"
        />
      ))

      expect(getAllByText('9 lines hidden')).toHaveLength(2)
      expect(getByText('11 lines hidden')).toBeInTheDocument()
    })
  })
})

describe('DiffView whole replacement', () => {
  const hunks = replacedWholeHunks('a\nb\n', 'c\nd\n')

  it('states that too many lines changed to compare them one by one', () => {
    const { getByTestId } = render(() => (
      <DiffView
        diff={{
          hunks,
          wordDiffs: new Map(),
          wholeReplacement: 'too-many-edits',
          drawnLineLimit: DIFF_DRAWN_LINE_LIMIT,
        }}
        view="unified"
      />
    ))

    expect(getByTestId('diff-replacement-notice')).toHaveTextContent(
      'Too many lines changed to compare them one by one. This diff removes every old line and adds every new line.',
    )
  })

  it('states that the comparison did not finish, in the split view too', () => {
    const { getByTestId } = render(() => (
      <DiffView
        diff={{
          hunks,
          wordDiffs: new Map(),
          wholeReplacement: 'not-computed',
          drawnLineLimit: DIFF_DRAWN_LINE_LIMIT,
        }}
        view="split"
      />
    ))

    expect(getByTestId('diff-replacement-notice')).toHaveTextContent(
      'The line-by-line comparison did not finish. This diff removes every old line and adds every new line.',
    )
  })

  it('states nothing for a diff of the changed lines', () => {
    const { queryByTestId } = render(() => <DiffView diff={patchDiff(hunks)} view="unified" />)

    expect(queryByTestId('diff-replacement-notice')).not.toBeInTheDocument()
  })
})

describe('DiffView word diffs', () => {
  const hunks: StructuredPatchHunk[] = [
    {
      oldStart: 1,
      oldLines: 1,
      newStart: 1,
      newLines: 1,
      lines: ['-let first = 1', '+let second = 1'],
    },
  ]

  it('highlights the changed words of a paired line from the computed word diff', () => {
    const { container } = render(() => <DiffView diff={patchDiff(hunks)} view="unified" />)

    expect([
      ...container.querySelectorAll(`.${diffRemovedInline}`),
    ].map(el => el.textContent)).toEqual(['first'])
    expect([
      ...container.querySelectorAll(`.${diffAddedInline}`),
    ].map(el => el.textContent)).toEqual(['second'])
  })

  it('draws a paired line whole when the diff has no word diff for it', () => {
    const { container, getByText } = render(() => (
      <DiffView
        diff={{
          hunks,
          wordDiffs: new Map(),
          wholeReplacement: null,
          drawnLineLimit: DIFF_DRAWN_LINE_LIMIT,
        }}
        view="unified"
      />
    ))

    expect(getByText('let first = 1')).toBeInTheDocument()
    expect(getByText('let second = 1')).toBeInTheDocument()
    expect(container.querySelector(`.${diffRemovedInline}`)).toBeNull()
    expect(container.querySelector(`.${diffAddedInline}`)).toBeNull()
  })
})

describe('DiffView progressive mount', () => {
  /** One change block that rewrites `count` lines: `old row <i>` becomes `new row <i>`. */
  function rewrite(count: number): StructuredPatchHunk[] {
    return [
      {
        oldStart: 1,
        oldLines: count,
        newStart: 1,
        newLines: count,
        lines: [
          ...Array.from({ length: count }, (_, i) => `-old row ${i}`),
          ...Array.from({ length: count }, (_, i) => `+new row ${i}`),
        ],
      },
    ]
  }

  /**
   * The line elements that the view draws, in order: one for each line of the
   * unified view, and two for each row of the split view. A line sits in the
   * group element of its hunk.
   */
  function lineElements(container: HTMLElement): HTMLElement[] {
    return [
      ...container.querySelectorAll<HTMLElement>(`[data-file-diff] > [data-diff-hunk] > .${diffLine}`),
    ]
  }

  /** Whether each line element draws a word highlight. */
  function highlightedLines(container: HTMLElement): boolean[] {
    return lineElements(container).map(line => line.querySelector(`.${diffRemovedInline}, .${diffAddedInline}`) !== null)
  }

  /** The diff of `hunks` before the diff worker adds its word diffs. */
  function beforeWordDiffs(hunks: StructuredPatchHunk[]) {
    return patchWithoutWordDiffs({ kind: 'hunks', hunks })
  }

  for (const view of ['unified', 'split'] as const) {
    it(`mounts one slice of a ${DIFF_DRAWN_LINE_LIMIT}-line diff in its first task, and the rest as the scheduler runs, in the ${view} view`, () => {
      const slices = installControlledSlices(100)
      // The unified view draws each diff line, and the split view draws each
      // removal beside its addition, as one row of two elements.
      const rows = view === 'unified'
        ? DIFF_DRAWN_LINE_LIMIT
        : DIFF_DRAWN_LINE_LIMIT / 2
      const elementsPerRow = view === 'unified'
        ? 1
        : 2
      const { container } = render(() => <DiffView diff={patchDiff(rewrite(DIFF_DRAWN_LINE_LIMIT / 2))} view={view} />)

      expect(lineElements(container)).toHaveLength(100 * elementsPerRow)
      expect(slices.waiting()).toBe(1)

      slices.runSlice()

      expect(lineElements(container)).toHaveLength(200 * elementsPerRow)

      expect(slices.runAllSlices()).toBe(rows / 100 - 2)
      expect(lineElements(container)).toHaveLength(rows * elementsPerRow)
      expect(lineElements(container)[0]).toHaveTextContent('old row 0')
      expect(lineElements(container).at(-1)).toHaveTextContent('new row 999')
    })
  }

  it('mounts one line in each slice whose budget is used at once', () => {
    const slices = installControlledSlices(0)
    const { container } = render(() => <DiffView diff={patchDiff(rewrite(5))} view="unified" />)

    expect(lineElements(container)).toHaveLength(1)

    slices.runSlice()

    expect(lineElements(container)).toHaveLength(2)
    expect(slices.runAllSlices()).toBe(8)
    expect(lineElements(container)).toHaveLength(10)
  })

  it('stops a slice when the style and layout that its lines leave to the browser use its budget', () => {
    const deferred: number[] = []
    setSliceSchedulerForTests({
      startSlice: () => ({
        defer: ms => deferred.push(ms),
        used: () => deferred.reduce((sum, ms) => sum + ms, 0) > 0,
      }),
      requestSlice: () => () => {},
    })
    const { container } = render(() => <DiffView diff={patchDiff(rewrite(5))} view="unified" />)

    expect(lineElements(container)).toHaveLength(1)
    expect(deferred).toHaveLength(1)
    expect(deferred[0]).toBeGreaterThan(0)
  })

  it('lays out the lines of an idle slice before the slice ends', () => {
    const slices = installControlledSlices(3)
    const { container } = render(() => <DiffView diff={patchDiff(rewrite(5))} view="unified" />)
    const drawnAtLayout: number[] = []
    const root = document.documentElement
    const layout = vi.spyOn(root, 'getBoundingClientRect').mockImplementation(() => {
      drawnAtLayout.push(lineElements(container).length)
      return new DOMRect()
    })
    try {
      slices.runSlice()
    }
    finally {
      layout.mockRestore()
    }

    // The slice lays out after it inserts its three lines, so that its own
    // budget pays for their style and layout.
    expect(drawnAtLayout).toEqual([6])
  })

  it('holds its readiness scope pending until the last slice, and not again when the word diffs and the tokens land', async () => {
    const slices = installControlledSlices(10)
    const hunks: StructuredPatchHunk[] = [
      {
        oldStart: 1,
        oldLines: 30,
        newStart: 1,
        newLines: 30,
        lines: [
          ...Array.from({ length: 30 }, (_, i) => `-const progressiveOld${i} = ${i}`),
          ...Array.from({ length: 30 }, (_, i) => `+const progressiveNew${i} = ${i}`),
        ],
      },
    ]
    const [diff, setDiff] = createSignal(beforeWordDiffs(hunks))
    const seen: boolean[] = []
    let scope!: RenderReadinessScope
    const { container } = render(() => {
      scope = createRenderReadinessScope()
      createRenderEffect(() => seen.push(scope.pending()))
      return <DiffView filePath="progressive-readiness.ts" diff={diff()} view="unified" context={{ readiness: scope }} />
    })

    expect(scope.pending()).toBe(true)
    slices.runSlice()
    expect(scope.pending()).toBe(true)
    slices.runAllSlices()
    expect(scope.pending()).toBe(false)

    setDiff(patchDiff(hunks))
    await settleWorkerReplies()
    slices.runAllSlices()

    expect(highlightedLines(container).every(Boolean)).toBe(true)
    expect(container.querySelector('span[data-shiki-token]')).not.toBeNull()
    // Ready before the body mounted, pending while its lines mounted, then ready for good.
    expect(seen).toEqual([false, true, false])
  })

  it('applies new word highlights in slices to the line elements that it drew', () => {
    const slices = installControlledSlices(5)
    const hunks = rewrite(10)
    const [diff, setDiff] = createSignal(beforeWordDiffs(hunks))
    const { container } = render(() => <DiffView diff={diff()} view="unified" />)
    slices.runAllSlices()
    const drawn = lineElements(container)
    expect(drawn).toHaveLength(20)

    setDiff(patchDiff(hunks))

    expect(highlightedLines(container).some(Boolean)).toBe(false)

    slices.runSlice()

    expect(highlightedLines(container)).toEqual(Array.from({ length: 20 }, (_, i) => i < 5))
    for (const [i, line] of lineElements(container).entries())
      expect(line).toBe(drawn[i])

    slices.runAllSlices()

    expect(highlightedLines(container).every(Boolean)).toBe(true)
    for (const [i, line] of lineElements(container).entries())
      expect(line).toBe(drawn[i])
  })

  it('applies tokens that land after the mount in slices to the row elements that it drew, in the split view', async () => {
    const slices = installControlledSlices(3)
    const lines = Array.from({ length: 8 }, (_, i) => `const progressiveSplit${i} = ${i}`)
    const hunks: StructuredPatchHunk[] = [
      {
        oldStart: 1,
        oldLines: 8,
        newStart: 1,
        newLines: 8,
        lines: lines.map(line => ` ${line}`),
      },
    ]
    const { container } = render(() => <DiffView filePath="progressive-split.ts" diff={patchDiff(hunks)} view="split" />)
    slices.runAllSlices()
    const drawn = lineElements(container)
    expect(drawn).toHaveLength(16)
    const tokenized = () => lineElements(container).map(cell => cell.querySelector('span[data-shiki-token]') !== null)

    await settleWorkerReplies()

    expect(tokenized().some(Boolean)).toBe(false)

    slices.runSlice()

    // Three rows of two cells each show their tokens, and the later rows still show their text.
    expect(tokenized()).toEqual(Array.from({ length: 16 }, (_, i) => i < 6))
    expect(lineElements(container).map(cell => cell.textContent)).toEqual(drawn.map(cell => cell.textContent))
    for (const [i, cell] of lineElements(container).entries())
      expect(cell).toBe(drawn[i])

    slices.runAllSlices()

    expect(tokenized().every(Boolean)).toBe(true)
  })

  it('keeps the marks of its drawn lines while the display holds, and applies them after', () => {
    const slices = installControlledSlices(100)
    const [paused, setPaused] = createSignal(true)
    const hunks = rewrite(3)
    const [diff, setDiff] = createSignal(beforeWordDiffs(hunks))
    const { container } = render(() => (
      <DiffView diff={diff()} view="unified" context={{ syntaxHighlightingPaused: paused }} />
    ))
    slices.runAllSlices()

    setDiff(patchDiff(hunks))
    slices.runAllSlices()

    expect(highlightedLines(container).some(Boolean)).toBe(false)

    setPaused(false)
    slices.runAllSlices()

    expect(highlightedLines(container).every(Boolean)).toBe(true)
  })

  it('mounts a diff of other lines in slices again, and holds its readiness scope until then', () => {
    const slices = installControlledSlices(4)
    const [diff, setDiff] = createSignal(patchDiff(rewrite(3)))
    let scope!: RenderReadinessScope
    const { container } = render(() => {
      scope = createRenderReadinessScope()
      return <DiffView diff={diff()} view="unified" context={{ readiness: scope }} />
    })
    slices.runAllSlices()
    expect(scope.pending()).toBe(false)

    setDiff(patchDiff(rewrite(6)))

    expect(scope.pending()).toBe(true)
    expect(lineElements(container)).toHaveLength(4)
    slices.runAllSlices()
    expect(scope.pending()).toBe(false)
    expect(lineElements(container)).toHaveLength(12)
    expect(lineElements(container).at(-1)).toHaveTextContent('new row 5')
  })

  it('reveals the gap of a hunk that has mounted while the later hunks still mount, and keeps it revealed', () => {
    const slices = installControlledSlices(2)
    const originalFile = Array.from({ length: 30 }, (_, i) => `line ${i + 1}`).join('\n')
    const hunks: StructuredPatchHunk[] = [
      {
        oldStart: 10,
        oldLines: 1,
        newStart: 10,
        newLines: 1,
        lines: ['-line 10', '+LINE 10'],
      },
      {
        oldStart: 20,
        oldLines: 1,
        newStart: 20,
        newLines: 1,
        lines: ['-line 20', '+LINE 20'],
      },
    ]
    render(() => <DiffView originalFile={originalFile} diff={patchDiff(hunks)} view="unified" />)

    // The first slice mounts the first hunk, with the gap of lines 1 to 9 before it.
    expect(screen.getAllByText('9 lines hidden')).toHaveLength(1)
    expect(screen.queryByText('LINE 20')).not.toBeInTheDocument()
    fireEvent.click(screen.getByText('9 lines hidden'))
    expect(screen.getByText('line 1')).toBeInTheDocument()

    slices.runAllSlices()

    // The second hunk brings the gap of lines 11 to 19, and the gap after it.
    expect(screen.getByText('line 1')).toBeInTheDocument()
    expect(screen.getByText('line 9')).toBeInTheDocument()
    expect(screen.getAllByText('9 lines hidden')).toHaveLength(1)
    expect(screen.getByText('10 lines hidden')).toBeInTheDocument()
    fireEvent.click(screen.getByText('10 lines hidden'))
    slices.runAllSlices()
    expect(screen.getByText('line 30')).toBeInTheDocument()
  })

  it('cancels its waiting slice when it unmounts before its last line mounts, and disposes the lines that mounted in a later slice', () => {
    const slices = installControlledSlices(10)
    const { unmount } = render(() => <DiffView diff={patchDiff(rewrite(50))} view="unified" />)
    expect(slices.waiting()).toBe(1)

    unmount()

    expect(lineContents).toEqual({ drawn: 10, disposed: 0 })
    // The one slice that waits disposes the ten lines that mounted, and mounts no other.
    expect(slices.runAllSlices()).toBe(1)
    expect(lineContents).toEqual({ drawn: 10, disposed: 10 })
  })

  it('reports ready at once for an empty diff and for a one-line diff', () => {
    const slices = installControlledSlices(0)
    const oneLine: StructuredPatchHunk[] = [
      {
        oldStart: 1,
        oldLines: 0,
        newStart: 1,
        newLines: 1,
        lines: ['+only line'],
      },
    ]
    const cases: Array<{ hunks: StructuredPatchHunk[], lines: number }> = [
      { hunks: [], lines: 0 },
      { hunks: oneLine, lines: 1 },
    ]
    for (const { hunks, lines } of cases) {
      let scope!: RenderReadinessScope
      const { container, unmount } = render(() => {
        scope = createRenderReadinessScope()
        return <DiffView diff={patchDiff(hunks)} view="unified" context={{ readiness: scope }} />
      })

      expect(scope.pending()).toBe(false)
      expect(lineElements(container)).toHaveLength(lines)
      expect(slices.waiting()).toBe(0)
      unmount()
    }
  })

  /** Every line element under `root`, of every diff container that it holds. */
  function drawnLines(root: ParentNode): HTMLElement[] {
    return [
      ...root.querySelectorAll<HTMLElement>(`[data-file-diff] .${diffLine}`),
    ]
  }

  /** What one slice did to the subtree of a diff container. */
  interface SliceMutations {
    /** The line elements that the slice inserted, anywhere in the subtree. */
    insertedLines: number
    /** The nodes that the slice removed, anywhere in the subtree. */
    removedNodes: number
    /** The changes that the slice made to the children of the container itself. */
    containerChanges: number
  }

  /** Record the mutations under `container`, and read them slice by slice. */
  function watchMutations(container: Element): { take: () => SliceMutations, stop: () => void } {
    const observer = new MutationObserver(() => {})
    observer.observe(container, { childList: true, subtree: true })
    return {
      take() {
        const taken: SliceMutations = { insertedLines: 0, removedNodes: 0, containerChanges: 0 }
        for (const record of observer.takeRecords()) {
          if (record.target === container)
            taken.containerChanges += 1
          taken.removedNodes += record.removedNodes.length
          for (const node of record.addedNodes) {
            if (node instanceof HTMLElement && node.classList.contains(diffLine))
              taken.insertedLines += 1
          }
        }
        return taken
      },
      stop: () => observer.disconnect(),
    }
  }

  for (const view of ['unified', 'split'] as const) {
    it(`inserts only the lines that each slice mounts, up to the last slice of a ${DIFF_DRAWN_LINE_LIMIT}-line diff, in the ${view} view`, () => {
      const slices = installControlledSlices(100)
      const elementsPerRow = view === 'unified'
        ? 1
        : 2
      const rows = view === 'unified'
        ? DIFF_DRAWN_LINE_LIMIT
        : DIFF_DRAWN_LINE_LIMIT / 2
      const { container } = render(() => <DiffView diff={patchDiff(rewrite(DIFF_DRAWN_LINE_LIMIT / 2))} view={view} />)
      const diff = container.querySelector('[data-file-diff]')!
      const mutations = watchMutations(diff)
      const perSlice: SliceMutations[] = []
      try {
        while (slices.waiting() > 0) {
          slices.runSlice()
          perSlice.push(mutations.take())
        }
      }
      finally {
        mutations.stop()
      }

      // Each slice puts its own lines into the hunk that they belong to. It
      // moves no line that an earlier slice drew, and it leaves the children of
      // the container alone, so the cost of a slice does not grow with the lines
      // drawn before it.
      expect(perSlice).toEqual(Array.from({ length: rows / 100 - 1 }, () => ({
        insertedLines: 100 * elementsPerRow,
        removedNodes: 0,
        containerChanges: 0,
      })))
      expect(drawnLines(container)).toHaveLength(rows * elementsPerRow)
    })
  }

  it('reads the word diff of no line when the word diffs land, until a slice reaches the line', () => {
    const slices = installControlledSlices(5)
    const hunks = rewrite(10)
    const [diff, setDiff] = createSignal(beforeWordDiffs(hunks))
    render(() => <DiffView diff={diff()} view="unified" />)
    slices.runAllSlices()
    const computed = patchDiff(hunks)
    const wordDiffs = new ReadCountingMap(computed.wordDiffs)

    setDiff({ ...computed, wordDiffs })

    expect(wordDiffs.reads).toBe(0)

    slices.runSlice()

    // One slice of five steps reads the word diffs of its five lines.
    expect(wordDiffs.reads).toBe(5)
  })

  it('reads the marks of no line again for a new diff object with the same word diffs', () => {
    const slices = installControlledSlices(100)
    const computed = patchDiff(rewrite(5))
    const wordDiffs = new ReadCountingMap(computed.wordDiffs)
    const [diff, setDiff] = createSignal({ ...computed, wordDiffs })
    render(() => <DiffView diff={diff()} view="unified" />)
    slices.runAllSlices()
    wordDiffs.reads = 0

    setDiff({ ...computed, wordDiffs })
    slices.runAllSlices()

    expect(wordDiffs.reads).toBe(0)
  })

  it('keeps its line elements, and builds no line, for equal hunks in new arrays', () => {
    const slices = installControlledSlices(100)
    const builds = vi.mocked(buildUnifiedLines)
    const [diff, setDiff] = createSignal(patchDiff(rewrite(5)))
    const { container } = render(() => <DiffView diff={diff()} view="unified" />)
    slices.runAllSlices()
    const drawn = lineElements(container)
    builds.mockClear()

    // The new diff carries word diffs in a new map, which the slices compare line by line.
    setDiff(patchDiff(rewrite(5)))
    slices.runAllSlices()

    expect(builds).not.toHaveBeenCalled()
    expect(lineElements(container)).toHaveLength(10)
    for (const [i, line] of lineElements(container).entries())
      expect(line).toBe(drawn[i])
  })

  it('builds the lines of a diff once, whatever tokens and word diffs land after it mounts', async () => {
    const slices = installControlledSlices(100)
    const builds = vi.mocked(buildUnifiedLines)
    const hunks: StructuredPatchHunk[] = [
      {
        oldStart: 1,
        oldLines: 3,
        newStart: 1,
        newLines: 3,
        lines: [
          ...Array.from({ length: 3 }, (_, i) => `-const buildOnceOld${i} = ${i}`),
          ...Array.from({ length: 3 }, (_, i) => `+const buildOnceNew${i} = ${i}`),
        ],
      },
    ]
    const [diff, setDiff] = createSignal(beforeWordDiffs(hunks))
    builds.mockClear()
    const { container } = render(() => <DiffView filePath="build-once.ts" diff={diff()} view="unified" />)
    slices.runAllSlices()

    await settleWorkerReplies()
    setDiff(patchDiff(hunks))
    slices.runAllSlices()

    expect(builds).toHaveBeenCalledTimes(1)
    expect(container.querySelector('span[data-shiki-token]')).not.toBeNull()
    expect(highlightedLines(container).every(Boolean)).toBe(true)
  })

  it('draws the text of a changed line at once, not the tokens of its earlier text, while the tokens of the new text compute', () => {
    installControlledSlices(100)
    const before = 'const staleTokenBefore = 1'
    setCachedTokens('typescript', before, [
      [
        { content: before, className: 'sk-stale-before' },
      ],
    ])
    const contextHunk = (text: string): StructuredPatchHunk[] => [
      {
        oldStart: 1,
        oldLines: 1,
        newStart: 1,
        newLines: 1,
        lines: [` ${text}`],
      },
    ]
    const [diff, setDiff] = createSignal(patchDiff(contextHunk(before)))
    const { container } = render(() => <DiffView filePath="stale-tokens.ts" diff={diff()} view="unified" />)
    expect(container.querySelector('.sk-stale-before')).not.toBeNull()

    setDiff(patchDiff(contextHunk('const staleTokenAfter = 2')))

    expect(lineElements(container).map(line => line.querySelector(`.${diffContent}`)?.textContent)).toEqual(['const staleTokenAfter = 2'])
    expect(container.querySelector('.sk-stale-before')).toBeNull()
  })

  it('replaces a diff whose lines still mount with the lines of the new diff, and holds its readiness scope until they mount', () => {
    const slices = installControlledSlices(10)
    const other: StructuredPatchHunk[] = [
      {
        oldStart: 1,
        oldLines: 25,
        newStart: 1,
        newLines: 25,
        lines: [
          ...Array.from({ length: 25 }, (_, i) => `-former ${i}`),
          ...Array.from({ length: 25 }, (_, i) => `+latter ${i}`),
        ],
      },
    ]
    const [diff, setDiff] = createSignal(patchDiff(rewrite(25)))
    let scope!: RenderReadinessScope
    const { container } = render(() => {
      scope = createRenderReadinessScope()
      return <DiffView diff={diff()} view="unified" context={{ readiness: scope }} />
    })
    expect(drawnLines(container)).toHaveLength(10)

    setDiff(patchDiff(other))

    // A slice that disposes the ten old lines, and one that mounts the next new lines.
    expect(slices.waiting()).toBe(2)
    expect(drawnLines(container).map(line => line.textContent)).toEqual(Array.from({ length: 10 }, (_, i) => `${i + 1}-former ${i}`))
    expect(scope.pending()).toBe(true)
    while (slices.waiting() > 0) {
      expect(scope.pending()).toBe(true)
      slices.runSlice()
    }
    expect(scope.pending()).toBe(false)
    expect(drawnLines(container)).toHaveLength(50)
    expect(container.textContent).not.toContain('old row')
  })

  describe('a switch between the unified and the split layout', () => {
    it('keeps the old layout shown and the readiness scope ready while the new layout mounts, then shows only the new one', () => {
      const slices = installControlledSlices(10)
      const [view, setView] = createSignal<'unified' | 'split'>('unified')
      const seen: boolean[] = []
      let scope!: RenderReadinessScope
      const { container } = render(() => {
        scope = createRenderReadinessScope()
        createRenderEffect(() => seen.push(scope.pending()))
        return <DiffView diff={patchDiff(rewrite(25))} view={view()} context={{ readiness: scope }} />
      })
      slices.runAllSlices()
      const unified = container.querySelector('[data-file-diff]')

      setView('split')

      expect(scope.pending()).toBe(false)
      expect(layouts(container)).toEqual([
        { split: false, shown: true },
        { split: true, shown: false },
      ])
      expect(container.querySelector('[data-file-diff]')).toBe(unified)
      expect(unified!.querySelectorAll(`.${diffLine}`)).toHaveLength(50)
      // The first slice of the split layout mounted ten rows of two cells.
      expect(container.querySelectorAll(`.${diffLayoutIncoming} .${diffLine}`)).toHaveLength(20)

      slices.runAllSlices()

      expect(layouts(container)).toEqual([
        { split: true, shown: true },
      ])
      expect(unified!.isConnected).toBe(false)
      expect(lineElements(container)).toHaveLength(50)
      // Pending while the first layout mounted, then ready for good.
      expect(seen).toEqual([false, true, false])
    })

    it('keeps mounting the new layout when the view notifies again with the same value', () => {
      const slices = installControlledSlices(10)
      // A tab object that each patch replaces, as the file viewer's tab does: a
      // patch of another field notifies the view with the value that it holds.
      const [tab, setTab] = createSignal<{ view: 'unified' | 'split' }>({ view: 'unified' })
      const { container } = render(() => <DiffView diff={patchDiff(rewrite(25))} view={tab().view} />)
      slices.runAllSlices()
      setTab({ view: 'split' })
      slices.runSlice()
      // Two slices of ten rows of two cells.
      expect(container.querySelectorAll(`.${diffLayoutIncoming} .${diffLine}`)).toHaveLength(40)

      setTab({ view: 'split' })

      expect(container.querySelectorAll(`.${diffLayoutIncoming} .${diffLine}`)).toHaveLength(40)
      // One more slice mounts the last five rows, and the view swaps.
      slices.runSlice()
      expect(layouts(container)).toEqual([
        { split: true, shown: true },
      ])
      expect(lineElements(container)).toHaveLength(50)
    })

    it('removes the old layout from the document at the swap, and disposes its lines in slices after it', () => {
      const slices = installControlledSlices(10)
      const [view, setView] = createSignal<'unified' | 'split'>('unified')
      const { container } = render(() => <DiffView diff={patchDiff(rewrite(25))} view={view()} />)
      slices.runAllSlices()
      const unified = container.querySelector('[data-file-diff]')!
      setView('split')
      while (layouts(container).length === 2)
        slices.runSlice()
      // The 50 unified lines and the 25 split rows of two cells drew their content once each.
      expect(lineContents).toEqual({ drawn: 100, disposed: 0 })

      // The swap removed the old layout and disposed none of its lines.
      expect(unified.isConnected).toBe(false)
      expect(layouts(container)).toEqual([
        { split: true, shown: true },
      ])

      // Each slice disposes ten of the 50 unified lines.
      slices.runSlice()
      expect(lineContents.disposed).toBe(10)
      expect(slices.runAllSlices()).toBe(4)
      expect(lineContents).toEqual({ drawn: 100, disposed: 50 })
      expect(lineElements(container)).toHaveLength(50)
    })

    it('drops the new layout when the view switches back before it mounts', () => {
      const slices = installControlledSlices(10)
      const [view, setView] = createSignal<'unified' | 'split'>('unified')
      const { container } = render(() => <DiffView diff={patchDiff(rewrite(25))} view={view()} />)
      slices.runAllSlices()
      const unified = container.querySelector('[data-file-diff]')

      setView('split')
      setView('unified')

      // The one slice that waits disposes the ten rows of two cells that the new
      // layout mounted, and mounts no other.
      expect(lineContents.disposed).toBe(0)
      expect(slices.runAllSlices()).toBe(1)
      expect(lineContents.disposed).toBe(20)
      expect(layouts(container)).toEqual([
        { split: false, shown: true },
      ])
      expect(container.querySelector('[data-file-diff]')).toBe(unified)
    })

    it('mounts the new layout in place of a first layout that has not mounted yet, and holds the readiness scope until it mounts', () => {
      const slices = installControlledSlices(10)
      const [view, setView] = createSignal<'unified' | 'split'>('unified')
      let scope!: RenderReadinessScope
      const { container } = render(() => {
        scope = createRenderReadinessScope()
        return <DiffView diff={patchDiff(rewrite(25))} view={view()} context={{ readiness: scope }} />
      })

      setView('split')

      expect(layouts(container)).toEqual([
        { split: true, shown: true },
      ])
      expect(scope.pending()).toBe(true)
      slices.runAllSlices()
      expect(scope.pending()).toBe(false)
      expect(lineElements(container)).toHaveLength(50)
    })

    it('reports to its readiness scope that it replaces its content until the new layout shows', () => {
      const slices = installControlledSlices(10)
      const [view, setView] = createSignal<'unified' | 'split'>('unified')
      let scope!: RenderReadinessScope
      const { container } = render(() => {
        scope = createRenderReadinessScope()
        return <DiffView diff={patchDiff(rewrite(25))} view={view()} context={{ readiness: scope }} />
      })
      slices.runAllSlices()
      expect(scope.replacing()).toBe(false)
      expect(scope.measurable()).toBe(true)

      setView('split')

      expect(scope.replacing()).toBe(true)
      expect(scope.measurable()).toBe(false)
      expect(scope.pending()).toBe(false)
      while (layouts(container).length === 2) {
        expect(scope.replacing()).toBe(true)
        slices.runSlice()
      }
      expect(scope.replacing()).toBe(false)
      expect(scope.measurable()).toBe(true)
    })
  })
})

/** The diff containers in `container`, as the layout that each draws and whether it shows. */
function layouts(container: HTMLElement): Array<{ split: boolean, shown: boolean }> {
  return [
    ...container.querySelectorAll<HTMLElement>('[data-file-diff]'),
  ].map(diff => ({
    split: diff.classList.contains(diffSplitContainer),
    shown: diff.closest(`.${diffLayoutIncoming}`) === null,
  }))
}

/** A map that counts the reads of its entries. */
class ReadCountingMap<K, V> extends Map<K, V> {
  reads = 0

  override get(key: K): V | undefined {
    this.reads += 1
    return super.get(key)
  }
}

describe('DiffView gap reveals', () => {
  /** A file of 46 lines, and a hunk that changes its last line: a gap of 45 unchanged lines before it. */
  const originalFile = [
    ...Array.from({ length: 45 }, (_, i) => `line ${i + 1}`),
    'old 46',
  ].join('\n')
  const hunks: StructuredPatchHunk[] = [
    {
      oldStart: 46,
      oldLines: 1,
      newStart: 46,
      newLines: 1,
      lines: ['-old 46', '+new 46'],
    },
  ]

  /** The numbers of the gap lines that the view draws, in document order. */
  function gapLineNumbers(): number[] {
    return screen.queryAllByText(/^line \d+$/).map(content => Number(content.textContent!.slice('line '.length)))
  }

  /** The numbers from `first` to `last`. */
  function range(first: number, last: number): number[] {
    return Array.from({ length: last - first + 1 }, (_, i) => first + i)
  }

  it('mounts the lines that "expand all" reveals in slices, and inserts only those lines in each slice', () => {
    const slices = installControlledSlices(10)
    const { container } = render(() => <DiffView originalFile={originalFile} diff={patchDiff(hunks)} view="unified" />)
    slices.runAllSlices()
    const diff = container.querySelector('[data-file-diff]')!

    fireEvent.click(screen.getByText('45 lines hidden'))

    expect(gapLineNumbers()).toEqual(range(1, 10))
    const mutations = watchGapInsertions(diff)
    const perSlice: number[] = []
    try {
      while (slices.waiting() > 0) {
        slices.runSlice()
        perSlice.push(mutations.take())
      }
    }
    finally {
      mutations.stop()
    }
    expect(perSlice).toEqual([10, 10, 10, 5])
    expect(gapLineNumbers()).toEqual(range(1, 45))
  })

  it('draws no line past the end of a gap that shrinks under the lines that it revealed', () => {
    const slices = installControlledSlices(100)
    const fileOf = (lines: number) => [
      ...Array.from({ length: lines - 1 }, (_, i) => `line ${i + 1}`),
      `old ${lines}`,
    ].join('\n')
    const lastLineChange = (line: number): StructuredPatchHunk[] => [
      {
        oldStart: line,
        oldLines: 1,
        newStart: line,
        newLines: 1,
        lines: [`-old ${line}`, `+new ${line}`],
      },
    ]
    const [shape, setShape] = createSignal({
      originalFile: fileOf(10),
      diff: patchDiff(lastLineChange(10)),
    })
    const { container } = render(() => <DiffView originalFile={shape().originalFile} diff={shape().diff} view="unified" />)
    fireEvent.click(screen.getByText('9 lines hidden'))
    slices.runAllSlices()
    expect(gapLineNumbers()).toEqual(range(1, 9))

    setShape({
      originalFile: fileOf(5),
      diff: patchDiff(lastLineChange(5)),
    })
    slices.runAllSlices()

    expect(gapLineNumbers()).toEqual(range(1, 4))
    expect(container.querySelectorAll(`[data-file-diff] .${diffLine}`)).toHaveLength(4 + 2)
    expect(screen.queryByText(/lines? hidden/)).not.toBeInTheDocument()
  })

  it('keeps the lines that the reader revealed across a switch between unified and split', () => {
    const slices = installControlledSlices(10)
    const [view, setView] = createSignal<'unified' | 'split'>('unified')
    render(() => <DiffView originalFile={originalFile} diff={patchDiff(hunks)} view={view()} />)
    slices.runAllSlices()
    fireEvent.click(screen.getByText('Expand down'))
    slices.runAllSlices()
    expect(gapLineNumbers()).toEqual(range(1, 10))

    setView('split')
    slices.runAllSlices()

    // Each line draws in both columns of the split view.
    expect(gapLineNumbers()).toEqual(range(1, 10).flatMap(n => [n, n]))
    expect(screen.getByText('35 lines hidden')).toBeInTheDocument()
  })

  it('shows the new layout of a switch only once the lines that the reader revealed have mounted in it', () => {
    const slices = installControlledSlices(10)
    const [view, setView] = createSignal<'unified' | 'split'>('unified')
    const { container } = render(() => <DiffView originalFile={originalFile} diff={patchDiff(hunks)} view={view()} />)
    slices.runAllSlices()
    fireEvent.click(screen.getByText('45 lines hidden'))
    slices.runAllSlices()
    /** The cells of the gap lines that the new layout has mounted: the lines outside the groups of the hunks. */
    const incomingGapCells = () => [
      ...container.querySelectorAll(`.${diffLayoutIncoming} .${diffLine}`),
    ].filter(cell => cell.closest('[data-diff-hunk]') === null).length

    setView('split')

    // The hunk mounts in the first slice of the new layout, and the 45 revealed
    // lines take four more slices of ten rows of two cells.
    const cellsWhileHidden: number[] = []
    while (layouts(container).length === 2) {
      expect(layouts(container)).toEqual([
        { split: false, shown: true },
        { split: true, shown: false },
      ])
      cellsWhileHidden.push(incomingGapCells())
      slices.runSlice()
    }
    expect(cellsWhileHidden).toEqual([20, 40, 60, 80])
    expect(layouts(container)).toEqual([
      { split: true, shown: true },
    ])
    expect(gapLineNumbers()).toEqual(range(1, 45).flatMap(n => [n, n]))
  })

  it('shows the new layout of a switch only once the trailing gap that the reader revealed has mounted, when the last hunk starts in the last slice', () => {
    const slices = installControlledSlices(10)
    // Lines 1 to 15 change, then line 30: a gap of 14 lines between the hunks, and a trailing gap of 30.
    const file = Array.from({ length: 60 }, (_, i) => `line ${i + 1}`).join('\n')
    const twoHunks: StructuredPatchHunk[] = [
      {
        oldStart: 1,
        oldLines: 15,
        newStart: 1,
        newLines: 15,
        lines: [
          ...Array.from({ length: 15 }, (_, i) => `-line ${i + 1}`),
          ...Array.from({ length: 15 }, (_, i) => `+edited ${i + 1}`),
        ],
      },
      {
        oldStart: 30,
        oldLines: 1,
        newStart: 30,
        newLines: 1,
        lines: ['-line 30', '+edited 30'],
      },
    ]
    const [view, setView] = createSignal<'unified' | 'split'>('unified')
    const { container } = render(() => <DiffView originalFile={file} diff={patchDiff(twoHunks)} view={view()} />)
    slices.runAllSlices()
    fireEvent.click(screen.getByText('30 lines hidden'))
    slices.runAllSlices()

    setView('split')

    // The split layout mounts its 16 rows in two slices. The second also starts
    // the second hunk with the trailing gap, which shares what is left of that
    // slice, so its 30 revealed lines end three slices after it.
    let slicesWhileHidden = 0
    while (layouts(container).length === 2) {
      slices.runSlice()
      slicesWhileHidden += 1
    }
    expect(slicesWhileHidden).toBe(4)
    expect(layouts(container)).toEqual([
      { split: true, shown: true },
    ])
    expect(gapLineNumbers().filter(n => n > 30)).toEqual(range(31, 60).flatMap(n => [n, n]))
  })

  it('reports nothing pending to its readiness scope while the lines that the reader reveals mount', () => {
    const slices = installControlledSlices(10)
    let scope!: RenderReadinessScope
    render(() => {
      scope = createRenderReadinessScope()
      return <DiffView originalFile={originalFile} diff={patchDiff(hunks)} view="unified" context={{ readiness: scope }} />
    })
    slices.runAllSlices()
    expect(scope.pending()).toBe(false)

    fireEvent.click(screen.getByText('45 lines hidden'))

    expect(slices.waiting()).toBe(1)
    while (slices.waiting() > 0) {
      expect(scope.pending()).toBe(false)
      slices.runSlice()
    }
    expect(gapLineNumbers()).toEqual(range(1, 45))
  })

  it('holds its readiness scope while the lines that the reader revealed mount again for a diff of other lines', () => {
    const slices = installControlledSlices(10)
    const [diff, setDiff] = createSignal(patchDiff(hunks))
    let scope!: RenderReadinessScope
    const { container } = render(() => {
      scope = createRenderReadinessScope()
      return <DiffView originalFile={originalFile} diff={diff()} view="unified" context={{ readiness: scope }} />
    })
    slices.runAllSlices()
    fireEvent.click(screen.getByText('45 lines hidden'))
    slices.runAllSlices()

    setDiff(patchDiff([
      {
        oldStart: 46,
        oldLines: 1,
        newStart: 46,
        newLines: 1,
        lines: ['-old 46', '+newer 46'],
      },
    ]))

    // The first slice mounted the new hunk and ten of the revealed lines. The
    // slices that dispose the old lines and mount the other 35 follow.
    expect(gapLineNumbers()).toEqual(range(1, 10))
    while (scope.pending()) {
      expect(gapLineNumbers().length).toBeLessThan(45)
      slices.runSlice()
    }
    expect(gapLineNumbers()).toEqual(range(1, 45))
    expect(container.textContent).toContain('newer 46')
  })

  it('mounts the lines that "expand up" reveals from the hunk upward, and keeps them when "expand all" reveals the rest', () => {
    const slices = installControlledSlices(4)
    render(() => <DiffView originalFile={originalFile} diff={patchDiff(hunks)} view="split" />)
    slices.runAllSlices()

    fireEvent.click(screen.getByText('Expand up'))

    // Each line draws in both columns of the split view.
    expect(gapLineNumbers()).toEqual(range(42, 45).flatMap(n => [n, n]))
    slices.runAllSlices()
    expect(gapLineNumbers()).toEqual(range(36, 45).flatMap(n => [n, n]))
    const [bottomLeft, bottomRight] = screen.getAllByText('line 36')

    fireEvent.click(screen.getByText('35 lines hidden'))
    slices.runAllSlices()

    expect(gapLineNumbers()).toEqual(range(1, 45).flatMap(n => [n, n]))
    const [left, right] = screen.getAllByText('line 36')
    expect(left).toBe(bottomLeft)
    expect(right).toBe(bottomRight)
  })
})

/** Record the gap lines that each slice inserts under `container`. */
function watchGapInsertions(container: Element): { take: () => number, stop: () => void } {
  const observer = new MutationObserver(() => {})
  observer.observe(container, { childList: true, subtree: true })
  return {
    take() {
      let inserted = 0
      for (const record of observer.takeRecords()) {
        for (const node of record.addedNodes) {
          if (node instanceof HTMLElement && node.classList.contains(diffLine))
            inserted += 1
        }
      }
      return inserted
    },
    stop: () => observer.disconnect(),
  }
}

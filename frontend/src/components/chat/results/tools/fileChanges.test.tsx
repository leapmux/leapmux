import type { FileEditDiff } from '../../model/fileEditDiff'
import { render } from '@solidjs/testing-library'
import { beforeAll, describe, expect, it } from 'vitest'
import { toolCallFixture, toolRow } from '~/test-support/toolCallFixture'
import { TOOL_CALL_STATUSES } from '../../model/toolCallStatus'
import { ToolMessage } from '../ToolMessage'
import { fileChangeFailed } from './fileChanges'

// jsdom does not provide ResizeObserver, which the shared tool layout uses.
beforeAll(() => {
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
})

function edit(filePath: string): FileEditDiff {
  return { filePath, oldStr: 'before\n', newStr: 'after\n' }
}

function renderEdit(changes: FileEditDiff[]) {
  const call = toolCallFixture('edit', {
    request: { changes },
    result: { changes },
  })
  return render(() => <ToolMessage row={toolRow(call)} />)
}

describe('file change title presentation', () => {
  it('uses one self-contained title layout for single-file and multi-file statistics', () => {
    const single = renderEdit([edit('/project/a.ts')])
    const multiple = renderEdit([edit('/project/a.ts'), edit('/project/b.ts')])

    const singleBadge = single.getByTestId('git-diff-stats')
    const multipleBadges = multiple.getAllByTestId('git-diff-stats')
    const title = singleBadge.parentElement

    expect(title?.tagName).toBe('SPAN')
    expect(title?.className).not.toBe('')
    expect(multipleBadges).toHaveLength(2)
    for (const badge of multipleBadges) {
      expect(badge.parentElement?.tagName).toBe('SPAN')
      expect(badge.parentElement?.className).toBe(title?.className)
    }
  })
})

describe('fileChangeFailed', () => {
  // The three states where the tool applied nothing, so the file took nothing.
  // `declined` is the one a reader REFUSED: the tool never ran, so a row that drew
  // its changes drew the identical body a PENDING row draws.
  it.each(['failed', 'cancelled', 'declined'] as const)('answers true for a %s call, which changed no file', (status) => {
    expect(fileChangeFailed(status)).toBe(true)
  })

  it.each(['unstated', 'pending', 'in_progress', 'completed', 'incomplete'] as const)('answers false for a %s call, whose changes still stand', (status) => {
    expect(fileChangeFailed(status)).toBe(false)
  })

  // Every status takes one side, so a new one cannot arrive with no answer here.
  it('answers for every row status the model declares', () => {
    const answered = TOOL_CALL_STATUSES.filter(status => typeof fileChangeFailed(status) === 'boolean')
    expect(answered).toEqual([...TOOL_CALL_STATUSES])
  })
})

import type { FileEditDiff } from '../../model/fileEditDiff'
import type { ToolCallFixtureOptions } from '~/test-support/toolCallFixture'
import { render } from '@solidjs/testing-library'
import { beforeAll, describe, expect, it } from 'vitest'
import { settleWorkerReplies } from '~/test-support/fakeWorker'
import { toolCallFixture, toolRow } from '~/test-support/toolCallFixture'
import { failedResult, unparsedResult } from '../../model/toolCall'
import { ToolMessage } from '../ToolMessage'
import { toolCallMeta } from './meta'

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
  it('uses one self-contained title layout for single-file and multi-file statistics', async () => {
    const single = renderEdit([
      edit('/project/a.ts'),
    ])
    await settleWorkerReplies()
    const multiple = renderEdit([
      edit('/project/a.ts'),
      edit('/project/b.ts'),
    ])
    await settleWorkerReplies()

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

// The request row never reads how its call ended, and the pending row leaves when the
// result row joins the list. So a call that ended with words alone leaves the result row
// as the one row that can show the changes it asked for.
describe('a file change whose result states no changes of its own', () => {
  const asked = { filePath: '/project/a.ts', oldStr: 'ASKED-BEFORE\n', newStr: 'ASKED-AFTER\n' }

  function resultRow(options: ToolCallFixtureOptions<'edit'>) {
    return toolRow(toolCallFixture('edit', {
      request: {
        changes: [asked],
      },
      ...options,
    }), 'result', { request: true })
  }

  async function drawn(row: ReturnType<typeof resultRow>): Promise<string> {
    const { container } = render(() => <ToolMessage row={row} />)
    await settleWorkerReplies()
    return container.textContent ?? ''
  }

  it('draws the requested changes on a completed call, and offers the diff toggle', async () => {
    const row = resultRow({
      result: unparsedResult('Applied the edit.'),
    })
    const text = await drawn(row)
    expect(text).toContain('ASKED-AFTER')
    expect(text).toContain('Applied the edit.')
    expect(toolCallMeta(row).hasDiff).toBe(true)
  })

  // Nothing that a failed, interrupted or declined call asked for reached the file.
  it.each([
    ['failed', 'failed'],
    ['interrupted', 'cancelled'],
    ['declined', 'declined'],
  ] as const)('states only the reason of a %s call', async (_outcome, status) => {
    const row = resultRow({
      status,
      result: failedResult('No change was made.'),
    })
    const text = await drawn(row)
    expect(text).toContain('No change was made.')
    expect(text).not.toContain('ASKED-AFTER')
    expect(toolCallMeta(row).hasDiff).toBe(false)
  })

  // The closing copy of a turn that ended while the call never answered: the call is
  // incomplete, not failed, and nothing states what the file took. The row that ended it
  // is the one row that can still show what the call asked for.
  it('draws the requested changes of a call that ended incomplete with no result', async () => {
    const row = resultRow({ status: 'incomplete' })
    expect(row.call.result).toBeUndefined()
    const text = await drawn(row)
    expect(text).toContain('ASKED-AFTER')
    expect(text).toContain('Requested changes')
    expect(toolCallMeta(row).hasDiff).toBe(true)
  })

  it('draws the changes that landed, not the requested ones, for a typed result', async () => {
    const landed = { filePath: '/project/a.ts', oldStr: 'LANDED-BEFORE\n', newStr: 'LANDED-AFTER\n' }
    const text = await drawn(resultRow({
      result: {
        changes: [landed],
      },
    }))
    expect(text).toContain('LANDED-AFTER')
    expect(text).not.toContain('ASKED-AFTER')
  })
})

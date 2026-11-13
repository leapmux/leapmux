import type { FileEditContent, FileEditDiff } from '~/components/chat/model/fileEditDiff'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { failedResult, unparsedResult } from '~/components/chat/model/toolCall'
import { toolCallMeta } from '~/components/chat/results/tools/meta'
import { settleWorkerReplies } from '~/test-support/fakeWorker'
import { holdComputedDiffs, resetHeldComputedDiffs } from '~/test-support/heldComputedDiffs'
import { toolCallFixture, toolRow } from '~/test-support/toolCallFixture'

vi.mock('~/components/chat/diff/diffWorkerClient', async () => (await import('~/test-support/heldComputedDiffs')).heldDiffWorkerClient())

afterEach(() => {
  resetHeldComputedDiffs()
})

/**
 * One edit of `/p/a.ts` that counts each build of its copyable text. The copyable
 * text reads `notice` once per build, and nothing else on these rows reads it.
 */
function countedChange(content: FileEditContent): { change: FileEditDiff, builds: () => number } {
  let builds = 0
  const change: FileEditDiff = {
    filePath: '/p/a.ts',
    ...content,
    // The VALUE is nothing; the read is the counter. A defined string keeps the
    // getter's type inside the facts' optional member under exact-optional rules.
    get notice(): string {
      builds++
      return 'notice-read'
    },
  }
  return {
    change,
    builds: () => builds,
  }
}

describe('toolCallMeta', () => {
  it('answers the same copyable text from every read, with hasCopyable agreeing', () => {
    // hasCopyable promised that copyableContent() returns a string, and the
    // getter is cached behind it -- so repeated reads answer identically and
    // cheaply, which is what the Copy button depends on.
    const call = toolCallFixture('read', {
      result: { lines: null, fallbackContent: 'body text' },
    })
    const meta = toolCallMeta(toolRow(call))
    expect(meta.hasCopyable).toBe(true)
    expect(meta.copyableContent()).toBe('body text')
    expect(meta.copyableContent()).toBe('body text')
    expect(meta.hasCopyable).toBe(meta.copyableContent() !== null)
  })

  // `hasCopyable` is derived from the text, and the toolbar reads it on every
  // reactive pass -- so an edit row formatted its whole unified diff once per
  // streamed token. The ROW is the revision, so one build serves every pass.
  it('builds one row\'s copyable text once across separate reads', () => {
    const { change, builds } = countedChange({ oldStr: 'x', newStr: 'y', structuredPatch: null })
    const call = toolCallFixture('edit', {
      status: 'completed',
      result: {
        changes: [change],
      },
    })
    const row = toolRow(call)
    expect(toolCallMeta(row).hasCopyable).toBe(true)
    expect(builds()).toBe(1)
    expect(toolCallMeta(row).copyableContent()).toContain('/p/a.ts')
    expect(builds()).toBe(1)
    // A different row object is a different revision, and it builds its own.
    expect(toolCallMeta(toolRow(call)).copyableContent()).toContain('/p/a.ts')
    expect(builds()).toBe(2)
  })

  // The diff worker answers after the row mounts, so the first copyable text of
  // an edit is a stand-in. Keeping it for good would copy the stand-in for the
  // life of the row, and building it again on every read repeats the work.
  it('keeps an edit\'s stand-in copyable text until its diff is ready, then builds the final text once', async () => {
    const { change, builds } = countedChange({ oldStr: 'a\nb\nc\n', newStr: 'a\nB\nc\n', structuredPatch: null })
    const row = toolRow(toolCallFixture('edit', {
      status: 'completed',
      result: {
        changes: [change],
      },
    }))
    holdComputedDiffs(true)

    const standIn = toolCallMeta(row).copyableContent()
    expect(standIn).toContain('-a\n-b\n-c\n+a\n+B\n+c\n')
    expect(builds()).toBe(1)
    expect(toolCallMeta(row).copyableContent()).toBe(standIn)
    expect(builds()).toBe(1)

    holdComputedDiffs(false)
    // The released client asks the worker, which answers after this read.
    expect(toolCallMeta(row).copyableContent()).toBe(standIn)
    expect(builds()).toBe(1)
    await settleWorkerReplies()

    const final = toolCallMeta(row).copyableContent()
    expect(final).toContain(' a\n-b\n+B\n c\n')
    expect(builds()).toBe(2)
    expect(toolCallMeta(row).copyableContent()).toBe(final)
    expect(builds()).toBe(2)
  })

  it('keeps the copyable text of an edit whose patch came with it from the first read', () => {
    const { change, builds } = countedChange({
      structuredPatch: [
        {
          oldStart: 1,
          oldLines: 1,
          newStart: 1,
          newLines: 1,
          lines: ['-x', '+y'],
        },
      ],
    })
    const row = toolRow(toolCallFixture('edit', {
      status: 'completed',
      result: {
        changes: [change],
      },
    }))
    holdComputedDiffs(true)

    expect(toolCallMeta(row).copyableContent()).toContain('-x\n+y\n')
    expect(builds()).toBe(1)
    toolCallMeta(row).copyableContent()
    expect(builds()).toBe(1)
  })

  // The text is final only once EVERY change states its final diff: a first
  // change whose patch came with it must not end the stand-in of a later one.
  it('keeps the stand-in copyable text of a row whose first change has its patch until the diff of a later change is ready', async () => {
    const patched: FileEditDiff = {
      filePath: '/p/patched.ts',
      structuredPatch: [
        {
          oldStart: 1,
          oldLines: 1,
          newStart: 1,
          newLines: 1,
          lines: ['-x', '+y'],
        },
      ],
    }
    const { change, builds } = countedChange({ oldStr: 'a\nb\nc\n', newStr: 'a\nB\nc\n', structuredPatch: null })
    const row = toolRow(toolCallFixture('edit', {
      status: 'completed',
      result: {
        changes: [
          patched,
          change,
        ],
      },
    }))
    holdComputedDiffs(true)

    const standIn = toolCallMeta(row).copyableContent()
    expect(standIn).toContain('-x\n+y\n')
    expect(standIn).toContain('-a\n-b\n-c\n+a\n+B\n+c\n')
    toolCallMeta(row).copyableContent()
    expect(builds()).toBe(1)

    holdComputedDiffs(false)
    toolCallMeta(row).copyableContent()
    await settleWorkerReplies()

    const final = toolCallMeta(row).copyableContent()
    expect(final).toContain('-x\n+y\n')
    expect(final).toContain(' a\n-b\n+B\n c\n')
    expect(final).not.toContain('-a\n-b\n-c\n+a\n+B\n+c\n')
    expect(builds()).toBe(2)
  })

  it('keeps a requested edit\'s stand-in copyable text until its diff is ready, on a paired request row', async () => {
    const { change, builds } = countedChange({ oldStr: 'a\nb\nc\n', newStr: 'a\nB\nc\n', structuredPatch: null })
    const call = toolCallFixture('edit', {
      status: 'completed',
      request: {
        changes: [change],
      },
      result: {
        changes: [
          {
            filePath: '/p/a.ts',
            oldStr: 'x',
            newStr: 'y',
            structuredPatch: null,
          },
        ],
      },
    })
    const requestRow = toolRow(call, 'request', { result: true })
    holdComputedDiffs(true)

    expect(toolCallMeta(requestRow).copyableContent()).toContain('-a\n-b\n-c\n+a\n+B\n+c\n')
    expect(builds()).toBe(1)
    toolCallMeta(requestRow).copyableContent()
    expect(builds()).toBe(1)

    holdComputedDiffs(false)
    toolCallMeta(requestRow).copyableContent()
    await settleWorkerReplies()

    expect(toolCallMeta(requestRow).copyableContent()).toContain(' a\n-b\n+B\n c\n')
    expect(builds()).toBe(2)
    toolCallMeta(requestRow).copyableContent()
    expect(builds()).toBe(2)
  })

  it('ors an unparsed result into the request meta and keeps a requested diff\'s hasDiff', () => {
    const call = toolCallFixture('edit', {
      status: 'completed',
      request: {
        changes: [
          {
            filePath: '/p/a.ts',
            oldStr: 'x',
            newStr: 'y',
            structuredPatch: null,
          },
        ],
      },
      result: unparsedResult('raw payload'),
    })
    const meta = toolCallMeta(toolRow(call))
    expect(meta.hasCopyable).toBe(true)
    expect(meta.copyableContent()).toContain('raw payload')
    expect(meta.hasDiff).toBe(true)
  })

  it('answers about the request alone on a paired request row', () => {
    const call = toolCallFixture('edit', {
      status: 'completed',
      request: {
        changes: [
          {
            filePath: '/p/a.ts',
            oldStr: 'x',
            newStr: 'y',
            structuredPatch: null,
          },
        ],
      },
      result: {
        changes: [
          {
            filePath: '/p/a.ts',
            oldStr: 'x',
            newStr: 'y',
            structuredPatch: null,
          },
        ],
      },
    })
    const requestRow = toolRow(call, 'request', { result: true })
    const meta = toolCallMeta(requestRow)
    expect(meta.hasDiff).toBe(true)
  })

  it('keeps result text and extra content off a paired request row', () => {
    const call = toolCallFixture('execute', {
      request: { command: 'printf request' },
      result: {
        commands: [
          { output: 'result output' },
        ],
        unresolvedTerminals: [],
      },
      extraContent: [
        { type: 'text', text: 'extra output' },
      ],
    })
    expect(toolCallMeta(toolRow(call, 'request', { result: true })).copyableContent()).toBe('printf request')
  })

  it('keeps request text off a result row whose request row is visible', () => {
    const call = toolCallFixture('execute', {
      request: { command: 'printf request' },
      result: {
        commands: [
          { output: '' },
        ],
        unresolvedTerminals: [],
      },
    })
    expect(toolCallMeta(toolRow(call, 'result', { request: true })).copyableContent()).toBeNull()
  })

  it('uses request text on a result row when the fetched request is not visible', () => {
    const call = toolCallFixture('execute', {
      request: { command: 'printf request' },
      result: {
        commands: [
          { output: '' },
        ],
        unresolvedTerminals: [],
      },
    })
    expect(toolCallMeta(toolRow(call, 'result', { request: false })).copyableContent()).toBe('printf request')
  })

  // A call the turn CUT keeps the body it printed so far, and it is the one status
  // whose result is partial: a call still running carries no result at all.
  it('answers about the partial result of an update row with no result row', () => {
    const call = toolCallFixture('read', {
      status: 'cancelled',
      result: { lines: null, fallbackContent: 'partial' },
    })
    const meta = toolCallMeta(toolRow(call, 'update'))
    expect(meta.copyableContent()).toBe('partial')
  })

  it('states a failure\'s text as the copyable content', () => {
    const call = toolCallFixture('fetch', {
      status: 'failed',
      result: failedResult('boom'),
    })
    const meta = toolCallMeta(toolRow(call))
    expect(meta.copyableContent()).toBe('boom')
    expect(meta.hasCopyable).toBe(true)
  })

  // The button must describe the text it writes. A command whose run printed nothing
  // copies the COMMAND, and reading the words off the result side alone offered that
  // command under the bare word "Copy".
  it('words Copy from the side that stated the text', () => {
    const call = toolCallFixture('execute', {
      status: 'completed',
      request: { command: 'ls -la' },
      result: {
        commands: [
          { output: '' },
        ],
        unresolvedTerminals: [],
      },
    })
    const meta = toolCallMeta(toolRow(call))
    expect(meta.copyableContent()).toBe('ls -la')
    expect(meta.copyLabel).toBe('Copy Command')
  })

  it('keeps the result body\'s own words when that body stated the text', () => {
    const call = toolCallFixture('execute', {
      status: 'completed',
      request: { command: 'ls -la' },
      result: {
        commands: [
          { output: 'a\nb' },
        ],
        unresolvedTerminals: [],
      },
    })
    const meta = toolCallMeta(toolRow(call))
    expect(meta.copyableContent()).toBe('a\nb')
    // The output is what Copy writes, and "Copy Command" would name the wrong text.
    expect(meta.copyLabel).toBeUndefined()
  })

  it('takes the expand words from the request when no result body clips', () => {
    const longCommand = Array.from({ length: 12 }, (_, index) => `echo line-${index}`).join('\n')
    // A short failure clips nothing, so the only thing this row can un-clip is its
    // command -- and the command's own words say so.
    const shortFailure = toolCallFixture('execute', {
      status: 'failed',
      request: { command: longCommand },
      result: failedResult('boom'),
    })
    expect(toolCallMeta(toolRow(shortFailure)).expandLabel).toBe('Show full command')
    // A LONG failure is what the row clips, and `plainMeta` words nothing, so the
    // toolbar keeps its own last resort rather than promising the command.
    const longFailure = toolCallFixture('execute', {
      status: 'failed',
      request: { command: longCommand },
      result: failedResult(Array.from({ length: 12 }, (_, index) => `line ${index}`).join('\n')),
    })
    expect(toolCallMeta(toolRow(longFailure)).expandLabel).toBeUndefined()
  })

  it('keeps the request expand words when a typed result does not clip', () => {
    const call = toolCallFixture('execute', {
      request: {
        command: 'compound command',
        actions: [
          {
            kind: 'read',
            command: 'cat a.ts',
            name: 'a.ts',
            path: '/repo/a.ts',
          },
        ],
      },
      result: {
        commands: [
          { output: 'short output' },
        ],
        unresolvedTerminals: [],
      },
    })

    expect(toolCallMeta(toolRow(call)).expandLabel).toBe('Show all actions')
  })

  // A running TodoWrite draws its whole checklist and has no result behind it, so
  // `resultMeta` never runs. The row had nothing to copy and nothing to quote.
  it('offers the carried checklist while no result answers the to-do call', () => {
    const call = toolCallFixture('todo', {
      status: 'in_progress',
      request: {
        items: [
          {
            rowKey: '0:carried',
            content: 'carried task',
            status: 'pending',
            activeForm: 'Doing it',
          },
        ],
      },
    })
    const meta = toolCallMeta(toolRow(call, 'request'))
    expect(meta.hasCopyable).toBe(true)
    expect(meta.copyableContent()).toBe('- [ ] carried task')
  })

  it('states the saved list, not the carried one, once a result answers the to-do call', () => {
    const call = toolCallFixture('todo', {
      status: 'completed',
      request: {
        items: [
          {
            rowKey: '0:carried',
            content: 'carried task',
            status: 'pending',
            activeForm: 'Doing it',
          },
        ],
      },
      result: {
        items: [
          {
            rowKey: '0:saved',
            content: 'saved task',
            status: 'completed',
            activeForm: 'Doing it',
          },
        ],
      },
    })
    const meta = toolCallMeta(toolRow(call))
    expect(meta.copyableContent()).toBe('- [x] saved task')
  })
})

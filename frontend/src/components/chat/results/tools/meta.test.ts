import { describe, expect, it } from 'vitest'
import { failedResult, unparsedResult } from '~/components/chat/model/toolCall'
import { toolCallMeta } from '~/components/chat/results/tools/meta'
import { toolCallFixture, toolRow } from '~/test-support/toolCallFixture'

describe('toolCallMeta', () => {
  it('answers the same copyable text from every read, with hasCopyable agreeing', () => {
    // hasCopyable promised that copyableContent() returns a string, and the
    // getter is cached behind it -- so repeated reads answer identically and
    // cheaply, which is what the Copy button depends on.
    const call = toolCallFixture('read', { result: { lines: null, fallbackContent: 'body text' } })
    const meta = toolCallMeta(toolRow(call))
    expect(meta.hasCopyable).toBe(true)
    expect(meta.copyableContent()).toBe('body text')
    expect(meta.copyableContent()).toBe('body text')
    expect(meta.hasCopyable).toBe(meta.copyableContent() !== null)
  })

  // `hasCopyable` is derived from the text, and the toolbar reads it on every
  // reactive pass -- so an edit row formatted its whole unified diff once per
  // streamed token. The ROW is the revision, so one build serves every pass.
  // `notice` is read by the copyable text alone, which is what makes it the counter.
  it('builds one row\'s copyable text once across separate reads', () => {
    let copyableBuilds = 0
    const change = {
      filePath: '/p/a.ts',
      oldStr: 'x',
      newStr: 'y',
      structuredPatch: null,
      // The VALUE is nothing; the read is the counter. A defined string keeps the
      // getter's type inside the facts' optional member under exact-optional rules.
      get notice(): string {
        copyableBuilds++
        return 'notice-read'
      },
    }
    const call = toolCallFixture('edit', { status: 'completed', result: { changes: [change] } })
    const row = toolRow(call)
    expect(toolCallMeta(row).hasCopyable).toBe(true)
    expect(copyableBuilds).toBe(1)
    expect(toolCallMeta(row).copyableContent()).toContain('/p/a.ts')
    expect(copyableBuilds).toBe(1)
    // A different row object is a different revision, and it builds its own.
    expect(toolCallMeta(toolRow(call)).copyableContent()).toContain('/p/a.ts')
    expect(copyableBuilds).toBe(2)
  })

  it('ors an unparsed result into the request meta and keeps a requested diff\'s hasDiff', () => {
    const call = toolCallFixture('edit', {
      status: 'completed',
      request: { changes: [{ filePath: '/p/a.ts', oldStr: 'x', newStr: 'y', structuredPatch: null }] },
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
      request: { changes: [{ filePath: '/p/a.ts', oldStr: 'x', newStr: 'y', structuredPatch: null }] },
      result: { changes: [{ filePath: '/p/a.ts', oldStr: 'x', newStr: 'y', structuredPatch: null }] },
    })
    const requestRow = toolRow(call, 'request', { result: true })
    const meta = toolCallMeta(requestRow)
    expect(meta.hasDiff).toBe(true)
  })

  it('keeps result text and extra content off a paired request row', () => {
    const call = toolCallFixture('execute', {
      request: { command: 'printf request' },
      result: { commands: [{ output: 'result output' }], unresolvedTerminals: [] },
      extraContent: [{ type: 'text', text: 'extra output' }],
    })
    expect(toolCallMeta(toolRow(call, 'request', { result: true })).copyableContent()).toBe('printf request')
  })

  it('keeps request text off a result row whose request row is visible', () => {
    const call = toolCallFixture('execute', {
      request: { command: 'printf request' },
      result: { commands: [{ output: '' }], unresolvedTerminals: [] },
    })
    expect(toolCallMeta(toolRow(call, 'result', { request: true })).copyableContent()).toBeNull()
  })

  it('uses request text on a result row when the fetched request is not visible', () => {
    const call = toolCallFixture('execute', {
      request: { command: 'printf request' },
      result: { commands: [{ output: '' }], unresolvedTerminals: [] },
    })
    expect(toolCallMeta(toolRow(call, 'result', { request: false })).copyableContent()).toBe('printf request')
  })

  // A call the turn CUT keeps the body it printed so far, and it is the one status
  // whose result is partial: a call still running carries no result at all.
  it('answers about the partial result of an update row with no result row', () => {
    const call = toolCallFixture('read', { status: 'cancelled', result: { lines: null, fallbackContent: 'partial' } })
    const meta = toolCallMeta(toolRow(call, 'update'))
    expect(meta.copyableContent()).toBe('partial')
  })

  it('states a failure\'s text as the copyable content', () => {
    const call = toolCallFixture('fetch', { status: 'failed', result: failedResult('boom') })
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
      result: { commands: [{ output: '' }], unresolvedTerminals: [] },
    })
    const meta = toolCallMeta(toolRow(call))
    expect(meta.copyableContent()).toBe('ls -la')
    expect(meta.copyLabel).toBe('Copy Command')
  })

  it('keeps the result body\'s own words when that body stated the text', () => {
    const call = toolCallFixture('execute', {
      status: 'completed',
      request: { command: 'ls -la' },
      result: { commands: [{ output: 'a\nb' }], unresolvedTerminals: [] },
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
    const shortFailure = toolCallFixture('execute', { status: 'failed', request: { command: longCommand }, result: failedResult('boom') })
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
        actions: [{ kind: 'read', command: 'cat a.ts', name: 'a.ts', path: '/repo/a.ts' }],
      },
      result: { commands: [{ output: 'short output' }], unresolvedTerminals: [] },
    })

    expect(toolCallMeta(toolRow(call)).expandLabel).toBe('Show all actions')
  })

  // A running TodoWrite draws its whole checklist and has no result behind it, so
  // `resultMeta` never runs. The row had nothing to copy and nothing to quote.
  it('offers the carried checklist while no result answers the to-do call', () => {
    const call = toolCallFixture('todo', {
      status: 'in_progress',
      request: { items: [{ rowKey: '0:carried', content: 'carried task', status: 'pending', activeForm: 'Doing it' }] },
    })
    const meta = toolCallMeta(toolRow(call, 'request'))
    expect(meta.hasCopyable).toBe(true)
    expect(meta.copyableContent()).toBe('- [ ] carried task')
  })

  it('states the saved list, not the carried one, once a result answers the to-do call', () => {
    const call = toolCallFixture('todo', {
      status: 'completed',
      request: { items: [{ rowKey: '0:carried', content: 'carried task', status: 'pending', activeForm: 'Doing it' }] },
      result: { items: [{ rowKey: '0:saved', content: 'saved task', status: 'completed', activeForm: 'Doing it' }] },
    })
    const meta = toolCallMeta(toolRow(call))
    expect(meta.copyableContent()).toBe('- [x] saved task')
  })
})

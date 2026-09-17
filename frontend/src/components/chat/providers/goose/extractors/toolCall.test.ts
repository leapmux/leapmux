import type { ToolCallIR } from '../../../ir/toolCall'
import { describe, expect, it } from 'vitest'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { isFailedResult, isUnparsedResult, typedResult } from '../../../ir/toolCall'
import { acpToolCallIR } from '../../acp/extractors/toolCall'
import { gooseToolCallAdapter } from './toolCall'

function delegate(rawInput: Record<string, unknown>, tool: Record<string, unknown> = {}): ToolCallIR {
  return acpToolCallIR({
    sessionUpdate: 'tool_call',
    toolCallId: 'goose-tool',
    status: 'pending',
    kind: 'other',
    title: 'delegate',
    _meta: { goose: { toolCall: { toolName: 'delegate', extensionName: 'summon' } } },
    rawInput,
    ...tool,
  }, gooseToolCallAdapter, undefined)
}

describe('goose delegate launches', () => {
  it('asks with the instructions the launch carried', () => {
    const call = delegate({ instructions: 'Inspect project structure', source: 'explore' })
    expect(call.kind).toBe('agent')
    expect(call.kind === 'agent' ? call.request.agentType : undefined).toBe('explore')
    expect(call.result).toBeUndefined()
  })

  // Goose states its own fallback, so the shared `Task` never reaches this call.
  it('keeps the fallback Goose gives a launch that carries nothing', () => {
    const call = delegate({})
    expect(call.kind === 'agent' ? call.request.description : '').toBe('Delegate task')
  })

  it('reports the run once the call finished', () => {
    const call = delegate({ instructions: 'Inspect project structure' }, {
      sessionUpdate: 'tool_call_update',
      status: 'completed',
      content: [{ type: 'content', content: { type: 'text', text: 'Found two' } }],
    })
    expect(call.kind).toBe('agent')
    const source = call.kind === 'agent' ? typedResult(call)?.agents[0] : undefined
    expect(source).toMatchObject({ description: 'Inspect project structure', body: 'Found two' })
  })
})

// The frames below are the ones a live `goose acp` session sent for a
// `read_image` call: the image rides as an ACP content block beside the text
// summary, the path is in `rawInput.source`, and the size is in `rawOutput`.
const PNG = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=='

function readImage(tool: Record<string, unknown> = {}): ToolCallIR {
  return acpToolCallIR({
    sessionUpdate: 'tool_call_update',
    toolCallId: 'goose-image',
    status: 'completed',
    title: 'read image · /repo/dot.png',
    _meta: { goose: { toolCall: { toolName: 'read_image', extensionName: 'developer' } } },
    rawInput: { source: '/repo/dot.png' },
    content: [
      { type: 'content', content: { type: 'text', text: 'Loaded image from /repo/dot.png (70 bytes, image/png, 1x1).' } },
      { type: 'content', content: { type: 'image', data: PNG, mimeType: 'image/png' } },
    ],
    rawOutput: { source: '/repo/dot.png', mimeType: 'image/png', width: 1, height: 1, bytes: 70, originalWidth: 4, originalHeight: 4 },
    ...tool,
  }, gooseToolCallAdapter, undefined)
}

describe('goose read_image calls', () => {
  it('identifies the file and the size the result states', () => {
    const call = readImage()
    expect(call.name).toBe('read_image')
    expect(call.kind).toBe('read')
    expect(call.label).toBe('Read Image')
    expect(call.images).toEqual([{
      data: PNG,
      mimeType: 'image/png',
      filePath: '/repo/dot.png',
      dimensions: { width: 1, height: 1 },
    }])
  })

  // The size Goose reports is of the bytes it SENT. `originalWidth` describes the
  // file before Goose scaled it down, so reading it would size the box for an
  // image the row never draws.
  it('reports the sent size, not the file the image came from', () => {
    expect(readImage().images[0]?.dimensions).toEqual({ width: 1, height: 1 })
  })

  it('leaves the image alone when the result states neither the path nor the size', () => {
    const call = readImage({ rawInput: {}, rawOutput: { mimeType: 'image/png' } })
    expect(call.images).toEqual([{ data: PNG, mimeType: 'image/png' }])
  })

  // A failed read carries no image and no result record, so the call must not
  // invent a size or a path for one.
  it('carries no image for a failed read', () => {
    const call = readImage({ status: 'failed', content: [{ type: 'content', content: { type: 'text', text: 'Error: no such file' } }], rawOutput: undefined })
    expect(call.images).toEqual([])
  })
})

/**
 * A shell call whose output came from a TERMINAL, which states a `signal` for a
 * process the OS killed.
 *
 * The branch that folds Goose's own exit code into that command used to spread the
 * terminal's entry and write `exitCode` beside the signal it already carried -- the
 * one pair `CommandExit` exists to forbid -- and `commandExit` reads the code first,
 * so the row drew the success glyph for a process that was killed. The exit half is
 * now replaced rather than merged.
 */
describe('goose shell exit', () => {
  function shell(tool: Record<string, unknown>): ToolCallIR {
    return acpToolCallIR({
      sessionUpdate: 'tool_call',
      toolCallId: 'goose-shell',
      status: 'completed',
      kind: 'execute',
      title: 'shell',
      _meta: { goose: { toolCall: { toolName: 'shell', extensionName: 'developer' } } },
      rawInput: { command: 'sleep 99' },
      ...tool,
    }, gooseToolCallAdapter, undefined)
  }

  it('states the code Goose reported', () => {
    const call = shell({ rawOutput: { stdout: 'done\n', exit_code: 3 } })
    const result = call.kind === 'execute' ? typedResult(call) : undefined
    expect(result?.commands[0]).toMatchObject({ exitCode: 3 })
    expect(result?.commands[0]).not.toHaveProperty('signal')
  })

  it('never carries an exit code and a signal at once', () => {
    const call = shell({ rawOutput: { stdout: 'done\n', exit_code: 0 } })
    const command = call.kind === 'execute' ? typedResult(call)?.commands[0] : undefined
    const keys = Object.keys(command ?? {})
    expect(keys.includes('exitCode') && keys.includes('signal')).toBe(false)
  })
})

/**
 * A developer-extension call reads the facts AGAIN at the kind Goose's table states.
 *
 * The remap used to be a hand-written clone of the facts, so `args` skipped the shared
 * `locations` recovery: a file tool that states its file only there drew with no path.
 */
describe('goose developer tool facts', () => {
  function developer(name: string, rawInput: Record<string, unknown>, tool: Record<string, unknown> = {}): ToolCallIR {
    return acpToolCallIR({
      sessionUpdate: 'tool_call_update',
      toolCallId: 'goose-tool',
      status: 'completed',
      kind: 'other',
      title: name,
      _meta: { goose: { toolCall: { toolName: name, extensionName: 'developer' } } },
      rawInput,
      ...tool,
    }, gooseToolCallAdapter, undefined)
  }

  it('recovers the file a read states only in its locations', () => {
    const call = developer('read', {}, {
      locations: [{ path: '/p/a.ts' }],
      content: [{ type: 'content', content: { text: 'const a = 1' } }],
    })
    expect(call.kind).toBe('read')
    expect(call.kind === 'read' && call.request.path).toBe('/p/a.ts')
  })

  it('reports the subagent run of a retained row the frame never completed', () => {
    const call = acpToolCallIR({
      sessionUpdate: 'tool_call_update',
      toolCallId: 'goose-tool',
      status: 'in_progress',
      kind: 'other',
      title: 'delegate',
      _meta: { goose: { toolCall: { toolName: 'delegate', extensionName: 'summon' } } },
      rawInput: { instructions: 'Inspect it' },
      content: [{ type: 'content', content: { text: 'Found two' } }],
    }, gooseToolCallAdapter, undefined, MessageCompletion.COMPLETE)
    expect(call.kind).toBe('agent')
    expect(call.kind === 'agent' ? typedResult(call)?.agents[0]?.body : undefined).toBe('Found two')
  })

  // The kind table already states `list` for this tool, so the shared build supplies
  // the path -- under every alias, which the hand-written `input.path` missed.
  it('reads the directory a tree call states under any path alias', () => {
    const aliased = developer('tree', { file_path: '/p/src' })
    expect(aliased.kind).toBe('list')
    expect(aliased.kind === 'list' && aliased.request.path).toBe('/p/src')
    const none = developer('tree', {})
    expect(none.kind === 'list' && none.request.path).toBe('.')
    expect(none.label).toBe('List Files')
  })

  // The branch art and the line counts are the point, so the words the tool wrote are
  // the answer -- and a tree that FAILED states a failure rather than an unread body.
  //
  // A tree the reader STOPPED keeps the branches it had drawn: the shared ladder tests
  // the provider's own fault flag, and a cancelled call carries none. The `Interrupted`
  // header still reaches the row from its status.
  it.each([['completed', false], ['failed', true], ['cancelled', false]])('answers a %s tree with the words it printed', (status, failed) => {
    const call = developer('tree', { path: '/p/src' }, { status, content: [{ type: 'content', content: { text: 'src\n|-- a.ts' } }] })
    expect(call.kind).toBe('list')
    expect(isFailedResult(call.result)).toBe(failed)
    expect(isFailedResult(call.result) || isUnparsedResult(call.result) ? call.result.text : undefined).toBe('src\n|-- a.ts')
  })
})

/**
 * The shared ACP ladder states a failed call's reason for every kind it builds, and
 * this branch answers ahead of that ladder. Without the same test a failed server call
 * drew its (usually empty) card and `[no output]` where the reason belongs.
 */
describe('goose extension tool calls', () => {
  function extensionCall(status: string, tool: Record<string, unknown> = {}): ToolCallIR {
    return acpToolCallIR({
      sessionUpdate: 'tool_call_update',
      toolCallId: 'goose-tool',
      status,
      kind: 'other',
      title: 'recall',
      _meta: { goose: { toolCall: { toolName: 'recall', extensionName: 'memory' } } },
      rawInput: { query: 'needle' },
      ...tool,
    }, gooseToolCallAdapter, undefined)
  }

  it('states the reason a failed server call gave', () => {
    const call = extensionCall('failed', { content: [{ type: 'content', content: { text: 'the memory server is not running' } }] })
    expect(call.kind).toBe('mcp')
    expect(isFailedResult(call.result) && call.result.text).toBe('the memory server is not running')
  })

  // A call the reader STOPPED is not a failure. The blocks that arrived are the part
  // of the answer they asked to see, so the card keeps them; the `Interrupted` header
  // comes from the row's own status.
  it('keeps the card a cancelled server call had built', () => {
    const call = extensionCall('cancelled', { content: [{ type: 'content', content: { type: 'text', text: 'one memory so far' } }] })
    expect(call.kind).toBe('mcp')
    expect(isFailedResult(call.result)).toBe(false)
    expect(call.result).toStrictEqual({ content: [{ type: 'text', text: 'one memory so far' }] })
  })

  it('draws the card of a server call that answered', () => {
    const call = extensionCall('completed', { content: [{ type: 'content', content: { type: 'text', text: 'two memories' } }] })
    expect(call.kind).toBe('mcp')
    expect(call.result).toMatchObject({ content: [{ type: 'text', text: 'two memories' }] })
  })

  it('answers nothing while the server call still runs', () => {
    expect(extensionCall('in_progress').result).toBeUndefined()
  })
})

/**
 * The checklist survives every outcome, because the tool ASKED for it.
 *
 * The branch used to skip a failed or cancelled call, which dropped the row to the
 * generic server card: a checklist drew as a wrench above its raw markdown.
 */
describe('goose to-do lists', () => {
  const MARKDOWN = '- [x] Completed task\n- [ ] Pending task'

  function todoCall(status: string, tool: Record<string, unknown> = {}): ToolCallIR {
    return acpToolCallIR({
      sessionUpdate: 'tool_call_update',
      toolCallId: 'goose-todo',
      status,
      kind: 'other',
      title: 'todo write',
      _meta: { goose: { toolCall: { toolName: 'todo__todo_write', extensionName: 'todo' } } },
      rawInput: { content: MARKDOWN },
      ...tool,
    }, gooseToolCallAdapter, undefined)
  }

  const requested = [
    { rowKey: '0:Completed task', content: 'Completed task', status: 'completed', activeForm: '' },
    { rowKey: '1:Pending task', content: 'Pending task', status: 'pending', activeForm: '' },
  ]

  it('answers a completed list with the tasks it saved', () => {
    const call = todoCall('completed', { content: [{ type: 'content', content: { type: 'text', text: 'Updated' } }] })
    expect(call.kind).toBe('todo')
    expect(call.kind === 'todo' ? call.request.items : undefined).toStrictEqual(requested)
    expect(call.kind === 'todo' ? typedResult(call)?.items : undefined).toStrictEqual(requested)
  })

  // The reason, under the checklist's OWN kind. A status test here sent the row to
  // the generic card, where the reason sat under a wrench.
  it('states the reason a failed list gave', () => {
    const call = todoCall('failed', { content: [{ type: 'content', content: { text: 'the todo file is read only' } }] })
    expect(call.kind).toBe('todo')
    expect(call.kind === 'todo' ? call.request.items : undefined).toStrictEqual(requested)
    expect(isFailedResult(call.result) && call.result.text).toBe('the todo file is read only')
  })

  // A call the reader STOPPED keeps the list it collected. The row marks it partial
  // from its own status, so nothing here states that.
  it('keeps the list a cancelled call collected', () => {
    const call = todoCall('cancelled', { content: [{ type: 'content', content: { text: 'stopped' } }] })
    expect(call.kind).toBe('todo')
    expect(isFailedResult(call.result)).toBe(false)
    expect(call.kind === 'todo' ? typedResult(call)?.items : undefined).toStrictEqual(requested)
  })

  it('answers nothing while the list is still being written', () => {
    const call = todoCall('in_progress')
    expect(call.kind).toBe('todo')
    expect(call.result).toBeUndefined()
  })

  // A list with headings and nesting draws as the prose the agent wrote, and the
  // same ladder applies to it.
  it.each([
    ['failed', true],
    ['cancelled', false],
  ])('answers a %s prose list through the same ladder', (status, failed) => {
    const call = todoCall(status, {
      rawInput: { content: '## Tasks\n\n- [ ] **Inspect** files' },
      content: [{ type: 'content', content: { text: 'the todo file is read only' } }],
    })
    expect(call.kind).toBe('todo')
    expect(isFailedResult(call.result)).toBe(failed)
    expect(call.kind === 'todo' ? typedResult(call)?.note : undefined).toBe(failed ? undefined : '## Tasks\n\n- [ ] **Inspect** files')
  })
})

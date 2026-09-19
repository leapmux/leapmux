import type { ChatRowIR, ToolCallRow } from '../../../ir/row'
import type { ToolKind } from '../../../ir/toolKind'
import type { ToolSpanSides } from '~/components/chat/rowExtractionTypes'
import { describe, expect, it } from 'vitest'
import { CODEX_ITEM } from '~/generated/contracts/codex-protocol'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { fileEditHasDiff } from '../../../ir/fileEditDiff'
import { TOOL_KINDS } from '../../../ir/toolKind'
import { DEFAULT_TOOL_REQUESTS } from '../../defaultToolRequests'
import { CODEX_STATUS } from '../itemVocabulary'
import { CODEX_TOOL_READERS, codexExtractRow, codexItemKind, codexPayloadFor, codexToolFacts } from './row'

const NO_SIDES: ToolSpanSides = { current: undefined, request: undefined, result: undefined, role: 'other' }

// The completion rides BOTH carriers, the way the pipeline delivers it: the parser
// copies it onto the parsed message, and the row extractor takes it as its own field.
// The span's `finished` test reads the first and the row status reads the second, so a
// fixture that set one alone exercised half of a retained row.
function toolRow(item: Record<string, unknown>, completion?: MessageCompletion): ToolCallRow | null {
  const parsed = { wrapper: null, topLevel: { item }, parentObject: { item }, rawText: '', supplementalContent: undefined, messageMetadata: undefined, completion }
  const row: ChatRowIR | null = codexExtractRow({
    parsed,
    category: { kind: 'tool_use' },
    sides: NO_SIDES,
    completion,
  } as never)
  return row && row.kind === 'tool' ? row : null
}

describe('codex file change kinds', () => {
  // A rename states its DESTINATION in `kind.movePath` and its source in `path`.
  // Both halves used to read the same key, and the kind never reached `move` at
  // all, so the row drew the edit icon and "old.ts → old.ts".
  it('reads a rename as a move from its source to its destination', () => {
    const row = toolRow({
      type: CODEX_ITEM.FileChange,
      status: 'completed',
      changes: [{ path: 'old/name.ts', kind: { type: 'move', movePath: 'new/name.ts' } }],
    })
    expect(row?.call.kind).toBe('move')
    const changes = row?.call.kind === 'move' ? row.call.request.changes : []
    expect(changes[0]?.previousPath).toBe('old/name.ts')
    expect(changes[0]?.filePath).toBe('new/name.ts')
  })
})

describe('codex retained outcome', () => {
  // Codex was the only provider that did not fold LeapMux's own reading of how the
  // turn ended into the row status, which `ToolCallCommon.status` requires of every
  // provider. A turn the reader stopped leaves the last `inProgress` frame stored,
  // so the replayed row spun for the life of the transcript.
  it('words an interrupted command cancelled rather than running', () => {
    const row = toolRow({
      type: CODEX_ITEM.CommandExecution,
      status: 'inProgress',
      command: 'sleep 30',
    }, MessageCompletion.INTERRUPTED)
    expect(row?.call.status).toBe('cancelled')
  })

  it('leaves a completed command alone', () => {
    const row = toolRow({ type: CODEX_ITEM.CommandExecution, status: 'completed', command: 'ls' })
    expect(row?.call.status).toBe('completed')
  })
})

/**
 * A `fileChange` that did not land.
 *
 * The result used to key on the ITEM's wire word, which a retained row leaves at
 * `inProgress` -- so the row dropped the aggregated output the worker stored beside
 * it, and that output is the apply-patch error: the one thing that states WHY the
 * change did not land.
 */
describe('codex fileChange results', () => {
  const change = { path: 'src/a.ts', kind: 'update', diff: '@@ -1,1 +1,1 @@\n-old\n+new' }

  it('draws the diff of a change that landed', () => {
    const row = toolRow({ type: CODEX_ITEM.FileChange, status: 'completed', changes: [change] })
    expect(row?.call.result).toMatchObject({ changes: [{ filePath: 'src/a.ts' }] })
  })

  it.each(['failed', 'declined'])('states the apply-patch error of a %s change', (status) => {
    const row = toolRow({
      type: CODEX_ITEM.FileChange,
      status,
      changes: [change],
      aggregatedOutput: 'apply_patch: context does not match',
    })
    expect(row?.call.result).toEqual({ failure: true, text: 'apply_patch: context does not match' })
  })

  // The turn stopped while the change ran, so the last stored frame still reads
  // `inProgress`. LeapMux's own completion is what says the call ended.
  it('states the aggregated output of a retained change', () => {
    const row = toolRow({
      type: CODEX_ITEM.FileChange,
      status: 'inProgress',
      changes: [change],
      aggregatedOutput: 'apply_patch: interrupted',
    }, MessageCompletion.INTERRUPTED)
    expect(row?.call.status).toBe('cancelled')
    expect(row?.call.result).toEqual({ failure: true, text: 'apply_patch: interrupted' })
  })

  // A change still running has no outcome to state, so it draws its request alone.
  it('states no result while the change runs', () => {
    const row = toolRow({ type: CODEX_ITEM.FileChange, status: 'inProgress', changes: [change] })
    expect(row?.call.result).toBeUndefined()
  })

  it('states no result for a failed change that reported no output', () => {
    const row = toolRow({ type: CODEX_ITEM.FileChange, status: 'failed', changes: [change] })
    expect(row?.call.result).toBeUndefined()
  })

  // The diff of a change that never landed would report an edit the file never
  // received, so a failure states words and no diff.
  it('draws no diff for a change that did not land', () => {
    const row = toolRow({ type: CODEX_ITEM.FileChange, status: 'failed', changes: [change], aggregatedOutput: 'refused' })
    expect(row?.call.result).not.toHaveProperty('changes')
    expect(row?.call.kind === 'edit' && row.call.request.changes).toHaveLength(1)
  })
})

/**
 * The `imageView` path.
 *
 * Codex sends a `file:` URI where every other item sends a plain path, and the read
 * row passed the raw field through -- so it showed the URI where every other read row
 * shows a workspace path. The image source already stripped the scheme.
 */
describe('codex imageView paths', () => {
  it('strips the file scheme from the read row', () => {
    const row = toolRow({ type: CODEX_ITEM.ImageView, id: 'i1', path: 'file:///repo/shot.png' })
    expect(row?.call.kind === 'read' && row.call.request.path).toBe('/repo/shot.png')
  })

  it('keeps a plain path unchanged', () => {
    const row = toolRow({ type: CODEX_ITEM.ImageView, id: 'i1', path: '/repo/shot.png' })
    expect(row?.call.kind === 'read' && row.call.request.path).toBe('/repo/shot.png')
  })

  // A URI the parser refuses states more as its own text than as a blank path.
  it('keeps the raw text of a file URI that does not parse', () => {
    const row = toolRow({ type: CODEX_ITEM.ImageView, id: 'i1', path: 'file:not-a-uri' })
    expect(row?.call.kind === 'read' && row.call.request.path).toBe('file:not-a-uri')
  })

  // An `imageView` states NO status of its own, so its closing row is final by
  // `completedAtMs` alone and the resolver says so through the role. The default
  // status word for a statusless item reads in-progress, and a call that pairs that
  // word with a finished span's pictures is a draft the validating builder refuses:
  // the row degraded to the generic card and the picture never reached the reader.
  // The role's answer states the outcome the frame's own words cannot.
  it('reads a span final by completedAtMs alone as completed, with its pictures', () => {
    const parsed = { wrapper: null, topLevel: { item: { type: CODEX_ITEM.ImageView, id: 'i1', path: '/repo/shot.png' }, completedAtMs: 2 }, parentObject: { item: { type: CODEX_ITEM.ImageView, id: 'i1', path: '/repo/shot.png' }, completedAtMs: 2 }, rawText: '', supplementalContent: undefined, messageMetadata: undefined }
    const row: ChatRowIR | null = codexExtractRow({
      parsed,
      category: { kind: 'tool_use' },
      // The role the span index resolved: final by `completedAtMs`, no completion
      // column and no status word on the item.
      sides: { current: parsed, request: undefined, result: undefined, role: 'result' },
    } as never)
    const call = row?.kind === 'tool' ? row.call : undefined
    expect(call?.kind).toBe('read')
    expect(call?.status).toBe('completed')
    expect(call?.kind === 'read' ? call.images.length : 0).toBe(1)
  })
})

/**
 * An item type comes straight off the wire, and the title table is a plain object. A
 * type that identifies an `Object.prototype` member answered with a FUNCTION, which
 * the header then drew as that function's own source text.
 */
describe('codex item titles over an Object.prototype type', () => {
  it.each(['constructor', 'toString', 'valueOf', 'hasOwnProperty'])('humanizes an item typed %s', (type) => {
    const row = toolRow({ type, id: 'x1', status: 'completed' })
    expect(row?.call.kind).toBe('other')
    expect(typeof row?.call.title).toBe('string')
    expect(row?.call.title).not.toContain('native code')
  })

  it('still titles each status-shaped item the table holds', () => {
    expect(toolRow({ type: CODEX_ITEM.Sleep, id: 's1', status: 'completed' })?.call.title).toBe('Sleep')
    expect(toolRow({ type: CODEX_ITEM.EnteredReviewMode, id: 'r1', status: 'completed' })?.call.title).toBe('Entered review mode')
  })
})

/**
 * The web rows, which are the one place a Codex payload could ever have answered at a
 * kind other than the one the item took.
 *
 * `codexItemKind` reads the action and answers `fetch` for an opened page. The payload
 * read the SAME action again and could fall through to a `web_search` shape, which no
 * input reached -- both reads run the same function over the same item. The reader
 * table now keys on the kind and each reader states its own, so the two cannot differ
 * at all.
 */
describe('codex web search rows', () => {
  it('draws an opened page at the fetch kind', () => {
    const row = toolRow({ type: CODEX_ITEM.WebSearch, id: 'w1', status: 'completed', action: { type: 'openPage', url: 'https://example.com/p' } })
    expect(row?.call.kind).toBe('fetch')
    expect(row?.call.label).toBe('WebFetch')
    expect(row?.call.kind === 'fetch' && row.call.request.url).toBe('https://example.com/p')
  })

  it.each([
    ['search', { type: 'search', query: 'a query' }],
    ['findInPage', { type: 'findInPage', pattern: 'needle', url: 'https://example.com/p' }],
    ['an action no release declared', { type: 'somethingLater' }],
  ])('draws %s at the web_search kind', (_name, action) => {
    const row = toolRow({ type: CODEX_ITEM.WebSearch, id: 'w2', status: 'completed', action })
    expect(row?.call.kind).toBe('web_search')
    expect(row?.call.label).toBe('WebSearch')
  })
})

/**
 * The kinds Codex reads from its own facts, and the kinds it leaves to the shared
 * table.
 *
 * A reader that takes the facts fills the same slot as a reader that takes the shared
 * arguments, and no type can refuse the swap: a function of one argument is assignable
 * where a function of two is asked for. So the block is this test. Each case states
 * the Codex spelling BESIDE the shared table's spelling of the same field, and a
 * reader that quietly became the shared one answers the shared value and fails here.
 */
describe('CODEX_TOOL_READERS', () => {
  /** The kinds a Codex ITEM takes. Each has a reader of its own. */
  const CODEX_READ_KINDS = ['agent', 'delete', 'edit', 'execute', 'fetch', 'image', 'mcp', 'move', 'other', 'read', 'skill', 'switch_mode', 'wait', 'web_search', 'write'] as const

  /**
   * Every other kind. No Codex item takes one, so the table states the shared
   * declared request and no result.
   *
   * `todo` sits here although Codex DOES draw a to-do row: the plan arrives as a
   * notification with no item behind it, and `codexTurnPlanRow` builds that row on
   * its own. The case below pins that row.
   */
  const SHARED_REQUEST_KINDS = ['', 'agents', 'chart', 'glob', 'grep', 'list', 'memory', 'message', 'question', 'report', 'search', 'task', 'think', 'todo', 'trigger'] as const

  type CodexReadKind = typeof CODEX_READ_KINDS[number]

  /**
   * An item carrying every key `DEFAULT_TOOL_REQUESTS` reads, in both of its spellings
   * where it reads two.
   *
   * The delegated kinds are compared over this ONE item. A probe that stated none of
   * these keys would let a hand-written reader and the shared entry agree on an empty
   * request, and the comparison would pass for a kind that no longer delegates.
   */
  const SHARED_ARGUMENT_PROBE: Record<string, unknown> = {
    type: 'anItemFromALaterRelease',
    channel: 'a shared channel',
    cmd: 'a shared cmd',
    command: 'a shared command',
    description: 'a shared description',
    filePath: '/shared/a.ts',
    instructions: 'shared instructions',
    limit: 9,
    message: 'a shared message',
    mode: 'a shared mode',
    name: 'a shared name',
    offset: 3,
    paths: ['/shared/b.ts'],
    pattern: 'a shared pattern',
    prompt: 'a shared prompt',
    q: 'a shared q',
    query: 'a shared query',
    server: 'a shared server',
    skill: 'a shared skill',
    spec: 'a shared spec',
    summary: 'a shared summary',
    target: 'a shared target',
    targetModeId: 'a shared target mode',
    task_id: 'task-1',
    taskId: 'task-2',
    text: 'a shared text',
    thought: 'a shared thought',
    to: 'a shared recipient',
    tool: 'a shared tool',
    trigger_id: 'trigger-1',
    triggerId: 'trigger-2',
    uri: 'https://shared-uri.example',
    url: 'https://shared.example',
  }

  /** One item for each kind a Codex item takes, carrying the shared spellings beside Codex's own. */
  const CODEX_PROBE: Record<CodexReadKind, Record<string, unknown>> = {
    execute: { type: CODEX_ITEM.CommandExecution, status: 'completed', command: '/bin/zsh -lc \'ls -1\'', cwd: '/repo', aggregatedOutput: 'a.ts\n', exitCode: 0, durationMs: 40, cmd: 'shared cmd', description: 'shared description' },
    edit: { type: CODEX_ITEM.FileChange, status: 'completed', changes: [{ path: 'src/a.ts', kind: 'update', diff: '@@ -1,1 +1,1 @@\n-old\n+new' }, { path: 'src/b.ts', kind: 'update', diff: '@@ -1,1 +1,1 @@\n-x\n+y' }], filePath: '/shared.ts' },
    write: { type: CODEX_ITEM.FileChange, status: 'completed', changes: [{ path: 'src/new.ts', kind: 'add', diff: 'hello\n' }], filePath: '/shared.ts' },
    delete: { type: CODEX_ITEM.FileChange, status: 'failed', changes: [{ path: 'src/gone.ts', kind: 'delete', diff: 'body\n' }], aggregatedOutput: 'apply_patch: refused', filePath: '/shared.ts' },
    move: { type: CODEX_ITEM.FileChange, status: 'completed', changes: [{ path: 'old/name.ts', kind: { type: 'move', movePath: 'new/name.ts' } }], sourcePath: '/shared-src.ts', destinationPath: '/shared-dst.ts' },
    image: { type: CODEX_ITEM.ImageGeneration, status: 'completed', result: 'aGk=', savedPath: '/repo/out.png', revisedPrompt: 'a codex prompt', prompt: 'shared prompt' },
    read: { type: CODEX_ITEM.ImageView, id: 'i1', path: 'file:///repo/shot.png', filePath: '/shared.ts', offset: 3, limit: 9 },
    fetch: { type: CODEX_ITEM.WebSearch, status: 'completed', action: { type: 'openPage', url: 'https://codex.example/page' }, url: 'https://shared.example', query: 'shared query' },
    web_search: { type: CODEX_ITEM.WebSearch, status: 'completed', action: { type: 'search', query: 'a codex query', queries: ['a second query', 'a codex query'] }, query: 'shared query', q: 'shared q' },
    agent: { type: CODEX_ITEM.CollabAgentToolCall, status: 'completed', tool: 'spawnAgent', prompt: 'a codex prompt', receiverThreadIds: ['a-1'], agentsStates: { 'a-1': { status: 'completed', message: 'done' } }, description: 'shared description', instructions: 'shared instructions' },
    mcp: { type: CODEX_ITEM.McpToolCall, status: 'completed', server: 'srv', tool: 'do', arguments: { a: 1 }, args: { b: 2 }, result: { content: [{ type: 'text', text: 'ok' }] }, durationMs: 12 },
    wait: { type: CODEX_ITEM.Sleep, status: 'completed', durationMs: 1200 },
    switch_mode: { type: CODEX_ITEM.EnteredReviewMode, status: 'completed', review: 'the whole diff', mode: 'shared mode', targetModeId: 'shared target mode', target: 'shared target' },
    skill: { type: CODEX_ITEM.HookPrompt, status: 'completed', fragments: [{ hookRunId: 'run-1', text: 'first words' }, { text: 'second words' }], text: 'shared text', name: 'shared name', skill: 'shared skill' },
    other: { type: 'anItemFromALaterRelease', status: 'completed', review: 'the reason', durationMs: 1200 },
  }

  function payloadOf<K extends ToolKind>(kind: K, item: Record<string, unknown>, finished = true) {
    return codexPayloadFor(codexToolFacts(item, finished, NO_SIDES), kind)
  }

  it('answers for every tool kind', () => {
    expect(Object.keys(CODEX_TOOL_READERS).sort()).toEqual([...TOOL_KINDS].sort())
  })

  it('splits every tool kind between the two lists above', () => {
    expect([...CODEX_READ_KINDS, ...SHARED_REQUEST_KINDS].sort()).toEqual([...TOOL_KINDS].sort())
  })

  it.each(SHARED_REQUEST_KINDS)('takes the shared declared request for the %s kind', (kind) => {
    expect(payloadOf(kind, SHARED_ARGUMENT_PROBE)).toEqual({ kind, request: DEFAULT_TOOL_REQUESTS[kind](SHARED_ARGUMENT_PROBE) })
  })

  it.each(CODEX_READ_KINDS)('reads its own facts for the %s kind', (kind) => {
    expect(payloadOf(kind, CODEX_PROBE[kind])).not.toEqual({ kind, request: DEFAULT_TOOL_REQUESTS[kind](CODEX_PROBE[kind]) })
  })

  // The matrix's own no-degradation assertion: every probe is a frame this build
  // reads by design, so the full row extraction must answer the TYPED kind with no
  // degrade behind it. A reader that broke an invariant would still draw -- the
  // generic row is the degrade's answer -- and only the metadata names it.
  it.each(CODEX_READ_KINDS)('extracts the %s probe without a degrade', (kind) => {
    const row = toolRow(CODEX_PROBE[kind])
    expect(row, kind).not.toBeNull()
    expect(row!.call.degradation, kind).toBeUndefined()
    expect(row!.call.kind, kind).toBe(kind)
  })

  // The ITEM is Codex's argument record: it states a call's fields at the top level
  // rather than under an `arguments` object. A delegated kind that read anything else
  // would answer an empty request for every item.
  it('hands the item to the shared table as the argument record', () => {
    expect(payloadOf('search', { pattern: 'needle', filePath: '/repo/a.ts' }))
      .toEqual({ kind: 'search', request: { pattern: 'needle', paths: ['/repo/a.ts'] } })
  })

  it('keeps the shared priority between two spellings of one argument', () => {
    expect(payloadOf('agents', { q: 'first', query: 'second' }).request).toEqual({ channel: undefined, query: 'first' })
  })

  it('draws the plan of a turn/plan/updated notification at the todo kind', () => {
    const row = codexExtractRow({
      parsed: { wrapper: null, topLevel: {}, parentObject: { method: 'turn/plan/updated', params: { plan: [{ step: 'Inspect messages', status: 'inProgress' }] } }, rawText: '', supplementalContent: undefined, messageMetadata: undefined },
      category: { kind: 'tool_use' },
      sides: NO_SIDES,
    } as never)
    const call = row && row.kind === 'tool' ? row.call : null
    expect(call?.kind).toBe('todo')
    expect(call?.kind === 'todo' && call.request.items).toHaveLength(1)
  })

  it('unwraps the shell wrapper of a command and reads no shared description', () => {
    const payload = payloadOf('execute', CODEX_PROBE.execute)
    expect(payload.request).toEqual({ command: 'ls -1', cwd: '/repo' })
    expect(payload.result).toEqual({ commands: [{ output: 'a.ts\n', exitCode: 0, durationMs: 40 }], unresolvedTerminals: [] })
  })

  it('states the command a call asked for and no output while it runs', () => {
    const payload = payloadOf('execute', CODEX_PROBE.execute, false)
    expect(payload.request).toEqual({ command: 'ls -1', cwd: '/repo' })
    expect(payload.result).toBeUndefined()
  })

  it('counts the files an edit changed rather than reading one shared path', () => {
    const payload = payloadOf('edit', CODEX_PROBE.edit)
    expect(payload.title).toBe('2 files')
    expect(payload.request.changes.map(change => change.filePath)).toEqual(['src/a.ts', 'src/b.ts'])
    expect(payload.result).toMatchObject({ changes: [{ filePath: 'src/a.ts' }, { filePath: 'src/b.ts' }] })
  })

  it('reads the body of a write from the change entry', () => {
    const payload = payloadOf('write', CODEX_PROBE.write)
    expect(payload.title).toBe('src/new.ts')
    expect(payload.request.changes).toMatchObject([{ filePath: 'src/new.ts', operation: 'add' }])
  })

  it('reads the file a removal states in its own change entry', () => {
    const payload = payloadOf('delete', CODEX_PROBE.delete)
    expect(payload.request.changes).toMatchObject([{ filePath: 'src/gone.ts', operation: 'delete' }])
    expect(payload.result).toEqual({ failure: true, text: 'apply_patch: refused' })
  })

  it('reads a move from the change entry, whose kind states the destination', () => {
    const payload = payloadOf('move', CODEX_PROBE.move)
    expect(payload.request.changes).toMatchObject([{ filePath: 'new/name.ts', previousPath: 'old/name.ts', operation: 'move' }])
  })

  // The REQUEST is the half that survives an outcome, and Codex reports `declined` on
  // a file change. An update whose entry carried no readable body used to empty the
  // whole list, so the row drew the word "Edit" and named no file at all.
  it.each([
    ['declined', CODEX_STATUS.DECLINED],
    ['failed', CODEX_STATUS.FAILED],
    ['completed', CODEX_STATUS.COMPLETED],
  ])('names the file a %s change asked for when its entry carried no body', (_word, status) => {
    const payload = payloadOf('edit', { type: CODEX_ITEM.FileChange, status, changes: [{ path: '/repo/a.ts' }] })
    expect(payload.request.changes).toMatchObject([{ filePath: '/repo/a.ts', operation: 'edit' }])
    expect(payload.title).toBe('/repo/a.ts')
  })

  // The two halves answer differently on purpose. Nothing landed that the row can
  // draw, and the request still states the file the call was about.
  it('lands no change for a completed update whose entry carried no body', () => {
    const payload = payloadOf('edit', { type: CODEX_ITEM.FileChange, status: CODEX_STATUS.COMPLETED, changes: [{ path: '/repo/a.ts' }] })
    expect(payload.result).toEqual({ changes: [] })
  })

  // A body the row CAN draw still reaches the request, so a running change shows the
  // diff it proposes.
  it('keeps the body of a running change on its request', () => {
    const payload = payloadOf('edit', { type: CODEX_ITEM.FileChange, status: CODEX_STATUS.IN_PROGRESS, changes: [{ path: 'src/a.ts', kind: 'update', diff: '@@ -1,1 +1,1 @@\n-old\n+new' }] }, false)
    expect(payload.request.changes).toMatchObject([{ filePath: 'src/a.ts', operation: 'edit' }])
    expect(fileEditHasDiff(payload.request.changes[0])).toBe(true)
    expect(payload.result).toBeUndefined()
  })

  // An entry that names no file identifies nothing a reader can use, and both halves
  // read one list -- so neither draws it.
  it('drops a change entry that names no file', () => {
    const payload = payloadOf('edit', { type: CODEX_ITEM.FileChange, status: CODEX_STATUS.COMPLETED, changes: [{ kind: 'update', diff: '@@ -1,1 +1,1 @@\n-old\n+new' }] })
    expect(payload.request.changes).toEqual([])
    expect(payload.result).toEqual({ changes: [] })
    expect(payload.title).toBeUndefined()
  })

  // The KIND reads the same list the request does. A second reading of the raw array
  // counted an entry the request drops, so a one-file creation beside a nameless entry
  // took the `edit` kind and drew the wrong icon and noun.
  it('takes its kind from the entries that name a file', () => {
    const item = { type: CODEX_ITEM.FileChange, status: CODEX_STATUS.COMPLETED, changes: [{ path: 'src/new.ts', kind: 'add', diff: 'hello\n' }, { kind: 'delete' }] }
    expect(codexItemKind(item)).toBe('write')
    expect(payloadOf('write', item).request.changes).toMatchObject([{ filePath: 'src/new.ts', operation: 'add' }])
  })

  // The wire word wins over the aggregated output: a change that landed draws its
  // diff, and the output is what states why one that did NOT land failed.
  it('draws the diff of a completed change ahead of the output it aggregated', () => {
    const payload = payloadOf('edit', { ...CODEX_PROBE.edit, aggregatedOutput: 'apply_patch: refused' })
    expect(payload.result).toMatchObject({ changes: [{ filePath: 'src/a.ts' }, { filePath: 'src/b.ts' }] })
  })

  it('reads the revised prompt of a generated image, not the prompt the reader asked for', () => {
    const payload = payloadOf('image', CODEX_PROBE.image)
    expect(payload.request).toEqual({ prompt: 'a codex prompt' })
    expect(payload.images).toEqual([{ data: 'aGk=', mimeType: 'image/png', filePath: '/repo/out.png' }])
    expect(payload.result).toEqual({ revisedPrompt: 'a codex prompt' })
  })

  it('states the failure of an image ahead of the picture', () => {
    const payload = payloadOf('image', { ...CODEX_PROBE.image, failure: { type: 'contentPolicy' } })
    expect(payload.title).toBe('Generate image contentPolicy')
    expect(payload.result).toEqual({ failure: true, text: 'contentPolicy' })
    expect(payload.statusOverride).toBe('failed')
  })

  it('strips the file scheme of a viewed image and reads no shared offset', () => {
    const payload = payloadOf('read', CODEX_PROBE.read)
    expect(payload.request).toEqual({ path: '/repo/shot.png' })
    expect(payload.images).toEqual([{ filePath: '/repo/shot.png' }])
  })

  it('reads an opened page from the action rather than a shared url', () => {
    const payload = payloadOf('fetch', CODEX_PROBE.fetch)
    expect(payload.request).toEqual({ url: 'https://codex.example/page' })
    expect(payload.title).toBe('https://codex.example/page')
  })

  it('reads the page of an action that states no url from the item query', () => {
    const payload = payloadOf('fetch', { type: CODEX_ITEM.WebSearch, action: { type: 'openPage' }, query: 'https://from-query.example' })
    expect(payload.request).toEqual({ url: 'https://from-query.example' })
  })

  it('heads the query list of a search with the query the action states directly', () => {
    const payload = payloadOf('web_search', CODEX_PROBE.web_search)
    expect(payload.request).toEqual({ query: 'a codex query', queries: ['a codex query', 'a second query'] })
    expect(payload.result).toEqual({ links: [], summary: '' })
  })

  it('reads a find in page as the pattern it looked for', () => {
    const payload = payloadOf('web_search', { type: CODEX_ITEM.WebSearch, action: { type: 'findInPage', pattern: 'needle', url: 'https://example.com/p' }, query: 'shared query' })
    expect(payload.request).toEqual({ query: 'needle', inPage: { pattern: 'needle', url: 'https://example.com/p' } })
  })

  it('describes an agent launch by its tool rather than a shared description', () => {
    const payload = payloadOf('agent', CODEX_PROBE.agent)
    expect(payload.request).toEqual({
      description: 'Subagent',
      prompt: 'a codex prompt',
      metadata: [{ label: 'Agent ID', value: 'a-1' }],
      registryKey: 'a-1',
    })
    expect(payload.result).toMatchObject({ agents: [{ agentId: 'a-1', outcome: 'completed' }] })
  })

  it('words an interrupted agent call cancelled', () => {
    expect(payloadOf('agent', { ...CODEX_PROBE.agent, status: 'interrupted' }).statusOverride).toBe('cancelled')
  })

  it('reads the arguments of a server call from the arguments field', () => {
    const payload = payloadOf('mcp', CODEX_PROBE.mcp)
    expect(payload.request).toEqual({ server: 'srv', tool: 'do', args: { a: 1 } })
    expect(payload.result).toMatchObject({ content: [{ type: 'text', text: 'ok' }], durationMs: 12 })
  })

  // `callId` is the call this output answers, and `id` is the output's own row. A
  // reader that took `id` titled every output after itself.
  it('identifies a function call output by its call id ahead of its own id', () => {
    const payload = payloadOf('mcp', { type: CODEX_ITEM.FunctionCallOutput, callId: 'call-1', id: 'item-1', namespace: 'tools', output: 'the result' })
    expect(payload.request).toEqual({ server: 'tools', tool: 'call-1', args: {} })
    expect(payload.result).toEqual({ content: [{ type: 'text', text: 'the result' }] })
  })

  it('reads the duration of a sleep as a number', () => {
    const payload = payloadOf('wait', CODEX_PROBE.wait)
    expect(payload.request).toEqual({ durationMs: 1200 })
    expect(payload.title).toBe('Sleep')
    expect(payload.result).toEqual({ text: '1.2s', format: 'plain' })
  })

  it('states the review of a status-shaped item ahead of its duration', () => {
    expect(payloadOf('wait', { ...CODEX_PROBE.wait, review: 'the reason' }).result).toEqual({ text: 'the reason', format: 'plain' })
  })

  // `switchModeRenderer` titles the row from `request.mode` first, so a mode here
  // would draw the bare word for BOTH review markers and hide the sentence below.
  it('states no mode for a review marker, whatever the shared keys say', () => {
    const payload = payloadOf('switch_mode', CODEX_PROBE.switch_mode)
    expect(payload.request).toEqual({})
    expect(payload.title).toBe('Entered review mode')
    expect(payload.result).toEqual({ text: 'the whole diff', format: 'plain' })
  })

  it('joins the fragments of a hook ahead of the text beside them', () => {
    const payload = payloadOf('skill', CODEX_PROBE.skill)
    expect(payload.request).toEqual({ name: 'run-1' })
    expect(payload.title).toBe('Hook prompt (run-1)')
    expect(payload.result).toEqual({ text: 'first words\n\nsecond words', format: 'markdown' })
  })

  it('states the wire word of an item no release declared, and the note it carries', () => {
    const payload = payloadOf('other', CODEX_PROBE.other)
    expect(payload.title).toBe('AnItemFromALaterRelease')
    expect(payload.request).toEqual({ args: CODEX_PROBE.other })
    expect(payload.result).toEqual({ content: [{ type: 'text', text: 'the reason' }] })
  })
})

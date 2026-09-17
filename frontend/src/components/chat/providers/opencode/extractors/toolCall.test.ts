import type { ToolCallIR } from '../../../ir/toolCall'
import type { ToolKind } from '../../../ir/toolKind'
import { describe, expect, it } from 'vitest'
import { isFailedResult, isUnparsedResult, typedResult } from '../../../ir/toolCall'
import { acpToolCallIR } from '../../acp/extractors/toolCall'
import { openCodeToolCallAdapterFor } from './toolCall'

function model(tool: Record<string, unknown>): ToolCallIR {
  return acpToolCallIR({ sessionUpdate: 'tool_call', toolCallId: 'open-code-tool', status: 'pending', kind: 'think', title: 'task', ...tool }, openCodeToolCallAdapterFor(), undefined)
}

describe('openCodeToolCallAdapterFor subagent launches', () => {
  it('titles the row from the description', () => {
    const call = model({ rawInput: { description: 'Inspect project structure', subagent_type: 'explore', prompt: 'Read the entry points.' } })
    expect(call.kind).toBe('agent')
    expect(call.title).toBe('Inspect project structure')
    expect(call.kind === 'agent' && call.request).toEqual({ description: 'Inspect project structure', agentType: 'explore', prompt: 'Read the entry points.' })
  })

  it('falls back to the shared word when the launch describes nothing', () => {
    // This row read `Agent` before OpenCode joined the shared card, while Cursor and
    // Reasonix drew `Task` for the same launch.
    expect(model({ rawInput: { prompt: 'Read the entry points.' } }).title).toBe('Task')
  })

  it('keeps the tool label OpenCode gives the row', () => {
    expect(model({ rawInput: { description: 'Inspect project structure' } }).label).toBe('Task')
    expect(model({ rawInput: {} }).label).toBe('Task')
  })

  /*
   * `openCodeTaskResult` reads the exact `<task id=".." state="..">` wrapper the
   * native tool writes, and a launch that never started writes no wrapper at all. With
   * the result attached on that wrapper ALONE, the row drew its title and the Error
   * header with nothing between them, where every sibling states a reason.
   */
  it('states the reason a failed launch gave', () => {
    const call = model({
      sessionUpdate: 'tool_call_update',
      status: 'failed',
      rawInput: { description: 'Inspect project structure', subagent_type: 'explore', prompt: 'Read the entry points.' },
      content: [{ type: 'content', content: { type: 'text', text: 'no such agent: explore' } }],
    })
    expect(call.kind).toBe('agent')
    expect(isFailedResult(call.result) && call.result.text).toBe('no such agent: explore')
  })

  /*
   * A launch the reader STOPPED writes no wrapper either, and it is not a fault: the
   * shared ladder hands back the words it printed, which is the part of the run
   * that did happen. The row heads them `Interrupted` from its own status.
   */
  it('keeps the words a cancelled launch printed', () => {
    const call = model({
      sessionUpdate: 'tool_call_update',
      status: 'cancelled',
      rawInput: { description: 'Inspect project structure', subagent_type: 'explore', prompt: 'Read the entry points.' },
      content: [{ type: 'content', content: { type: 'text', text: 'Read two entry points so far.' } }],
    })
    expect(call.kind).toBe('agent')
    expect(isFailedResult(call.result)).toBe(false)
    expect(isUnparsedResult(call.result) && call.result.text).toBe('Read two entry points so far.')
  })

  // A launch that finished and wrote words no wrapper encloses still states them.
  it('states the words a finished launch printed outside the task wrapper', () => {
    const call = model({
      sessionUpdate: 'tool_call_update',
      status: 'completed',
      rawInput: { description: 'Inspect project structure', subagent_type: 'explore' },
      content: [{ type: 'content', content: { type: 'text', text: 'The agent reported nothing.' } }],
    })
    expect(call.kind).toBe('agent')
    expect(isUnparsedResult(call.result) && call.result.text).toBe('The agent reported nothing.')
  })

  // The wrapper still wins where it is present: the run's own report is a typed
  // result, not the raw words.
  it('reads the task wrapper into a run report when the launch wrote one', () => {
    const call = model({
      sessionUpdate: 'tool_call_update',
      status: 'completed',
      rawInput: { description: 'Inspect project structure', subagent_type: 'explore' },
      content: [{ type: 'content', content: { type: 'text', text: '<task id="t1" state="completed">\n<task_result>\nFound two entry points.\n</task_result>\n</task>' } }],
    })
    expect(call.kind).toBe('agent')
    expect(call.kind === 'agent' ? typedResult(call)?.agents[0]?.body : undefined).toBe('Found two entry points.')
  })

  // A launch that has not finished answers nothing at all, so the row draws the
  // prompt it was given.
  it('answers nothing while the launch still runs', () => {
    expect(model({ rawInput: { description: 'Inspect project structure', subagent_type: 'explore' } }).result).toBeUndefined()
  })
})

// The frames below are the ones a live `kilo acp` session sent for the family's
// `question` tool: an ordinary tool call whose `rawInput` carries the questions.
// It is NOT the `question.asked` control payload the frontend's own OpenCode
// control surface expects, and nothing over ACP answers it -- see the constant's
// own note in `toolPresentation.ts`.
const KILO_QUESTION_INPUT = {
  questions: [{
    header: 'Task',
    question: 'What would you like me to do in this session? You gave no task yet, so I need one before I start.',
    options: [
      { label: 'Describe the task', description: 'Tell me what you want built, fixed, or investigated in this repo.' },
      { label: 'Inspect this directory', description: 'I explore the repo structure and report what is here, with no code changes.' },
    ],
  }],
}

describe('openCodeToolCallAdapterFor question rows', () => {
  it('states the question and the choices it offered', () => {
    const call = model({ title: 'question', kind: 'other', status: 'in_progress', rawInput: KILO_QUESTION_INPUT })
    expect(call.name).toBe('question')
    expect(call.kind).toBe('question')
    expect(call.label).toBe('Question')
    expect(call.title).toBe('Task')
    expect(call.kind === 'question' && call.request.questions[0]?.options).toHaveLength(2)
    expect(call.kind === 'question' && call.request.questions[0]?.question).toContain('What would you like me to do')
  })

  // `questionsFromRecords` holds the control flow and the two invariants; the daemons'
  // own key spellings are what stays in the plugin. An option that carries a sentence
  // and no label is answerable by that sentence, so it becomes the label rather than
  // sitting beside an empty one.
  const questionsOf = (questions: unknown) => {
    const built = model({ title: 'question', kind: 'other', status: 'in_progress', rawInput: { questions } })
    return built.kind === 'question' ? built.request.questions : []
  }

  it('labels an option with its description when it states no label', () => {
    expect(questionsOf([{ question: 'Which files?', options: [{ description: 'Only the changed ones' }] }])[0]?.options)
      .toStrictEqual([{ label: 'Only the changed ones' }])
  })

  it('keeps the description beside a label that states one', () => {
    expect(questionsOf([{ header: 'Scope', question: 'Which files?', options: [{ label: 'Changed', description: 'Only the changed ones' }] }]))
      .toStrictEqual([{ header: 'Scope', question: 'Which files?', options: [{ label: 'Changed', description: 'Only the changed ones' }] }])
  })

  it('drops a question that states no question field', () => {
    expect(questionsOf([{ header: 'Scope', options: [{ label: 'Changed' }] }])).toStrictEqual([])
  })

  it('drops an option that states neither a label nor a description', () => {
    expect(questionsOf([{ question: 'Which files?', options: [{ value: 'changed' }] }])[0]?.options).toStrictEqual([])
  })

  // The OPENING frame carries an empty `rawInput`; the questions arrive on the
  // update after it. The row must not claim a question it has not been given.
  it('draws plain text before the questions arrive', () => {
    const call = model({ title: 'question', kind: 'other', status: 'pending', rawInput: {} })
    expect(call.kind).toBe('question')
    expect(call.kind === 'question' && call.request.questions).toEqual([])
  })

  // The words a question row carries are the ANSWER. A call that has not finished has
  // no answer yet, and a call that FAILED never asked, so its reason must not draw as
  // a choice somebody made.
  it('answers nothing while the question is unanswered', () => {
    const call = model({ title: 'question', kind: 'other', status: 'in_progress', rawInput: KILO_QUESTION_INPUT, content: [{ type: 'content', content: { text: 'Describe the task' } }] })
    expect(call.kind).toBe('question')
    expect(call.result).toBeUndefined()
  })

  it('states the reason a failed question gave', () => {
    const call = model({
      sessionUpdate: 'tool_call_update',
      title: 'question',
      kind: 'other',
      status: 'failed',
      rawInput: KILO_QUESTION_INPUT,
      content: [{ type: 'content', content: { text: 'the question could not be shown' } }],
    })
    expect(call.kind).toBe('question')
    expect(isFailedResult(call.result) && call.result.text).toBe('the question could not be shown')
  })

  it('reads the choice a completed question carried', () => {
    const call = model({
      sessionUpdate: 'tool_call_update',
      title: 'question',
      kind: 'other',
      status: 'completed',
      rawInput: KILO_QUESTION_INPUT,
      content: [{ type: 'content', content: { text: 'Describe the task' } }],
    })
    expect(call.kind === 'question' ? typedResult(call)?.answers : undefined)
      .toStrictEqual([{ header: 'Task', answer: 'Describe the task' }])
  })

  // An option with no sentence beside it still reads as a choice.
  it('draws an option that carries only a label', () => {
    const call = model({
      title: 'question',
      kind: 'other',
      status: 'in_progress',
      rawInput: { questions: [{ question: 'Which one?', options: [{ label: 'The first' }] }] },
    })
    expect(call.kind === 'question' && call.request.questions[0]?.options[0]?.label).toBe('The first')
  })
})

describe('openCodeToolCallAdapterFor search counts', () => {
  function glob(count: number, text: string) {
    return model({
      title: 'glob',
      kind: 'search',
      status: 'completed',
      rawInput: { pattern: '*.ts' },
      rawOutput: { metadata: { count } },
      ...(text ? { content: [{ type: 'content', content: { text } }] } : {}),
    })
  }

  it('lists the files the daemon printed', () => {
    const call = glob(2, 'a.ts\nb.ts')
    expect(call.kind === 'glob' ? typedResult(call)?.filenames : undefined).toEqual(['a.ts', 'b.ts'])
  })

  // `''.trim().split('\n')` is `['']`, so trusting the count alone listed one file
  // with no name at all.
  it('lists no file when the count disagrees with an empty body', () => {
    const call = glob(3, '')
    expect(call.kind === 'glob' ? typedResult(call)?.filenames : undefined).toEqual([])
  })
})

/**
 * One call draws ONE kind, from the frame it opened with to the answer it ended with.
 *
 * The daemon states the kind on the opening frame and on a failed one, and omits the
 * field from a COMPLETED one. A reading that re-derived the kind from the answer's own
 * counters therefore moved it: a grep whose body this build cannot read drew the Grep
 * icon and label while it ran, and the Search pair once it answered.
 *
 * Two kinds still arrive with the answer, and each case below states why the call
 * itself could not have supplied it.
 */
describe('openCodeToolCallAdapterFor kind stability', () => {
  /** The kind one call draws while it runs, once it answers, and once it fails. */
  function lifecycle(frame: Record<string, unknown>, answer: Record<string, unknown>, kinds?: (toolName: string) => ToolKind | undefined): ToolKind[] {
    const build = (tool: Record<string, unknown>) => acpToolCallIR(
      { sessionUpdate: 'tool_call', toolCallId: 'open-code-tool', ...tool },
      openCodeToolCallAdapterFor(kinds),
      undefined,
    ).kind
    return [
      build({ ...frame, status: 'pending' }),
      build({ ...frame, ...answer, status: 'completed' }),
      build({ ...frame, status: 'failed', content: [{ type: 'content', content: { text: 'no such path' } }] }),
    ]
  }

  const GREP = { title: 'grep', kind: 'search', rawInput: { pattern: 'needle' } }
  /** OpenCode's own grep format: the heading, the path, then one row per match. */
  const GREP_BODY = 'Found 1 matches\n/p/a.ts:\n  Line 1: needle'

  it('holds the grep kind when the answer prints the format this build reads', () => {
    expect(lifecycle(GREP, {
      rawOutput: { metadata: { matches: 1, truncated: false } },
      content: [{ type: 'content', content: { text: GREP_BODY } }],
    })).toStrictEqual(['grep', 'grep', 'grep'])
  })

  // The registry id spells the tool, and a body this build cannot read says nothing
  // about which tool ran. Reading the kind off the match counter answered the wider
  // `search` here, so the row swapped its icon and its label as the answer landed.
  it('holds the grep kind when the answer prints a format this build cannot read', () => {
    expect(lifecycle(GREP, {
      rawOutput: { metadata: { matches: 1, truncated: false } },
      content: [{ type: 'content', content: { text: '/p/a.ts:1:needle' } }],
    })).toStrictEqual(['grep', 'grep', 'grep'])
  })

  it('holds the glob kind when the answer counts matches rather than files', () => {
    expect(lifecycle({ title: 'glob', kind: 'search', rawInput: { pattern: '*.ts' } }, {
      rawOutput: { metadata: { matches: 2, truncated: false } },
      content: [{ type: 'content', content: { text: '/p/a.ts\n/p/b.ts' } }],
    })).toStrictEqual(['glob', 'glob', 'glob'])
  })

  // Kilo's table decides the kind of every tool it adds on top of the OpenCode set, and
  // that decision is the call's own. A file counter in the answer must not turn its
  // semantic search into a glob over the filesystem.
  it('holds a kind the family table decided when the answer counts files', () => {
    const kinds = (toolName: string) => toolName === 'semantic_search' ? 'search' as const : undefined
    expect(lifecycle({ title: 'semantic_search', kind: 'other', rawInput: { query: 'the parser' } }, {
      rawOutput: { metadata: { count: 2, truncated: false } },
      content: [{ type: 'content', content: { text: '/p/a.ts\n/p/b.ts' } }],
    }, kinds)).toStrictEqual(['search', 'search', 'search'])
  })

  /*
   * The protocol's `search` is the one kind that leaves the question open: the daemon
   * spells a glob, a grep and a documentation lookup with it, and the title states no
   * registry id this build knows. The counters the answer carries are the only evidence
   * of which search ran, so the narrowing stands HERE and nowhere else.
   */
  it('narrows the wide search kind from the counter the answer carries', () => {
    const wide = { title: 'context7_get_library_docs', kind: 'search', rawInput: { pattern: 'solid-js' } }
    expect(lifecycle(wide, {
      rawOutput: { metadata: { count: 2, truncated: false } },
      content: [{ type: 'content', content: { text: '/p/a.ts\n/p/b.ts' } }],
    })).toStrictEqual(['search', 'glob', 'search'])
    expect(lifecycle(wide, {
      rawOutput: { metadata: { matches: 1, truncated: false } },
      content: [{ type: 'content', content: { text: GREP_BODY } }],
    })).toStrictEqual(['search', 'grep', 'search'])
  })

  /*
   * The second kind the answer decides. OpenCode's `read` runs two operations behind one
   * registry id -- its description opens "Read a file or directory" -- and the display
   * metadata of a successful answer is the only statement of which one ran. The two
   * carry different request and result types, so no result shape can hold the
   * difference and leave the kind alone.
   */
  it('draws the list kind only once the answer states a directory', () => {
    const read = { title: 'read', kind: 'read', rawInput: { filePath: '/p' } }
    expect(lifecycle(read, {
      rawOutput: { metadata: { display: { type: 'directory', path: '/p', entries: ['a.ts'], offset: 1, totalEntries: 1, truncated: false } } },
    })).toStrictEqual(['read', 'list', 'read'])
    expect(lifecycle(read, {
      rawOutput: { metadata: { display: { type: 'file', path: '/p/a.ts', text: 'alpha', lineStart: 1, totalLines: 1, truncated: false } } },
    })).toStrictEqual(['read', 'read', 'read'])
  })
})

/**
 * A file body reaches the read row only from a block that CARRIES text.
 *
 * The filter asked `typeof pickString(inner, 'text') === 'string'`, which is true for
 * every block -- `pickString` answers `''` for a key that is absent or holds a number
 * -- so the join swallowed each one as the empty string.
 */
describe('openCodeToolCallAdapterFor file bodies', () => {
  it('reads the numbered body of a completed file read', () => {
    const call = model({
      title: 'read',
      kind: 'read',
      status: 'completed',
      rawInput: { filePath: '/p/a.ts' },
      content: [
        { type: 'content', content: { type: 'resource_link', uri: 'file:///p/a.ts' } },
        { type: 'content', content: { text: '1\tconst a = 1\n2\tconst b = 2\n' } },
      ],
    })
    expect(call.kind).toBe('read')
    expect(call.kind === 'read' ? typedResult(call)?.lines?.map(line => line.text) : undefined).toEqual(['const a = 1', 'const b = 2'])
  })

  it('answers the reason a failed read stated', () => {
    const call = model({
      title: 'read',
      kind: 'read',
      status: 'failed',
      rawInput: { filePath: '/p/a.ts' },
      content: [{ type: 'content', content: { text: 'no such file' } }],
    })
    expect(isFailedResult(call.result) && call.result.text).toBe('no such file')
  })
})

/**
 * The edit-family request carries the keys the IR DECLARES, and no other one.
 *
 * `FileChangeRequest` states `changes` and an optional `replaceAll`. This branch also
 * put `replaceAll: undefined` and `patchText: undefined` on the object, and nothing
 * caught the second one: TypeScript checks a fresh object literal for excess
 * properties only in a contextually typed position, and the request was a `const`
 * that the returned literal then read as a VARIABLE.
 *
 * The assertion reads the KEYS rather than comparing objects, because `toEqual`
 * ignores a property whose value is `undefined` -- which is exactly what both extra
 * keys held, so an object comparison passed over them and proved nothing.
 */
describe('openCodeToolCallAdapterFor edit-family requests', () => {
  it('states only the declared keys', () => {
    const call = model({
      title: 'edit',
      kind: 'edit',
      status: 'completed',
      rawInput: { filePath: '/p/a.ts', oldString: 'a', newString: 'b' },
    })
    expect(call.kind).toBe('edit')
    expect(Object.keys(call.request).sort()).toEqual(['changes'])
  })

  // An absent `replaceAll` and an explicit `replaceAll: undefined` render the same,
  // so dropping the key changed no row: `fileChangesTitle` takes the field as
  // `boolean | undefined` and draws its suffix from a `<Show>`.
  it('keeps the change the call asked for', () => {
    const call = model({
      title: 'edit',
      kind: 'edit',
      status: 'completed',
      rawInput: { filePath: '/p/a.ts', oldString: 'a', newString: 'b' },
    })
    expect(call.kind === 'edit' ? call.request.changes.map(change => change.filePath) : []).toEqual(['/p/a.ts'])
  })
})

/**
 * Whether the extractor RECOGNIZED an empty result.
 *
 * The renderer decided this by comparing the raw text against LeapMux's own summary
 * prose, which put a provider's output format in the layer that knows no provider.
 * Two branches build a search here, and only one of them reads a wording.
 */
describe('openCodeToolCallAdapterFor empty search results', () => {
  // The registry id decides the kind, so each case states the tool whose counter it
  // carries: `matches` is the grep's and `count` is the glob's. A grep frame that
  // reported a file count used to draw as a Glob, which is the kind swap
  // `openCodeToolCallAdapterFor kind stability` above pins.
  const searchCall = (text: string, metadata: Record<string, unknown>, title = 'grep') => model({
    title,
    kind: 'search',
    status: 'completed',
    rawInput: { pattern: 'x' },
    rawOutput: { metadata },
    content: [{ type: 'content', content: { text } }],
  })

  // `openCodeSearchLines` answers a list only when it read EVERY row of OpenCode's
  // own format, and it recognizes that format's empty wording itself.
  it('reads the wording the native grep format prints when it matched nothing', () => {
    const call = searchCall('No files found', { matches: 0 })
    expect(call.kind).toBe('grep')
    expect(call.kind === 'grep' ? typedResult(call)?.empty : undefined).toBe(true)
  })

  it('states no empty result for a native grep that listed matches', () => {
    const call = searchCall('Found 1 matches\n/p/a.ts:\n  Line 3: hit', { matches: 1 })
    expect(call.kind === 'grep' ? typedResult(call)?.empty : undefined).toBe(false)
  })

  // The COUNT branch reads a total the daemon stated and never the body's own words,
  // and no transcript records what OpenCode prints here when it matches nothing. An
  // empty body is the one empty result it can recognize.
  it('recognizes an empty count result from an empty body alone', () => {
    const empty = searchCall('', { count: 0 }, 'glob')
    expect(empty.kind).toBe('glob')
    expect(empty.kind === 'glob' ? typedResult(empty)?.empty : undefined).toBe(true)
    const worded = searchCall('nothing matched', { count: 0 }, 'glob')
    expect(worded.kind === 'glob' ? typedResult(worded)?.empty : undefined).toBe(false)
  })
})

/**
 * The checklist walks the same lifecycle as every other kind.
 *
 * This branch carried no lifecycle rung at all: it attached a result from the OPENING
 * frame, so a row drew its saved list before the tool ran -- and `ToolMessage` shows
 * the live output tail only while the result is absent, so the tail went with it.
 */
describe('openCodeToolCallAdapterFor to-do lists', () => {
  const todos = [{ content: 'Inspect sample', status: 'in_progress' }, { content: 'Report findings', status: 'pending' }]
  const saved = [
    { rowKey: '0:Inspect sample', content: 'Inspect sample', status: 'in_progress', activeForm: '' },
    { rowKey: '1:Report findings', content: 'Report findings', status: 'pending', activeForm: '' },
  ]

  const todoCall = (tool: Record<string, unknown>) => model({
    sessionUpdate: 'tool_call_update',
    title: 'todowrite',
    kind: 'other',
    status: 'completed',
    rawInput: { todos },
    ...tool,
  })

  // The OPENING frame. The call has not answered, so the row carries the list it
  // ASKED for and no result at all.
  it('answers nothing on the opening frame of a list', () => {
    const call = todoCall({ sessionUpdate: 'tool_call', status: 'pending' })
    expect(call.kind).toBe('todo')
    expect(call.kind === 'todo' ? call.request.items : undefined).toStrictEqual(saved)
    expect(call.result).toBeUndefined()
  })

  it('answers a completed list with the tasks it saved', () => {
    const call = todoCall({ content: [{ type: 'content', content: { type: 'text', text: 'Saved' } }] })
    expect(call.kind).toBe('todo')
    expect(call.kind === 'todo' ? typedResult(call)?.items : undefined).toStrictEqual(saved)
  })

  it('states the reason a failed list gave', () => {
    const call = todoCall({ status: 'failed', content: [{ type: 'content', content: { text: 'the todo store is not writable' } }] })
    expect(call.kind).toBe('todo')
    expect(isFailedResult(call.result) && call.result.text).toBe('the todo store is not writable')
  })

  // A call the reader STOPPED keeps the list it collected. The row marks it partial
  // from its own status, so nothing here states that.
  it('keeps the list a cancelled call collected', () => {
    const call = todoCall({ status: 'cancelled', content: [{ type: 'content', content: { text: 'stopped' } }] })
    expect(call.kind).toBe('todo')
    expect(isFailedResult(call.result)).toBe(false)
    expect(call.kind === 'todo' ? typedResult(call)?.items : undefined).toStrictEqual(saved)
  })

  // A finished call that carried no list states the words it printed. `UnparsedResult`
  // is the brand for that -- this build could not read an answer into the kind's shape
  // -- and a `FailedResult` would claim the call failed under a `completed` header.
  it('states the words a finished list printed when it carried no list', () => {
    const call = todoCall({ rawInput: {}, content: [{ type: 'content', content: { text: 'nothing to save' } }] })
    expect(call.kind).toBe('todo')
    expect(isFailedResult(call.result)).toBe(false)
    expect(isUnparsedResult(call.result) && call.result.text).toBe('nothing to save')
  })
})

/**
 * A call the reader STOPPED keeps the body the builder read.
 *
 * Every kind this family decorates keyed its reason branch on the two words `failed`
 * and `cancelled`, so a stopped call lost the lines, the hits and the diff that had
 * already arrived, and the row stated the same reason twice.
 */
describe('openCodeToolCallAdapterFor stopped calls', () => {
  it('keeps the file body a cancelled read collected', () => {
    const call = model({
      sessionUpdate: 'tool_call_update',
      title: 'read',
      kind: 'read',
      status: 'cancelled',
      rawInput: { filePath: '/p/a.ts' },
      content: [{ type: 'content', content: { text: '1\tconst a = 1\n' } }],
    })
    expect(call.kind).toBe('read')
    expect(isFailedResult(call.result)).toBe(false)
    expect(call.kind === 'read' ? typedResult(call)?.lines?.map(line => line.text) : undefined).toStrictEqual(['const a = 1'])
  })

  it('states the reason a failed read gave', () => {
    const call = model({
      sessionUpdate: 'tool_call_update',
      title: 'read',
      kind: 'read',
      status: 'failed',
      rawInput: { filePath: '/p/a.ts' },
      content: [{ type: 'content', content: { text: 'no such file' } }],
    })
    expect(isFailedResult(call.result) && call.result.text).toBe('no such file')
  })

  it.each([
    ['cancelled', false],
    ['failed', true],
  ])('answers a %s edit through the same ladder', (status, failed) => {
    const call = model({
      sessionUpdate: 'tool_call_update',
      title: 'edit',
      kind: 'edit',
      status,
      rawInput: { filePath: '/p/a.ts', oldString: 'a', newString: 'b' },
      content: [{ type: 'diff', path: '/p/a.ts', oldText: 'a', newText: 'b' }],
    })
    expect(call.kind).toBe('edit')
    // The file stays in the REQUEST at every state, so the row heads itself with it.
    expect(call.kind === 'edit' ? call.request.changes.map(change => change.filePath) : []).toStrictEqual(['/p/a.ts'])
    expect(isFailedResult(call.result)).toBe(failed)
    expect(call.kind === 'edit' ? typedResult(call)?.changes.map(change => change.filePath) : undefined)
      .toStrictEqual(failed ? undefined : ['/p/a.ts'])
  })

  // The change the call ASKED for becomes the RESULT for a call that COMPLETED alone.
  // A call the reader stopped applied nothing, and `FileChangesBody` draws whatever
  // the result holds -- so a promoted list states a change the file never took.
  // `RequestedChangesBody` refuses the same list on the request side already.
  it('promotes no requested change into the result of a cancelled edit', () => {
    const call = model({
      sessionUpdate: 'tool_call_update',
      title: 'edit',
      kind: 'edit',
      status: 'cancelled',
      rawInput: { filePath: '/p/a.ts', oldString: 'a', newString: 'b' },
    })
    expect(call.kind).toBe('edit')
    expect(call.result).toBeUndefined()
    // The file stays in the REQUEST, so the row still heads itself with it.
    expect(call.kind === 'edit' ? call.request.changes.map(change => change.filePath) : []).toStrictEqual(['/p/a.ts'])
  })

  it.each([
    ['cancelled', false],
    ['failed', true],
  ])('answers a %s search through the same ladder', (status, failed) => {
    const call = model({
      sessionUpdate: 'tool_call_update',
      title: 'grep',
      kind: 'search',
      status,
      rawInput: { pattern: 'needle' },
      rawOutput: { metadata: { matches: 1 } },
      content: [{ type: 'content', content: { text: 'Found 1 matches\n/p/a.ts:\n  Line 3: needle' } }],
    })
    expect(isFailedResult(call.result)).toBe(failed)
    expect(call.kind === 'grep' ? typedResult(call)?.numLines : undefined).toBe(failed ? undefined : 1)
  })

  // Kilo's `chart` is the family's one kind whose whole body is the configuration it
  // normalized. A stopped call keeps the configuration that arrived.
  it.each([
    ['cancelled', false],
    ['failed', true],
  ])('answers a %s chart through the same ladder', (status, failed) => {
    const spec = JSON.stringify({ type: 'bar', data: { labels: ['A'], datasets: [{ label: 'Hits', data: [3] }] } })
    const call = acpToolCallIR({
      sessionUpdate: 'tool_call_update',
      toolCallId: 'open-code-tool',
      status,
      kind: 'other',
      title: 'chart',
      rawInput: { title: 'Weekly hits', spec },
      content: [{ type: 'content', content: { type: 'text', text: spec } }],
    }, openCodeToolCallAdapterFor(name => (name === 'chart' ? 'chart' : undefined)), undefined)
    expect(call.kind).toBe('chart')
    expect(isFailedResult(call.result)).toBe(failed)
    expect(call.kind === 'chart' ? typedResult(call)?.series.map(series => series.label) : undefined)
      .toStrictEqual(failed ? undefined : ['Hits'])
  })
})

/**
 * The file a change states, under every spelling the family sends.
 *
 * OpenCode's own tools spell it `filePath`; the tools Kilo adds on top of them spell
 * it `path`, with `old_string` and `new_string` beside it. Reading `filePath` alone
 * leaves `notebook_edit` with an empty change list, so the row draws the word "Edit"
 * and states no file at all.
 */
describe('openCodeToolCallAdapterFor file-change aliases', () => {
  it('states the file a change spells under any alias', () => {
    const call = model({
      sessionUpdate: 'tool_call_update',
      title: 'notebook_edit',
      kind: 'edit',
      status: 'completed',
      rawInput: { path: '/p/nb.ipynb', old_string: 'x', new_string: 'y' },
    })
    expect(call.kind).toBe('edit')
    expect(call.kind === 'edit' ? call.request.changes : undefined).toStrictEqual([
      { filePath: '/p/nb.ipynb', oldStr: 'x', newStr: 'y', structuredPatch: null },
    ])
  })
})

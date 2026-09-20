import type { ToolFailureFixture, ToolResultCheck, ToolResultFixture } from './toolVocabulary'
import type { ToolCall, ToolCallFault, ToolCallSpecVariant } from '~/components/chat/model/toolCall'
import type { ToolCallStatus } from '~/components/chat/model/toolCallStatus'
import type { ToolKind } from '~/components/chat/model/toolKind'
import { describe, expect, it } from 'vitest'
import { buildToolCall, createToolCall } from '~/components/chat/model/createToolCall'
import { failedResult, FILE_CHANGE_KINDS, proseResult, unparsedResult } from '~/components/chat/model/toolCall'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { toolCallFixture } from './toolCallFixture'
import {
  failuresThatMisreadTheirKind,
  failuresThatMisreadTheirStatus,
  failuresWithoutASuccessFixture,
  fixturesOnTheUncategorizedKind,
  invariantViolations,
  kindsWithoutFailureFixture,
  openingFrameOf,
  requestsThatAnswerEarly,
  staleNoFailureReasons,
} from './toolVocabulary'

// Each case below states a call that BREAKS one rule, so the rule's own message is
// pinned to the shape that produces it. A guard whose matcher stops matching looks
// exactly like a clean tree -- green either way -- and these are what separate the
// two. Every provider's failure ladder runs through the same functions.

const FIXTURE: ToolResultFixture = { payload: {} }

/** One check, with only the fields a case is about. */
function check(overrides: Partial<ToolResultCheck> = {}): ToolResultCheck {
  return {
    provider: AgentProvider.CLAUDE_CODE,
    fixtures: {},
    failures: [],
    noFailure: {},
    noResult: {},
    unparsed: {},
    ...overrides,
  }
}

/** One failed entry, with only the fields a case is about. */
function failure(overrides: Partial<ToolFailureFixture> = {}): ToolFailureFixture {
  return { payload: {}, kind: 'execute', name: 'bash', status: 'failed', ...overrides }
}

/**
 * A call that breaks an invariant, for the guards that report one.
 *
 * Production has NO route to such a value: the lifecycle union refuses the shape at
 * compile time and `buildToolCall` refuses it again at runtime, so the only way to
 * state one is to go around both. Each guard below still earns its keep -- it is what
 * reports a call some later assertion smuggles past the pair -- and it cannot be
 * tested without one.
 */
function smuggle(call: ToolCall, broken: Record<string, unknown>): ToolCall {
  return { ...call, ...broken } as unknown as ToolCall
}

describe('invariantViolations', () => {
  it('answers nothing for a call the builder produced', () => {
    expect(invariantViolations(toolCallFixture('read', { result: { lines: null, fallbackContent: '' } }))).toStrictEqual([])
  })

  // The tool call succeeded and the command it ran failed, which are two facts. ZCode's
  // app-server states exactly this pair, and `commandFailed` draws `Error (exit 1)` from
  // the exit code. This case pins the pair as LEGAL, so a rule that refuses it cannot
  // come back without turning the suite red.
  it('answers nothing for a completed command that reported a non-zero exit code', () => {
    const call = toolCallFixture('execute', { result: { commands: [{ output: '', exitCode: 1 }], unresolvedTerminals: [] } })
    expect(invariantViolations(call)).toStrictEqual([])
  })

  // I4 admits `cancelled` beside `completed`. A call the reader interrupted keeps the
  // partial body it printed, and that body is an UnparsedToolResult wherever no kind's
  // shape reads it -- which is what the lifecycle ladder answers for every kind.
  it('answers nothing for an unparsed result on a call the reader stopped', () => {
    const call = toolCallFixture('read', { status: 'cancelled', result: unparsedResult('the lines that arrived') })
    expect(invariantViolations(call)).toStrictEqual([])
  })

  it('answers nothing for a file change whose request names its file', () => {
    const change = { filePath: '/p/a.ts', structuredPatch: null, oldStr: 'a', newStr: 'b' }
    const call = toolCallFixture('edit', { request: { changes: [change] }, status: 'failed', result: failedResult('nope') })
    expect(invariantViolations(call)).toStrictEqual([])
  })

  // The adapter reports the BUILDER's word, so the two can never drift into two
  // vocabularies. It reaches a call only through an assertion, which is what the case
  // below states -- production has no route to one.
  it('reports the builder\'s own fault for a call that reached it invalid', () => {
    const smuggled = smuggle(toolCallFixture('mcp', { result: { content: [] } }), { images: [{ data: 'aGk=', mimeType: 'image/png' }] })
    expect(invariantViolations(smuggled)).toStrictEqual(['a-generic-kind-carries-its-own-images'])
  })
})

/**
 * Every rule the lifecycle states, as the fault the BUILDER answers.
 *
 * The union refuses each of these pairs at compile time, so no provider can write
 * one. What reaches the builder is a draft assembled from the WIRE -- a status word
 * one frame stated beside a result another frame carried -- and these cases are the
 * whole of the runtime coverage. A draft built here therefore states its parts
 * loosely on purpose: the payload type admits any of the kind's result shapes,
 * because the status it will meet lives on the envelope.
 */
describe('buildToolCall', () => {
  function faultOf<K extends ToolKind>(kind: K, status: ToolCallStatus, payload: Omit<ToolCallSpecVariant<K>, 'kind'>): ToolCallFault | 'built' {
    const built = buildToolCall<K>({ id: 'c1', name: 'tool', lifecycle: { frameStatus: status, providerOutcome: null, retainedOutcome: null, rowFinal: false, resultFrameLanded: false } }, { kind, ...payload } as ToolCallSpecVariant<K>)
    return built.ok ? 'built' : built.fault
  }

  const READ_RESULT = { lines: null, fallbackContent: '' }
  const NAMED_CHANGE = { filePath: '/p/a.ts', structuredPatch: null, oldStr: 'a', newStr: 'b' }

  it('refuses a result on a call that has not ended', () => {
    for (const status of ['unstated', 'pending', 'in_progress'] as const)
      expect(faultOf('read', status, { request: { path: '/p/a.ts' }, result: READ_RESULT })).toBe('result-before-the-call-finished')
  })

  // The result SIDE follows the result. `ToolMessage` gates all of it behind the row
  // that draws the result, so a picture or a truncation notice on a live row is a
  // fact no reader can see and no provider meant to state.
  it('refuses a result-side field on a call that has not ended', () => {
    const request = { path: '/p/a.ts' }
    expect(faultOf('read', 'pending', { request, images: [{ data: 'aGk=', mimeType: 'image/png' }] })).toBe('pictures-before-the-call-finished')
    expect(faultOf('read', 'pending', { request, extraContent: [] })).toBe('pictures-before-the-call-finished')
    expect(faultOf('read', 'pending', { request, truncated: true })).toBe('pictures-before-the-call-finished')
  })

  it('derives incomplete for a completed frame with no result', () => {
    const built = buildToolCall({ id: 'c1', name: 'Read', lifecycle: { frameStatus: 'completed', providerOutcome: null, retainedOutcome: null, rowFinal: true, resultFrameLanded: true } }, { kind: 'read', request: { path: '/p/a.ts' } })
    expect(built.ok && built.call.status).toBe('incomplete')
  })

  // The two brands draw the same pixels, so a completed call that answers the failure
  // brand claims an outcome its own status denies.
  it('refuses a failure result on a completed call', () => {
    expect(faultOf('read', 'completed', { request: { path: '/p/a.ts' }, result: failedResult('nope') })).toBe('completed-with-a-failure-result')
  })

  it('refuses an unparsed result on a failed call', () => {
    expect(faultOf('read', 'failed', { request: { path: '/p/a.ts' }, result: unparsedResult('nope') })).toBe('failed-with-an-unparsed-result')
  })

  it('refuses a typed payload on a call the reader declined', () => {
    expect(faultOf('read', 'declined', { request: { path: '/p/a.ts' }, result: READ_RESULT })).toBe('declined-with-a-typed-payload')
    expect(faultOf('read', 'declined', { request: { path: '/p/a.ts' }, result: unparsedResult('nope') })).toBe('declined-with-a-typed-payload')
  })

  // A declined call produced nothing, so a prose-shaped object is legal only where
  // the KIND's own result is prose: `switch_mode` sends the plan back with feedback,
  // which is the refusal in words. `read` answers a file, so the same object there is
  // a payload the tool never produced.
  it('admits the refusal words only where the kind\'s own result is prose', () => {
    expect(faultOf('switch_mode', 'declined', { request: { mode: 'plan' }, result: proseResult('Not yet') })).toBe('built')
    expect(faultOf('read', 'declined', { request: { path: '/p/a.ts' }, result: { text: 'Not yet', format: 'plain' } as never })).toBe('declined-with-a-typed-payload')
  })

  it('admits the failure brand on every status that ends a call badly', () => {
    for (const status of ['failed', 'cancelled', 'declined'] as const)
      expect(faultOf('read', status, { request: { path: '/p/a.ts' }, result: failedResult('nope') })).toBe('built')
  })

  it('refuses a generic kind that carries its own pictures', () => {
    const payload = { request: { args: {}, server: 's', tool: 't' }, result: { content: [] }, images: [{ data: 'aGk=', mimeType: 'image/png' }] }
    expect(faultOf('mcp', 'completed', payload)).toBe('a-generic-kind-carries-its-own-images')
  })

  // I7 is the one the failure ladder exists for. A file-change row composes its header
  // from the request at EVERY state, so a builder that empties the list on a failure
  // heads the row with the operation word and no file.
  it('refuses a file change whose request states no file', () => {
    for (const kind of FILE_CHANGE_KINDS)
      expect(faultOf(kind, 'failed', { request: { changes: [] }, result: failedResult('nope') })).toBe('a-file-change-states-no-file')
  })

  it('refuses a file change whose one entry carries an empty path', () => {
    const change = { filePath: '', structuredPatch: null, oldStr: 'a', newStr: 'b' }
    expect(faultOf('write', 'completed', { request: { changes: [change] }, result: { changes: [change] } })).toBe('a-file-change-states-no-file')
  })

  it('builds a file change whose request names its file', () => {
    expect(faultOf('edit', 'failed', { request: { changes: [NAMED_CHANGE] }, result: failedResult('nope') })).toBe('built')
  })
})

/**
 * The row a refused draft becomes.
 *
 * `createToolCall` never hands back an invalid call, and it never throws either: a frame
 * this build read wrongly still has to draw something, and the uncategorized row is
 * the codebase's own word for that. It keeps what the reader can still trust -- the
 * tool name, the arguments, the status -- and drops what the fault says cannot be
 * true.
 */
describe('createToolCall', () => {
  it('degrades a refused draft to the uncategorized row', () => {
    const call = createToolCall({ id: 'c1', name: 'Edit', lifecycle: { frameStatus: 'failed', providerOutcome: null, retainedOutcome: null, rowFinal: false, resultFrameLanded: false } }, { kind: 'edit', request: { changes: [] }, result: failedResult('no such file') })
    expect(call.kind).toBe('other')
    expect(call.name).toBe('Edit')
    expect(call.status).toBe('failed')
    expect(call.result).toStrictEqual(failedResult('no such file'))
  })

  // The degrade must not restate the very rule it exists to enforce, so an unfinished
  // status keeps no result at all.
  it('drops the result of a refused draft that had not finished', () => {
    const call = createToolCall({ id: 'c1', name: 'Read', lifecycle: { frameStatus: 'in_progress', providerOutcome: null, retainedOutcome: null, rowFinal: false, resultFrameLanded: false } }, { kind: 'read', request: { path: '/p/a.ts' }, result: { lines: null, fallbackContent: '' } })
    expect(call.kind).toBe('other')
    expect(call.result).toBeUndefined()
    expect(invariantViolations(call)).toStrictEqual([])
  })

  it('states the fault when the refused draft carried no words of its own', () => {
    const call = createToolCall({ id: 'c1', name: 'Write', lifecycle: { frameStatus: 'completed', providerOutcome: null, retainedOutcome: null, rowFinal: false, resultFrameLanded: false } }, { kind: 'write', request: { changes: [] }, result: { changes: [] } })
    expect(call.kind).toBe('other')
    expect(call.result).toStrictEqual(unparsedResult('This build could not read the call: a file change states no file.'))
  })

  it('keeps the arguments of a refused draft that carried them', () => {
    const call = createToolCall({ id: 'c1', name: 'weird', lifecycle: { frameStatus: 'completed', providerOutcome: null, retainedOutcome: null, rowFinal: false, resultFrameLanded: false } }, { kind: 'mcp', request: { args: { q: 1 }, server: 's', tool: 't' }, result: { content: [] }, images: [{ data: 'aGk=', mimeType: 'image/png' }] })
    expect(call.kind === 'other' && call.request.args).toStrictEqual({ q: 1 })
  })

  // The generic trio's result IS the uncategorized kind's result, so only the
  // pictures broke the rule here. Dropping the content blocks with them would throw
  // away the whole of what the tool answered.
  it('keeps the body of a refused draft the uncategorized kind can hold', () => {
    const content = [{ type: 'text' as const, text: 'the tool answered this' }]
    const call = createToolCall({ id: 'c1', name: 'weird', lifecycle: { frameStatus: 'completed', providerOutcome: null, retainedOutcome: null, rowFinal: false, resultFrameLanded: false } }, { kind: 'mcp', request: { args: {}, server: 's', tool: 't' }, result: { content }, images: [{ data: 'aGk=', mimeType: 'image/png' }] })
    expect(call.kind === 'other' && call.result).toStrictEqual({ content })
    expect(call.images).toStrictEqual([])
  })

  it('builds every valid draft unchanged', () => {
    const call = createToolCall({ id: 'c1', name: 'Read', lifecycle: { frameStatus: 'completed', providerOutcome: null, retainedOutcome: null, rowFinal: false, resultFrameLanded: false } }, { kind: 'read', request: { path: '/p/a.ts' }, result: { lines: null, fallbackContent: '' } })
    expect(call.kind).toBe('read')
  })
})

describe('fixturesOnTheUncategorizedKind', () => {
  const results = check({ fixtures: { bash: FIXTURE, wrench: FIXTURE, unstated: FIXTURE, recall: FIXTURE } })
  const KIND_OF: Record<string, ToolKind> = { bash: 'execute', wrench: 'other', unstated: 'unspecified', recall: 'mcp' }

  // `recall` draws the Model Context Protocol card, which `isGenericKind` holds beside
  // the other two. It stays OUT of the answer: that card states the server and the tool
  // it called, so a fixture that reaches it is identified.
  it('reports the fixtures whose frame draws a wrench and nothing else', () => {
    expect(fixturesOnTheUncategorizedKind(results, (name) => {
      const kind = KIND_OF[name]
      if (kind === undefined)
        throw new Error(`no kind stated for "${name}"`)
      return toolCallFixture(kind) as ToolCall
    }))
      .toStrictEqual(['wrench', 'unstated'])
  })

  it('answers nothing for a fixture whose frame draws no row', () => {
    expect(fixturesOnTheUncategorizedKind(results, () => null)).toStrictEqual([])
  })
})

describe('failuresWithoutASuccessFixture', () => {
  it('reports a failed frame whose tool holds no successful fixture', () => {
    const results = check({ failures: [failure({ name: 'ghost' })] })
    expect(failuresWithoutASuccessFixture(results)).toStrictEqual(['ghost (execute)'])
  })

  it('answers nothing when every failed frame pairs with its own success', () => {
    const results = check({ fixtures: { bash: FIXTURE }, failures: [failure()] })
    expect(failuresWithoutASuccessFixture(results)).toStrictEqual([])
  })
})

describe('kindsWithoutFailureFixture', () => {
  const results = check({ fixtures: { bash: FIXTURE, read: FIXTURE } })
  const callOf = (name: string) => toolCallFixture(name === 'bash' ? 'execute' : 'read') as ToolCall

  it('reports a kind a fixture draws that no failed frame pins', () => {
    expect(kindsWithoutFailureFixture({ ...results, failures: [failure()] }, callOf)).toStrictEqual(['read'])
  })

  it('takes a reason in place of a failed frame', () => {
    const excused = { ...results, failures: [failure()], noFailure: { read: 'A read cannot fail here.' } }
    expect(kindsWithoutFailureFixture(excused, callOf)).toStrictEqual([])
  })

  // The STATED kind counts. A frame that drew another kind would otherwise pin that
  // one and leave its own unpinned, which reports the defect against the wrong row.
  it('counts the kind a failed frame states rather than the kind it draws', () => {
    const stated = { ...results, failures: [failure(), failure({ kind: 'read', name: 'read' })] }
    expect(kindsWithoutFailureFixture(stated, callOf)).toStrictEqual([])
  })
})

describe('staleNoFailureReasons', () => {
  const callOf = () => toolCallFixture('execute') as ToolCall

  it('reports a reason for a kind a failed frame pins', () => {
    const results = check({ fixtures: { bash: FIXTURE }, failures: [failure()], noFailure: { execute: 'stale' } })
    expect(staleNoFailureReasons(results, callOf)).toStrictEqual(['execute'])
  })

  it('reports a reason for a kind no fixture reaches', () => {
    const results = check({ fixtures: { bash: FIXTURE }, noFailure: { chart: 'stale' } })
    expect(staleNoFailureReasons(results, callOf)).toStrictEqual(['chart'])
  })
})

describe('failuresThatMisreadTheirKind', () => {
  it('reports a failed frame that lands on another kind', () => {
    const results = check({ failures: [failure({ kind: 'todo', name: 'todo_write' })] })
    expect(failuresThatMisreadTheirKind(results, () => toolCallFixture('edit') as ToolCall))
      .toStrictEqual(['todo_write: states "todo" and draws "edit"'])
  })

  it('answers nothing when the frame draws the kind it states', () => {
    const results = check({ failures: [failure()] })
    expect(failuresThatMisreadTheirKind(results, () => toolCallFixture('execute') as ToolCall)).toStrictEqual([])
  })
})

describe('failuresThatMisreadTheirStatus', () => {
  it('reports a failed frame that reads as another outcome word', () => {
    const results = check({ failures: [failure()] })
    expect(failuresThatMisreadTheirStatus(results, () => toolCallFixture('execute', { status: 'declined' }) as ToolCall))
      .toStrictEqual(['bash: states "failed" and reads "declined"'])
  })

  it('answers nothing when the frame reads the word it states', () => {
    const results = check({ failures: [failure()] })
    expect(failuresThatMisreadTheirStatus(results, () => toolCallFixture('execute', { status: 'failed' }) as ToolCall)).toStrictEqual([])
  })
})

describe('openingFrameOf', () => {
  it('answers the raw request frame a fixture pairs with', () => {
    const parentObject = { sessionUpdate: 'tool_call' }
    const fixture: ToolResultFixture = {
      payload: {},
      options: { request: { wrapper: null, topLevel: null, parentObject, rawText: '', supplementalContent: undefined, messageMetadata: undefined } },
    }
    expect(openingFrameOf(fixture)).toBe(parentObject)
  })

  it('answers null for a fixture that pairs with no request', () => {
    expect(openingFrameOf(FIXTURE)).toBeNull()
  })

  // `messageParser` narrows `parentObject` to a plain object or `undefined` before it
  // ever reaches a fixture, so this reader states no narrowing of its own.
  it('answers null for a fixture whose request states no frame', () => {
    const fixture: ToolResultFixture = {
      payload: {},
      options: { request: { wrapper: null, topLevel: null, parentObject: undefined, rawText: '', supplementalContent: undefined, messageMetadata: undefined } },
    }
    expect(openingFrameOf(fixture)).toBeNull()
  })
})

describe('requestsThatAnswerEarly', () => {
  it('reports an opening frame that already carries a result', () => {
    const results = check({ fixtures: { todowrite: FIXTURE } })
    const early = smuggle(toolCallFixture('todo', { status: 'pending' }), { result: { items: [] } })
    expect(requestsThatAnswerEarly(results, () => early)).toStrictEqual(['todowrite'])
  })

  it('answers nothing for an opening frame that states no result', () => {
    const results = check({ fixtures: { todowrite: FIXTURE } })
    expect(requestsThatAnswerEarly(results, () => toolCallFixture('todo', { status: 'pending' }) as ToolCall)).toStrictEqual([])
  })
})

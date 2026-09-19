import type { ClaudeToolRow } from './toolCommon'
import { describe, expect, it } from 'vitest'
import { DEFAULT_TOOL_REQUESTS } from '../../defaultToolRequests'
import { claudeRemoteTriggerFromToolResult, claudeTriggerPayload } from './remoteTrigger'
import { claudeRequestFor } from './toolRequests'

describe('claudeRemoteTriggerFromToolResult', () => {
  it('returns null when tool_use_result is missing and content is unparseable', () => {
    expect(claudeRemoteTriggerFromToolResult(null, 'oops')).toBeNull()
    expect(claudeRemoteTriggerFromToolResult(undefined, '')).toBeNull()
  })

  it('extracts status and parses JSON from tool_use_result', () => {
    const source = claudeRemoteTriggerFromToolResult({
      status: 200,
      json: '{"trigger":{"id":"trig_1","name":"hello"}}',
    }, '')
    expect(source).not.toBeNull()
    expect(source!.status).toBe(200)
    expect(source!.trigger).toEqual({ id: 'trig_1', name: 'hello' })
  })

  it('falls back to parsing the literal HTTP {status}\\n{json} content', () => {
    const source = claudeRemoteTriggerFromToolResult(
      undefined,
      'HTTP 201\n{"trigger":{"id":"trig_2","name":"created"}}',
    )
    expect(source).not.toBeNull()
    expect(source!.status).toBe(201)
    expect(source!.trigger).toEqual({ id: 'trig_2', name: 'created' })
  })

  it('exposes top-level object as trigger when payload has no trigger field', () => {
    const source = claudeRemoteTriggerFromToolResult({
      status: 200,
      json: '{"triggers":[{"id":"trig_a"}]}',
    }, '')
    expect(source!.trigger).toEqual({ triggers: [{ id: 'trig_a' }] })
  })

  it('keeps trigger null when JSON is not an object', () => {
    const source = claudeRemoteTriggerFromToolResult({
      status: 500,
      json: '"oops"',
    }, '')
    expect(source!.status).toBe(500)
    expect(source!.parsed).toBe('oops')
    expect(source!.trigger).toBeNull()
  })

  it('keeps parsed null and trigger null when JSON is malformed', () => {
    const source = claudeRemoteTriggerFromToolResult({
      status: 502,
      json: 'not-json',
    }, '')
    expect(source!.status).toBe(502)
    expect(source!.parsed).toBeNull()
    expect(source!.trigger).toBeNull()
    expect(source!.json).toBe('not-json')
  })
})

/** One Claude REQUEST row, carrying the arguments a trigger call reads and nothing else. */
function triggerRow(input: Record<string, unknown>, toolName = 'RemoteTrigger'): ClaudeToolRow {
  return {
    id: 'tu_1',
    role: 'request',
    toolName,
    input,
    toolUseResult: undefined,
    resultContent: '',
    rawResultContent: undefined,
    images: [],
    isError: undefined,
  }
}

/** The declared request of a call that has not answered yet. */
function requestOf(input: Record<string, unknown>, toolName = 'RemoteTrigger') {
  return claudeRequestFor('trigger', input, { toolName, result: undefined, context: {} })
}

/** The payload of a call, with its request read the way a mounted row reads it. */
function payloadOf(input: Record<string, unknown>, result: ClaudeToolRow | undefined, toolName = 'RemoteTrigger') {
  return claudeTriggerPayload(requestOf(input, toolName), triggerRow(input, toolName), result)
}

// Claude reads its trigger request through the SHARED entry, and deviates on two
// fields alone. The cases below pin which argument keys reach the request, at which
// priority, and that Claude adds no key of its own.
//
// Every key assertion uses `toStrictEqual` or a sorted key list. `toEqual` ignores a
// property whose value is `undefined`, so it passes straight over a stray
// `key: undefined` -- which is exactly what an un-annotated request literal admits.
describe('claudeTriggerPayload', () => {
  it('answers the shared entry on every field when no body and no action argument arrive', () => {
    const input = { trigger_id: 't-1', name: 'Nightly', schedule: '0 0 * * *' }
    expect(requestOf(input)).toStrictEqual(DEFAULT_TOOL_REQUESTS.trigger(input))
  })

  it('carries only the keys a trigger request states, and no other one', () => {
    // An absent field is an omitted key, never an explicit undefined, so the bare
    // default answers the action alone.
    expect(Object.keys(requestOf({})).sort()).toStrictEqual(['action'])
    expect(Object.keys(requestOf({ action: 'create', body: { name: 'Nightly' } })).sort())
      .toStrictEqual(['action', 'name'])
  })

  it('reads the action out of the `action` argument, which the shared entry answers `other` for', () => {
    for (const action of ['list', 'get', 'create', 'update', 'run', 'delete'] as const)
      expect(requestOf({ action }).action).toBe(action)
    expect(DEFAULT_TOOL_REQUESTS.trigger({ action: 'list' }).action).toBe('other')
  })

  it('falls back to `other` for an action word the tool does not spell', () => {
    expect(requestOf({ action: 'pause' }).action).toBe('other')
    expect(requestOf({ action: 42 }).action).toBe('other')
    expect(requestOf({}).action).toBe('other')
  })

  it('titles an unworded action with the tool name, and leaves a worded one untitled', () => {
    expect(payloadOf({}, undefined, 'CronCreate').title).toBe('CronCreate')
    expect(payloadOf({ action: 'list' }, undefined).title).toBeUndefined()
  })

  it('reads the id under `trigger_id`, then `triggerId`, then `id`', () => {
    expect(requestOf({ trigger_id: 't-1', triggerId: 'never read', id: 'never read' }).triggerId).toBe('t-1')
    expect(requestOf({ triggerId: 't-2', id: 'never read' }).triggerId).toBe('t-2')
    expect(requestOf({ id: 't-3' }).triggerId).toBe('t-3')
    expect(requestOf({}).triggerId).toBeUndefined()
  })

  it('reads the schedule under `schedule`, then `cron`', () => {
    expect(requestOf({ schedule: '0 0 * * *', cron: 'never read' }).schedule).toBe('0 0 * * *')
    expect(requestOf({ cron: '@daily' }).schedule).toBe('@daily')
    expect(requestOf({}).schedule).toBeUndefined()
  })

  // The one deviation that no shared entry can answer, and the one real priority
  // decision in this reading. `body` is where `RemoteTrigger` states the label of the
  // trigger it creates or updates, so it wins over a root `name` beside it.
  it('reads the name out of `body`, ahead of a root `name`', () => {
    expect(requestOf({ action: 'create', body: { name: 'From the body' }, name: 'From the root' }).name)
      .toBe('From the body')
    expect(requestOf({ action: 'create', body: { name: 'From the body' } }).name).toBe('From the body')
  })

  it('falls back to the shared root `name` when the body states none', () => {
    expect(requestOf({ action: 'create', name: 'From the root' }).name).toBe('From the root')
    expect(requestOf({ action: 'create', body: {}, name: 'From the root' }).name).toBe('From the root')
    expect(requestOf({ action: 'create', body: { name: '' }, name: 'From the root' }).name).toBe('From the root')
    expect(requestOf({ action: 'create', body: 'not an object', name: 'From the root' }).name).toBe('From the root')
  })

  it('leaves the name undefined when neither the body nor the root states one', () => {
    expect(requestOf({ action: 'create' }).name).toBeUndefined()
    expect(requestOf({ action: 'create', body: { label: 'wrong key' } }).name).toBeUndefined()
  })
})

/** One Claude RESULT row of a trigger call, carrying the endpoint's answer. */
function triggerResult(resultContent: string, isError?: boolean): ClaudeToolRow {
  return {
    id: 'tu_1',
    role: 'result',
    toolName: 'RemoteTrigger',
    input: {},
    toolUseResult: undefined,
    resultContent,
    rawResultContent: resultContent,
    images: [],
    isError,
  }
}

/**
 * The trigger kind asks the failure rung ONE step lower than every other kind.
 *
 * An endpoint that answered outside 2xx still answered, and that answer draws better
 * as the prettified body under a `failed` status than as raw text. A call the tool
 * itself failed carries no `HTTP <status>` line, so it lands on the rung below, where
 * the reason is all the row has.
 */
describe('claudeTriggerPayload outcome', () => {
  it('draws the response body of an endpoint that answered outside 2xx', () => {
    const payload = payloadOf({ action: 'run' }, triggerResult('HTTP 500\n{"error":"boom"}'))
    expect(payload.statusOverride).toBe('failed')
    expect(payload.title).toBe('HTTP 500')
    expect(payload.result).toStrictEqual({ text: '{"error": "boom"}\n', format: 'plain' })
  })

  it('states the reason alone for a call the tool failed', () => {
    const payload = payloadOf({ action: 'run' }, triggerResult('Unknown trigger id', true))
    expect(payload.result).toStrictEqual({ failure: true, text: 'Unknown trigger id' })
  })

  // The unparsed brand stays for a call that did NOT fail and whose text this build
  // cannot read: it states that the call completed, which that row did.
  it('keeps the unparsed brand for an unreadable answer that did not fail', () => {
    const payload = payloadOf({ action: 'run' }, triggerResult('something else entirely'))
    expect(payload.result).toStrictEqual({ unparsed: true, text: 'something else entirely' })
  })
})

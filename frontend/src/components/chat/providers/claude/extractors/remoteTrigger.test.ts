import { describe, expect, it } from 'vitest'
import { claudeRemoteTriggerFromToolResult } from './remoteTrigger'

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

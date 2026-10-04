import type { ProviderPlugin } from '~/components/chat/providers/capabilities'
import type { RowExtractionInput } from '~/components/chat/rowExtractionTypes'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { createToolCall } from '~/components/chat/model/createToolCall'
import { toolCallRow } from '~/components/chat/model/row'
import { __resetProviderRegistryForTest, registerProvider } from '~/components/chat/providers/registry'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { pickString } from '~/lib/jsonPick'
import { outputFilePathFixture } from './outputFilePathFixture'

const provider = AgentProvider.CLAUDE_CODE
const observations: RowExtractionInput[] = []

function plugin(): ProviderPlugin {
  return {
    transcript: {
      classify: () => ({ kind: 'tool_result' }),
      spanRole: () => 'result',
      extractDivider: () => null,
      extractRow: (input) => {
        const id = pickString(input.resolved.parentObject, 'id')
        if (!id)
          return null
        return toolCallRow(createToolCall({
          id,
          name: 'fixture',
          lifecycle: { frameStatus: 'completed', providerOutcome: null, retainedOutcome: null, rowFinal: true, resultFrameLanded: true },
        }, { kind: 'other', request: { args: {} }, result: { content: [] } }), 'result', { request: false, result: true })
      },
      outputFilePaths: (input, call) => {
        observations.push(input)
        const own = input.resolved.parentObject
        const path = pickString(own, 'path')
        return own?.id === call.id && path ? [path] : []
      },
    },
  }
}

beforeEach(() => {
  __resetProviderRegistryForTest()
  observations.length = 0
  registerProvider(provider, plugin())
})

afterEach(() => {
  __resetProviderRegistryForTest()
})

describe('outputFilePathFixture', () => {
  it('uses the original registered call for a foreign test payload', () => {
    const pathsFor = outputFilePathFixture(provider, { id: 'owner', path: '/native/output' })
    expect(pathsFor({ id: 'foreign', path: '/native/output' })).toEqual([])
  })

  it('refuses fixture mutation instead of deriving a new call from it', () => {
    const original = { id: 'owner', path: '/native/output' }
    const pathsFor = outputFilePathFixture(provider, original)
    original.id = 'foreign'
    expect(pathsFor()).toEqual([])
  })

  it('keeps payload bytes and option defaults unchanged across overrides', () => {
    const original = { id: 'owner', path: '/native/output' }
    const options = { agentSessionId: 'initial', spanId: 'owner' }
    const before = JSON.stringify(original)
    const pathsFor = outputFilePathFixture(provider, original, options)
    observations.length = 0
    pathsFor(original, { agentSessionId: 'override', spanId: 'separate' })
    pathsFor()
    expect(observations.map(value => [value.resolved.agentSessionId, value.spanId])).toEqual([
      ['override', 'separate'],
      ['initial', 'owner'],
    ])
    expect(JSON.stringify(original)).toBe(before)
    expect(options).toEqual({ agentSessionId: 'initial', spanId: 'owner' })
  })

  it('copies the initial option shape before a caller changes it', () => {
    const original = { id: 'owner', path: '/native/output' }
    const options = { agentSessionId: 'initial' }
    const pathsFor = outputFilePathFixture(provider, original, options)
    options.agentSessionId = 'changed'
    pathsFor()
    expect(observations.at(-1)?.resolved.agentSessionId).toBe('initial')
  })

  it('throws clearly when a provider or path hook is absent', () => {
    __resetProviderRegistryForTest()
    expect(() => outputFilePathFixture(provider, { id: 'owner' })).toThrow('registered path hook')
    const missing = plugin()
    delete missing.transcript.outputFilePaths
    registerProvider(provider, missing)
    expect(() => outputFilePathFixture(provider, { id: 'owner' })).toThrow('registered path hook')
  })

  it('throws clearly when registered extraction supplies no call', () => {
    expect(() => outputFilePathFixture(provider, { path: '/native/output' })).toThrow('valid registered tool call')
  })
})

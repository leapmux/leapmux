import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '~/lib/jsonPick'
import { providerRow } from '~/test-support/toolCallFixture'
import { museNativeParentInput } from './delivery'
import '~/components/chat/providers'
import '~/components/chat/providers/testMocks'

const fixturePath = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../../../testdata/muse_parent_input_conformance.json')
const fixture = JSON.parse(readFileSync(fixturePath, 'utf8')) as {
  cases: Array<{ name: string, childSessionId: string, expectedBody: string, original: Record<string, unknown>, records: Record<string, unknown>[] }>
}

function objectPath(value: unknown, ...path: string[]): Record<string, unknown> {
  for (const key of path) {
    if (!isObject(value))
      throw new Error('The native fixture path requires an object.')
    value = value[key]
  }
  if (!isObject(value))
    throw new Error('The native fixture path requires an object.')
  return value
}

describe('museNativeParentInput', () => {
  it.each(fixture.cases)('reads the exact delivered body of the native $name parent input', (scenario) => {
    const payload = structuredClone(scenario.original)
    const supplementalContent = {
      nativeRecords: structuredClone(scenario.records),
    }
    const before = structuredClone({ payload, supplementalContent })
    expect(providerRow(AgentProvider.MUSE_CODE, payload, { supplementalContent }))
      .toEqual({ kind: 'user', text: scenario.expectedBody, attachments: [] })
    expect({ payload, supplementalContent }).toEqual(before)
  })

  it.each(fixture.cases)('keeps the worker-owned human $name input distinct from a parent instruction', (scenario) => {
    const payload = structuredClone(scenario.original)
    objectPath(payload, 'payload', 'event', 'delivery_snapshot', 'source').caller = { kind: 'user', actor_id: 'msp-wire' }
    expect(providerRow(AgentProvider.MUSE_CODE, payload, {
      supplementalContent: { nativeRecords: scenario.records },
    })).toBeNull()
  })

  it.each(fixture.cases)('refuses the $name input without its committed parent-call evidence', (scenario) => {
    expect(providerRow(AgentProvider.MUSE_CODE, scenario.original, {
      supplementalContent: {
        nativeRecords: scenario.records.slice(1),
      },
    })).toBeNull()
  })
})

function objectEntry(value: Record<string, unknown>, key: string): Record<string, unknown> {
  const entries = value[key]
  if (!Array.isArray(entries) || !isObject(entries[0]))
    throw new Error('The native fixture requires an object entry.')
  return entries[0]
}

function changeArguments(records: Record<string, unknown>[], change: (args: Record<string, unknown>) => void): void {
  const call = objectEntry(objectPath(records[0], 'payload', 'event'), 'tool_calls')
  const args: unknown = JSON.parse(String(call.args))
  if (!isObject(args))
    throw new Error('The native call arguments require an object.')
  change(args)
  call.args = JSON.stringify(args)
}

function changeReceipt(records: Record<string, unknown>[], change: (receipt: Record<string, unknown>) => void): void {
  const entry = objectEntry(objectPath(records[1], 'payload', 'event'), 'results')
  const receipt: unknown = JSON.parse(String(entry.text))
  if (!isObject(receipt))
    throw new Error('The native receipt requires an object.')
  change(receipt)
  entry.text = JSON.stringify(receipt)
}

type InputChange = (payload: Record<string, unknown>, records: Record<string, unknown>[]) => void

const corruptions: Array<{ name: string, change: InputChange }> = [
  {
    name: 'foreign evidence stream',
    change: (_, records) => { objectPath(records[0], 'stream').id = 'foreign' },
  },
  {
    name: 'conflicting record id',
    change: (_, records) => { records[1]!.id = records[0]!.id },
  },
  {
    name: 'conflicting record sequence',
    change: (_, records) => { records[1]!.sequence = records[0]!.sequence },
  },
  {
    name: 'drain sequence collision',
    change: (payload, records) => { payload.sequence = records[0]!.sequence },
  },
  {
    name: 'foreign child binding',
    change: (_, records) => { objectPath(records[3], 'payload', 'record').child_session_id = 'foreign' },
  },
  {
    name: 'foreign target attestation',
    change: (_, records) => { objectPath(records[4], 'payload', 'record').run_stream = 'run/foreign' },
  },
  {
    name: 'foreign invocation run',
    change: (_, records) => { objectPath(records[1], 'payload').run_id = 'foreign' },
  },
  {
    name: 'foreign batch',
    change: (_, records) => { objectPath(records[1], 'payload', 'event').batch_id = 'foreign' },
  },
  {
    name: 'negative call index',
    change: (_, records) => { objectEntry(objectPath(records[1], 'payload', 'event'), 'results').tool_call_index = -1 },
  },
  {
    name: 'out of range call index',
    change: (_, records) => { objectEntry(objectPath(records[1], 'payload', 'event'), 'results').tool_call_index = 1000 },
  },
  {
    name: 'foreign call id',
    change: (_, records) => { objectEntry(objectPath(records[1], 'payload', 'event'), 'results').tool_call_id = 'foreign' },
  },
  {
    name: 'missing inbox receipt',
    change: (_, records) => changeReceipt(records, (receipt) => { receipt.evidence_refs = [] }),
  },
  {
    name: 'duplicate inbox receipt',
    change: (_, records) => changeReceipt(records, (receipt) => { receipt.evidence_refs = [receipt.evidence_refs, receipt.evidence_refs].flat() }),
  },
  {
    name: 'foreign accepted subagent',
    change: (_, records) => changeReceipt(records, (receipt) => { receipt.subagent_id = 'foreign' }),
  },
  {
    name: 'foreign command',
    change: (_, records) => changeArguments(records, (args) => { args.command_id = 'foreign' }),
  },
  {
    name: 'foreign subagent selector',
    change: (_, records) => changeArguments(records, (args) => { args.subagent_id = 'foreign' }),
  },
  {
    name: 'foreign path selector',
    change: (_, records) => changeArguments(records, (args) => { args.agent_path = 'foreign' }),
  },
  {
    name: 'unsupported delivery mode',
    change: (_, records) => changeArguments(records, (args) => { args.mode = 'unsupported' }),
  },
  {
    name: 'foreign child-agent caller',
    change: (payload) => { objectPath(payload, 'payload', 'event', 'delivery_snapshot', 'source').caller = { kind: 'child_agent', actor_id: 'foreign' } },
  },
  {
    name: 'owned child-agent caller without origin',
    change: (payload, records) => {
      objectPath(payload, 'payload', 'event', 'delivery_snapshot', 'source').caller = { kind: 'child_agent', actor_id: objectPath(payload, 'stream').id }
      records.splice(0, 1)
    },
  },
  {
    name: 'owned child-agent caller without receipt',
    change: (payload, records) => {
      objectPath(payload, 'payload', 'event', 'delivery_snapshot', 'source').caller = { kind: 'child_agent', actor_id: objectPath(payload, 'stream').id }
      records.splice(1, 1)
    },
  },
  {
    name: 'human caller with parent actor id',
    change: (payload) => { objectPath(payload, 'payload', 'event', 'delivery_snapshot', 'source').caller = { kind: 'user', actor_id: objectPath(payload, 'stream').id } },
  },
  {
    name: 'zero payload version',
    change: (_, records) => { records[2]!.payload_schema_version = 0 },
  },
  {
    name: 'negative payload version',
    change: (_, records) => { records[2]!.payload_schema_version = -1 },
  },
  {
    name: 'fractional payload version',
    change: (_, records) => { records[2]!.payload_schema_version = 1.5 },
  },
  {
    name: 'null caller',
    change: (payload) => { objectPath(payload, 'payload', 'event', 'delivery_snapshot', 'source').caller = null },
  },
  {
    name: 'malformed caller',
    change: (payload) => { objectPath(payload, 'payload', 'event', 'delivery_snapshot', 'source').caller = { kind: 'parent_agent' } },
  },
  {
    name: 'absent body',
    change: (payload) => { delete objectPath(payload, 'payload', 'event', 'delivery_snapshot').body },
  },
  {
    name: 'nonstring body',
    change: (payload) => { objectPath(payload, 'payload', 'event', 'delivery_snapshot').body = 0 },
  },
]

const optionalMetadata: Array<{ name: string, change: InputChange }> = [
  {
    name: 'owned child-agent caller',
    change: (payload) => { objectPath(payload, 'payload', 'event', 'delivery_snapshot', 'source').caller = { kind: 'child_agent', actor_id: objectPath(payload, 'stream').id } },
  },
  {
    name: 'independent positive payload versions',
    change: (payload, records) => {
      payload.payload_schema_version = 2
      for (const record of records)
        record.payload_schema_version = 4
    },
  },
  {
    name: 'absent caller',
    change: (payload) => { delete objectPath(payload, 'payload', 'event', 'delivery_snapshot', 'source').caller },
  },
  {
    name: 'independent source version',
    change: (payload) => { objectPath(payload, 'payload', 'event', 'delivery_snapshot', 'source').schema_version = 77 },
  },
  {
    name: 'absent causation',
    change: (payload) => { delete payload.causation_id },
  },
  {
    name: 'unknown causation',
    change: (payload) => { payload.causation_id = null },
  },
  {
    name: 'independent owner run',
    change: (payload) => { objectPath(payload, 'payload', 'event', 'delivery_snapshot', 'source').parent_run_id = 'independent-owner' },
  },
  {
    name: 'independent disposition',
    change: (payload) => { objectPath(payload, 'payload', 'event', 'delivery_snapshot').disposition = 'interrupt' },
  },
  {
    name: 'omitted optional selectors and mode',
    change: (_, records) => changeArguments(records, (args) => {
      delete args.mode
      delete args.subagent_id
      delete args.agent_path
    }),
  },
]

describe('museNativeParentInput evidence validation', () => {
  for (const scenario of fixture.cases) {
    it.each(corruptions)(`refuses ${scenario.name} input with $name`, ({ change }) => {
      const payload = structuredClone(scenario.original)
      const records = structuredClone(scenario.records)
      change(payload, records)
      expect(providerRow(AgentProvider.MUSE_CODE, payload, {
        supplementalContent: { nativeRecords: records },
      })).toBeNull()
    })

    it.each(optionalMetadata)(`accepts ${scenario.name} input with $name`, ({ change }) => {
      const payload = structuredClone(scenario.original)
      const records = structuredClone(scenario.records)
      change(payload, records)
      expect(providerRow(AgentProvider.MUSE_CODE, payload, {
        supplementalContent: { nativeRecords: records },
      }))
        .toEqual({ kind: 'user', text: scenario.expectedBody, attachments: [] })
    })

    it(`checks the stored child identity for ${scenario.name} input`, () => {
      const supplement = { nativeRecords: scenario.records }
      expect(museNativeParentInput(scenario.original, supplement, scenario.childSessionId)).toBe(scenario.expectedBody)
      expect(museNativeParentInput(scenario.original, supplement, 'another-child')).toBeUndefined()
    })

    it.each([
      { name: 'empty', body: '' },
      { name: 'different', body: 'a different delivered body' },
      {
        name: 'large',
        body: 'z'.repeat(1024 * 1024),
      },
    ])(`reads the exact ${scenario.name} $name delivered body independently of the model arguments`, ({ body }) => {
      const payload = structuredClone(scenario.original)
      objectPath(payload, 'payload', 'event', 'delivery_snapshot').body = body
      expect(museNativeParentInput(payload, { nativeRecords: scenario.records }, scenario.childSessionId)).toBe(body)
    })
  }
})

import type { OrgCrdtState } from '~/generated/leapmux/v1/org_crdt_pb'
import type { OrgOp } from '~/generated/leapmux/v1/org_ops_pb'
import { create, toBinary } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import {
  FloatingWindowRecordSchema,

  HLCSchema,
  NodeRecordSchema,

  TabRecordSchema,
} from '~/generated/leapmux/v1/org_crdt_pb'
import {

  OrgOpSchema,
  SetFloatingWindowRegisterOpSchema,
  SetNodeRegisterOpSchema,
  SetTabRegisterOpSchema,
  TombstoneTabOpSchema,
} from '~/generated/leapmux/v1/org_ops_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { applyOp, newState } from './apply'

/**
 * canonicalize follows the same recipe as the Go-side `parity_test.go`
 * canonicalizeState helper: sort each map's keys, marshal each entry
 * with deterministic binary proto, concatenate. The hex output is
 * suitable for byte-equal comparison between independent runs.
 */
function canonicalize(state: OrgCrdtState): string {
  const parts: string[] = []

  parts.push('01:')
  for (const k of Object.keys(state.nodes).sort()) {
    parts.push(`${k}=${bytesToHex(toBinary(NodeRecordSchema, state.nodes[k]))};`)
  }
  parts.push('|02:')
  for (const k of Object.keys(state.tabs).sort()) {
    parts.push(`${k}=${bytesToHex(toBinary(TabRecordSchema, state.tabs[k]))};`)
  }
  parts.push('|03:')
  for (const k of Object.keys(state.floatingWindows).sort()) {
    parts.push(`${k}=${bytesToHex(toBinary(FloatingWindowRecordSchema, state.floatingWindows[k]))};`)
  }
  return parts.join('')
}

function bytesToHex(bytes: Uint8Array): string {
  let out = ''
  for (let i = 0; i < bytes.length; i++) {
    const b = bytes[i].toString(16)
    out += b.length === 1 ? `0${b}` : b
  }
  return out
}

function applyAll(ops: OrgOp[]): OrgCrdtState {
  const state = newState('org')
  for (const op of ops) applyOp(state, op)
  return state
}

function mulberry32(seed: number): () => number {
  let a = seed
  return function () {
    let t = (a += 0x6D2B79F5)
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

function shuffle<T>(items: T[], seed: number): T[] {
  const rng = mulberry32(seed)
  const out = items.slice()
  for (let i = out.length - 1; i > 0; i--) {
    const j = Math.floor(rng() * (i + 1))
    ;[out[i], out[j]] = [out[j], out[i]]
  }
  return out
}

function hlc(p: bigint, l: bigint, c: string) {
  return create(HLCSchema, { physical: p, logical: l, clientId: c })
}

describe('parity', () => {
  it('many shuffled permutations of a heterogeneous op log converge byte-equal', () => {
    const ops: OrgOp[] = [
      // Two clients add two tabs concurrently.
      create(OrgOpSchema, {
        canonicalHlc: hlc(10n, 0n, 'a'),
        body: { case: 'setTabRegister', value: create(SetTabRegisterOpSchema, {
          tabType: TabType.AGENT,
          tabId: 'tA',
          field: { case: 'tileId', value: 'root' },
        }) },
      }),
      create(OrgOpSchema, {
        canonicalHlc: hlc(10n, 1n, 'a'),
        body: { case: 'setTabRegister', value: create(SetTabRegisterOpSchema, {
          tabType: TabType.AGENT,
          tabId: 'tA',
          field: { case: 'workerId', value: 'w1' },
        }) },
      }),
      create(OrgOpSchema, {
        canonicalHlc: hlc(11n, 0n, 'b'),
        body: { case: 'setTabRegister', value: create(SetTabRegisterOpSchema, {
          tabType: TabType.TERMINAL,
          tabId: 'tB',
          field: { case: 'tileId', value: 'root' },
        }) },
      }),
      // Tombstone tA at higher HLC.
      create(OrgOpSchema, {
        canonicalHlc: hlc(50n, 0n, 'a'),
        body: { case: 'tombstoneTab', value: create(TombstoneTabOpSchema, { tabType: TabType.AGENT, tabId: 'tA' }) },
      }),
      // Late SetTab on tA — must drop (remove-wins).
      create(OrgOpSchema, {
        canonicalHlc: hlc(60n, 0n, 'a'),
        body: { case: 'setTabRegister', value: create(SetTabRegisterOpSchema, {
          tabType: TabType.AGENT,
          tabId: 'tA',
          field: { case: 'tileId', value: 'late' },
        }) },
      }),
      // Floating window with an opacity that includes -0.0 (canonicalized).
      create(OrgOpSchema, {
        canonicalHlc: hlc(70n, 0n, 'a'),
        body: { case: 'setFloatingWindowRegister', value: create(SetFloatingWindowRegisterOpSchema, {
          windowId: 'fw1',
          field: { case: 'opacity', value: -0.0 },
        }) },
      }),
      // Concurrent ratio writes on a node.
      create(OrgOpSchema, {
        canonicalHlc: hlc(80n, 0n, 'a'),
        body: { case: 'setNodeRegister', value: create(SetNodeRegisterOpSchema, {
          nodeId: 'split1',
          field: { case: 'ratios', value: { $typeName: 'leapmux.v1.DoubleList', values: [0.6, 0.4] } as never },
        }) },
      }),
      create(OrgOpSchema, {
        canonicalHlc: hlc(81n, 0n, 'b'),
        body: { case: 'setNodeRegister', value: create(SetNodeRegisterOpSchema, {
          nodeId: 'split1',
          field: { case: 'ratios', value: { $typeName: 'leapmux.v1.DoubleList', values: [0.3, 0.7] } as never },
        }) },
      }),
    ]
    const baseline = canonicalize(applyAll(ops))
    for (let i = 0; i < 100; i++) {
      const got = canonicalize(applyAll(shuffle(ops, i)))
      expect(got).toBe(baseline)
    }
  })

  it('-0.0 produces byte-equal canonical output as +0.0', () => {
    const posOp = create(OrgOpSchema, {
      canonicalHlc: hlc(10n, 0n, 'a'),
      body: { case: 'setFloatingWindowRegister', value: create(SetFloatingWindowRegisterOpSchema, {
        windowId: 'fw',
        field: { case: 'opacity', value: 0.0 },
      }) },
    })
    const negOp = create(OrgOpSchema, {
      canonicalHlc: hlc(10n, 0n, 'a'),
      body: { case: 'setFloatingWindowRegister', value: create(SetFloatingWindowRegisterOpSchema, {
        windowId: 'fw',
        field: { case: 'opacity', value: -0.0 },
      }) },
    })
    expect(canonicalize(applyAll([posOp]))).toBe(canonicalize(applyAll([negOp])))
  })
})

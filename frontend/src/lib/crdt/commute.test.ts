import type { OrgCrdtState } from '~/generated/leapmux/v1/org_crdt_pb'
import type { OrgOp } from '~/generated/leapmux/v1/org_ops_pb'
import { create, toBinary } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import {
  HLCSchema,
  NodeRecordSchema,

  TabRecordSchema,
} from '~/generated/leapmux/v1/org_crdt_pb'
import {

  OrgOpSchema,
  SetNodeRegisterOpSchema,
  SetTabRegisterOpSchema,
  TombstoneNodeOpSchema,
} from '~/generated/leapmux/v1/org_ops_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { applyOp, newState } from './apply'

function hlc(physical: bigint, logical: bigint, clientId: string) {
  return create(HLCSchema, { physical, logical, clientId })
}

/**
 * canonicalize returns a deterministic byte representation of the
 * post-Apply state. Walks each map in sorted key order and binary-
 * marshals each entry. The hashes match across permutations IFF the
 * state is invariant under op order — that's the property the parity
 * test asserts.
 */
function canonicalize(state: OrgCrdtState): string {
  const parts: string[] = []
  parts.push('nodes:')
  for (const k of Object.keys(state.nodes).sort()) {
    parts.push(`${k}=${bytesToHex(toBinary(NodeRecordSchema, state.nodes[k]))};`)
  }
  parts.push('|tabs:')
  for (const k of Object.keys(state.tabs).sort()) {
    parts.push(`${k}=${bytesToHex(toBinary(TabRecordSchema, state.tabs[k]))};`)
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

function shuffle<T>(items: T[], seed: number): T[] {
  const rng = mulberry32(seed)
  const out = items.slice()
  for (let i = out.length - 1; i > 0; i--) {
    const j = Math.floor(rng() * (i + 1))
    ;[out[i], out[j]] = [out[j], out[i]]
  }
  return out
}

function mulberry32(a: number): () => number {
  return function () {
    let t = (a += 0x6D2B79F5)
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

function setKindOp(nodeId: string, kind: number, p: bigint, l: bigint, c: string): OrgOp {
  return create(OrgOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: { case: 'setNodeRegister', value: create(SetNodeRegisterOpSchema, { nodeId, field: { case: 'kind', value: kind } }) },
  })
}

function setPosOp(nodeId: string, position: string, p: bigint, l: bigint, c: string): OrgOp {
  return create(OrgOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: { case: 'setNodeRegister', value: create(SetNodeRegisterOpSchema, { nodeId, field: { case: 'position', value: position } }) },
  })
}

function setTabTile(tabId: string, tileId: string, p: bigint, l: bigint, c: string): OrgOp {
  return create(OrgOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: {
      case: 'setTabRegister',
      value: create(SetTabRegisterOpSchema, { tabType: TabType.AGENT, tabId, field: { case: 'tileId', value: tileId } }),
    },
  })
}

function tombstoneNode(nodeId: string, p: bigint, l: bigint, c: string): OrgOp {
  return create(OrgOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: { case: 'tombstoneNode', value: create(TombstoneNodeOpSchema, { nodeId }) },
  })
}

describe('commute', () => {
  it('every permutation of a 5-op stream yields byte-equal canonical state', () => {
    const ops: OrgOp[] = [
      setKindOp('n1', 1, 10n, 0n, 'a'),
      setPosOp('n1', 'A', 15n, 0n, 'a'),
      setPosOp('n1', 'B', 20n, 0n, 'b'),
      setKindOp('n2', 1, 12n, 0n, 'a'),
      setTabTile('t1', 'n2', 25n, 0n, 'a'),
    ]
    const baseline = canonicalize(applyAll(shuffle(ops, 0)))
    for (let seed = 1; seed < 30; seed++) {
      const got = canonicalize(applyAll(shuffle(ops, seed)))
      expect(got).toBe(baseline)
    }
  })

  it('set then Tombstone commutes with Tombstone then Set', () => {
    const ops: OrgOp[] = [
      setPosOp('n1', 'A', 10n, 0n, 'a'),
      tombstoneNode('n1', 20n, 0n, 'a'),
    ]
    const a = canonicalize(applyAll(ops))
    const b = canonicalize(applyAll([ops[1], ops[0]]))
    expect(a).toBe(b)
  })

  it('concurrent ops at the same (physical, logical) tie-break by client_id', () => {
    const a = setPosOp('n1', 'from-a', 10n, 0n, 'alpha')
    const b = setPosOp('n1', 'from-b', 10n, 0n, 'bravo')
    const s1 = applyAll([a, b])
    const s2 = applyAll([b, a])
    expect(s1.nodes.n1.position?.value).toBe('from-b')
    expect(s2.nodes.n1.position?.value).toBe('from-b')
  })
})

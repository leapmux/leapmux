import type { UserCrdtState } from '~/generated/proto/leapmux/v1/user_crdt_pb'
import type { CrdtOp } from '~/generated/proto/leapmux/v1/user_ops_pb'
import { create, toBinary } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import {
  FloatingWindowRecordSchema,

  HLCSchema,
  NodeRecordSchema,

  TabRecordSchema,
  WorkspaceContentsRecordSchema,
} from '~/generated/proto/leapmux/v1/user_crdt_pb'
import {

  CrdtOpSchema,
  SetFloatingWindowRegisterOpSchema,
  SetNodeRegisterOpSchema,
  SetTabRegisterOpSchema,
  SetWorkspaceRegisterOpSchema,
  TombstoneTabOpSchema,
  TombstoneWorkspaceOpSchema,
} from '~/generated/proto/leapmux/v1/user_ops_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { applyOp, newState } from './apply'

/**
 * canonicalize follows the same recipe as the Go-side `parity_test.go`
 * canonicalizeState helper: sort each map's keys, marshal each entry
 * with deterministic binary proto, concatenate. The hex output is
 * suitable for byte-equal comparison between independent runs.
 *
 * Covers the workspaces map (section 04) just like the backend helper, so
 * SetWorkspaceRegister / TombstoneWorkspace convergence is observable.
 */
function canonicalize(state: UserCrdtState): string {
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
  parts.push('|04:')
  for (const k of Object.keys(state.workspaces).sort()) {
    parts.push(`${k}=${bytesToHex(toBinary(WorkspaceContentsRecordSchema, state.workspaces[k]))};`)
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

function applyAll(ops: CrdtOp[]): UserCrdtState {
  const state = newState('user')
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
    const ops: CrdtOp[] = [
      // Two clients add two tabs concurrently.
      create(CrdtOpSchema, {
        canonicalHlc: hlc(10n, 0n, 'a'),
        body: { case: 'setTabRegister', value: create(SetTabRegisterOpSchema, {
          tabType: TabType.AGENT,
          tabId: 'tA',
          field: { case: 'tileId', value: 'root' },
        }) },
      }),
      create(CrdtOpSchema, {
        canonicalHlc: hlc(10n, 1n, 'a'),
        body: { case: 'setTabRegister', value: create(SetTabRegisterOpSchema, {
          tabType: TabType.AGENT,
          tabId: 'tA',
          field: { case: 'workerId', value: 'w1' },
        }) },
      }),
      create(CrdtOpSchema, {
        canonicalHlc: hlc(11n, 0n, 'b'),
        body: { case: 'setTabRegister', value: create(SetTabRegisterOpSchema, {
          tabType: TabType.TERMINAL,
          tabId: 'tB',
          field: { case: 'tileId', value: 'root' },
        }) },
      }),
      // Tombstone tA at higher HLC.
      create(CrdtOpSchema, {
        canonicalHlc: hlc(50n, 0n, 'a'),
        body: { case: 'tombstoneTab', value: create(TombstoneTabOpSchema, { tabType: TabType.AGENT, tabId: 'tA' }) },
      }),
      // Late SetTab on tA — must drop (remove-wins).
      create(CrdtOpSchema, {
        canonicalHlc: hlc(60n, 0n, 'a'),
        body: { case: 'setTabRegister', value: create(SetTabRegisterOpSchema, {
          tabType: TabType.AGENT,
          tabId: 'tA',
          field: { case: 'tileId', value: 'late' },
        }) },
      }),
      // Floating window with an opacity that includes -0.0 (canonicalized).
      create(CrdtOpSchema, {
        canonicalHlc: hlc(70n, 0n, 'a'),
        body: { case: 'setFloatingWindowRegister', value: create(SetFloatingWindowRegisterOpSchema, {
          windowId: 'fw1',
          field: { case: 'opacity', value: -0.0 },
        }) },
      }),
      // Concurrent ratio writes on a node.
      create(CrdtOpSchema, {
        canonicalHlc: hlc(80n, 0n, 'a'),
        body: { case: 'setNodeRegister', value: create(SetNodeRegisterOpSchema, {
          nodeId: 'split1',
          field: { case: 'ratios', value: { $typeName: 'leapmux.v1.DoubleList', values: [0.6, 0.4] } as never },
        }) },
      }),
      create(CrdtOpSchema, {
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
    const posOp = create(CrdtOpSchema, {
      canonicalHlc: hlc(10n, 0n, 'a'),
      body: { case: 'setFloatingWindowRegister', value: create(SetFloatingWindowRegisterOpSchema, {
        windowId: 'fw',
        field: { case: 'opacity', value: 0.0 },
      }) },
    })
    const negOp = create(CrdtOpSchema, {
      canonicalHlc: hlc(10n, 0n, 'a'),
      body: { case: 'setFloatingWindowRegister', value: create(SetFloatingWindowRegisterOpSchema, {
        windowId: 'fw',
        field: { case: 'opacity', value: -0.0 },
      }) },
    })
    expect(canonicalize(applyAll([posOp]))).toBe(canonicalize(applyAll([negOp])))
  })

  // Workspace lifecycle create/delete now flow through the op log as
  // SetWorkspaceRegister / TombstoneWorkspace. Workspace IDs are fresh
  // nanoids per CreateWorkspace and soft-deleted IDs are never recycled,
  // so a register and a tombstone for the SAME workspace id never co-occur
  // in a real op log — the commuting case is independent workspaces, which
  // this pins: permuting creates/deletes of distinct workspaces converges.
  it('workspace register/tombstone ops on distinct ids converge under any permutation', () => {
    const ops: CrdtOp[] = [
      create(CrdtOpSchema, {
        canonicalHlc: hlc(10n, 0n, 'hub'),
        body: { case: 'setWorkspaceRegister', value: create(SetWorkspaceRegisterOpSchema, { workspaceId: 'w1' }) },
      }),
      create(CrdtOpSchema, {
        canonicalHlc: hlc(20n, 0n, 'hub'),
        body: { case: 'tombstoneWorkspace', value: create(TombstoneWorkspaceOpSchema, { workspaceId: 'w2' }) },
      }),
      create(CrdtOpSchema, {
        canonicalHlc: hlc(30n, 0n, 'hub'),
        body: { case: 'setWorkspaceRegister', value: create(SetWorkspaceRegisterOpSchema, { workspaceId: 'w3' }) },
      }),
    ]
    const baseline = canonicalize(applyAll(ops))
    for (let i = 0; i < 50; i++) {
      const got = canonicalize(applyAll(shuffle(ops, i)))
      expect(got).toBe(baseline)
    }
    // w1 and w3 seeded, w2 tombstoned (was never created) — final state.
    const final = applyAll(ops).workspaces
    expect(final.w1).toBeDefined()
    expect(final.w2).toBeUndefined()
    expect(final.w3).toBeDefined()
  })
})

import type { OrgCrdtState } from '~/generated/leapmux/v1/org_crdt_pb'
import type { CRDTBridge } from '~/lib/crdt'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { OrgCrdtStateSchema } from '~/generated/leapmux/v1/org_crdt_pb'
import { ctxFromBridge, HLCClock, withBridgeAndState } from '~/lib/crdt'

/**
 * `withBridgeAndState` is the wiring-readiness preamble every CRDT-
 * emitting op-builder in the layout / floating-window stores relies
 * on. The contract has three failure modes (no orgId, no clock, no
 * speculativeState) and one happy path; these tests pin each so a
 * future change to the bridge interface or the helper's signature
 * can't silently bypass a guard.
 */
describe('withBridgeAndState', () => {
  function makeBridge(overrides: Partial<{
    orgId: string | null
    clock: HLCClock | null
    state: OrgCrdtState | null
  }> = {}): CRDTBridge {
    const orgId = overrides.orgId === undefined ? 'org-1' : overrides.orgId
    const clock = overrides.clock === undefined ? new HLCClock('client-1') : overrides.clock
    const state = overrides.state === undefined ? create(OrgCrdtStateSchema, { orgId: 'org-1' }) : overrides.state
    return {
      orgId: () => orgId ?? '',
      workspaceId: () => 'ws-1',
      enqueue: batch => batch.batchId,
      clock: () => clock,
      originClientId: () => 'origin-1',
      speculativeState: () => state,
    }
  }

  it('happy path: invokes fn(ctx, state) and returns its result', () => {
    const bridge = makeBridge()
    const result = withBridgeAndState(bridge, (ctx, state) => {
      expect(ctx.orgId).toBe('org-1')
      expect(ctx.originClientId).toBe('origin-1')
      expect(ctx.clock).toBeInstanceOf(HLCClock)
      expect(state.orgId).toBe('org-1')
      return 'ok' as const
    }, null)
    expect(result).toBe('ok')
  })

  it('returns fallback when bridge has no orgId', () => {
    const bridge = makeBridge({ orgId: '' })
    let called = false
    const result = withBridgeAndState(bridge, () => {
      called = true
      return 'should-not-run'
    }, 'fallback')
    expect(called).toBe(false)
    expect(result).toBe('fallback')
  })

  it('returns fallback when bridge has no clock', () => {
    const bridge = makeBridge({ clock: null })
    let called = false
    const result = withBridgeAndState(bridge, () => {
      called = true
      return 'should-not-run'
    }, null)
    expect(called).toBe(false)
    expect(result).toBeNull()
  })

  it('returns fallback when bridge.speculativeState() returns null', () => {
    const bridge = makeBridge({ state: null })
    let called = false
    const result = withBridgeAndState(bridge, () => {
      called = true
      return 'should-not-run'
    }, 42)
    expect(called).toBe(false)
    expect(result).toBe(42)
  })

  it('propagates exceptions thrown from fn (does not swallow)', () => {
    const bridge = makeBridge()
    expect(() => withBridgeAndState(bridge, () => {
      throw new Error('boom')
    }, null)).toThrow('boom')
  })

  it('ctx threads through the bridge\'s originClientId verbatim', () => {
    // Side-channel: the helper must pass originClientId from
    // `bridge.originClientId()` into the OpBuilderCtx so the op-id
    // / origin tracking flows through unchanged for downstream
    // op-emitters.
    const bridge: CRDTBridge = {
      orgId: () => 'org-2',
      workspaceId: () => 'ws-2',
      enqueue: batch => batch.batchId,
      clock: () => new HLCClock('client-2'),
      originClientId: () => 'unique-origin-xyz',
      speculativeState: () => create(OrgCrdtStateSchema, { orgId: 'org-2' }),
    }
    let captured: string | null = null
    withBridgeAndState(bridge, (ctx) => {
      captured = ctx.originClientId
    }, undefined as void)
    expect(captured).toBe('unique-origin-xyz')
  })
})

describe('ctxFromBridge', () => {
  // Mirror of the helper's null-guards — these are the same checks
  // `withBridgeAndState` performs internally, but `ctxFromBridge` is
  // also called directly from sites that don't need state (e.g. the
  // `emitUpdateRatios` / `emitUpdatePosition` single-op writes).
  function makeBridge(orgId: string, clock: HLCClock | null): CRDTBridge {
    return {
      orgId: () => orgId,
      workspaceId: () => 'ws',
      enqueue: batch => batch.batchId,
      clock: () => clock,
      originClientId: () => 'origin',
      speculativeState: () => null,
    }
  }

  it('returns the ctx with bridge fields when fully wired', () => {
    const clock = new HLCClock('client')
    const ctx = ctxFromBridge(makeBridge('org-1', clock))
    expect(ctx).not.toBeNull()
    expect(ctx!.orgId).toBe('org-1')
    expect(ctx!.originClientId).toBe('origin')
    expect(ctx!.clock).toBe(clock)
  })

  it('returns null when orgId is empty', () => {
    const clock = new HLCClock('client')
    expect(ctxFromBridge(makeBridge('', clock))).toBeNull()
  })

  it('returns null when clock is missing', () => {
    expect(ctxFromBridge(makeBridge('org-1', null))).toBeNull()
  })
})

import type { FileAttachment } from '~/components/chat/attachments'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AgentInputKind } from '~/generated/proto/leapmux/v1/agent_pb'
import { deferred } from '~/test-support/async'
import { createAgentInputEnqueueRetry } from './agentInputEnqueueRetry'

// ---------------------------------------------------------------------------
// A hash that finishes after the hash of a later call
//
// The hash worker answers in post order, so through the real worker a later
// call never finishes its hash first, and agentInputEnqueueRetry.test.ts holds
// the replies of that worker instead. The order inverts in the browser when the
// first hash runs past its time budget: the worker client then hashes it on the
// main thread in work slices, while the hash of the later call runs in a fresh
// worker (see digestFieldsOffThread). Those slices end by the clock, so a test
// cannot keep them waiting. This file therefore replaces the off-thread digest
// with the real digest behind a gate that the test opens.
// ---------------------------------------------------------------------------

/**
 * The gate of the next digest that starts. A call reads it when it starts, so a
 * later reset does not open the gate of a held digest.
 */
const digest = vi.hoisted(() => ({
  gate: Promise.resolve(),
}))

vi.mock('~/lib/fieldDigestClient', async () => {
  const { digestFields } = await import('~/lib/fieldDigest')
  return {
    digestFieldsOffThread: async (fields: readonly Uint8Array[]) => {
      const gate = digest.gate
      await gate
      return digestFields(fields)
    },
  }
})

afterEach(() => {
  digest.gate = Promise.resolve()
})

function message(text: string) {
  return {
    agentId: 'agent-1',
    kind: AgentInputKind.USER_MESSAGE,
    text,
    attachments: [] as FileAttachment[],
    scheduledAt: '',
  }
}

describe('createAgentInputEnqueueRetry', () => {
  it('joins the attempt that a later call for the same payload minted first', async () => {
    let sequence = 0
    const retry = createAgentInputEnqueueRetry(() => `input-${++sequence}`)

    const gate = deferred<void>()
    digest.gate = gate.promise
    const held = retry.inputIdFor(message('deploy it'))
    digest.gate = Promise.resolve()
    const later = await retry.inputIdFor(message('deploy it'))
    gate.resolve()

    expect(await held).toBe(later)
    expect(sequence).toBe(1)
  })
})

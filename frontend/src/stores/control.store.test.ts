import type { ControlRequest } from '~/stores/control.store'
import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { ControlResponseState } from '~/generated/proto/leapmux/v1/agent_pb'
import { createControlStore, pendingQuestionCount, requestInstanceId, shownControlRequest } from '~/stores/control.store'

// claimToken defaults to a per-requestId token (so a re-add of the SAME id models a reconnect replay of
// the SAME instance); pass an explicit token to model a REISSUED instance that reused the request_id.
function makeRequest(requestId: string, agentId: string, payload: Record<string, unknown> = { data: requestId }, claimToken: string = `tok-${requestId}`): ControlRequest {
  return { requestId, agentId, payload, claimToken }
}

describe('createControlStore', () => {
  it('retains confirmed delivery when an older request update arrives', () => {
    const store = createControlStore()
    const request = makeRequest('request', 'agent')
    store.addRequest('agent', { ...request, responseState: ControlResponseState.DELIVERED })
    for (const responseState of [ControlResponseState.READY, ControlResponseState.PENDING, ControlResponseState.UNCERTAIN]) {
      store.addRequest('agent', { ...request, responseState })
      expect(store.getRequests('agent')[0]?.responseState).toBe(ControlResponseState.DELIVERED)
    }
    store.addRequest('agent', { ...request, claimToken: 'new', responseState: ControlResponseState.READY })
    expect(store.getRequests('agent')[1]?.responseState).toBe(ControlResponseState.READY)
  })
  it('retains an owned copy of the original request bytes', () => {
    const store = createControlStore()
    const originalPayload = new TextEncoder().encode('{"n":9007199254740993}')
    store.addRequest('agent', { ...makeRequest('request', 'agent'), originalPayload })
    originalPayload.fill(0)
    expect(new TextDecoder().decode(store.getRequests('agent')[0]?.originalPayload)).toBe('{"n":9007199254740993}')
  })

  it('keeps the current request when an older cancellation arrives', () => {
    const store = createControlStore()
    store.addRequest('agent-1', makeRequest('request', 'agent-1', {}, 'new-claim'))
    store.removeRequest('agent-1', 'request', 'old-claim')
    expect(store.getRequests('agent-1').map(request => request.claimToken)).toEqual(['new-claim'])
    store.removeRequest('agent-1', 'request', 'new-claim')
    expect(store.getRequests('agent-1')).toHaveLength(0)
  })

  it('keeps a new instance when an identical earlier request still awaits cancellation', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      const payload = { request: { tool_name: 'Read', input: { path: 'sample.txt' } } }
      store.addRequest('agent-1', makeRequest('1', 'agent-1', payload, 'first'))
      store.addRequest('agent-1', makeRequest('1', 'agent-1', payload, 'second'))
      store.addRequest('agent-1', makeRequest('1', 'agent-1', payload, 'second'))
      expect(store.getRequests('agent-1').map(request => request.claimToken)).toEqual(['first', 'second'])
      store.removeRequest('agent-1', '1', 'first')
      expect(store.getRequests('agent-1').map(request => request.claimToken)).toEqual(['second'])
      dispose()
    })
  })

  it('should initialize with empty state', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      expect(store.state.pendingByAgent).toEqual({})
      dispose()
    })
  })

  it('should create entry for new agent on addRequest', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      const req = makeRequest('r1', 'agent-1')
      store.addRequest('agent-1', req)
      expect(store.getRequests('agent-1')).toHaveLength(1)
      expect(store.getRequests('agent-1')[0]?.requestId).toBe('r1')
      dispose()
    })
  })

  it('should append to existing agent on addRequest', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      store.addRequest('agent-1', makeRequest('r1', 'agent-1'))
      store.addRequest('agent-1', makeRequest('r2', 'agent-1'))
      expect(store.getRequests('agent-1')).toHaveLength(2)
      expect(store.getRequests('agent-1')[0]?.requestId).toBe('r1')
      expect(store.getRequests('agent-1')[1]?.requestId).toBe('r2')
      dispose()
    })
  })

  it('should remove specific request on removeRequest', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      store.addRequest('agent-1', makeRequest('r1', 'agent-1'))
      store.addRequest('agent-1', makeRequest('r2', 'agent-1'))
      store.removeRequest('agent-1', 'r1', 'tok-r1')
      expect(store.getRequests('agent-1')).toHaveLength(1)
      expect(store.getRequests('agent-1')[0]?.requestId).toBe('r2')
      dispose()
    })
  })

  it('should not crash on removeRequest with non-existent agent', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      expect(() => store.removeRequest('no-agent', 'r1')).not.toThrow()
      dispose()
    })
  })

  it('should not crash on removeRequest with non-existent requestId', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      store.addRequest('agent-1', makeRequest('r1', 'agent-1'))
      expect(() => store.removeRequest('agent-1', 'nonexistent')).not.toThrow()
      expect(store.getRequests('agent-1')).toHaveLength(1)
      dispose()
    })
  })

  // Any per-request state that outlives its request keys on this value, so the
  // token has to reach it. Two instances of one id must not share a key, and a
  // request with no token must keep the bare id it had before the token existed.
  it('should distinguish two instances of one request id on requestInstanceId', () => {
    const first = makeRequest('r1', 'agent-1')
    const second = { ...makeRequest('r1', 'agent-1'), claimToken: 'tok-other' }
    expect(requestInstanceId(first)).toBe('r1:tok-r1')
    expect(requestInstanceId(second)).toBe('r1:tok-other')
    expect(requestInstanceId(first)).not.toBe(requestInstanceId(second))
    expect(requestInstanceId({ requestId: 'r1', agentId: 'agent-1', payload: {} })).toBe('r1')
  })

  it('should return empty array for unknown agent on getRequests', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      expect(store.getRequests('unknown-agent')).toEqual([])
      dispose()
    })
  })

  it('should clear all requests for an agent on clearAgent', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      store.addRequest('agent-1', makeRequest('r1', 'agent-1'))
      store.addRequest('agent-1', makeRequest('r2', 'agent-1'))
      store.addRequest('agent-2', makeRequest('r3', 'agent-2'))
      store.clearAgent('agent-1')
      expect(store.getRequests('agent-1')).toEqual([])
      expect(store.getRequests('agent-2')).toHaveLength(1)
      dispose()
    })
  })

  it('should empty everything on clearAll', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      store.addRequest('agent-1', makeRequest('r1', 'agent-1'))
      store.addRequest('agent-2', makeRequest('r2', 'agent-2'))
      // The flag opens only while a question waits, so give agent-2 one.
      // Otherwise the flag stays false, and the check after clearAll proves
      // nothing.
      store.addRequest('agent-2', { ...makeRequest('q2', 'agent-2'), nonBlocking: true })
      store.setQuestionsOpen('agent-2', true)
      expect(store.questionsOpen('agent-2')).toBe(true)
      store.clearAll()

      // Previously populated agents must be empty too: a store write MERGES
      // the object it is given, so `setState('pendingByAgent', {})` would
      // clear nothing.
      expect(store.getRequests('agent-1')).toEqual([])
      expect(store.getRequests('agent-2')).toEqual([])
      expect(store.questionsOpen('agent-2')).toBe(false)

      // After clearAll, new additions should work correctly
      store.addRequest('agent-3', makeRequest('r3', 'agent-3'))
      expect(store.getRequests('agent-3')).toHaveLength(1)
      expect(store.getRequests('agent-3')[0]?.requestId).toBe('r3')
      dispose()
    })
  })

  // Two Codex / ACP-family tabs commonly produce identical request_ids
  // (the JSON-RPC id space is per-subprocess and starts low). Answering
  // one tab's prompt must not silently suppress the sibling's prompt.
  it('does not suppress a sibling agent request that shares a requestId', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      store.addRequest('agent-A', makeRequest('1', 'agent-A'))
      store.removeRequest('agent-A', '1', 'tok-1')

      store.addRequest('agent-B', makeRequest('1', 'agent-B'))
      expect(store.getRequests('agent-B')).toHaveLength(1)
      expect(store.getRequests('agent-B')[0]?.requestId).toBe('1')
      dispose()
    })
  })

  // Stale replay protection still works within a single agent: a request
  // the user just answered must not redraw if the same row is replayed
  // before the backend's DELETE round-trip is observed by the new stream.
  it('keeps suppressing the same agent+requestId after a response', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      store.addRequest('agent-1', makeRequest('r1', 'agent-1'))
      store.removeRequest('agent-1', 'r1', 'tok-r1')

      // Simulate a replayed controlRequest landing during a reconnect.
      store.addRequest('agent-1', makeRequest('r1', 'agent-1'))
      expect(store.getRequests('agent-1')).toHaveLength(0)
      dispose()
    })
  })

  // Claude Code can reuse request_id for a revised ExitPlanMode prompt after
  // the user rejects a plan with feedback. That is not a stale replay: it is a
  // REISSUED instance carrying a fresh claimToken, so the next banner must be shown.
  it('does not suppress a revised request that reuses requestId within the same agent', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      store.addRequest('agent-1', makeRequest('r1', 'agent-1', {
        request: { tool_name: 'ExitPlanMode', input: { plan: 'first plan' } },
      }, 'tok-r1-first'))
      store.removeRequest('agent-1', 'r1', 'tok-r1-first')

      // The reissued prompt carries a fresh claimToken (a new PersistControlRequest instance).
      store.addRequest('agent-1', makeRequest('r1', 'agent-1', {
        request: { tool_name: 'ExitPlanMode', input: { plan: 'revised plan' } },
      }, 'tok-r1-revised'))
      expect(store.getRequests('agent-1')).toHaveLength(1)
      expect(store.getRequests('agent-1')[0]?.payload).toEqual({
        request: { tool_name: 'ExitPlanMode', input: { plan: 'revised plan' } },
      })
      dispose()
    })
  })

  // The regression this closes (the UI half of the backend claim_token fix): after the user answers a
  // request, an IDENTICAL re-ask (same request_id AND same payload) that carries a FRESH claimToken --
  // a genuinely new instance, e.g. the agent restarted and its JSON-RPC counter reset -- must be SHOWN,
  // not swallowed as a duplicate. The payload fingerprint alone would suppress it (same payload); the
  // per-instance claimToken is what tells the new instance apart.
  it('shows an identical re-ask (same payload) that carries a fresh claimToken after a prior answer', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      const payload = { request: { tool_name: 'Bash', input: { command: 'ls' } } }
      store.addRequest('agent-1', makeRequest('1', 'agent-1', payload, 'instA'))
      store.removeRequest('agent-1', '1', 'instA')

      // A reconnect replay of the SAME instance (same token) stays suppressed.
      store.addRequest('agent-1', makeRequest('1', 'agent-1', payload, 'instA'))
      expect(store.getRequests('agent-1')).toHaveLength(0)

      // A genuine re-ask by a NEW instance (fresh token, identical payload) is shown.
      store.addRequest('agent-1', makeRequest('1', 'agent-1', payload, 'instB'))
      expect(store.getRequests('agent-1')).toHaveLength(1)
      expect(store.getRequests('agent-1')[0]?.claimToken).toBe('instB')
      dispose()
    })
  })

  // Tokenless fallback: a synthetic / pre-token row (no claimToken) still dedups by the payload
  // fingerprint, so an identical replay is suppressed while a differing payload is shown.
  it('falls back to the payload fingerprint when a request carries no claimToken', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      const p1 = { request: { tool_name: 'Bash', input: { command: 'ls' } } }
      const p2 = { request: { tool_name: 'Bash', input: { command: 'rm -rf /' } } }
      store.addRequest('agent-1', makeRequest('1', 'agent-1', p1, ''))
      store.removeRequest('agent-1', '1', '')

      store.addRequest('agent-1', makeRequest('1', 'agent-1', p1, '')) // identical -> suppressed
      expect(store.getRequests('agent-1')).toHaveLength(0)
      store.addRequest('agent-1', makeRequest('1', 'agent-1', p2, '')) // different payload -> shown
      expect(store.getRequests('agent-1')).toHaveLength(1)
      dispose()
    })
  })

  it('keeps a revised same-id request pending when the old cancel arrives late', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      const firstPayload = {
        request: { tool_name: 'ExitPlanMode', input: { plan: 'first plan' } },
      }
      const revisedPayload = {
        request: { tool_name: 'ExitPlanMode', input: { plan: 'revised plan' } },
      }
      store.addRequest('agent-1', makeRequest('r1', 'agent-1', firstPayload, 'old-claim'))
      store.addRequest('agent-1', makeRequest('r1', 'agent-1', revisedPayload, 'new-claim'))

      store.removeRequest('agent-1', 'r1', 'old-claim')
      expect(store.getRequests('agent-1')).toHaveLength(1)
      expect(store.getRequests('agent-1')[0]?.payload).toEqual(revisedPayload)
      dispose()
    })
  })

  // The previous wipe-on-overflow behavior could let an answered prompt
  // re-add itself once 100 unrelated responses cleared the dedup set.
  // The LRU eviction must keep the most-recent entries protected even
  // after the cap is exceeded.
  it('preserves the most-recent suppressions after overflow', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      store.addRequest('agent-1', makeRequest('keep', 'agent-1'))
      store.removeRequest('agent-1', 'keep', 'tok-keep')

      // Answer 200 unrelated requests across other agents to force eviction.
      for (let i = 0; i < 200; i++) {
        const id = `noise-${i}`
        store.addRequest(`agent-noise-${i}`, makeRequest(id, `agent-noise-${i}`))
        store.removeRequest(`agent-noise-${i}`, id, `tok-${id}`)
      }

      // The most-recent suppressed key from the noise burst must still block.
      store.addRequest('agent-noise-199', makeRequest('noise-199', 'agent-noise-199'))
      expect(store.getRequests('agent-noise-199')).toHaveLength(0)
      dispose()
    })
  })

  // A blocking request takes the composer over, so every blocking request must
  // precede every question that waits for the reader to open it.
  it('places each blocking request before every non-blocking one', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      store.addRequest('agent-1', makeRequest('B1', 'agent-1'))
      store.addRequest('agent-1', { ...makeRequest('Q1', 'agent-1'), nonBlocking: true })
      store.addRequest('agent-1', makeRequest('B2', 'agent-1'))
      expect(store.getRequests('agent-1').map(request => request.requestId)).toEqual(['B1', 'B2', 'Q1'])
      expect(pendingQuestionCount(store.getRequests('agent-1'))).toBe(1)
      dispose()
    })
  })

  // A reconnect can replay one request twice: once before the worker read the
  // flag and once after. The second arrival merges into the first entry and
  // moves it out of the blocking group.
  it('moves a merged request behind the blocking requests when a replay adds the non-blocking flag', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      store.addRequest('agent-1', makeRequest('Q1', 'agent-1'))
      store.addRequest('agent-1', makeRequest('B1', 'agent-1'))
      store.addRequest('agent-1', { ...makeRequest('Q1', 'agent-1'), nonBlocking: true })
      const requests = store.getRequests('agent-1')
      expect(requests.map(request => request.requestId)).toEqual(['B1', 'Q1'])
      expect(requests).toHaveLength(2)
      expect(requests[1]?.nonBlocking).toBe(true)
      dispose()
    })
  })

  // A provider stop drops only the requests its agent still waits for. A
  // delivered request can still need recording, and a question belongs to the
  // conversation.
  it('keeps DELIVERED and non-blocking requests when the provider stops', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      store.addRequest('agent-1', { ...makeRequest('blocking', 'agent-1'), responseState: ControlResponseState.READY })
      store.addRequest('agent-1', { ...makeRequest('delivered', 'agent-1'), responseState: ControlResponseState.DELIVERED })
      store.addRequest('agent-1', { ...makeRequest('question', 'agent-1'), nonBlocking: true, responseState: ControlResponseState.READY })
      store.clearProviderRequests('agent-1')
      expect(store.getRequests('agent-1').map(request => request.requestId)).toEqual(['delivered', 'question'])
      dispose()
    })
  })

  it('closes the questions when the last one leaves, so a later question does not open by itself', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      store.addRequest('agent-A', { ...makeRequest('Q1', 'agent-A'), nonBlocking: true })
      store.setQuestionsOpen('agent-A', true)
      expect(store.questionsOpen('agent-A')).toBe(true)
      expect(store.shownRequest('agent-A')?.requestId).toBe('Q1')

      store.removeRequest('agent-A', 'Q1', 'tok-Q1')
      expect(store.questionsOpen('agent-A')).toBe(false)

      store.addRequest('agent-A', { ...makeRequest('Q2', 'agent-A'), nonBlocking: true })
      expect(store.questionsOpen('agent-A')).toBe(false)
      expect(store.shownRequest('agent-A')).toBeNull()
      dispose()
    })
  })

  // A click on the banner can race the removal of the last question. A flag
  // that stayed set would let the next question take over the composer.
  it('ignores a call to open the questions when no question waits', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      store.setQuestionsOpen('agent-1', true)
      expect(store.questionsOpen('agent-1')).toBe(false)

      store.addRequest('agent-1', makeRequest('B1', 'agent-1'))
      store.setQuestionsOpen('agent-1', true)
      expect(store.questionsOpen('agent-1')).toBe(false)

      store.addRequest('agent-1', { ...makeRequest('Q1', 'agent-1'), nonBlocking: true })
      expect(store.questionsOpen('agent-1')).toBe(false)
      expect(store.shownRequest('agent-1')?.requestId).toBe('B1')

      store.removeRequest('agent-1', 'B1', 'tok-B1')
      expect(store.shownRequest('agent-1')).toBeNull()
      dispose()
    })
  })

  it('opens and closes the questions while one waits', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      store.addRequest('agent-1', { ...makeRequest('Q1', 'agent-1'), nonBlocking: true })
      store.setQuestionsOpen('agent-1', true)
      expect(store.questionsOpen('agent-1')).toBe(true)
      expect(store.shownRequest('agent-1')?.requestId).toBe('Q1')

      store.setQuestionsOpen('agent-1', false)
      expect(store.questionsOpen('agent-1')).toBe(false)
      expect(store.shownRequest('agent-1')).toBeNull()
      dispose()
    })
  })

  it('closes the questions on clearAgent, so a later question does not open by itself', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      store.addRequest('agent-A', { ...makeRequest('Q1', 'agent-A'), nonBlocking: true })
      store.setQuestionsOpen('agent-A', true)
      expect(store.questionsOpen('agent-A')).toBe(true)

      store.clearAgent('agent-A')
      expect(store.questionsOpen('agent-A')).toBe(false)

      store.addRequest('agent-A', { ...makeRequest('Q2', 'agent-A'), nonBlocking: true })
      expect(store.shownRequest('agent-A')).toBeNull()
      dispose()
    })
  })

  // A provider stop keeps every question, so the reader's open questions stay
  // open and the next question takes the place of the dropped blocking head.
  it('keeps the questions open when the provider stops', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      store.addRequest('agent-A', makeRequest('B1', 'agent-A'))
      store.addRequest('agent-A', { ...makeRequest('Q1', 'agent-A'), nonBlocking: true })
      store.setQuestionsOpen('agent-A', true)
      expect(store.shownRequest('agent-A')?.requestId).toBe('B1')

      store.clearProviderRequests('agent-A')
      expect(store.questionsOpen('agent-A')).toBe(true)
      expect(store.shownRequest('agent-A')?.requestId).toBe('Q1')
      dispose()
    })
  })

  it('resets the questions-open map on clearAll', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      store.addRequest('agent-A', { ...makeRequest('Q1', 'agent-A'), nonBlocking: true })
      store.setQuestionsOpen('agent-A', true)
      store.clearAll()
      expect(store.questionsOpen('agent-A')).toBe(false)

      store.addRequest('agent-A', { ...makeRequest('Q2', 'agent-A'), nonBlocking: true })
      expect(store.shownRequest('agent-A')).toBeNull()
      dispose()
    })
  })
})

describe('shownControlRequest', () => {
  it('returns null for an empty list', () => {
    expect(shownControlRequest([], false)).toBeNull()
    expect(shownControlRequest([], true)).toBeNull()
  })

  it('returns the blocking head whatever the questions state is', () => {
    const blocking = makeRequest('B1', 'agent-1')
    const question = { ...makeRequest('Q1', 'agent-1'), nonBlocking: true }
    expect(shownControlRequest([blocking], false)?.requestId).toBe('B1')
    expect(shownControlRequest([blocking, question], true)?.requestId).toBe('B1')
  })

  it('hides a non-blocking head until the reader opens the questions', () => {
    const question = { ...makeRequest('Q1', 'agent-1'), nonBlocking: true }
    expect(shownControlRequest([question], false)).toBeNull()
    expect(shownControlRequest([question], true)?.requestId).toBe('Q1')
  })
})

describe('pendingQuestionCount', () => {
  it('returns zero for an empty list', () => {
    expect(pendingQuestionCount([])).toBe(0)
  })

  it('returns zero when every request blocks', () => {
    expect(pendingQuestionCount([makeRequest('B1', 'agent-1'), makeRequest('B2', 'agent-1')])).toBe(0)
  })

  it('counts only the non-blocking requests of a mixed list', () => {
    const requests = [
      makeRequest('B1', 'agent-1'),
      { ...makeRequest('B2', 'agent-1'), nonBlocking: false },
      { ...makeRequest('Q1', 'agent-1'), nonBlocking: true },
      { ...makeRequest('Q2', 'agent-1'), nonBlocking: true },
    ]
    expect(pendingQuestionCount(requests)).toBe(2)
  })
})

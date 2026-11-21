import type { TunnelKeyApproval } from '~/api/workerRpc'
import type { WorkerTrustScope } from '~/lib/workerTrustScope'
import { createSignal } from 'solid-js'
import { storageAccountFor, TEST_HUB } from '~/lib/browserStorage'

/** Fresh reactive Worker-trust state for one component test suite. */
export function createTestWorkerTrustState(initialUserId = 'user-1') {
  let controller = new AbortController()
  const [scope, setScope] = createSignal<WorkerTrustScope>({
    userId: initialUserId,
    storageAccount: storageAccountFor(TEST_HUB, initialUserId),
    generation: 1,
    signal: controller.signal,
  })
  return {
    scope,
    setUserId: (userId: string | undefined) => {
      controller.abort()
      controller = new AbortController()
      setScope(previous => ({
        userId: userId ?? null,
        storageAccount: userId === undefined
          ? null
          : storageAccountFor(TEST_HUB, userId),
        generation: previous.generation + 1,
        signal: controller.signal,
      }))
    },
  }
}

/** Approval metadata shared by tunnel component tests. */
export function testTunnelKeyApproval(scope: WorkerTrustScope, workerId = 'w1', publicKeyHex = 'verified'): TunnelKeyApproval {
  return {
    workerId,
    expectedUserId: scope.userId!,
    identityGeneration: scope.generation,
    publicKeyHex,
    pinSnapshot: {
      pin: { publicKeyHex, firstSeen: 1 },
      removalRevision: 0,
    },
  }
}

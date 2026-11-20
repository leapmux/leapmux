import { create } from '@bufbuild/protobuf'
import { IDBFactory } from 'fake-indexeddb'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { UserCrdtStateSchema } from '~/generated/proto/leapmux/v1/user_crdt_pb'
import { OpBatchSchema } from '~/generated/proto/leapmux/v1/user_ops_pb'
import { flushStorageWrites, resetBrowserStorageForTests, setStorageAccountForTests } from '~/lib/browserStorage'
import { loadCrdtOutbox, outboxBatchIsStale, storeCrdtOutbox } from './outbox'

function hlc(physical: bigint) {
  return { physical, logical: 0n, clientId: 'client' }
}

function xBatch(physical = 10n) {
  return create(OpBatchSchema, {
    batchId: 'batch-1',
    epoch: 7n,
    ops: [
      {
        opId: 'op-1',
        originClientId: 'client',
        clientHlc: hlc(physical),
        body: {
          case: 'setFloatingWindowRegister',
          value: {
            windowId: 'window-1',
            field: { case: 'x', value: 42 },
          },
        },
      },
    ],
  })
}

describe('CRDT outbox', () => {
  beforeEach(() => {
    vi.stubGlobal('indexedDB', new IDBFactory())
    resetBrowserStorageForTests()
    setStorageAccountForTests('outbox-user')
  })

  it('round-trips the original batch id, epoch, HLC and operations', async () => {
    const batch = xBatch()
    expect(await storeCrdtOutbox([batch]).durable).toBe(true)
    await flushStorageWrites()

    const restored = await loadCrdtOutbox()

    expect(restored).toEqual([batch])
  })

  it('drops a batch when a matching canonical register is newer', () => {
    const state = create(UserCrdtStateSchema, {
      currentEpoch: 7n,
      floatingWindows: {
        'window-1': {
          windowId: 'window-1',
          x: {
            value: 100,
            hlc: hlc(11n),
          },
        },
      },
    })

    expect(outboxBatchIsStale(xBatch(10n), state, 7n)).toBe(true)
    expect(outboxBatchIsStale(xBatch(12n), state, 7n)).toBe(false)
  })

  it('accepts the previous epoch and drops an older server epoch', () => {
    const state = create(UserCrdtStateSchema)

    expect(outboxBatchIsStale(xBatch(), state, 8n)).toBe(false)
    const older = xBatch()
    older.epoch = 6n
    expect(outboxBatchIsStale(older, state, 8n)).toBe(true)
  })
})

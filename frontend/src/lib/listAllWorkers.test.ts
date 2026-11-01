import { create } from '@bufbuild/protobuf'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { PageResponseSchema } from '~/generated/proto/leapmux/v1/common_pb'
import { WorkerSchema } from '~/generated/proto/leapmux/v1/worker_pb'
import { PAGE_SIZE } from './fetchAllPages'
import { listAllWorkers } from './listAllWorkers'

const listWorkers = vi.hoisted(() => vi.fn())

vi.mock('~/api/clients', () => ({
  workerClient: { listWorkers },
}))

function worker(id: string, registeredBy = '') {
  return create(WorkerSchema, { id, registeredBy })
}

/** One ListWorkers reply; an empty cursor ends the listing. */
function reply(workers: ReturnType<typeof worker>[], nextCursor: string) {
  return {
    workers,
    page: create(PageResponseSchema, { nextCursor }),
  }
}

beforeEach(() => {
  listWorkers.mockReset()
})

describe('listAllWorkers', () => {
  // A single call hides each worker past the first page, and a worker the
  // sidebar never draws is one the user cannot open a tab on.
  it('reads every page the hub reports', async () => {
    listWorkers
      .mockResolvedValueOnce(reply([
        worker('w1'),
        worker('w2'),
      ], 'c1'))
      .mockResolvedValueOnce(reply([
        worker('w3'),
      ], ''))

    const workers = await listAllWorkers()

    expect(workers.map(w => w.id)).toEqual(['w1', 'w2', 'w3'])
    expect(listWorkers.mock.calls).toEqual([
      [
        {
          page: { limit: PAGE_SIZE, cursor: '' },
        },
      ],
      [
        {
          page: { limit: PAGE_SIZE, cursor: 'c1' },
        },
      ],
    ])
  })

  it('keeps one row per worker id when a page repeats a worker', async () => {
    listWorkers
      .mockResolvedValueOnce(reply([
        worker('w1', 'first read'),
      ], 'c1'))
      .mockResolvedValueOnce(reply([
        worker('w1', 'second read'),
        worker('w2'),
      ], ''))

    const workers = await listAllWorkers()

    // The last copy wins, as from a hub that re-read the row.
    expect(workers.map(w => [w.id, w.registeredBy])).toEqual([
      ['w1', 'second read'],
      ['w2', ''],
    ])
  })

  it('returns no workers for an empty listing', async () => {
    listWorkers.mockResolvedValueOnce(reply([], ''))

    await expect(listAllWorkers()).resolves.toEqual([])
    expect(listWorkers).toHaveBeenCalledOnce()
  })

  it('ends the listing when a reply carries no page', async () => {
    listWorkers.mockResolvedValueOnce({
      workers: [
        worker('w1'),
      ],
    })

    const workers = await listAllWorkers()

    expect(workers.map(w => w.id)).toEqual(['w1'])
    expect(listWorkers).toHaveBeenCalledOnce()
  })

  it('rejects when a later page fails', async () => {
    listWorkers
      .mockResolvedValueOnce(reply([
        worker('w1'),
      ], 'c1'))
      .mockRejectedValueOnce(new Error('hub unreachable'))

    await expect(listAllWorkers()).rejects.toThrow('hub unreachable')
  })
})

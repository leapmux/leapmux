import type { Worker } from '~/generated/proto/leapmux/v1/worker_pb'
import { workerClient } from '~/api/clients'
import { fetchAllPages } from '~/lib/fetchAllPages'

/**
 * Read every worker the user owns. The hub answers one page per call, so a
 * single `listWorkers` call hides each worker past the first page.
 */
export async function listAllWorkers(): Promise<Worker[]> {
  return fetchAllPages(async (page) => {
    const resp = await workerClient.listWorkers({ page })
    return { items: resp.workers, page: resp.page }
  }, {
    keyOf: worker => worker.id,
  })
}

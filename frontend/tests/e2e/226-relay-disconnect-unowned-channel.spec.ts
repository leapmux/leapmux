import type { BrowserContext, Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import { withCleanup } from './helpers/cleanup'
import { markSuiteServerLog, readSuiteServerLog } from './helpers/suiteServerLog'
import { gotoWorkspace } from './helpers/ui'
import { waitForAgentStartupViaAPI } from './helpers/workerTabs'

/**
 * A page that closes must leave no frame for a channel that the hub closed.
 *
 * When a relay connection ends, the hub forgets each channel of that connection
 * first and tells the worker to close the channel after that. A frame that the
 * worker sends while it closes the channel therefore reaches a hub that cannot
 * route the frame, and the hub logs "worker sent message for an unowned
 * channel". The worker sent such a frame on every close: the End frame of the
 * WatchEvents stream, which the stream sends when the close cancels it.
 *
 * The spec reads the log of the suite server, which records the hub and the
 * worker of one process as JSON lines.
 */

const RELAY_CLOSED_CHANNEL = 'channel closed (relay disconnected)'
const WORKER_CLOSED_CHANNEL = 'channel closed'
const UNOWNED_CHANNEL_WARNING = 'channel relay: worker sent message for an unowned channel'

interface ServerLogRecord {
  msg?: unknown
  channel_id?: unknown
}

/** Parse every JSON line of a log segment. A line that is not JSON is skipped. */
function logRecords(segment: string): ServerLogRecord[] {
  return segment.split('\n').flatMap((line) => {
    if (!line.startsWith('{'))
      return []
    try {
      const record = JSON.parse(line) as ServerLogRecord
      return [record]
    }
    catch {
      return []
    }
  })
}

/** List the IDs of the channels that the records of `msg` give. */
function channelIdsLogged(records: ServerLogRecord[], msg: string): string[] {
  return records.flatMap(record => record.msg === msg && typeof record.channel_id === 'string'
    ? [record.channel_id]
    : [])
}

interface RelayPageState {
  caughtUp: boolean
  pendingRPCs: number
}

/** Open a page and wait for its agent replay and outstanding unary requests. */
async function openWatchingPage(context: BrowserContext, adminToken: string, workspaceId: string, agentId: string): Promise<Page> {
  const page = await context.newPage()
  await page.addInitScript((targetAgentId) => {
    const state: RelayPageState = { caughtUp: false, pendingRPCs: 0 }
    Object.assign(window, { __relayPageState: state })
    window.addEventListener('leapmux:watch-events-catch-up-complete', (event) => {
      if ((event as CustomEvent<{ agentId: string }>).detail.agentId === targetAgentId)
        state.caughtUp = true
    })
    window.addEventListener('leapmux:rpc-send', () => {
      state.pendingRPCs++
    })
    window.addEventListener('leapmux:rpc-recv', () => {
      state.pendingRPCs--
    })
  }, agentId)
  await gotoWorkspace(page, adminToken, workspaceId)
  await expect.poll(
    () => page.evaluate(() => (window as Window & { __relayPageState?: RelayPageState }).__relayPageState),
    'the page has completed its agent replay and outstanding RPCs',
  ).toEqual({ caughtUp: true, pendingRPCs: 0 })
  return page
}

test.describe('relay disconnect', () => {
  test('a closed page leaves no frame for a channel that the hub closed', async ({ browser, leapmuxServer, workspace }) => {
    const { hubUrl, adminToken, workerId, serverLogPath } = leapmuxServer
    const { workspaceId, agentId } = workspace
    await waitForAgentStartupViaAPI(hubUrl, adminToken, workerId, workspaceId)
    const mark = markSuiteServerLog(serverLogPath)
    const context = await browser.newContext({ baseURL: hubUrl })
    await withCleanup(async () => {
      // Requests already in flight can race relay teardown. Wait for the
      // initial requests before closing the page to exercise stream teardown.
      const page = await openWatchingPage(context, adminToken, workspaceId, agentId)
      await page.close()

      // The hub logs each channel that it closes for the relay. The worker then
      // logs its own close of each one, and a late frame would follow that close.
      let closedIds: string[] = []
      await expect.poll(() => {
        closedIds = channelIdsLogged(logRecords(readSuiteServerLog(serverLogPath, mark)), RELAY_CLOSED_CHANNEL)
        return closedIds.length
      }, 'the hub closes the channels of the relay').toBeGreaterThan(0)
      await expect.poll(() => {
        const workerClosed = new Set(channelIdsLogged(logRecords(readSuiteServerLog(serverLogPath, mark)), WORKER_CLOSED_CHANNEL))
        return closedIds.filter(id => !workerClosed.has(id))
      }, 'the worker closes every channel that the hub closed').toEqual([])

      // A new page opens its WatchEvents stream only after the worker's answer
      // to its channel open reached it. The worker queues that answer behind
      // every frame that it sent before, on one ordered connection, and the hub
      // reads the connection in order. So by then the hub has read every frame
      // that the worker sent while it closed the old channels.
      await openWatchingPage(context, adminToken, workspaceId, agentId)

      const warnings = channelIdsLogged(logRecords(readSuiteServerLog(serverLogPath, mark)), UNOWNED_CHANNEL_WARNING)
      expect(warnings.filter(id => closedIds.includes(id)), 'no frame reaches the hub after it closed the channel').toEqual([])
    }, () => context.close())
  })
})

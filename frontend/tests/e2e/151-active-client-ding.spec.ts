import type { Page } from '@playwright/test'
import type { ModelScript } from './helpers/modelScriptFixture'
import type { UserEventsWatch } from './helpers/userEventsWatch'
import { expect, test } from './fixtures'
import { freshAdminSessionViaAPI, logoutViaAPI, openPinnedModeAgentViaAPI } from './helpers/api'
import { finishCleanup, withCleanup } from './helpers/cleanup'
import { withExtraClients } from './helpers/multiClient'
import { armTurnEndSound, sendToolUsingTurn, soundReceiptCursor, waitForIdleSoundReceipt } from './helpers/turnEndSound'
import { gotoWorkspace, waitForWorkspaceReady } from './helpers/ui'
import { waitForActiveClient, waitForSubscriberClientId, watchUserEvents } from './helpers/userEventsWatch'

/**
 * Active-client ding gate.
 *
 * Two browser contexts authenticate as the same admin user, each with a
 * session of its own: the hub names a client by its session, so two contexts
 * of one session would be one client. Only the focused (most-recently-active)
 * client should play the turn-end ding when an agent finishes a turn. The
 * hub's per-workspace presence tracker computes the active client from input
 * heartbeats; the frontend gates `playDingDong` on `activeClient.activeFor(wsId)
 * === ownClientId`.
 *
 * The audio element is unobservable from Playwright (autoplay is
 * blocked in some contexts), so the test listens for the
 * `leapmux:turn-end-played` custom event the gate dispatches when —
 * and only when — the local client plays the ding. The other context
 * must not fire the event.
 *
 * The app shows presence nowhere, so the test reads it from each page's
 * `/ws/userevents` stream: the identity the hub gave the page, and the
 * presence updates. A client claims presence with a heartbeat when its stream
 * starts, and its input heartbeats are throttled for five seconds after that.
 * So the test makes a client active by starting its stream last, not by
 * typing into it.
 *
 * The hub sends a presence update only when the active client changes. So a
 * reload of the client that already leads tells no page anything, and the
 * new document of that client knows no active client. The test therefore
 * makes a client active only while another client leads.
 */

async function recordDing(page: Page) {
  await page.evaluate(() => {
    ;(window as unknown as { __leapmuxDings?: number }).__leapmuxDings = 0
    window.addEventListener('leapmux:turn-end-played', () => {
      const w = window as unknown as { __leapmuxDings?: number }
      w.__leapmuxDings = (w.__leapmuxDings ?? 0) + 1
    })
  })
}

async function readDings(page: Page): Promise<number> {
  return await page.evaluate(() => (window as unknown as { __leapmuxDings?: number }).__leapmuxDings ?? 0)
}

/** One client of the test: its page, the watch of its stream, and the identity that the hub gave it. */
interface Client {
  page: Page
  watch: UserEventsWatch
}

/**
 * Open the workspace in `client` with the turn-end sound armed, and return the identity that the hub gave the page.
 * The arm reloads the page, so the stream starts again after it, and the ding record starts after it too.
 */
async function openArmed(client: Client, session: string, workspaceId: string, userId: string): Promise<string> {
  await gotoWorkspace(client.page, session, workspaceId)
  await armTurnEndSound(client.page, userId, 'ding-dong')
  await waitForWorkspaceReady(client.page)
  await recordDing(client.page)
  return waitForSubscriberClientId(client.watch)
}

/**
 * Reload `client`, so that its stream starts last and claims presence, and require that every watch of `watches` then
 * names `clientId` the active client of the workspace. Another client must lead before the reload: the hub sends no
 * update when the leader does not change. The ding record starts again after the reload.
 */
async function becomeActive(client: Client, clientId: string, workspaceId: string, watches: readonly UserEventsWatch[]): Promise<void> {
  await client.page.reload()
  await waitForWorkspaceReady(client.page)
  await recordDing(client.page)
  await waitForActiveClient(watches, workspaceId, clientId)
}

/**
 * Run one tool-using turn from `active` and wait until every client applied its idle edge.
 * The answer waits behind a gate until every client received the presence update that names `activeClientId`, so
 * the turn cannot end while any client still holds an older presence.
 */
async function settledTurn(options: {
  active: Client
  clients: readonly Client[]
  activeClientId: string
  workspaceId: string
  modelScript: ModelScript
  gate: string
}): Promise<void> {
  const { active, clients, activeClientId, workspaceId, modelScript, gate } = options
  const cursors = await Promise.all(clients.map(client => soundReceiptCursor(client.page)))
  const boundary = await sendToolUsingTurn(active.page, modelScript, { answerGate: gate })
  await waitForActiveClient(clients.map(client => client.watch), workspaceId, activeClientId)
  await modelScript.releaseGate(gate)
  await Promise.all(clients.map((client, index) => waitForIdleSoundReceipt(client.page, { agentId: boundary.agentId, after: cursors[index]! })))
}

test.describe('Active-client ding gate', () => {
  test('dispatches `leapmux:turn-end-played` only when this client is the active client', async ({ browser, emptyWorkspace, leapmuxServer, modelScript }) => {
    const { hubUrl, adminToken, workerId, adminUserId } = leapmuxServer
    const wsId = emptyWorkspace.workspaceId
    await openPinnedModeAgentViaAPI(hubUrl, adminToken, workerId, wsId)
    const sessions = [await freshAdminSessionViaAPI(hubUrl), await freshAdminSessionViaAPI(hubUrl)] as const

    await withCleanup(() => withExtraClients(browser, leapmuxServer, 2, async ([pageA, pageB]) => {
      const a: Client = { page: pageA, watch: watchUserEvents(pageA) }
      const b: Client = { page: pageB, watch: watchUserEvents(pageB) }
      const watches = [a.watch, b.watch]

      // A opens first and B last, so B leads. Then A starts its stream last
      // and takes the lead from B, and the hub tells both pages.
      const clientA = await openArmed(a, sessions[0], wsId, adminUserId)
      const clientB = await openArmed(b, sessions[1], wsId, adminUserId)
      expect(clientA, 'the hub names the two sessions apart').not.toBe(clientB)
      await becomeActive(a, clientA, wsId, watches)

      // A is active: only A plays the ding. B has never played, so its
      // cooldown cannot be what silences it.
      await settledTurn({ active: a, clients: [a, b], activeClientId: clientA, workspaceId: wsId, modelScript, gate: 'turn-of-a' })
      expect(await readDings(pageA), 'the active client plays the ding').toBe(1)
      expect(await readDings(pageB), 'the other client plays no ding').toBe(0)

      // B opens the app again, which makes B the active client: B plays the
      // ding of the next turn. A's silence here is no proof of the gate,
      // because A's own sixty-second cooldown also holds it. B's silence
      // above is the proof, and B's ding here shows that B could play.
      await becomeActive(b, clientB, wsId, watches)
      await settledTurn({ active: b, clients: [a, b], activeClientId: clientB, workspaceId: wsId, modelScript, gate: 'turn-of-b' })
      expect(await readDings(pageB), 'the client that became active plays the ding').toBe(1)
      expect(await readDings(pageA), 'the client that is no longer active plays no further ding').toBe(1)
    }), () => finishCleanup(sessions.map(session => logoutViaAPI(hubUrl, session))))
  })
})

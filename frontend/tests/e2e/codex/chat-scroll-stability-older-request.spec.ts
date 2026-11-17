import type { Page } from '@playwright/test'
import type { RowBox } from '../helpers/chatScrollStability'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { MESSAGE_PAGE_LIMIT } from '../../../src/generated/contracts/chat-history'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import {
  clearStoredRenders,
  clearStoredRowHeights,
  dragChatUp,
  expectAtBottom,
  expectNoVisibleHeightChange,
  readRowBox,
  readViewportHeight,
  recordRowHeightChangesFromNextLoad,
  waitForScrollRest,
  watchFrames,
} from '../helpers/chatScrollStability'
import { fillerTurn, fillRows } from '../helpers/historyFill'
import { readAllAgentMessages } from '../helpers/nativeMessages'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { PHONE_METRICS, touchFling } from '../helpers/touch'
import { assistantBubbles, chatScrollContainer, chatScrollTop, waitForAgentStarted } from '../helpers/ui'
import { listAgentsViaAPI } from '../helpers/workerTabs'

/**
 * The chat view keeps a tool result row at its height when the older page that holds its request row arrives, on a
 * phone.
 *
 * A result row beside its request row draws no header and no request side: the request row draws them. When the
 * request row lay in an older page than its result row, the result row drew both until that page loaded. The reader
 * scrolled up to the result row, the older page arrived, and the row lost its header and its command summary in view.
 * On a phone the summary wraps to several lines, so the rows below the result row moved up by 170px, as a reader of a
 * long Codex session saw.
 *
 * The result row now draws what the transcript holds, not what the loaded window holds: its hidden copy fetches the
 * span of its call before it measures, and the row draws no header from the start.
 *
 * The test places the result row first in the newest page of the history, and its request row last in the page before
 * it. It opens the chat again as a browser does that never showed it, scrolls up through both rows slowly and with
 * flings, and checks every frame of the scroll and every height that the app commits.
 */

codexTest.use(PHONE_METRICS)

/** A word in the command, and only in the command: its output and every other row hold none of it. */
const COMMAND_MARKER = 'older-request-boundary'

/**
 * The command of the call. Its comment makes the command summary wrap to several lines on a phone, so a result row that
 * drew the summary is far taller than one that does not. The shell prints only the word that `printf` states.
 */
const COMMAND = `printf 'boundary-output\\n' # ${COMMAND_MARKER}: ${'every word of this comment makes the summary of the command wrap on a narrow screen '.repeat(3)}`

/**
 * The rows of the history after the result row: with the result row they fill the newest page, which the chat loads
 * alone when it opens. The request row is then the last row of the page before it.
 */
const ROWS_AFTER_RESULT = MESSAGE_PAGE_LIMIT - 1

/** The distance of one slow scroll step, and the distance and the speed of one fling. */
const SLOW_STEP_PX = 180
const FLING_DISTANCE_PX = 500
const FLING_SPEED_PX_PER_S = 2500

/** The most scroll gestures that the reader needs to reach the request row. */
const MAX_GESTURES = 60

/** The sequence numbers of the request row and the result row of the call. */
interface CallRows {
  request: bigint
  result: bigint
}

/**
 * Run the call, and find its two rows through the Worker: the one command span of the turn, with a request row and a
 * result row.
 */
async function runCall(native: ManagedNativeScenarioContext, agentId: string): Promise<CallRows> {
  await runNativeToolTurn(native, {
    toolCalls: [
      bashToolCall(AgentProvider.CODEX, 'boundary-command', COMMAND),
    ],
    prompt: 'Run the boundary command.',
    answer: 'The boundary command ran.',
  })
  const spans = new Map<string, bigint[]>()
  for (const message of await readAllAgentMessages(native, agentId)) {
    if (message.spanType === 'commandExecution') {
      spans.set(message.spanId, [
        ...(spans.get(message.spanId) ?? []),
        message.seq,
      ])
    }
  }
  const seqs = [
    ...spans.values(),
  ]
  expect(seqs.map(span => span.length), 'the turn holds one command span of two rows').toEqual([2])
  const [request, result] = seqs[0]!
  return { request: request!, result: result! }
}

/** Send filler turns until exactly ROWS_AFTER_RESULT rows follow the result row. Returns the answer of the last turn. */
async function fillAfterResult(native: ManagedNativeScenarioContext, agentId: string, result: bigint): Promise<string> {
  const { page, modelScript } = native
  const rowsAfter = async (): Promise<number> => (await readAllAgentMessages(native, agentId)).filter(message => message.seq > result).length
  const last = await fillRows({
    page,
    modelScript,
    count: rowsAfter,
    target: ROWS_AFTER_RESULT,
    firstTurn: 1,
  })
  await expectAtBottom(page, 'after the seed')
  return fillerTurn(last, false).answer
}

/** Whether the row box reaches into a view of `viewHeight`. */
function inView(box: RowBox | null, viewHeight: number): boolean {
  return box !== null && box.distance + box.height > 0 && box.distance < viewHeight
}

/**
 * Scroll up, slowly and with flings in turn, until the request row and the result row are both in the view. A gesture
 * that reaches the end of the measured rows moves nothing, and the reader waits there until more rows join above.
 */
async function scrollUpToCall(page: Page, rows: CallRows): Promise<void> {
  const viewHeight = await readViewportHeight(page)
  const box = await chatScrollContainer(page).boundingBox()
  expect(box, 'the chat has a box on screen').not.toBeNull()
  const from = { x: box!.x + box!.width / 2, y: box!.y + box!.height * 0.3 }
  for (let gesture = 0; ; gesture++) {
    if (inView(await readRowBox(page, rows.request.toString()), viewHeight) && inView(await readRowBox(page, rows.result.toString()), viewHeight))
      return
    expect(gesture, 'the reader reaches the request row within the expected number of gestures').toBeLessThan(MAX_GESTURES)
    const before = await chatScrollTop(page)
    if (gesture % 2 === 0)
      await dragChatUp(page, SLOW_STEP_PX)
    else
      await touchFling(page, { from, distanceY: FLING_DISTANCE_PX, speed: FLING_SPEED_PX_PER_S })
    await waitForScrollRest(page)
    if (await chatScrollTop(page) < before || before > 0)
      continue
    await expect.poll(() => chatScrollTop(page), { message: 'the rows above join the list, so the view can move up again' }).toBeGreaterThan(0)
  }
}

codexTest.describe('chat scroll stability while the page of a request row arrives', () => {
  codexTest('keeps a result row at its height when the older page with its request row arrives, on a phone', async ({ native }) => {
    const { page } = native
    await waitForAgentStarted(page)
    // A phone shows no tab bar, so the one agent of the workspace comes from the API.
    const { hubUrl, adminToken, workerId } = native.leapmuxServer
    const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, native.workspaceId)
    expect(agents, 'the workspace holds one agent').toHaveLength(1)
    const agentId = agents[0]!.id
    const call = await runCall(native, agentId)
    const lastAnswer = await fillAfterResult(native, agentId, call.result)

    // Open the chat again as a browser does that never showed it, with no stored row height and no stored render.
    const chatUrl = page.url()
    const heights = await recordRowHeightChangesFromNextLoad(page)
    await clearStoredRowHeights(page)
    // The helper above left the browser on an empty page of the app's origin.
    await clearStoredRenders(page)
    await page.goto(chatUrl)
    await expect(assistantBubbles(page).filter({ hasText: lastAnswer }), 'the last answer shows after the chat opened again').toHaveCount(1)
    await expectAtBottom(page, 'after the chat opened again')
    // The result row joins the list only after a hidden copy measured it, and no older page loads before it joins.
    await expect.poll(async () => (await heights.all()).some(change => change.seq === call.result.toString()), {
      message: 'a hidden copy measures the result row',
    }).toBe(true)

    const watch = await watchFrames(page)
    await scrollUpToCall(page, call)
    await waitForScrollRest(page)
    await watch.expectNoJump('while the reader scrolls up past the result row and its request row')

    await expectNoVisibleHeightChange(heights, 'after the reader scrolled past the call')
    const resultChanges = (await heights.all()).filter(change => change.seq === call.result.toString())
    expect(resultChanges.map(change => change.source), 'the hidden copy of the result row measures its height once, before the older page arrives').toEqual(['premeasure'])
    const resultBox = await readRowBox(page, call.result.toString())
    expect(Math.abs(resultBox!.height - resultChanges[0]!.height), `the result row has the height that its hidden copy measured, within 1px (${resultChanges[0]!.height}px for ${resultBox!.height}px)`).toBeLessThanOrEqual(1)
    // The request row draws the command, and the result row beside it draws neither the header nor the command.
    const row = (seq: bigint) => chatScrollContainer(page).locator(`[data-seq="${seq}"]`)
    await expect(row(call.request), 'the request row draws the command').toContainText(COMMAND_MARKER)
    await expect(row(call.result), 'the result row draws its output').toContainText('boundary-output')
    await expect(row(call.result), 'the result row leaves the command to the request row').not.toContainText(COMMAND_MARKER)
  })
})

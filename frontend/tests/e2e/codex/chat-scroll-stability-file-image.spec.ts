import type { Page } from '@playwright/test'
import type { PremeasureCommits, RowBox } from '../helpers/chatScrollStability'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import {
  clearStoredRenders,
  clearStoredRowHeights,
  dragChatUp,
  expectAtBottom,
  readRowBox,
  readRowStates,
  readViewportHeight,
  recordPremeasureCommitsFromNextLoad,
  waitForScrollRest,
  watchRow,
} from '../helpers/chatScrollStability'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { codexViewImageToolCall } from '../helpers/providerToolCalls'
import { sendScriptedTurn } from '../helpers/scriptedTurn'
import { writeSolidPng } from '../helpers/toolImages'
import { assistantBubbles, chatDistanceFromBottom, chatScrollContainer, waitForAgentStarted } from '../helpers/ui'

/**
 * The chat view keeps the rows that the reader looks at in place when a tool row draws an image from a file.
 *
 * Such a row holds only the path of the file. The app reads the image through the Worker when the row comes near the
 * view, so the row showed one line of text until then, and grew by the height of the image when the image arrived.
 * The app measures a row that is not on screen in a hidden copy, and that copy measured the one line.
 *
 * The app now reads the size of the image from the first bytes of the file, also in the hidden copy, and the row
 * holds the box of the image from then on. The image replaces the box, and the row keeps its height.
 *
 * The row comes from Codex: its `view_image` tool gives a row that holds a path and no image data. Each test seeds
 * the chat, then opens it again as a browser does that never showed it, with no stored row height and no stored
 * render. So only a hidden copy knows the image row before the reader scrolls up to it.
 */

const VIEW_WIDTH = 1024
const VIEW_HEIGHT = 600

/** The image: wider and taller than the box that a tool row gives an image, so the row holds a box of the full height. */
const IMAGE_FILE = 'chat-scroll-file-image.png'
const IMAGE_SIZE = { width: 800, height: 600 }

/**
 * The short turns below the image row: more rows than the app keeps mounted above the view at the bottom of the
 * chat, so the image row is not mounted there.
 */
const TURNS_BELOW = 8

/** The scroll step toward the image row, and the most steps that the reader needs. */
const STEP_PX = 150
const STEPS_MAX = 80

/** How far below the top of the view the image row starts once the reader reached it. */
const IMAGE_TOP_IN_VIEW_PX = 100

/** A short scripted turn. */
function shortTurn(turn: number) {
  return { prompt: `Turn ${turn}.`, answer: `Answer ${turn}.`, entry: 'insert' as const }
}

/** A seeded chat that was opened again, with the view at its bottom. */
interface ImageChat {
  /** The sequence number of the row that draws the image. */
  imageSeq: string
  /** The heights that hidden copies committed since the chat opened again. */
  premeasured: PremeasureCommits
}

/** What the row of `seq` shows for its image: the box that holds its place, and the decoded image. */
interface ImageRowState {
  /** The row holds the box of an image that does not show yet. */
  box: boolean
  /** The row shows the image, and the browser decoded it. */
  image: boolean
}

/** Read what the row of `seq` shows for its image, or null while the row is not mounted. */
function readImageRowState(page: Page, seq: string): Promise<ImageRowState | null> {
  return chatScrollContainer(page).evaluate((container, rowSeq) => {
    const row = container.querySelector(`[data-seq="${rowSeq}"]`)
    if (row === null)
      return null
    const image = row.querySelector('img')
    return {
      box: row.querySelector('div[data-size-reserved]') !== null,
      image: image !== null && image.complete && image.naturalWidth > 0,
    }
  }, seq)
}

/** The sequence number of the mounted row that shows a decoded image, or null. */
function readDecodedImageRowSeq(page: Page): Promise<string | null> {
  return chatScrollContainer(page).evaluate((container) => {
    for (const image of container.querySelectorAll<HTMLImageElement>('[data-seq] img')) {
      if (image.complete && image.naturalWidth > 0)
        return image.closest<HTMLElement>('[data-seq]')?.dataset.seq ?? null
    }
    return null
  })
}

/**
 * Seed a turn whose tool views an image file, and short turns below it. Then delete each stored row height and each
 * stored render, and open the chat again. The view is at the bottom, and the image row is not mounted.
 */
async function seedChatWithFileImage(native: ManagedNativeScenarioContext, workingDir: string): Promise<ImageChat> {
  const { page, modelScript } = native
  await page.setViewportSize({ width: VIEW_WIDTH, height: VIEW_HEIGHT })
  await waitForAgentStarted(page)
  writeSolidPng(workingDir, IMAGE_FILE, IMAGE_SIZE)
  await runNativeToolTurn(native, {
    toolCalls: [
      codexViewImageToolCall('view-image', IMAGE_FILE),
    ],
    prompt: `Look at ${IMAGE_FILE}.`,
    answer: 'I looked at the image.',
  })
  await expect.poll(() => readDecodedImageRowSeq(page), { message: 'the tool row draws the image' }).not.toBeNull()
  const imageSeq = (await readDecodedImageRowSeq(page))!
  for (let turn = 1; turn <= TURNS_BELOW; turn++)
    await sendScriptedTurn(page, modelScript, shortTurn(turn))
  await expectAtBottom(page, 'after the seed')

  const chatUrl = page.url()
  const premeasured = await recordPremeasureCommitsFromNextLoad(page)
  await clearStoredRowHeights(page)
  // The helper above left the browser on an empty page of the app's origin.
  await clearStoredRenders(page)
  await page.goto(chatUrl)
  await expect(assistantBubbles(page).filter({ hasText: `Answer ${TURNS_BELOW}.` }), 'the last answer shows after the chat opened again').toHaveCount(1)
  await expectAtBottom(page, 'after the chat opened again')
  expect(await readRowBox(page, imageSeq), 'the image row is far above the view, so it is not mounted').toBeNull()
  return { imageSeq, premeasured }
}

/**
 * Scroll up in steps until the image row is mounted and ends less than one view height above the view, and return
 * its box. A step that reaches the end of the measured rows moves nothing, and the next step moves again once more
 * rows are measured.
 *
 * A mounted row alone is not enough. A fast scroll also mounts the rows that it is about to reach, farther above the
 * view than the app keeps rows mounted at rest, and the app unmounts such a row at its next update of the mounted
 * rows. A row that ends less than one view height above the view is inside the band that the app keeps mounted.
 */
async function scrollUpUntilImageRowMounts(page: Page, imageSeq: string): Promise<RowBox> {
  const viewHeight = await readViewportHeight(page)
  for (let step = 0; ; step++) {
    const box = await readRowBox(page, imageSeq)
    if (box !== null && box.distance + box.height > -viewHeight)
      return box
    expect(step, 'the image row mounts near the view within the expected number of steps').toBeLessThan(STEPS_MAX)
    await dragChatUp(page, STEP_PX)
  }
}

codexTest.describe('chat scroll stability while a tool row loads an image from a file', () => {
  codexTest('keeps the rows below a file image in place when the image loads', async ({ native, authenticatedCodexWorkspace }) => {
    const { page } = native
    const chat = await seedChatWithFileImage(native, authenticatedCodexWorkspace.workingDir)
    // The row joins the list when a hidden copy measured it. Without this wait, the reader can reach the end of the
    // measured rows first, and the row then joins right above the view, where the app reads its image at once.
    await expect.poll(() => chat.premeasured.commitOf(chat.imageSeq), { message: 'a hidden copy measures the image row' }).not.toBeNull()

    await scrollUpUntilImageRowMounts(page, chat.imageSeq)
    // The row mounts more than half a view above the view. The app reads no image for such a row, and the row holds
    // the box. The poll also waits for the placeholder of a row that mounted during a fast scroll to give way to the
    // row.
    await expect.poll(() => readImageRowState(page, chat.imageSeq), { message: 'the image row holds the box of its image, and no image yet' }).toEqual({ box: true, image: false })
    await waitForScrollRest(page)
    const mounted = (await readRowBox(page, chat.imageSeq))!
    expect(mounted.distance + mounted.height, 'the image row is above the view').toBeLessThan(0)
    const below = (await readRowStates(page)).find(row => Number(row.seq) > Number(chat.imageSeq))
    expect(below, 'a row below the image row is mounted').toBeDefined()
    const tailBefore = await chatDistanceFromBottom(page)

    // One step brings the image row into the view, where the app reads the image.
    const distance = IMAGE_TOP_IN_VIEW_PX - mounted.distance
    await chatScrollContainer(page).evaluate((container, pixels) => {
      container.scrollTop -= pixels
    }, distance)
    const watch = await watchRow(page, below!.seq)
    await expect.poll(() => readImageRowState(page, chat.imageSeq), { message: 'the image replaces its box' }).toEqual({ box: false, image: true })
    await watch.expectStill('while the image loads')
    await waitForScrollRest(page)

    const loaded = await readRowBox(page, chat.imageSeq)
    expect(loaded, 'the image row is mounted').not.toBeNull()
    expect(Math.abs(loaded!.height - mounted.height), `the image row keeps its height when the image loads, within 1px (${mounted.height}px to ${loaded!.height}px)`).toBeLessThanOrEqual(1)
    // A row that grows below the top of the view pushes the tail farther away.
    const travelled = await chatDistanceFromBottom(page) - tailBefore
    expect(Math.abs(travelled - distance), `the tail is ${distance}px farther below the view, within 1px (it is ${travelled}px farther)`).toBeLessThanOrEqual(1)
  })

  codexTest('measures a file image row at its final height before the reader reaches it', async ({ native, authenticatedCodexWorkspace }) => {
    const { page } = native
    const chat = await seedChatWithFileImage(native, authenticatedCodexWorkspace.workingDir)

    await expect.poll(() => chat.premeasured.commitOf(chat.imageSeq), { message: 'a hidden copy measures the image row' }).not.toBeNull()
    const commit = (await chat.premeasured.commitOf(chat.imageSeq))!
    expect(await readRowBox(page, chat.imageSeq), 'the image row is still not mounted').toBeNull()

    const mounted = await scrollUpUntilImageRowMounts(page, chat.imageSeq)
    await dragChatUp(page, IMAGE_TOP_IN_VIEW_PX - mounted.distance)
    await expect.poll(() => readImageRowState(page, chat.imageSeq), { message: 'the image row shows its image' }).toEqual({ box: false, image: true })
    await waitForScrollRest(page)

    const loaded = await readRowBox(page, chat.imageSeq)
    expect(loaded, 'the image row is mounted').not.toBeNull()
    expect(Math.abs(loaded!.height - commit.height), `the hidden copy measured the height of the row with its image, within 1px (${commit.height}px for ${loaded!.height}px)`).toBeLessThanOrEqual(1)
  })
})

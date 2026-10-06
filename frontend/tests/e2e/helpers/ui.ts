import type { JsonValue } from '@bufbuild/protobuf'
import type { Locator, Page } from '@playwright/test'
import type { ToolSpanRowPosition } from '../../../src/components/chat/model/row'
import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { FileSortOrder } from '../../../src/lib/fileSort'
import process from 'node:process'
import { fromJson } from '@bufbuild/protobuf'
import { expect } from '@playwright/test'
import { permissionPresetsFor } from '../../../src/components/chat/providers/permissionPresets'
import { permissionPresetAvailable } from '../../../src/components/chat/providerSettings'
import { hasOptions } from '../../../src/components/chat/settingsGroups'
import { AgentStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { LocateTabResponseSchema, TabType } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
import { accountStorageKey, getTtlForKey, KEY_BROWSER_PREFS, PREFIX_EDITOR_DRAFT, PREFIX_FILES_SORT_ORDER } from '../../../src/lib/browserStorage'
import { isObject } from '../../../src/lib/jsonPick'
import { callHub, SESSION_COOKIE_NAME, TEST_ADMIN_PASSWORD, TEST_ADMIN_USERNAME } from './api'
import { solveCaptchaViaUI } from './captcha'
import { cssAttributeValue } from './cssAttribute'
import { nativeAgentById, nativeOptionGroup, nativeOptionValue, selectedAgentTab, selectedAgentTabId } from './nativeScenario'
import { E2E_BROWSER_HOST } from './server'
import { readEntry, waitForStoredEntry, writeEntry } from './storage'
import { terminalXterm } from './terminal'
import { waitTimeoutBeforeTestDeadline } from './testDeadline'

/** Read immediate locator visibility. Return false when the read fails. */
export async function isMaybeVisible(locator: Locator): Promise<boolean> {
  return locator.isVisible().catch(() => false)
}

/** Wait until at least one locator in the list is visible. */
export async function expectAnyVisible(...locators: Locator[]) {
  await expect.poll(async () => {
    const visibility = await Promise.all(locators.map(locator => isMaybeVisible(locator)))
    return visibility.some(Boolean)
  }).toBe(true)
}

// ──────────────────────────────────────────────
// Sidebar text clipping
// ──────────────────────────────────────────────

/**
 * Check the composed styles for single-line label clipping in a real browser.
 * The vanilla-extract composition supplies multiple classes and rules. jsdom does not load their stylesheets.
 * Check the two overflow axes also. Horizontal clipping must not cut off the
 * font ink above or below a compact line box.
 * Check min-width also. A flex item with min-width:auto retains its text width and prevents ellipsis.
 * Pair this with expectClipsLongText to verify the resulting layout.
 * Pass the label itself. A Tooltip wrapper contains the same text but uses display:contents and reports text-overflow:clip.
 */
export async function expectClipsToOneLine(label: Locator) {
  await expect(label).toHaveCSS('white-space', 'nowrap')
  await expect(label).toHaveCSS('text-overflow', 'ellipsis')
  await expect(label).toHaveCSS('overflow-x', 'clip')
  await expect(label).toHaveCSS('overflow-y', 'visible')
  await expect(label).toHaveCSS('min-width', '0px')
}

/**
 * Check that a long label clips without widening an ancestor scroller.
 * Style declarations cannot detect a container that grows to its widest row.
 * Replace the text and restore it within the same synchronous browser operation. This forces layout and keeps Solid references to the same node.
 * Check ancestors with overflow-x:auto or scroll. The label must have scrollWidth greater than clientWidth.
 * Allow one pixel for subpixel rounding in ancestor measurements.
 */
export async function expectClipsLongText(label: Locator) {
  const measured = await label.evaluate((el) => {
    const node = el.firstChild
    if (!(node instanceof Text))
      throw new TypeError('expectClipsLongText needs a label whose first child is its text')
    const original = node.nodeValue
    // A repeated letter has no break opportunity, which is the input that
    // escaped its box and grew the sideways scrollbar in the first place.
    node.nodeValue = 'W'.repeat(200)
    try {
      const scrolling: string[] = []
      for (let box: Element | null = el; box && box !== document.body; box = box.parentElement) {
        const { overflowX } = getComputedStyle(box)
        if (overflowX !== 'auto' && overflowX !== 'scroll')
          continue
        if (box.scrollWidth - box.clientWidth > 1)
          scrolling.push(`${box.tagName.toLowerCase()}.${box.getAttribute('class') ?? ''} (${box.scrollWidth} > ${box.clientWidth})`)
      }
      return { clipped: el.scrollWidth - el.clientWidth > 1, scrolling }
    }
    finally {
      node.nodeValue = original
    }
  })
  expect(measured.clipped, 'the label must clip its own text').toBe(true)
  expect(measured.scrolling, 'no ancestor should scroll horizontally').toEqual([])
}

// ──────────────────────────────────────────────
// Sidebar sections
// ──────────────────────────────────────────────

/**
 * Locate the visible header of one sidebar section by its slug, such as `workers` or `workspaces_archived`.
 * The desktop and mobile sidebars mount one copy each, so the locator takes the first visible copy.
 */
export function sidebarSectionHeader(page: Page, slug: string): Locator {
  return page.locator(`[data-testid="section-header-${slug}"]:visible`).first()
}

/**
 * Expand a collapsed sidebar section through its header button, leave an open section unchanged, and require that
 * the section is open at the end.
 * Pass the section header locator, which carries `data-closed` while the section is collapsed.
 * The open state is read once and not waited for, because the header button toggles: a click on an open section
 * closes it. A header that the read cannot reach counts as open, and the final check then fails with the reason.
 */
export async function expandSidebarSection(section: Locator): Promise<void> {
  const isOpen = await section.evaluate(el => !el.hasAttribute('data-closed')).catch(() => true)
  if (!isOpen)
    await section.locator('> [role="button"]').click()
  await expect(section, 'the sidebar section is open').not.toHaveAttribute('data-closed')
}

// ──────────────────────────────────────────────
// Common UI interaction helpers
// ──────────────────────────────────────────────

/**
 * How a test enters the text of a message into the composer.
 * `type` sends key events for each character.
 * `insert` sends each line with one insertion, for a text that typing would take longer than the test.
 */
export type MessageEntry = 'type' | 'insert'

/**
 * Put the text into the focused composer. `insert` presses Enter for each line break, as typing does,
 * and inserts each line at once. Both modes give the same text for a line of prose.
 * A line with a markdown trigger, such as backticks, can differ, because only typing presses each key.
 * Use `type` for such a line.
 */
export async function enterMessageText(page: Page, text: string, entry: MessageEntry): Promise<void> {
  if (entry === 'type') {
    await page.keyboard.type(text)
    return
  }
  for (const [index, line] of text.split('\n').entries()) {
    if (index > 0)
      await page.keyboard.press('Enter')
    if (line !== '')
      await page.keyboard.insertText(line)
  }
}

/**
 * The modifier key of the `$mod` chords of the app on `platform`: Meta on macOS, Control on every other platform.
 * tinykeys maps `$mod` the same way.
 */
export function platformModifier(platform: NodeJS.Platform): 'Meta' | 'Control' {
  return platform === 'darwin' ? 'Meta' : 'Control'
}

/**
 * The modifier key of the `$mod` chords of the app on this host. Press it in place of a fixed Meta: a fixed Meta is a
 * different chord on Linux and Windows, and the composer accepts either modifier for a send, so such a spec can pass
 * without the chord under test. The browser runs on the host of the test runner, so the platform of the runner decides.
 */
export const PLATFORM_MOD = platformModifier(process.platform)

/**
 * Locate the editable area of the visible composer.
 * The shell mounts one composer at most, for the focused agent tab. The locator still requires a visible composer,
 * because only a visible composer can take the input.
 */
export function composerEditor(page: Page): Locator {
  return page.locator('[data-testid="composer-editor"]:visible .ProseMirror')
}

/** Wait for the composer on screen, click it so that it holds the focus, and return it. */
export async function focusComposer(page: Page): Promise<Locator> {
  const editor = composerEditor(page)
  await expect(editor).toBeVisible()
  await editor.click()
  return editor
}

/**
 * Locate the pause toggle of the input queue in the visible composer. Its label is "Pause Queue" or "Resume Queue".
 * A composer that takes no input, such as a read-only subagent tab, shows no toggle.
 */
export function queuePauseButton(page: Page): Locator {
  return page.locator('[data-testid="queue-pause-button"]:visible')
}

/**
 * Resume a paused input queue.
 * The queue must be paused. A running queue fails the call, so a pause that never happened cannot pass.
 */
export async function resumePausedQueue(page: Page): Promise<void> {
  const button = queuePauseButton(page)
  await expect(button).toHaveText('Resume Queue')
  await button.click()
  await expect(button).toHaveText('Pause Queue')
}

/**
 * The state of the input queue after a failed turn.
 *
 * The Worker pauses the queue for a stop, for a process exit that nobody asked for, and for an input that it could
 * not deliver. A provider that reports a failed model call as the end of its turn leaves the queue running: its
 * `SendInput` returns at delivery, before the model call, and a turn end never pauses the queue.
 */
export type QueueAfterFailure = 'paused' | 'running'

/**
 * Require the queue state that a failed turn leaves, and resume a paused queue, so the next prompt reaches the agent.
 * The caller states the state of its provider. A check that accepted either state would let a lost pause pass, and
 * also a pause that a provider crash causes.
 */
export async function resumeQueueAfterFailure(page: Page, state: QueueAfterFailure): Promise<void> {
  if (state === 'paused') {
    await resumePausedQueue(page)
    return
  }
  await expect(queuePauseButton(page)).toHaveText('Pause Queue')
}

/**
 * Send a message through the ProseMirror editor with no inter-key delay.
 * ProseMirror handles the ordered key events synchronously. The former 100ms delay added about five seconds to each arithmetic prompt.
 * Tests of input rules, mention triggers, and slash commands retain their deliberate local typing intervals.
 */
export async function sendMessage(page: Page, text: string, entry: MessageEntry = 'type') {
  const editor = await focusComposer(page)
  // The send can raise a control request at once (a native editor request, an MCP form), and its banner then hides
  // this composer. A `:visible` locator stops matching the hidden composer, and an unscoped one is ambiguous when
  // another tab mounts its own composer. So the clear check holds the element that received the text.
  const sent = await editor.elementHandle()
  try {
    await enterMessageText(page, text, entry)
    await page.keyboard.press('Meta+Enter')
    // Wait for the composer to clear after it accepts the send. This prevents the caller from proceeding before that local acknowledgement.
    await expect.poll(() => sent.evaluate(element => element.textContent ?? ''), {
      message: 'the composer that received the message must clear after the send',
    }).toBe('')
  }
  finally {
    await sent.dispose()
  }
}

/**
 * ChatView mounts hidden copies of rows whose heights remain unknown.
 * ChatHiddenPremeasure retains the same IDs, text, and classes. A visible-list row can also stay hidden until measurement completes.
 * An unfiltered locator can therefore match multiple copies and fail Playwright strict mode.
 * A 20ms sample once found six bubbles for two messages, including four hidden copies. Higher load can lengthen that measurement period.
 * Apply :visible to the outermost chat locator. Descendants of a visible bubble need no extra filter.
 * Use these shared helpers for chat locators.
 */
const VISIBLE = ':visible'

/**
 * CSS selector for agent bubbles without a visibility filter.
 * Use it only below an already visible element or as a relative has filter.
 * A page-level lookup also matches hidden measurement copies. Use assistantBubbles for that lookup.
 */
export const ASSISTANT_BUBBLE_SELECTOR = '[data-testid="message-bubble"][data-role="agent"]'

/** CSS selector for user message bubbles. Same caveat as {@link ASSISTANT_BUBBLE_SELECTOR}. */
export const USER_BUBBLE_SELECTOR = '[data-testid="message-bubble"][data-role="user"]'

/** Return a locator for all visible assistant message bubbles. */
export function assistantBubbles(page: Page) {
  return page.locator(ASSISTANT_BUBBLE_SELECTOR + VISIBLE)
}

/** Return a locator for all visible user message bubbles. */
export function userBubbles(page: Page) {
  return page.locator(USER_BUBBLE_SELECTOR + VISIBLE)
}

/** Return a locator for all visible message bubbles, whatever their role. */
export function messageBubbles(page: Page) {
  return page.locator(`[data-testid="message-bubble"]${VISIBLE}`)
}

/** Return a locator for all visible message content nodes. */
export function messageContents(page: Page) {
  return page.locator(`[data-testid="message-content"]${VISIBLE}`)
}

/** The place of one tool row in its span: the call, a live update, or the result. */
export type ToolCallRowRole = ToolSpanRowPosition['role']

/** Locate the visible row of one tool call in one role. The result row by default. */
export function toolCallRow(page: Page, callId: string, role: ToolCallRowRole = 'result'): Locator {
  return page.locator(`[data-testid="message-bubble"][data-tool-call-id="${cssAttributeValue(callId)}"][data-tool-row-role="${role}"]${VISIBLE}`)
}

/** Locate every visible tool row, whatever its call or role. */
export function toolRows(page: Page): Locator {
  return page.locator(`[data-tool-message]${VISIBLE}`)
}

/**
 * Locate the visible bubble in which a subagent's report reached its parent, and which holds `report`.
 * The bubble header reads "<reporter> reported", and "Subagent" is the reporter when the provider gives no label.
 */
export function subagentReportBubble(page: Page, report: string | RegExp, reporter = 'Subagent'): Locator {
  return messageBubbles(page).filter({ hasText: `${reporter} reported` }).filter({ hasText: report })
}

/**
 * State why the rows do not hold `texts` in order, or return '' when they do.
 * Each text must sit in its own row, and the rows of the texts must follow one another in the order of `texts`.
 * A row holds a text when the text is part of the row's text. The first row that holds a text decides its place.
 */
export function rowOrderProblem(rowTexts: readonly string[], texts: readonly string[]): string {
  const places = texts.map(text => rowTexts.findIndex(row => row.includes(text)))
  const missing = texts.filter((_, index) => places[index] === -1)
  if (missing.length > 0)
    return `no row holds ${missing.map(text => JSON.stringify(text)).join(', ')}`
  for (let index = 1; index < texts.length; index++) {
    const earlier = JSON.stringify(texts[index - 1])
    const later = JSON.stringify(texts[index])
    if (places[index] === places[index - 1])
      return `${earlier} and ${later} are in one row (row ${places[index]})`
    if (places[index]! < places[index - 1]!)
      return `${later} (row ${places[index]}) comes before ${earlier} (row ${places[index - 1]})`
  }
  return ''
}

/**
 * Require each of `texts` in its own row of `rows`, in the order of `texts`.
 * An order check through `findIndex` passes when the earlier text is missing, because a missing text reads as -1.
 * This check fails for a missing text, for a reversed order, and for two texts in one row.
 */
export async function expectRowsInOrder(rows: Locator, texts: readonly string[]): Promise<void> {
  if (texts.length < 2)
    throw new Error('A row order needs at least two texts.')
  if (texts.includes(''))
    throw new Error('A row order needs texts that are not empty, because every row holds an empty text.')
  // The poll reports the last problem as the received value.
  await expect.poll(async () => rowOrderProblem(await rows.allTextContents(), texts), {
    message: `the rows hold ${texts.map(text => JSON.stringify(text)).join(', ')} in this order`,
  }).toBe('')
}

/** Locate every visible control request banner without narrowing its count. */
export function controlBanner(page: Page): Locator {
  return page.getByTestId('control-banner').filter({ visible: true })
}

/**
 * Wait for the visible control request banner and return it.
 * The wait is strict, so a second visible banner fails it.
 */
export async function waitForControlBanner(page: Page): Promise<Locator> {
  const banner = controlBanner(page)
  await expect(banner).toBeVisible()
  return banner
}

/**
 * Require that the page holds no control request banner, visible or hidden.
 * The locator has no `:visible` scope on purpose. A zero count of `controlBanner` accepts a hidden banner,
 * and this check refuses one.
 */
export async function expectNoControlBanner(page: Page): Promise<void> {
  await expect(page.getByTestId('control-banner'), 'the page holds no control request banner').toHaveCount(0)
}

/**
 * The action of a control request button:
 * - `allow` and `deny`: the decision buttons of a permission or plan request. With text in the composer, `deny` sends
 *   that text as feedback.
 * - `submit`: the button that sends the answers of a question.
 * - `stop`: the question button that refuses the question.
 * - `yolo`: the question button that fills each unanswered question with the recommended option and sends the answers.
 */
export type ControlAction = 'allow' | 'deny' | 'submit' | 'stop' | 'yolo'

/** Locate every visible button of one control request action. */
export function controlButton(page: Page, action: ControlAction): Locator {
  return page.getByTestId(`control-${action}-btn`).filter({ visible: true })
}

/**
 * Locate the visible page buttons of a question request with more than one question.
 * The page buttons sit in the action row of the request, beside Submit, and the action row is outside the banner.
 * So a locator inside the banner finds no page button, and this locator starts at the page, as `controlButton` does.
 */
export function questionPagination(page: Page): Locator {
  return page.getByTestId('control-pagination').filter({ visible: true })
}

/**
 * Click the visible Allow or Deny button of the control request.
 * The click is strict, so a second visible button, as from a duplicate banner, fails it.
 */
export async function answerControl(page: Page, decision: 'allow' | 'deny'): Promise<void> {
  await controlButton(page, decision).click()
}

/**
 * Click the visible Approve or Reject button of a plan review.
 * The click is strict, as the click of `answerControl` is.
 */
export async function answerPlanReview(page: Page, decision: 'approve' | 'reject'): Promise<void> {
  await page.getByTestId(`plan-${decision}-btn`).filter({ visible: true }).click()
}

/**
 * Type `reason` into the composer, which holds the feedback of an open control request.
 * Most requests then relabel the Deny button "Send feedback". The caller checks the label, because it differs by request.
 */
export async function enterControlFeedback(page: Page, reason: string): Promise<void> {
  if (reason.trim() === '')
    throw new Error('A control feedback needs text, because an empty composer sends no feedback.')
  await focusComposer(page)
  await enterMessageText(page, reason, 'type')
}

/** Join all visible chat content with the original single-space separator. */
export async function chatText(page: Page): Promise<string> {
  return (await messageContents(page).allTextContents()).join(' ')
}

/** Locate every visible transcript row, including rows outside message bubbles. */
export function transcriptRows(page: Page) {
  return page.locator('[data-seq]:visible')
}

/** Locate the saved control answer text and preserve its full visible count. */
export function savedControlAnswer(page: Page) {
  return page.locator('[data-testid="control-response-text"]:visible')
}

/**
 * Locate visible message-band rows. Restrict kind to text or thought when needed.
 * The row supplies the full-width background and border, so the marker belongs to the row.
 * The test attribute avoids dependence on hashed style class names.
 */
export function bandRows(page: Page, kind?: 'text' | 'thought') {
  const selector = kind === undefined ? '[data-band]' : `[data-band="${kind}"]`
  return page.locator(selector + VISIBLE)
}

/** The chat's scrolling element, which the app publishes for exactly this purpose. */
export const CHAT_SCROLL_CONTAINER = '[data-chat-scroll-container="true"]'

/**
 * Return a locator for the chat's visible scrolling element.
 * Each agent tab of a tile mounts its own ChatView, so only the visible one is the chat on screen.
 */
export function chatScrollContainer(page: Page) {
  return page.locator(CHAT_SCROLL_CONTAINER + VISIBLE)
}

/**
 * Find a visible row that contains `path` and a diff badge.
 * A new-file Write row has no badge. Its `renderWriteTitle` title contains the line count, so this helper cannot match that row.
 * For a new file, find the tool row by its path and line count. Find its diff through `[data-file-diff]`.
 * `codex/file-tool-execution.spec.ts` uses those new-file checks.
 *
 * Both filters are necessary. The user prompt contains the same path and appears first. A text-only filter can select that prompt.
 * The `has` filter requires the diff badge. The `:visible` filter removes the hidden copy that ChatView uses for unmeasured rows.
 */
export function fileChangeRow(page: Page, path: string): Locator {
  return page.locator('[data-seq]:visible')
    .filter({ has: page.getByTestId('git-diff-stats') })
    .filter({ hasText: path })
    .first()
}

/**
 * Maximum wait for an attached match in readAttached.
 * A row replacement takes a few frames. Keep this below expect.timeout so an absent chat row produces a specific error before the test deadline.
 */
const ATTACHED_READ_TIMEOUT_MS = 15_000

/**
 * Read from an attached locator match. Use this for chat rows and their descendants because the app can replace them.
 * Locator resolution and evaluation require separate browser requests. A replacement between those requests leaves a detached node.
 * Detached nodes report zero geometry and empty styles. That can fail a layout check or falsely pass a color comparison.
 * See https://github.com/leapmux/leapmux/issues/402.
 *
 * evaluateAll also resolves an array handle before evaluating it. A replacement can occur between these two requests also.
 * A single page.evaluate request would lose Playwright visibility filtering, which excludes the hidden measurement copies.
 * Instead, the reader skips detached candidates and returns null for another attempt.
 * Every returned measurement then comes from one synchronous browser operation on an attached node.
 *
 * The serialized reader must filter the candidate array itself. It cannot call a Node closure across the browser boundary.
 * For the same reason, this helper passes CHAT_SCROLL_CONTAINER as the second reader argument.
 * A reader that does not need it can omit that parameter.
 */
export async function readAttached<R>(
  locator: Locator,
  what: string,
  read: (possiblyDetachedMatches: (SVGElement | HTMLElement)[], chatScrollContainerSelector: string) => R | null,
): Promise<R> {
  return readAttachedWithArgument(locator, what, read, CHAT_SCROLL_CONTAINER)
}

/** Read attached elements with an explicit serializable argument. */
export async function readAttachedWithArgument<R, A>(
  locator: Locator,
  what: string,
  read: Exclude<Parameters<typeof locator.evaluateAll<R | null, A>>[0], string>,
  argument: A,
): Promise<R> {
  // Held on an object rather than in a `let`: the assignment happens inside the
  // retry closure, which control-flow narrowing cannot see through.
  const held: { value: R | null } = { value: null }
  await expect(async () => {
    held.value = await locator.evaluateAll(read, argument)
    expect(held.value, `${what}: matched no element that was still in the document`).not.toBeNull()
  }).toPass({ timeout: ATTACHED_READ_TIMEOUT_MS })
  return held.value!
}

/**
 * Read all geometry for the two chat measurements in one browser operation.
 * Both callers use this reader and select the fields they need, so their layout measurements cannot diverge.
 */
interface ChatBoxGeometry {
  /** The element's own border-box width. */
  width: number
  /**
   * The list width used to calculate both gaps.
   * Include it in errors to distinguish the wrong scroller from an incorrect card width.
   * Derive the card width from listWidth minus both gaps.
   */
  listWidth: number
  /** Distance from the element's right side to the list's padding-box right edge. */
  rightGap: number
  /** Distance from the list's padding-box left edge to the element's left side. */
  leftGap: number
  /** Distance from the element's top edge down from its ROW's top edge, null when it has no parent. */
  topGapInRow: number | null
  /** The element's computed `border-top-right-radius`, as CSS reports it. */
  radius: string
}

/** A match that cannot be measured, and what it turned out to be instead. */
interface ChatBoxOutsideList {
  outside: string
}

/**
 * Measure an attached element against its own row and chat scroll container.
 * Find the container through its published attribute on an ancestor. Do not infer it from DOM position or computed overflow.
 * clientLeft accounts for its left border. clientWidth excludes the native scrollbar.
 * All values come from one layout, so a resize cannot split the measurement.
 */
function readChatBoxGeometry(
  possiblyDetachedMatches: (SVGElement | HTMLElement)[],
  chatScrollContainerSelector: string,
): ChatBoxGeometry | ChatBoxOutsideList | null {
  // A row can remount between resolution and this read. Its old element then reports a zero rectangle and empty computed style.
  // The browser returns those values instead of an error.
  const el = possiblyDetachedMatches.find(candidate => candidate.isConnected)
  if (!el)
    return null
  const list = el.closest(chatScrollContainerSelector)
  if (!list) {
    // Report a hidden measurement copy explicitly. It sits outside the chat scroller and can otherwise resemble a detached row.
    if (el.closest('[data-chat-premeasure-root="true"]'))
      return { outside: 'it is ChatView\'s hidden premeasure copy, not the live row' }
    const chain: string[] = []
    for (let node: Element | null = el; node && chain.length < 8; node = node.parentElement) {
      const testId = node.getAttribute('data-testid')
      chain.push(node.tagName.toLowerCase() + (testId ? `[${testId}]` : ''))
    }
    return { outside: `ancestors: ${chain.join(' < ')}` }
  }
  const listRect = list.getBoundingClientRect()
  const self = el.getBoundingClientRect()
  const row = el.parentElement
  const padLeft = listRect.left + list.clientLeft
  return {
    width: self.width,
    listWidth: list.clientWidth,
    rightGap: padLeft + list.clientWidth - self.right,
    leftGap: self.left - padLeft,
    topGapInRow: row ? self.top - row.getBoundingClientRect().top : null,
    radius: globalThis.getComputedStyle(el).borderTopRightRadius,
  }
}

/** Run {@link readChatBoxGeometry} through {@link readAttached} and reject a match it cannot measure. */
async function measureChatBox(locator: Locator, what: string): Promise<ChatBoxGeometry> {
  const read = await readAttached(locator, what, readChatBoxGeometry)
  // Not retried: an element outside every chat list stays outside, so looping on
  // it would replace this message with a test timeout 15 seconds later.
  if ('outside' in read)
    throw new Error(`${what}: element is not inside the chat scroll container -- ${read.outside}`)
  return read
}

/**
 * Measure the chat element against the scroll container's `clientWidth`.
 * That value includes the padding box and excludes the scrollbar. A band or turn-end rule must span that width.
 */
export async function measureAgainstChatList(locator: Locator): Promise<{ width: number, listWidth: number }> {
  const { width, listWidth } = await measureChatBox(locator, 'measureAgainstChatList')
  return { width, listWidth }
}

/** Where an end-of-line card's sides sit, and the corner it turns at the edge. */
export interface BubbleEdges {
  /**
   * Store the list's padding-box width to explain a failure.
   * That width distinguishes measurement against the wrong list from a missing bleed rule.
   * Calculate the card width from `listWidth - leftGap - rightGap`. Do not store that derived value again.
   */
  listWidth: number
  /** Distance from the card's right side to the list's padding-box right edge. */
  rightGap: number
  /** Distance from the list's padding-box left edge to the card's left side. */
  leftGap: number
  /** Distance from the card's top edge down from its ROW's top edge. */
  topGapInRow: number
  /** The card's computed `border-top-right-radius`, as CSS reports it. */
  radius: string
}

/**
 * Measure the card against both panel edges and its row.
 * User messages and plan execution cards share the right-alignment rule, so both tests use this reader.
 * topGapInRow checks that the card starts at the top of its row.
 * A bubble alignSelf rule can change that position. A real browser is required because jsdom does not calculate flex layout.
 */
export async function measureBubbleEdges(locator: Locator): Promise<BubbleEdges> {
  const { listWidth, rightGap, leftGap, topGapInRow, radius } = await measureChatBox(locator, 'measureBubbleEdges')
  if (topGapInRow === null)
    throw new Error('measureBubbleEdges: element has no row to measure against')
  return { listWidth, rightGap, leftGap, topGapInRow, radius }
}

/**
 * Restrict `locator` to visible elements.
 * Text queries such as `getByText` also match hidden premeasure copies. Apply this filter to page-rooted chat assertions that use text.
 */
export function visibleOnly(locator: Locator): Locator {
  return locator.filter({ visible: true })
}

/** Return a locator for the first visible assistant message bubble. */
export function firstAssistantBubble(page: Page) {
  return assistantBubbles(page).first()
}

/** Return a locator for the last visible assistant message bubble. */
export function lastAssistantBubble(page: Page) {
  return assistantBubbles(page).last()
}

/**
 * Locate the first assistant message row that offers quote and copy actions.
 * Turn dividers and startup notices also use agent-role bubbles, but supply no onReply handler or selectable message prose.
 * Their order can vary, so the first agent bubble does not necessarily contain an assistant message.
 * Use this helper for tests that quote or select a message.
 */
export function firstAssistantMessageRow(page: Page) {
  return assistantBubbles(page)
    .locator('..')
    .filter({ has: page.locator('[data-testid="message-quote"]') })
    .first()
}

/**
 * Share this arithmetic chat probe across agent end-to-end tests. Its answer, 6912, contains four distinct digits.
 * A single digit can match incidental UI text. These examples contain such digits:
 * - Model IDs such as gpt-5.4.
 * - Durations.
 * - Token counts.
 * - Dates.
 */
export const ARITHMETIC_PROMPT = 'What is 1234 + 5678? Reply with just the number.'

/**
 * Matches the {@link ARITHMETIC_PROMPT} answer, tolerating a thousands comma.
 * Word-boundary anchored so it can't match 6912 as a substring of a larger
 * number (a token count, duration, or id) that incidentally contains it.
 */
export const ARITHMETIC_ANSWER = /\b6,?912\b/

/**
 * The literal a model scenario returns for {@link ARITHMETIC_PROMPT}.
 *
 * The mock endpoint never reads the prompt, so the prompt and its answer are
 * only related here. `helpers/ui.test.ts` pins that relation.
 */
export const ARITHMETIC_ANSWER_TEXT = '6912'

/**
 * Arithmetic prompt for a second turn with a distinct answer.
 * Neither 3333 nor 6912 contains the other, so one answer cannot satisfy an assertion for the other turn.
 */
export const SECOND_ARITHMETIC_PROMPT = 'What is 1111 + 2222? Reply with just the number, nothing else.'

/** Matches the {@link SECOND_ARITHMETIC_PROMPT} answer. See {@link ARITHMETIC_ANSWER}. */
export const SECOND_ARITHMETIC_ANSWER = /\b3,?333\b/

/** The literal a model scenario returns for {@link SECOND_ARITHMETIC_PROMPT}. */
export const SECOND_ARITHMETIC_ANSWER_TEXT = '3333'

/**
 * Require the `ARITHMETIC_PROMPT` answer in at least one visible assistant bubble. Check all assistant bubbles.
 * A trailing "Turn ended" divider is an agent-role bubble. `lastAssistantBubble()` can select that divider instead of the actual answer.
 */
export async function expectAssistantAnswer(page: Page, opts?: { answer?: RegExp }) {
  const answer = opts?.answer ?? ARITHMETIC_ANSWER
  await expect(assistantBubbles(page).filter({ hasText: answer })).not.toHaveCount(0)
}

/**
 * Require `text` in at least one visible user bubble.
 * The restart specs use this prompt guard to verify stored history.
 */
export async function expectUserMessage(page: Page, text: string) {
  await expect(userBubbles(page).filter({ hasText: text })).not.toHaveCount(0)
}

/**
 * Wait until the agent of the visible tab ends its startup.
 *
 * - The visible composer proves that the agent tab rendered.
 * - The startup overlay shows while the agent is STARTING, so the wait requires that no visible overlay remains.
 * - A failed startup shows `agent-startup-error`, which fails the wait here and not later as a send that went nowhere.
 *
 * The overlay is matched by its test ID, never by its text. The overlay shows the startup message of the provider when
 * the provider reports one, so a check for "Starting ..." passes while the startup still runs.
 */
export async function waitForAgentStarted(page: Page): Promise<void> {
  await expect(composerEditor(page), 'the agent tab shows its composer').toBeVisible()
  await expect(visibleOnly(page.getByTestId('agent-startup-overlay')), 'the agent ends its startup').toHaveCount(0)
  await expect(visibleOnly(page.getByTestId('agent-startup-error')), 'the agent starts with no error').toHaveCount(0)
}

/**
 * Maximum wait for the thinking indicator to appear after a send.
 * The indicator may finish before the wait starts. A short observation period avoids a full action timeout in that case.
 */
const APPEARANCE_PROBE_MS = 2000

/**
 * Wait for the agent to finish its current turn (thinking indicator gone).
 *
 * The helper takes no limit. It waits until `WAIT_REPORT_MARGIN_MS` (`./testDeadline`) before the test's own
 * deadline, as the model-script waits do. A turn that never ends then fails here, at the idle step, and the margin
 * leaves time for the fixtures to attach the model script. A test that needs more time raises its own timeout.
 */
export async function waitForAgentIdle(page: Page) {
  // Each tab in a tile mounts its own ChatView. Hidden panes keep their thinking indicator.
  // Scope the indicator to the visible pane. Otherwise a second tab creates two matches and a strict-mode failure.
  const thinking = page.locator('[data-testid="thinking-indicator"]:visible')
  // Observe the indicator before checking that it is hidden. An immediate absence check could precede the start of a turn.
  // An expired observation is permitted because a fast turn can finish before the first check.
  // Use the short explicit interval instead of the 30-second action timeout.
  // This helper alone cannot distinguish a completed turn from one that starts after the observation interval.
  // Callers must also check the expected response or operation result.
  await thinking.waitFor({ state: 'visible', timeout: APPEARANCE_PROBE_MS }).catch(() => {})
  await expect(thinking).not.toBeVisible({ timeout: waitTimeoutBeforeTestDeadline() })
}

// ──────────────────────────────────────────────
// UI helpers
// ──────────────────────────────────────────────

/**
 * Locate the app-menu trigger for the active layout.
 * Desktop uses app-menu-trigger in the title bar. Phones use collapsed-new-tab-button in the tab bar.
 * AppShell renders these only after AuthGuard permits access. Their presence also identifies the authenticated shell for login and startup tests.
 */
export function appMenuTrigger(page: Page): Locator {
  return page.getByTestId('app-menu-trigger').first().or(page.getByTestId('collapsed-new-tab-button')).first()
}

/**
 * Open the app menu for the active layout. Wait for either trigger before selecting one.
 * isVisible does not wait. Checking it immediately after navigation could select the hidden phone trigger before the desktop title bar mounts.
 */
async function openAppMenu(page: Page) {
  const appMenu = page.getByTestId('app-menu-trigger').first()
  const collapsed = page.getByTestId('collapsed-new-tab-button')
  await appMenuTrigger(page).waitFor({ state: 'visible' })
  if (await appMenu.isVisible())
    await appMenu.click()
  else
    await collapsed.click()
}

/**
 * Open the About dialog from the app menu.
 *
 * The item's label differs between the desktop shell and the browser ("About
 * LeapMux Desktop..." against "About..."), so it is matched by prefix.
 */
export async function openAboutDialog(page: Page): Promise<Locator> {
  await openAppMenu(page)
  await page.getByRole('menuitem', { name: /^About/ }).click()
  const dialog = page.getByRole('dialog', { name: 'About' })
  await expect(dialog).toBeVisible()
  return dialog
}

/**
 * Locate the step-up prompt that asks the user to prove a factor ("Verify your identity").
 * The hub refuses a sensitive action on a session that proved no factor, and the client then opens this prompt.
 */
export function elevationPrompt(page: Page): Locator {
  return page.getByRole('dialog', { name: 'Verify your identity' })
}

/**
 * Require the step-up prompt, answer it with `password`, and return it.
 * The caller asserts the outcome: the refused action runs again after a correct password, and the prompt shows an
 * alert after a wrong one.
 * `prompt` is the dialog of the app by default. The standalone `/elevate` page, where the hub sends a browser from an
 * authorization request, carries the same password form, so a caller passes that page's card instead.
 */
export async function answerElevationPrompt(page: Page, password: string, prompt: Locator = elevationPrompt(page)): Promise<Locator> {
  await expect(prompt, 'the hub asks the session to prove a factor').toBeVisible()
  await prompt.getByTestId('elevate-password').fill(password)
  await prompt.getByTestId('elevate-password-submit').click()
  return prompt
}

/**
 * Open the agent info card and return its popover.
 *
 * The card is the popover of the status-bar info trigger. It carries the
 * context-usage and the rate-limit rows, so those helpers share this
 * navigation rather than repeat the open sequence.
 */
export async function openAgentInfoCard(page: Page): Promise<Locator> {
  const infoTrigger = page.locator('[data-testid="agent-info-trigger"]')
  await expect(infoTrigger).toBeVisible()
  await infoTrigger.click()
  const popover = page.locator('[data-testid="agent-info-popover"]')
  await expect(popover).toBeVisible()
  return popover
}

/**
 * Open Preferences and select a category.
 * Desktop uses sidebar tabs, and phones use a section menu.
 * category specifies the navigation ID. If absent, keep the dialog default.
 */
export async function openPreferencesDialog(page: Page, category?: string) {
  const dialog = page.getByRole('dialog', { name: 'Preferences' })
  // The `prefs` query parameter restores the open dialog and its category after reload. Use that restored dialog.
  // Its modal overlay blocks the app-menu trigger.
  // Read the query parameter to select the route. The restored dialog may not mount before a visibility check.
  // Opening another dialog in that interval lets the restored overlay intercept the trigger click and cause a timeout.
  if (new URL(page.url()).searchParams.has('prefs')) {
    await expect(dialog).toBeVisible()
  }
  else if (!(await dialog.isVisible())) {
    await openAppMenu(page)
    await page.getByRole('menuitem', { name: 'Preferences' }).click()
  }
  await expect(dialog).toBeVisible()
  if (category) {
    const item = dialog.getByTestId(`preferences-nav-${category}`)
    const compactTrigger = dialog.getByTestId('preferences-nav')
    // Wait for the category tab or compact menu trigger. Admin categories appear only after ListSettings responds.
    // An early check could choose a compact trigger that the desktop layout never renders.
    await item.or(compactTrigger).first().waitFor({ state: 'visible' })
    // Compact (phone) keeps the sections inside a closed dropdown; open it
    // first. Desktop tabs are already visible in the sidebar.
    if (!(await item.isVisible()))
      await compactTrigger.click()
    await item.click()
  }
}

/** Sign-in attempts {@link loginViaUI} makes before it gives up. */
const LOGIN_ATTEMPTS = 3
/**
 * Maximum duration of one sign-in attempt. Three attempts must fit within the 300-second test deadline.
 * Default URL and shell waits can consume 150 seconds together, leaving no time for the third attempt or a specific error.
 */
const LOGIN_ATTEMPT_TIMEOUT_MS = 60_000

/**
 * Fill the sign-in form on the current page, solve its captcha, and submit it.
 * The caller asserts the outcome: a test of a refused sign-in reads the error, and `loginViaUI` waits for the app.
 */
export async function submitLoginForm(page: Page, username: string, password: string): Promise<void> {
  await page.getByLabel('Username').fill(username)
  await page.getByLabel('Password').fill(password)
  await solveCaptchaViaUI(page)
  await page.getByRole('button', { name: 'Sign in' }).click()
}

/**
 * Login via the UI form. Navigates to /login, fills credentials, solves the
 * captcha, and returns once the authenticated app shell is on screen.
 * Use it for a test of the sign-in itself. A test that only needs a signed-in app uses `openAppAs`, which costs no
 * captcha solve.
 */
export async function loginViaUI(page: Page, username = TEST_ADMIN_USERNAME, password = TEST_ADMIN_PASSWORD) {
  await page.goto('/login')
  await submitLoginForm(page, username, password)

  // Login selects /. Workspace activation does not change the path, so the URL check can match exactly.
  // Wait for the authenticated shell trigger. Do not wait for networkidle after an ALTCHA challenge.
  //
  // ALTCHA starts up to 16 solver workers and terminates the others after the first solution.
  // Chromium can cancel unfinished worker-script loads while Playwright retains their in-flight request records.
  // The idle timer then never starts, and an unlimited navigation wait lasts until the test deadline.
  // Faster solutions increase this overlap. One SCRYPT run finished while seven of ten workers still loaded their scripts.
  //
  // Retry a transient login refusal, such as a database that is not ready after restart.
  const loggedInURL = /\/$/
  let lastError: unknown
  for (let attempt = 0; attempt < LOGIN_ATTEMPTS; attempt++) {
    try {
      await expect(page).toHaveURL(loggedInURL, { timeout: LOGIN_ATTEMPT_TIMEOUT_MS })
      await appMenuTrigger(page).waitFor({ state: 'visible', timeout: LOGIN_ATTEMPT_TIMEOUT_MS })
      return // success
    }
    catch (err) {
      lastError = err
      // The test timeout tears the context down mid-wait. Probing a closed
      // page throws a second, unrelated error that buries the first one.
      if (page.isClosed())
        break
      // Check if there's an error message on the login page
      const error = page.locator('[class*="error"], [class*="Error"]')
      if (await error.count().catch(() => 0) > 0) {
        // Transient error — retry sign-in. The rejected submit consumed the
        // captcha payload and LoginPage.handleSubmit resets the field, so
        // `blocksSubmit()` disables the button until a fresh challenge is
        // solved. Re-solve BEFORE the click, or the click waits out the
        // action timeout on a disabled button.
        try {
          await solveCaptchaViaUI(page)
          await page.getByRole('button', { name: 'Sign in' }).click()
        }
        catch (resubmitError) {
          // The form cannot submit again. Another attempt returns the same failure.
          // Report this error, which identifies the failed step.
          lastError = resubmitError
          break
        }
      }
      // No error visible — the page may still be loading; the next iteration
      // checks the URL again.
    }
  }
  // Every attempt failed. Carry the last one's error: the retry loop is the
  // only thing that saw it, and a bare message here hid which wait expired.
  throw new Error(
    `loginViaUI: the app shell never mounted after ${LOGIN_ATTEMPTS} sign-in attempts`,
    { cause: lastError },
  )
}

/**
 * Open a new agent in the currently selected workspace.
 * Clicks the agent button in the tab bar which directly creates an agent.
 */
export async function openAgentViaUI(page: Page) {
  // Wait for the active tab directory before clicking. The agent handler reads workerId and workingDir synchronously.
  // If either is absent, it opens the directory dialog and does not retry.
  // An early click therefore creates no tab and would leave the later count check waiting until timeout.
  await waitForActiveTabContext(page)
  // Count existing agent tabs so we can wait for the new one to appear.
  const tabsBefore = await agentTabs(page).count()
  await page.locator('[data-testid^="new-agent-button"]').first().click()
  // Wait for the new agent tab to appear (the API call is async)
  await expectAgentTabCount(page, tabsBefore + 1)
  // Wait for the new tab to become selected and its editor to be ready
  await expect(selectedAgentTab(page)).toBeVisible()
  await expect(composerEditor(page)).toBeVisible()
}

/**
 * Wait for the active tab working directory.
 * A projected tab initially holds only tile, position, and worker data. useTabHydrators retrieves the directory later.
 * Check the nonempty data-working-dir that the Files section publishes after resolution.
 * The tree root is insufficient: it can render from workerId alone with a ~ directory fallback.
 *
 * Wait for attachment, not visibility. Mobile layout can collapse the sidebar even when the directory is ready.
 * Report a timeout here so a hydration failure does not appear as an unrelated interaction failure.
 */
export async function waitForActiveTabContext(page: Page) {
  await page.locator('[data-working-dir]:not([data-working-dir=""])').first().waitFor({ state: 'attached' })
}

/**
 * Open a terminal through the tab-bar button, wait until its xterm shows, and return the ID of the new terminal.
 *
 * - Wait for the active tab directory first. The handler reads the directory synchronously. If absent, it opens a
 *   directory dialog without retrying, so an early click creates no terminal.
 * - The new terminal is the terminal tab that was not there before the click. The tab list is optimistic state, so
 *   the wait then requires the xterm of THAT terminal: a bare `.xterm` matches each mounted terminal, and a hidden
 *   terminal tab stays mounted.
 */
export async function openTerminalViaUI(page: Page): Promise<string> {
  await waitForActiveTabContext(page)
  const tabIds = () => terminalTabs(page).evaluateAll(tabs => tabs.map(tab => tab.getAttribute('data-tab-id') ?? ''))
  const before = new Set(await tabIds())
  await page.locator('[data-testid="new-terminal-button"]').click()
  let opened = ''
  await expect.poll(async () => {
    opened = (await tabIds()).find(id => id !== '' && !before.has(id)) ?? ''
    return opened
  }, { message: 'the button opens a terminal tab' }).not.toBe('')
  await expect(terminalXterm(page, opened), 'the new terminal shows its xterm').toBeVisible()
  return opened
}

/**
 * Sign up a new user via the signup form.
 */
export async function signUpViaUI(page: Page, username: string, password: string, displayName = '', email = '') {
  await page.goto('/signup')
  await page.getByLabel('Username').fill(username)
  if (displayName) {
    await page.getByLabel('Display Name').fill(displayName)
  }
  if (email) {
    await page.getByLabel('Email').fill(email)
  }
  await page.getByLabel('New Password').fill(password)
  await page.getByLabel('Confirm Password').fill(password)
  await solveCaptchaViaUI(page)
  await page.getByRole('button', { name: 'Sign up' }).click()
}

/** Sign up with a passkey via the signup form (virtual authenticator must be enabled). */
export async function signUpWithPasskeyViaUI(
  page: Page,
  username: string,
  email: string,
  displayName = '',
) {
  await page.goto('/signup')
  await page.getByLabel('Username').fill(username)
  if (displayName) {
    await page.getByLabel('Display Name').fill(displayName)
  }
  await page.getByLabel('Email').fill(email)
  await page.getByRole('radio', { name: 'Passkey' }).click()
  await solveCaptchaViaUI(page)
  await page.getByRole('button', { name: 'Sign up with passkey' }).click()
  await expect(page).not.toHaveURL(/\/signup(?:\?.*)?$/)
}

/** Log in with a passkey via the login form (virtual authenticator must be enabled). */
export async function loginWithPasskeyViaUI(page: Page, username: string) {
  await page.goto('/login')
  await page.getByLabel('Username').fill(username)
  await page.getByLabel('Username').blur()
  const passkeyRadio = page.getByRole('radio', { name: 'Passkey' })
  if (await passkeyRadio.isVisible().catch(() => false))
    await passkeyRadio.click()
  await solveCaptchaViaUI(page)
  await page.getByRole('button', { name: /Sign in with passkey/i }).click()
  await expect(page).toHaveURL(/\/$/)
  await appMenuTrigger(page).waitFor({ state: 'visible' })
}

/**
 * Open Preferences at the requested section and return its canonical dialog locator.
 * Reuse `const dialog = await openSettingsAt(page, 'apps')` to keep the accessible name in one place.
 * This avoids inconsistent role lookups if that name changes.
 */
export async function openSettingsAt(page: Page, category?: string) {
  await openPreferencesDialog(page, category)
  return page.getByRole('dialog', { name: 'Preferences' })
}

/** Open Preferences on the account / profile section. */
export async function openAccountSettings(page: Page) {
  return openSettingsAt(page, 'account')
}

/**
 * Logout via the app menu (titlebar on desktop, tab-bar "+" menu on mobile).
 */
export async function logoutViaUI(page: Page) {
  await openAppMenu(page)
  await page.getByText('Log out').click()
  await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible()
}

/**
 * Browser preferences use the browserStorage wrapper with a value and expiry. The storage layer rejects missing or invalid wrappers.
 * Reading a raw field from the wrapper returns no preference. Writing an unwrapped object makes the app use defaults.
 * These helpers use the app registry for the key and lifetime, so storage-policy changes cannot invalidate fixture values unnoticed.
 */
function browserPrefsTtlMs(): number {
  const ttlMs = getTtlForKey(KEY_BROWSER_PREFS)
  if (ttlMs === null)
    throw new Error(`${KEY_BROWSER_PREFS} is missing from LOCAL_KEY_SPECS`)
  return ttlMs
}

const BROWSER_PREFS_TTL_MS = browserPrefsTtlMs()

/**
 * Read a single field from the consolidated browser preferences.
 *
 * Pass `leapmuxServer.adminUserId`. See `getBrowserPrefValue`.
 */
export async function getBrowserPref(page: Page, userId: string, field: string): Promise<string | null> {
  const value = await getBrowserPrefValue(page, userId, field)
  return value === null ? null : String(value)
}

/**
 * Read one browser preference without converting it to a string.
 * Structured values such as theme must remain objects. String conversion would make different objects compare as [object Object].
 * Use the supplied account ID and accountStorageKey. Scanning for a similar key could read another account after multiple logins.
 */
export async function getBrowserPrefValue(page: Page, userId: string, field: string): Promise<unknown> {
  const row = await readEntry(page, accountStorageKey(userId, KEY_BROWSER_PREFS))
  const prefs = row?.v
  if (prefs == null || typeof prefs !== 'object')
    return null
  const value = (prefs as Record<string, unknown>)[field]
  return value === undefined ? null : value
}

/**
 * Set one browser preference for the supplied account ID.
 * Pass leapmuxServer.adminUserId to select that account's stored preferences.
 * The page must already use the app origin. Await this write before reloading, so IndexedDB holds the value before the app reads it.
 * The value can be any supported preference type. For example, terminal size and opacity require numbers.
 * PreferencesContext rejects a numeric value stored as a string and uses the default instead.
 */
export async function setInitialBrowserPref(page: Page, userId: string, field: string, value: unknown) {
  const storedKey = accountStorageKey(userId, KEY_BROWSER_PREFS)
  const existing = await readEntry(page, storedKey)
  const prefs = (existing?.v != null && typeof existing.v === 'object')
    ? { ...existing.v as Record<string, unknown> }
    : {}
  prefs[field] = value
  await writeEntry(page, storedKey, prefs, Date.now() + BROWSER_PREFS_TTL_MS)
}

/**
 * The session cookie the browser context currently holds, as the
 * "leapmux-session=<value>" token `loginViaToken` takes. Its inverse.
 *
 * Three specs hand-rolled this lookup. One home means a rename of the
 * cookie name moves every reader with it.
 */
export async function readSessionCookie(page: Page, step: string): Promise<string> {
  const session = (await page.context().cookies()).find(c => c.name === SESSION_COOKIE_NAME)
  if (!session?.value)
    throw new Error(`${step} did not set a session cookie on the browser context`)
  return `${SESSION_COOKIE_NAME}=${session.value}`
}

/**
 * Sign in with the session `token`, load the app, and wait for the authenticated shell.
 * The shell trigger proves that the app accepted the session, so a later step does not act on the sign-in page.
 */
export async function openAppAs(page: Page, token: string): Promise<void> {
  await loginViaToken(page, token)
  await page.goto('/')
  await expect(appMenuTrigger(page), 'the app accepts the session').toBeVisible()
}

/**
 * Set the session cookie in the browser context so subsequent navigations
 * are authenticated. The token is a cookie string like "leapmux-session=<value>".
 * Must be called **before** any page.goto() calls.
 *
 * The NAME comes from the token, not from SESSION_COOKIE_NAME: the caller
 * passes back what a login handed it, whatever it was called.
 */
export async function loginViaToken(page: Page, token: string) {
  const [name, ...rest] = token.split('=')
  // split('=') always yields a first element, so this guard is type-level only.
  if (name === undefined)
    throw new Error(`loginViaToken: token is not a "<name>=<value>" cookie string: ${token}`)
  const value = rest.join('=')
  await page.context().addCookies([{
    name,
    value,
    domain: E2E_BROWSER_HOST,
    path: '/',
    httpOnly: true,
  }])
}

/**
 * Wait for the next layout save event. The promise retains an event that arrives before the caller awaits it.
 *
 * Usage:
 *   const saved = waitForLayoutSave(page)
 *   await doSomethingThatTriggersLayoutSave()
 *   await saved
 */
export function waitForLayoutSave(page: Pick<Page, 'evaluate'>): Promise<void> {
  return page.evaluate(() => {
    return new Promise<void>((resolve, reject) => {
      const timer = setTimeout(() => {
        window.removeEventListener('leapmux:layout-saved', onSaved)
        reject(new Error('layout save timeout'))
      }, 30_000)
      function onSaved(): void {
        clearTimeout(timer)
        window.removeEventListener('leapmux:layout-saved', onSaved)
        resolve()
      }
      window.addEventListener('leapmux:layout-saved', onSaved, { once: true })
    })
  })
}

/** The composer's status bar. Use {@link settingsChips} to read its values. */
export function settingsBar(page: Page) {
  return page.locator('[data-testid="composer-status-bar"]')
}

/**
 * Locate the visible status-bar chip triggers.
 * Assert on these triggers instead of the entire bar. Closed sibling popovers retain every option label in the DOM.
 * A bar-level text assertion could therefore pass for an option that the user did not select.
 */
export function settingsChips(page: Page) {
  return page.locator('[data-testid="composer-status-bar"] [data-testid$="-trigger"]')
}

/** Assert that some status-bar chip displays `text`. */
export async function expectSettingsChip(page: Page, text: string | RegExp) {
  await expect(settingsChips(page).filter({ hasText: text })).not.toHaveCount(0)
}

/** Assert that NO status-bar chip displays `text`. */
export async function expectNoSettingsChip(page: Page, text: string | RegExp) {
  await expect(settingsChips(page).filter({ hasText: text })).toHaveCount(0)
}

/**
 * The option-group id encoded in an option's test id.
 *
 * `OptionGroupMenuItems` emits `<groupId>-<value>` for every option. A group id
 * never contains a hyphen while a value can ("danger-full-access"), so the
 * split is on the FIRST hyphen.
 */
function settingsGroupIdOf(optionTestId: string): string {
  const i = optionTestId.indexOf('-')
  if (i <= 0)
    throw new Error(`settings option test id must be "<groupId>-<value>", got "${optionTestId}"`)
  return optionTestId.slice(0, i)
}

/**
 * Close composer popovers before another menu interaction. An open submenu can cover another group trigger.
 * Use hidePopover because Escape affects only the popover that holds focus, which the caller cannot reliably identify.
 */
export async function closeComposerMenus(page: Page) {
  await page.evaluate(() => {
    for (const el of document.querySelectorAll<HTMLElement>('[popover]')) {
      if (el.matches(':popover-open'))
        el.hidePopover()
    }
  })
  await expect(page.locator('[data-testid="composer-plus-trigger"]')).toHaveAttribute('aria-expanded', 'false')
}

/**
 * Click the trigger unless aria-expanded already reports an open popover. Then confirm aria-expanded.
 * A closing popover stays visible during its animation. A visibility check alone could incorrectly skip the click.
 * Call this idempotent operation inside toPass so an app update that closes the menu permits another attempt.
 */
async function ensureExpanded(trigger: Locator) {
  if (await trigger.getAttribute('aria-expanded') !== 'true')
    await trigger.click()
  await expect(trigger).toHaveAttribute('aria-expanded', 'true')
}

/**
 * Return whether the permission picker offers an option ID.
 * A shortcut appears only when the session offers every value that its preset sets.
 * Read the picker to select the expected shortcut. Close all composer menus before return.
 */
export async function permissionModeOffered(page: Page, modeId: string): Promise<boolean> {
  const group = await openSettingsMenu(page, 'permissionMode')
  const offered = await group.locator(`[data-testid="permissionMode-${modeId}"]`).count() > 0
  await closeComposerMenus(page)
  return offered
}

/** The native settings and Worker identity of the selected agent. */
interface NativeSettingsAgent {
  agent: AgentInfo
  context: Parameters<typeof nativeAgentById>[0]
}

/**
 * Return the hub that serves the page.
 * A spec can run its own hub and Worker, and the session cookie of that hub is not valid on the suite hub.
 */
function pageHubUrl(page: Page): string {
  const url = new URL(page.url())
  if (url.protocol !== 'http:' && url.protocol !== 'https:')
    throw new Error(`The native settings lookup needs a page that a hub serves, not a ${url.protocol} page.`)
  return url.origin
}

/** Resolve only the tab identity through the Hub. Read its actual settings from the owning Worker. */
async function nativeSettingsAgent(page: Page): Promise<NativeSettingsAgent> {
  const hubUrl = pageHubUrl(page)
  // A previous settings RPC can replace the process. Read the Worker catalog after that RPC settles.
  await waitForSettingsIdle(page)
  const agentId = await selectedAgentTabId(page)
  const cookie = await readSessionCookie(page, 'The native settings lookup')
  const located = await callHub<JsonValue>(hubUrl, 'WorkspaceService/LocateTab', { tabId: agentId, tabType: TabType.AGENT }, {
    cookie,
    operation: 'The native settings tab lookup',
  })
  const { tab } = fromJson(LocateTabResponseSchema, located)
  if (!tab || tab.tabId !== agentId || tab.tabType !== TabType.AGENT || !tab.workerId)
    throw new Error('The native settings tab lookup returned no matching Worker identity.')
  const context = { leapmuxServer: { hubUrl, adminToken: cookie, workerId: tab.workerId } }
  let agent = await nativeAgentById(context, agentId)
  if (agent?.status === AgentStatus.STARTING) {
    await expect.poll(async () => {
      agent = await nativeAgentById(context, agentId)
      return agent !== null && agent.status !== AgentStatus.STARTING
    }).toBe(true)
  }
  if (agent?.status === AgentStatus.STARTUP_FAILED)
    throw new Error(`The native settings agent failed to start: ${agent.startupError || 'The Worker supplied no startup error.'}`)
  if (!agent || agent.status !== AgentStatus.ACTIVE)
    throw notActiveSettingsAgentError(agent)
  return { agent, context }
}

/**
 * The error for a settings agent that the owning Worker does not report as ACTIVE.
 * It states the status that the Worker reported, or that it listed no row, because a
 * reader needs that fact to tell a restart, a stop, and a missing tab apart.
 */
function notActiveSettingsAgentError(agent: AgentInfo | null | undefined): Error {
  const reported = agent ? (AgentStatus[agent.status] ?? String(agent.status)) : 'no row'
  return new Error(`The native settings agent is not active on its owning Worker (${reported}).`)
}

/** Reject an unavailable native group or value before any menu retry begins. */
async function validateNativeSettingsOption(page: Page, testId: string): Promise<void> {
  const groupId = settingsGroupIdOf(testId)
  const value = testId.slice(groupId.length + 1)
  const { agent } = await nativeSettingsAgent(page)
  const group = nativeOptionGroup(agent, groupId)
  if (!group)
    throw new Error(`The native catalog has no option group ${groupId}.`)
  if (!group.options.some(option => option.id === value))
    throw new Error(`The native catalog has no value ${value} for option group ${groupId}.`)
  if (!group.mutable)
    throw new Error(`The native option group ${groupId} is read-only.`)
}

/** Apply an available preset, or verify its active values, through the actual Worker. */
export async function applyPermissionPreset(page: Page, kind: 'smart' | 'bypass') {
  const snapshot = await nativeSettingsAgent(page)
  const preset = permissionPresetsFor(snapshot.agent.agentProvider)?.[kind]
  if (!permissionPresetAvailable(preset, snapshot.agent.optionGroups))
    throw new Error(`The actual native catalog offers no mutable ${kind} permission preset.`)
  const entries = Object.entries(preset.sets)
  const active = entries.every(([groupId, value]) => nativeOptionValue(snapshot.agent, groupId) === value)
  const offered = await openPermissionShortcut(page, kind)
  if (active) {
    await expect(offered).toBeDisabled()
    for (const [groupId, value] of entries) {
      const groupMenu = await openSettingsMenu(page, groupId)
      const selected = groupMenu.getByTestId(`${groupId}-${value}`)
      await expect(selected).toBeEnabled()
      const selectedAttribute = (await selected.getAttribute('role')) === 'option' ? 'aria-selected' : 'aria-checked'
      await expect(selected).toHaveAttribute(selectedAttribute, 'true')
    }
    await closeComposerMenus(page)
    return
  }
  await expect(offered).toBeEnabled()
  await offered.click()
  await waitForSettingsIdle(page)
  await expect.poll(async () => {
    const current = await nativeAgentById(snapshot.context, snapshot.agent.id)
    return current !== null && entries.every(([groupId, value]) => nativeOptionValue(current, groupId) === value)
  }).toBe(true)
}

/**
 * Open the composer's `[+]` menu with the permission shortcut for `kind`, and return that shortcut.
 * An open menu keeps its row list, and a shortcut that arrives later enters the next row list.
 * When the shortcut is absent, open and close one probe menu so that the next open shows it.
 * This function reads no native settings, so a spec can drive it on a static page.
 */
export async function openPermissionShortcut(page: Page, kind: 'smart' | 'bypass'): Promise<Locator> {
  const testId = `composer-${kind}-permissions`
  const action = page.locator('[data-testid="composer-plus-popover"]').getByTestId(testId)
  await closeComposerMenus(page)
  if (await action.count() === 0) {
    await openPlusMenu(page)
    await closeComposerMenus(page)
  }
  await expect(action).toHaveCount(1)
  const menu = await openPlusMenu(page)
  const offered = menu.getByTestId(testId)
  await expect(offered).toBeVisible()
  return offered
}

/**
 * How the `[+]` menu offers a permission shortcut:
 * - `offered`: the shortcut is there and enabled.
 * - `disabled`: the shortcut is there and disabled, as it is while its preset is active.
 * - `absent`: the menu has no such shortcut.
 */
export type PermissionShortcutState = 'offered' | 'disabled' | 'absent'

/**
 * Require the stated state of each permission shortcut in the composer's `[+]` menu, then close the menus.
 * An open menu keeps its row list, and a shortcut that arrives later enters the next row list. So the function opens
 * and closes one probe menu first, as `openPermissionShortcut` does, and checks the rows of the next menu.
 */
export async function expectPermissionShortcuts(
  page: Page,
  expected: Partial<Record<'smart' | 'bypass', PermissionShortcutState>>,
): Promise<void> {
  const kinds = (['smart', 'bypass'] as const).filter(kind => expected[kind] !== undefined)
  if (kinds.length === 0)
    throw new Error('The permission shortcut check needs the state of at least one shortcut.')
  await closeComposerMenus(page)
  await openPlusMenu(page)
  await closeComposerMenus(page)
  const menu = await openPlusMenu(page)
  for (const kind of kinds) {
    const shortcut = menu.getByTestId(`composer-${kind}-permissions`)
    const state = expected[kind]
    if (state === 'absent') {
      await expect(shortcut, `the menu has no ${kind} permission shortcut`).toHaveCount(0)
      continue
    }
    await expect(shortcut, `the menu offers the ${kind} permission shortcut`).toBeVisible()
    if (state === 'disabled')
      await expect(shortcut, `the ${kind} permission shortcut is disabled`).toBeDisabled()
    else
      await expect(shortcut, `the ${kind} permission shortcut is enabled`).toBeEnabled()
  }
  await closeComposerMenus(page)
}

/** Open the composer's `[+]` menu and leave it open. */
export async function openPlusMenu(page: Page): Promise<Locator> {
  const plus = page.locator('[data-testid="composer-plus-trigger"]')
  await expect(plus).toBeVisible()
  await closeComposerMenus(page)
  await expect(async () => {
    await ensureExpanded(plus)
  }).toPass()
  return page.locator('[data-testid="composer-plus-popover"]')
}

/**
 * Locate an option-group trigger in the open plus menu.
 * The trigger exists only when the agent offers the group. Its absence indicates that the option group does not apply.
 */
export function settingsGroupTrigger(page: Page, groupId: string): Locator {
  return page.locator(`[data-testid="composer-group-${groupId}"]`)
}

/**
 * Open an option-group submenu through the composer plus menu and leave it open.
 * The plus menu includes every group at all widths. Status-bar chips expose only some groups and can be hidden.
 * Open both menus within the same toPass attempt because a settings response can close the first before the second opens.
 * Close existing menus inside each attempt. Otherwise, a menu with outdated content could remain open through every retry.
 */
export async function openSettingsMenu(page: Page, groupId: string): Promise<Locator> {
  const plus = page.locator('[data-testid="composer-plus-trigger"]')
  const submenu = settingsGroupTrigger(page, groupId)
  await expect(plus).toBeVisible()
  await expect(async () => {
    // Start closed: a submenu left open from a previous group covers this one's
    // trigger, and the click below would be intercepted.
    await closeComposerMenus(page)
    await ensureExpanded(plus)
    await ensureExpanded(submenu)
  }).toPass()
  // Look up every option inside this popover.
  // The status-bar chip uses the same group and per-option test IDs. A page-rooted query matches two elements and fails strict mode.
  return page.locator(`[data-testid="composer-group-${groupId}-popover"]`)
}

/**
 * Open the group before clicking `testId`. Treat open and click as one attempt.
 * A settings reply between those steps can rerender and close the dropdown. A click retry alone cannot repeat the completed open step.
 * Repeat the open and click operations until the click completes.
 */
export async function chooseSettingsOption(page: Page, testId: string) {
  await validateNativeSettingsOption(page, testId)
  await expect(async () => {
    const menu = await openSettingsMenu(page, settingsGroupIdOf(testId))
    await menu.getByTestId(testId).click()
  }).toPass()
}

/**
 * Return the values that the settings menu offers for one option group, in menu order.
 * The menu draws up to seven options as radio items and a longer list as a filterable list box. Both carry the test
 * ID `<groupId>-<value>`. The function reads the option rows by their role, so a row's label element and the filter
 * box, which carry test IDs with the same prefix, stay out of the list. It closes the menus before it returns.
 */
export async function offeredSettingsOptions(page: Page, groupId: string): Promise<string[]> {
  const menu = await openSettingsMenu(page, groupId)
  const testIds = await menu.locator('[role="menuitemradio"], [role="option"]')
    .evaluateAll(rows => rows.map(row => row.getAttribute('data-testid') ?? ''))
  await closeComposerMenus(page)
  const prefix = `${groupId}-`
  return testIds.map((testId) => {
    if (!testId.startsWith(prefix) || testId.length === prefix.length)
      throw new Error(`The ${groupId} settings menu holds an option whose test ID is not "${prefix}<value>": "${testId}".`)
    return testId.slice(prefix.length)
  })
}

/**
 * Require `testId` as the selected option in the settings menu.
 * The status bar shows chips for these groups:
 * - The model.
 * - The plugin's `configuration.effortGroupKey`, which defaults to `effort`.
 * - The mode.
 * Other axes show their choices only in the menu.
 */
export async function expectSettingsOptionChosen(page: Page, testId: string) {
  await expect(async () => {
    const menu = await openSettingsMenu(page, settingsGroupIdOf(testId))
    const option = menu.locator(`[data-testid="${testId}"]`)
    const selectedAttribute = (await option.getAttribute('role')) === 'option' ? 'aria-selected' : 'aria-checked'
    await expect(option).toHaveAttribute(selectedAttribute, 'true')
  }).toPass()
  await closeComposerMenus(page)
}

/**
 * Locate a directory-tree row by its displayed name and row test ID.
 * A Tooltip duplicates truncated text, so a text-only locator can match twice.
 * Apply :visible before first() because the sidebar has another mounted copy. The other copy can intercept or reject pointer actions.
 * The name matches as a substring by default. Pass `exact` where a longer name can contain it, such as `src` in
 * `src-tauri`: the exact form compares the row's label, not the row's text, which also holds its menu items.
 */
export function treeRow(page: Page, name: string, options: { exact?: boolean } = {}): Locator {
  const rows = page.locator(`[data-testid="tree-row"]${VISIBLE}`)
  const matching = options.exact
    ? rows.filter({ has: page.getByTestId('tree-row-name').getByText(name, { exact: true }) })
    : rows.filter({ hasText: name })
  return matching.first()
}

/**
 * Read every visible tree row's label in display order to check sorting.
 * The row text also contains its mounted three-dot menu items, even when that menu is closed. Read the label alone.
 */
export function treeRowNames(page: Page): Locator {
  return page.locator(`[data-testid="tree-row"]${VISIBLE} [data-testid="tree-row-name"]`)
}

/**
 * Wait until the draft reaches durable browser storage before reload.
 * The caller lacks the agent ID, so inspect the draft rows of its account.
 */
export async function waitForEditorDraft(page: Page, userId: string, text: string) {
  await waitForStoredEntry(
    page,
    accountStorageKey(userId, PREFIX_EDITOR_DRAFT),
    value => isObject(value) && typeof value.content === 'string' && value.content.includes(text),
    `the editor draft "${text}" must be persisted before the reload`,
  )
}

/**
 * Wait until the Files sort preference reaches durable browser storage.
 * The pagehide flush also awaits a connection, so it does not complete synchronously.
 * Match the worker ID and the stored value, not the directory: the worker can canonicalize the directory path, such
 * as /var to /private/var on macOS. One agent per test makes this lookup unambiguous.
 */
export async function waitForFilesSortOrder(
  page: Page,
  userId: string,
  workerId: string,
  expected: FileSortOrder,
) {
  await waitForStoredEntry(
    page,
    accountStorageKey(userId, `${PREFIX_FILES_SORT_ORDER}${workerId}:`),
    value => isObject(value) && value.key === expected.key && value.direction === expected.direction,
    `the sort order ${expected.key}/${expected.direction} must be persisted before the reload`,
  )
}

/**
 * Rename a tab through its inline editor and wait for the label.
 * A rename writes a terminal title to storage. Terminal creation supplies the initial Terminal <Name> value.
 * Terminal escape-sequence titles are live overlays and do not persist. See SignalTitle in the worker terminal.go file.
 */
export async function renameTabViaUI(page: Page, tab: Locator, newTitle: string) {
  await tab.dblclick()
  const input = page.locator(`[data-testid="tab-rename-input"]${VISIBLE}`)
  await expect(input).toBeVisible()
  await input.fill(newTitle)
  await input.press('Enter')
  await expect(tab).toContainText(newTitle)
}

/**
 * Open a sidebar row menu and wait for the requested item. Retry these steps together:
 * - Hover the row.
 * - Click its trigger.
 * - Find the requested item.
 * Git refresh and turn completion can replace the row during this sequence. Workspace, Worker, and to-do updates can also replace sidebar rows.
 * An open-menu check cannot guarantee that the requested item remains. File-tree and branch-group menus supply their own trigger locators.
 */
export async function openRowMenu(row: Locator | null, trigger: Locator, item: Locator) {
  await expect(async () => {
    if (!await item.isVisible()) {
      // A row menu's trigger is `opacity: 0` until its row is hovered. A
      // SECTION HEADER's trigger is always painted, so that caller passes
      // `null` rather than a row to hover.
      await row?.hover()
      await trigger.click()
    }
    await expect(item).toBeVisible()
  }).toPass()
}

/**
 * Open a row menu and click an item as one attempt.
 * The menu can close between open and click. Retry the whole operation until the item click completes.
 */
export async function clickRowMenuItem(row: Locator | null, trigger: Locator, item: Locator) {
  await expect(async () => {
    await openRowMenu(row, trigger, item)
    await item.click()
  }).toPass()
}

/** The three-dot trigger inside a file-tree row. */
function treeMenuTrigger(row: Locator): Locator {
  return row.locator('[data-testid="tree-context-button"]')
}

/**
 * Open a tree row's context menu, with `requiredItem` on screen.
 *
 * `requiredItem` defaults to the one entry every variant of the menu carries,
 * for callers that only need it open.
 */
export async function openTreeContextMenu(row: Locator, requiredItem = 'tree-copy-path-button') {
  await openRowMenu(row, treeMenuTrigger(row), treeMenuItem(row, requiredItem))
}

/** Open a tree row's context menu and click one of its items. */
export async function clickTreeContextItem(row: Locator, itemTestId: string) {
  await clickRowMenuItem(row, treeMenuTrigger(row), treeMenuItem(row, itemTestId))
}

/**
 * One item of a tree row's context menu.
 *
 * The menu lies inside its row, so a lookup below the row never matches the
 * menu of another row. A page-wide lookup can: a closed popover stays laid out
 * while it fades out, and Playwright counts it as visible at any opacity.
 */
export function treeMenuItem(row: Locator, testId: string): Locator {
  return row.locator(`[data-testid="${testId}"]:visible`)
}

/**
 * Locate the first visible branch-group row below `root`.
 * The sidebar has two mounted copies. An unfiltered first() can select a covered copy that rejects pointer actions.
 * Pass `workspaceChildren(page, id)` as the root when another workspace can also stay expanded, or when the row must
 * belong to one workspace. A page-level lookup selects the first expanded workspace in the whole sidebar.
 * Use aria-expanded to identify the menu trigger. A last-button lookup can instead select a hidden menu item inside the popover.
 */
export function branchGroupRow(root: Page | Locator): Locator {
  return root.locator(`[data-testid="tab-tree-branch-group"]${VISIBLE}`).first()
}

function branchMenuTrigger(row: Locator): Locator {
  return row.locator('[aria-expanded]').first()
}

/**
 * Locate the first visible repository-group header below root.
 * Visibility filtering excludes the covered sidebar copy.
 * Supply the workspace children as root when another workspace can also remain expanded. A page-level lookup selects the first workspace.
 * Branch rows are siblings of the header, so a lookup below this row reaches only the repository menu.
 */
export function repoGroupRow(root: Page | Locator): Locator {
  return root.locator(`[data-testid="tab-tree-repo-group"]${VISIBLE}`).first()
}

/** The repository row's three-dot trigger. Only the trigger carries `aria-expanded`. */
function repoMenuTrigger(row: Locator): Locator {
  return row.locator('[data-testid="repo-row-menu-trigger"]')
}

/**
 * Open the repository group's three-dot menu and require `requiredItem` inside that row.
 * `DropdownMenu` mounts its children eagerly. Each other repository row can contain a hidden copy of the same item.
 * A page-rooted role query matches several copies. Scope the query to this row.
 */
export async function openRepoMenu(row: Locator, requiredItem = 'Collapse all branches') {
  await openRowMenu(row, repoMenuTrigger(row), repoMenuItem(row, requiredItem))
}

/** Open a repository group's three-dot menu and click one of its items. */
export async function clickRepoMenuItem(row: Locator, itemName: string) {
  await clickRowMenuItem(row, repoMenuTrigger(row), repoMenuItem(row, itemName))
}

/**
 * One item of a repository row's menu, by its exact name.
 *
 * `exact`, because Playwright matches an accessible name by substring
 * otherwise -- and this menu carries `Copy repository URL` beside
 * `Copy repository path`, so a loose `Copy repository` matches both.
 */
export function repoMenuItem(row: Locator, name: string): Locator {
  return row.getByRole('menuitem', { name, exact: true })
}

/**
 * The three-dot trigger of a workspace row, by its test ID.
 * A lookup by position, such as the first button of the row, finds another control when a button is added before it.
 */
function workspaceMenuTrigger(row: Locator): Locator {
  return row.getByTestId('workspace-row-menu-trigger')
}

/**
 * One item of a workspace row's menu, by its exact name.
 * The menu lies inside its row, so the lookup never matches the menu of another row. `exact`, because the menu's
 * info block joins every info row into one accessible name, so a loose `Delete` also matches that block.
 */
export function workspaceMenuItem(page: Page, workspaceId: string, name: string): Locator {
  return workspaceRow(page, workspaceId).getByRole('menuitem', { name, exact: true })
}

/**
 * Open the three-dot menu of a workspace row, with `requiredItem` on screen. The open is retried, because the
 * sidebar replaces a row on each workspace, Worker, or to-do update.
 * An archived workspace has no Rename item, so pass an item that its menu holds, such as `Unarchive`.
 */
export async function openWorkspaceRowMenu(page: Page, workspaceId: string, requiredItem = 'Rename'): Promise<void> {
  const row = workspaceRow(page, workspaceId)
  await openRowMenu(row, workspaceMenuTrigger(row), workspaceMenuItem(page, workspaceId, requiredItem))
}

/** Open a workspace row's menu and click one of its items, as one retried attempt. */
export async function clickWorkspaceMenuItem(page: Page, workspaceId: string, name: string): Promise<void> {
  const row = workspaceRow(page, workspaceId)
  await clickRowMenuItem(row, workspaceMenuTrigger(row), workspaceMenuItem(page, workspaceId, name))
}

/**
 * Archive a workspace through its row menu and the confirmation dialog, and wait for the Archived section.
 * For a test that archives as a precondition. A test of the dialog itself drives each step and reads the dialog text.
 */
export async function archiveWorkspaceViaUI(page: Page, workspaceId: string): Promise<void> {
  await clickWorkspaceMenuItem(page, workspaceId, 'Archive')
  const dialog = page.getByRole('dialog', { name: 'Archive workspace' })
  await dialog.getByRole('button', { name: 'Archive', exact: true }).click()
  await expect(dialog).toBeHidden()
  await expect(sidebarSectionHeader(page, 'workspaces_archived')).toBeVisible()
}

/**
 * Delete a workspace through its row menu and both steps of the confirm button, and wait until its row is gone.
 * The first click arms the button, and its name changes to `Confirm?`. The second click deletes.
 */
export async function deleteWorkspaceViaUI(page: Page, workspaceId: string): Promise<void> {
  await clickWorkspaceMenuItem(page, workspaceId, 'Delete')
  const dialog = page.getByRole('dialog', { name: 'Delete workspace' })
  await dialog.getByRole('button', { name: 'Delete', exact: true }).click()
  await dialog.getByRole('button', { name: 'Confirm?' }).click()
  await expect(workspaceRow(page, workspaceId)).toBeHidden()
}

/**
 * Wait for no visible `menu[popover]` before reopening the branch menu after a dialog closes.
 * The old menu stays open behind the modal dialog. `openRowMenu` can see its item and skip the trigger.
 * The item then disappears as the dialog closes. Waiting for zero visible menus prevents that stale click.
 */
export async function openBranchMenu(page: Page, row: Locator, requiredItem = 'Switch to branch...') {
  await openRowMenu(row, branchMenuTrigger(row), page.getByRole('menuitem', { name: requiredItem }))
}

/** Open a branch group's three-dot menu and click one of its items. */
export async function clickBranchMenuItem(page: Page, row: Locator, itemName: string) {
  await clickRowMenuItem(row, branchMenuTrigger(row), page.getByRole('menuitem', { name: itemName }))
}

/**
 * Wait for the pending settings indicator to clear.
 * The always-present composer `[+]` trigger carries the marker. A preference can hide the status bar.
 * Keeping the marker on the trigger preserves feedback when the status bar is hidden.
 */
export async function waitForSettingsIdle(page: Page) {
  await expect(page.locator('[data-testid="settings-loading-spinner"]')).not.toBeVisible()
}

/**
 * Wait for an option catalog through a submenu that the provider offers.
 * A preference can hide the status-bar chips. The plus menu stays available.
 * A submenu exists only when the agent supplies a group with at least one option.
 * The default `model` group is correct only for a provider whose running agent offers a model group.
 * A shared helper that serves every provider must call `waitForNativeSettingsHydrated` instead.
 */
export async function waitForSettingsHydrated(page: Page, groupId = 'model') {
  await waitForSettingsGroupsOffered(page, async () => [groupId])
}

/**
 * Wait until the plus menu offers each option group of the live Worker catalog of the active agent.
 * A fixed group ID is not correct for every provider:
 * - A running Fast Agent or Amp agent has no model group, so the default wait of `waitForSettingsHydrated` never ends.
 * - The Worker adds a read-only model group to the catalog of an agent that does not run.
 *   A wait for the model group can thus end before the live catalog arrives.
 * Each attempt reads the catalog again, because a provider can change its groups after its start.
 */
export async function waitForNativeSettingsHydrated(page: Page): Promise<void> {
  const { agent, context } = await nativeSettingsAgent(page)
  await waitForSettingsGroupsOffered(page, async () => {
    const current = await nativeAgentById(context, agent.id)
    if (current?.status !== AgentStatus.ACTIVE)
      throw notActiveSettingsAgentError(current)
    const offered = current.optionGroups.filter(hasOptions).map(group => group.id)
    if (offered.length === 0)
      throw new Error('The live native catalog has no group with an option, so the settings menu cannot show that catalog.')
    return offered
  })
}

/**
 * Wait until the plus menu offers each group that `expectedGroupIds` returns.
 * Close and reopen the menu on each attempt so a previous empty menu cannot hide newly supplied options.
 * So each attempt reads each group once, without waiting. A waiting assertion there would hold an early,
 * empty menu for the whole expect timeout before the next attempt could reopen it.
 */
async function waitForSettingsGroupsOffered(page: Page, expectedGroupIds: () => Promise<readonly string[]>): Promise<void> {
  const plus = page.locator('[data-testid="composer-plus-trigger"]')
  await expect(plus).toBeVisible()
  await expect(async () => {
    const groupIds = await expectedGroupIds()
    await closeComposerMenus(page)
    await ensureExpanded(plus)
    await expect(page.locator('[data-testid="composer-plus-popover"]')).toBeVisible()
    for (const groupId of groupIds)
      expect(await settingsGroupTrigger(page, groupId).isVisible(), `the menu offers the ${groupId} group`).toBe(true)
  }).toPass()
  await closeComposerMenus(page)
}

/**
 * Wait for a tab or the empty-tile actions or hint to prove that the workspace page is ready.
 * Use `.first()` for each locator because several tabs can be visible.
 * Without that restriction, strict mode throws. `isMaybeVisible` catches the error and returns false, which hides the actual ready state.
 */
export async function waitForWorkspaceReady(page: Page) {
  await expect.poll(async () => {
    if (await isMaybeVisible(page.locator('[data-testid="tab"]').first()))
      return true
    // Mobile has no tab strip. A workspace with tabs also hides the empty-tile placeholder.
    // Its current-tab chip proves that the workspace shell rendered the tabs.
    if (await isMaybeVisible(page.locator('[data-testid="tab-chip"]').first()))
      return true
    if (await isMaybeVisible(page.locator('[data-testid="empty-tile-actions"]').first()))
      return true
    return isMaybeVisible(page.locator('[data-testid="empty-tile-hint"]').first())
  }).toBe(true)
}

/**
 * Locate the visible sidebar row for workspaceId.
 * Desktop and mobile sidebars mount separate copies of the same workspace ID.
 * Filter visibility before resolving the locator, or Playwright can reject both matches before its wait begins.
 * Use first() because both copies can briefly remain visible during a layout transition.
 * Either visible copy identifies the same workspace.
 */
export function workspaceRow(page: Page, workspaceId: string): Locator {
  return page.locator(`[data-testid="workspace-item-${workspaceId}"]${VISIBLE}`).first()
}

/**
 * Locate the workspace chevron through its test ID and visible workspace row.
 * The grip and chevron both contain SVGs, so a first-SVG lookup is ambiguous.
 * An independent lookup can select the hidden collapsed sidebar copy and wait for a click that cannot succeed.
 */
export function workspaceChevron(page: Page, workspaceId: string): Locator {
  return workspaceRow(page, workspaceId)
    .locator(`[data-testid="workspace-chevron-${workspaceId}"]`)
}

/**
 * Locate the visible active workspace row.
 * The app keeps the active workspace in browser storage, not in the URL, so this row is the one place that shows it.
 */
export function activeWorkspaceRow(page: Page): Locator {
  return page.locator(`[data-testid^="workspace-item-"][data-active="true"]${VISIBLE}`).first()
}

/** The ID of the active workspace, read from its visible sidebar row. The read waits for the row. */
export async function activeWorkspaceId(page: Page): Promise<string> {
  const row = activeWorkspaceRow(page)
  await expect(row, 'the sidebar shows an active workspace').toBeVisible()
  const testId = await row.getAttribute('data-testid')
  const prefix = 'workspace-item-'
  if (!testId?.startsWith(prefix) || testId.length === prefix.length)
    throw new Error(`The active workspace row has an unexpected test ID: ${testId}.`)
  return testId.slice(prefix.length)
}

/**
 * Locate the children wrapper of the visible workspace row: the subtree that holds its branch, repository, and tab rows.
 * The wrapper follows the row as its next sibling. The lookup starts at the visible row, because the other mounted
 * sidebar copy holds a wrapper with the same test ID, and that copy can be hidden or unhydrated.
 * The test ID check makes a change of that structure fail here, not as a missing row later.
 */
export function workspaceChildren(page: Page, workspaceId: string): Locator {
  return workspaceRow(page, workspaceId)
    .locator(`xpath=following-sibling::*[1][@data-testid="workspace-children-${workspaceId}"]`)
}

/**
 * Locate tab leaves below the visible workspace row.
 * The other mounted sidebar copy can retain unhydrated labels, so a page query can read a permanent generic Agent label.
 * A collapsed workspace keeps its leaves in the DOM with `visibility: hidden`, so add `:visible` to count what a user sees.
 */
export function sidebarLeaves(page: Page, workspaceId: string): Locator {
  return workspaceChildren(page, workspaceId).locator('[data-testid="tab-tree-leaf"]')
}

/**
 * Rendered titles of `workspaceId`'s sidebar leaves, with the close icon /
 * badges stripped so only the label text remains.
 */
export async function sidebarLeafLabels(page: Page, workspaceId: string): Promise<string[]> {
  return sidebarLeaves(page, workspaceId).evaluateAll(leaves =>
    leaves.map((leaf) => {
      const clone = leaf.cloneNode(true) as HTMLElement
      clone.querySelectorAll('button, svg').forEach(n => n.remove())
      return (clone.textContent ?? '').trim()
    }),
  )
}

/**
 * Tab ids of `workspaceId`'s sidebar leaves.
 *
 * Ids, not rendered titles: a title is Worker-sourced metadata, so with the
 * Worker offline every row falls back to the generic "Agent" label. That
 * fallback is correct behaviour and says nothing about where the tab lives.
 */
export async function sidebarLeafIds(page: Page, workspaceId: string): Promise<string[]> {
  return sidebarLeaves(page, workspaceId)
    .evaluateAll(leaves => leaves.map(leaf => leaf.getAttribute('data-tab-id') ?? ''))
}

/**
 * Load the app, select workspaceId through its sidebar row, and wait for its shell.
 * The app uses / for every workspace. resolveActiveWorkspace reads the saved selection from browser storage.
 * A click uses the same selection path as the user. Skip it when the requested workspace is already active.
 *
 * A cold load can first activate another saved workspace. That activation expands its sidebar row and hydrates its tabs.
 * Tests of expansion state must establish their starting state explicitly. See the expanded-state test in 017.
 */
export async function openWorkspace(page: Page, workspaceId: string) {
  await page.goto('/')
  const row = workspaceRow(page, workspaceId)
  await row.waitFor()
  if (await row.getAttribute('data-active') !== 'true') {
    // Open the mobile sidebar drawer before clicking a row. Sidebar drawers start closed. Selecting a workspace closes them again.
    // `isVisible` can pass for off-screen drawer content, so a row wait cannot prove that the drawer is open.
    const toggle = page.getByRole('button', { name: 'Toggle workspaces' })
    if (await toggle.isVisible().catch(() => false))
      await toggle.click()
    await row.click()
  }
  await expect(row).toHaveAttribute('data-active', 'true')
  await waitForWorkspaceReady(page)
}

/**
 * Reload / and verify that the app restores workspaceId from saved state.
 * Do not click the sidebar row. That click would hide a broken restore.
 * Use openWorkspace only when explicit selection is intended.
 */
export async function reopenWorkspace(page: Page, workspaceId: string) {
  await page.goto('/')
  await expect(workspaceRow(page, workspaceId)).toHaveAttribute('data-active', 'true')
  await waitForWorkspaceReady(page)
}

/**
 * Authenticate as `token`, open `workspaceId`, and wait for the workspace shell
 * to be ready (first tile rendered). Shared across the multi-context
 * CRDT-convergence specs (150/151/152/153) that all need the same setup before
 * driving layout mutations.
 */
export async function gotoWorkspace(page: Page, token: string, workspaceId: string) {
  await loginViaToken(page, token)
  await openWorkspace(page, workspaceId)
  // Wait for the first tile to mount before the caller changes the layout.
  await tiles(page).first().waitFor()
}

/** Locate a tab by its hub-side `tab_id`. */
export function tabById(page: Page, tabId: string): Locator {
  return page.locator(`[data-testid="tab"][data-tab-id="${tabId}"]`)
}

/** Locate every agent tab of every tile's tab bar. */
export function agentTabs(page: Page): Locator {
  return page.locator('[data-testid="tab"][data-tab-type="agent"]')
}

/** Locate every terminal tab of every tile's tab bar. */
export function terminalTabs(page: Page): Locator {
  return page.locator('[data-testid="tab"][data-tab-type="terminal"]')
}

/** Locate every tile of the workspace layout. */
export function tiles(page: Page): Locator {
  return page.locator('[data-testid="tile"]')
}

/**
 * Require `count` agent tabs in the tab bars.
 * This is an assertion about the view, not a readiness wait. The tab list is optimistic state, so a tab can show
 * before its agent starts and can leave before its agent stops. Wait on the Worker for an agent's state.
 */
export async function expectAgentTabCount(page: Page, count: number): Promise<void> {
  if (!Number.isSafeInteger(count) || count < 0)
    throw new Error(`An agent tab count must be a nonnegative integer, not ${count}.`)
  await expect(agentTabs(page)).toHaveCount(count)
}

/**
 * Wait for `locator` to become visible before reading its rectangle.
 * `boundingBox()` can return null before layout. A count assertion does not guarantee layout.
 * Drag tests need a real rectangle for pointer coordinates. Waiting prevents the unrelated "Could not get bounding boxes" failure.
 */
export async function boxOf(locator: Locator): Promise<{ x: number, y: number, width: number, height: number }> {
  // Return the geometry captured inside the poll.
  // A separate read could follow a workspace switch that replaces the tile and tab strip.
  // expect.poll retries through the global assertion timeout.
  let box: { x: number, y: number, width: number, height: number } | null = null
  await expect.poll(async () => {
    box = await locator.boundingBox()
    return box !== null
  }).toBe(true)
  if (!box)
    throw new Error(`No bounding box for ${locator}`)
  return box
}

/**
 * Pick an option from a `DropdownMenu`.
 *
 * The app renders no native `<select>` any more (see the dropdown rule in
 * AGENTS.md), so `selectOption` has nothing to drive. A menu keeps its items
 * mounted, so this helper scopes the click to the row that owns them.
 */
// `~/components/common/LoadingMenu` writes these test IDs. `src/test-support/menu.ts` holds the equivalent Vitest query templates.
// DOM queries and Playwright locators use different APIs, so they cannot share query code. Apply test-ID changes in all three files.
export async function pickMenuOption(scope: Locator, base: string, value: string): Promise<void> {
  await openMenu(scope, base)
  await scope.getByTestId(base).getByTestId(`loading-menu-option-${value}`).first().click()
}

/**
 * Open `LoadingMenu` only when it is closed.
 * A caller can then read its options and select one without closing the menu between those operations.
 * An unconditional toggle would hide the item from the accessibility tree before the click.
 */
export async function openMenu(scope: Locator, base: string): Promise<void> {
  const trigger = scope.getByTestId(`${base}-trigger`)
  if (await trigger.getAttribute('aria-expanded') !== 'true')
    await trigger.click()
}

/**
 * Resolve `value` through the browser's color parser.
 * `getPropertyValue('--code-block-background')` returns the specified token. A derived token can retain its `color-mix()` expression when the theme changes.
 * Assign that token to a probe element. Read its computed `background-color`.
 * The normalized `rgb(...)` result can be compared with the computed color of a real element.
 */
export async function resolvedColor(page: Page, value: string): Promise<string> {
  return page.evaluate((css) => {
    const probe = document.createElement('div')
    probe.style.backgroundColor = css
    document.body.append(probe)
    const painted = getComputedStyle(probe).backgroundColor
    probe.remove()
    return painted
  }, value)
}

/** The theme chooser's palette menu, whose options are keyed by theme id. */
export async function pickTheme(scope: Locator, themeId: string): Promise<void> {
  await scope.getByTestId('theme-chooser-name').click()
  await scope.getByTestId(`theme-option-${themeId}`).click()
}

/**
 * Open `LoadingMenu` before reading its option labels. A closed popover is absent from the accessibility tree.
 * A role query cannot distinguish that closed menu from an empty list, so an assertion can wait until the test deadline.
 * The previous native `<select>` exposed its `<option>` values while closed.
 */
export async function menuOptionTexts(scope: Locator, base: string): Promise<string[]> {
  await openMenu(scope, base)
  return scope.getByTestId(base).getByRole('menuitemradio').allTextContents()
}

/**
 * Read the option row's label. Its complete text also includes detail, such as a session age.
 * The age can change between read and comparison. `DropdownMenuCheckableItem` builds the label test ID from the row ID.
 * `src/test-support/menu.ts` uses the same suffix for Vitest.
 */
export function menuOptionLabel(row: Locator): Locator {
  return row.locator('[data-testid$="-label"]')
}

/** One element's box, and the four numbers a caller compares. */
export interface ElementBox {
  x: number
  y: number
  width: number
  height: number
}

/**
 * Read the element rectangle after movement stops.
 * An anchored popover moves after each underlying layout change. A long list grows across several frames and repositions the popover each time.
 * A sample during movement measures that shift. A click can hit the popover instead of the underlying trigger, which causes failures under load.
 * Two consecutive identical reads prove that movement stopped. `expect.poll` caps the wait at the project timeout.
 */
export async function stableBox(element: Locator): Promise<ElementBox> {
  let previous: string | null = null
  let box: ElementBox | null = null
  await expect.poll(async () => {
    box = await element.boundingBox()
    if (!box)
      return false
    const key = `${box.x},${box.y},${box.width},${box.height}`
    const settled = key === previous
    previous = key
    return settled
  }).toBe(true)
  if (!box)
    throw new Error('the element never reported a bounding box')
  return box
}

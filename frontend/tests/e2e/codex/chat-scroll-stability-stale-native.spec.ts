import type { Page } from '@playwright/test'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { MESSAGE_PAGE_LIMIT } from '../../../src/generated/contracts/chat-history'
import { codexTest } from '../codex-fixtures'
import { clearStoredChatHeightsAndRenders, expectAtBottom, startRowHeightRecordIn } from '../helpers/chatScrollStability'
import { fillerTurn, fillRows, spanRowSequences } from '../helpers/historyFill'
import { readAllAgentMessages } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { editToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, chatScrollContainer } from '../helpers/ui'

/** Deliver two old-coordinate momentum samples after the app rebases over a cold page. */
async function scrollThroughDelayedMomentum(page: Page, diffSeqs: string[]) {
  const handle = await page.evaluateHandle(startRowHeightRecordIn, { premeasureSeqs: diffSeqs })
  try {
    return await chatScrollContainer(page).evaluate(async (container, { recorder, diffSeqs }) => {
      const { get, set } = Object.getOwnPropertyDescriptor(Element.prototype, 'scrollTop')!
      const top = (): number => get!.call(container) as number
      const viewportHeight = container.clientHeight
      const measuredDiffs = recorder.heights
      const recentMoves: { target: number, actual: number, afterEvent: number }[] = []
      const deliverScroll = (rawTop: number) => {
        set!.call(container, rawTop)
        const actual = top()
        container.dispatchEvent(new Event('scroll'))
        if (recentMoves.length === 8)
          recentMoves.shift()
        recentMoves.push({
          target: rawTop,
          actual,
          afterEvent: top(),
        })
      }
      const wheelUp = () => container.dispatchEvent(new WheelEvent('wheel', { bubbles: true, deltaY: -120 }))
      let contactActive = false
      function setContact(active: boolean): void {
        if (contactActive === active)
          return
        contactActive = active
        const event = new Event(active ? 'touchstart' : 'touchend', { bubbles: true })
        const touches = active
          ? [
              { clientX: 90, clientY: 100 },
            ]
          : []
        Object.defineProperty(event, 'touches', { value: touches })
        container.dispatchEvent(event)
      }
      async function replayMomentum(before: number, after: number) {
        const view = container.getBoundingClientRect()
        const row = Array.from(container.querySelectorAll<HTMLElement>('[data-seq]')).find((row) => {
          const box = row.getBoundingClientRect()
          return box.top < view.bottom && box.bottom > view.top && getComputedStyle(row).visibility !== 'hidden'
        })
        if (row === undefined)
          throw new Error('The view has no visible row after the rebase.')
        const rowTop = row.getBoundingClientRect().top - view.top
        const realNow = performance.now.bind(performance)
        const epoch = realNow()
        const clock = Object.getOwnPropertyDescriptor(performance, 'now')
        let elapsedMs = 250
        let resumeAt: number | undefined
        let sawNativeScroll = false
        const inspectScroll = (event: Event) => {
          if (event.isTrusted)
            sawNativeScroll = true
        }
        container.addEventListener('scroll', inspectScroll)
        Object.defineProperty(performance, 'now', {
          configurable: true,
          value: () => epoch + elapsedMs + (resumeAt === undefined
            ? 0
            : realNow() - resumeAt),
        })
        try {
          deliverScroll(before - 4)
          const first = top()
          // The event of the translator's own write reads the position it just wrote.
          container.dispatchEvent(new Event('scroll'))
          elapsedMs = 330
          deliverScroll(before - 12)
          const second = top()
          resumeAt = realNow()
          await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
          const viewAfter = container.getBoundingClientRect()
          const rowAfter = row.getBoundingClientRect()
          return {
            viewportHeight,
            rebase: after - before,
            first: first - after,
            second: second - after,
            sawNativeScroll,
            rowKept: container.contains(row),
            rowVisible: rowAfter.top < viewAfter.bottom && rowAfter.bottom > viewAfter.top && getComputedStyle(row).visibility !== 'hidden',
            rowTravel: rowAfter.top - viewAfter.top - rowTop,
            diffHeights: diffSeqs.map(seq => measuredDiffs.get(seq) ?? 0),
          }
        }
        finally {
          container.removeEventListener('scroll', inspectScroll)
          if (clock === undefined)
            Reflect.deleteProperty(performance, 'now')
          else
            Object.defineProperty(performance, 'now', clock)
        }
      }
      let rebase: { before: number, after: number } | undefined
      recorder.start()
      try {
        Object.defineProperty(container, 'scrollTop', {
          configurable: true,
          get: top,
          set(value: number) {
            const before = top()
            set!.call(container, value)
            const after = top()
            if (after - before > 2 * viewportHeight && before >= 12 && measuredDiffs.size > 0)
              rebase ??= { before, after }
          },
        })
        // Keep moving toward older history until its first tall diff joins the measured rows. The driver uses the
        // native setter, so the recorder sees only writes made by the app during the rebase.
        const nearTop = Math.floor(viewportHeight * 0.8)
        // Ask for history at the actual edge. The render-ahead buffer can otherwise
        // satisfy the small moves below without fetching an older page.
        // Leave follow mode while the list still has room to move up.
        setContact(true)
        wheelUp()
        deliverScroll(0)
        wheelUp()
        deliverScroll(nearTop)
        deliverScroll(nearTop - 1)
        for (let frame = 0; frame < 360; frame++) {
          if (rebase !== undefined) {
            // The setup moves belong to the finger. Only the samples after release
            // represent delayed compositor momentum.
            setContact(false)
            return await replayMomentum(rebase.before, rebase.after)
          }
          await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
          if (rebase === undefined)
            deliverScroll(Math.max(16, Math.min(nearTop, top() - 1)))
        }
        const measuredSeqs = Array.from(measuredDiffs.keys()).join(', ')
        const mountedSeqs = Array.from(container.querySelectorAll<HTMLElement>('[data-seq]'), row => row.dataset.seq).join(', ')
        throw new Error(`No large rebase at ${top()} of ${container.scrollHeight}px. Measured diff rows: ${measuredSeqs}. Mounted rows: ${mountedSeqs}. Recent moves: ${JSON.stringify(recentMoves)}.`)
      }
      finally {
        setContact(false)
        Reflect.deleteProperty(container, 'scrollTop')
        recorder.stop()
      }
    }, { recorder: handle, diffSeqs })
  }
  finally {
    await handle.dispose()
  }
}

codexTest('keeps the same row in view when old-coordinate momentum outlasts a cold-history rebase', async ({ native }) => {
  const { page, modelScript } = native
  const agent = await currentNativeAgent(native)
  const diffLineCount = 100
  const before = Array.from({ length: diffLineCount }, (_, index) => `Before line ${index}`).join('\n')
  const after = Array.from({ length: diffLineCount }, (_, index) => `After line ${index}`).join('\n')
  const paths = ['first-tall-edit.txt', 'second-tall-edit.txt'].map(path => join(agent.workingDir, path))
  for (const path of paths)
    writeFileSync(path, `${before}\n`)
  await runNativeToolTurn(native, {
    toolCalls: paths.map((path, index) => editToolCall(native.provider, `tall-edit-${index}`, {
      path,
      before,
      after,
    })),
    prompt: 'Edit the two prepared text files.',
    answer: 'The two tall edits finished.',
  })

  const history = () => readAllAgentMessages(native, agent.id)
  const editSpans = spanRowSequences(await history(), 'fileChange')
  expect(editSpans.map(span => span.length), 'each edit has a request and a result').toEqual([2, 2])
  const editResults = editSpans.map(span => span.at(-1)!)
  const lastEdit = editResults.reduce((latest, seq) => seq > latest
    ? seq
    : latest)
  const lastTurn = await fillRows({
    page,
    modelScript,
    count: async () => (await history()).filter(message => message.seq > lastEdit).length,
    target: MESSAGE_PAGE_LIMIT,
    firstTurn: 1,
  })

  const chatUrl = page.url()
  await clearStoredChatHeightsAndRenders(page)
  await page.setViewportSize({ width: 430, height: 650 })
  await page.goto(chatUrl)
  await expect(assistantBubbles(page).filter({ hasText: fillerTurn(lastTurn, false).answer })).toHaveCount(1)
  await expectAtBottom(page, 'after the cold reload')

  const result = await scrollThroughDelayedMomentum(page, editResults.map(String))
  expect(Math.max(...result.diffHeights), 'an older edit diff measured more than two viewports').toBeGreaterThan(2 * result.viewportHeight)
  expect(result.rebase).toBeGreaterThan(2 * result.viewportHeight)
  expect(result.first, 'the first old-coordinate sample moves the view by 4px').toBeCloseTo(-4, 0)
  expect(result.second, 'the later sample still moves the view by only 12px from the rebase').toBeCloseTo(-12, 0)
  expect(result.sawNativeScroll, 'the browser delivered its queued scroll event').toBe(true)
  expect(result.rowKept, 'the reader’s row stayed mounted after the browser scroll event').toBe(true)
  expect(result.rowVisible, 'the reader’s row stayed visible').toBe(true)
  expect(result.rowTravel).toBeCloseTo(12, 0)
})

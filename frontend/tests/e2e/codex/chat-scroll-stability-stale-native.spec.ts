import type { Page } from '@playwright/test'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { MESSAGE_PAGE_LIMIT } from '../../../src/generated/contracts/chat-history'
import { codexTest } from '../codex-fixtures'
import { clearStoredRenders, clearStoredRowHeights, expectAtBottom } from '../helpers/chatScrollStability'
import { fillerTurn, fillRows } from '../helpers/historyFill'
import { readAllAgentMessages } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { editToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, chatScrollContainer } from '../helpers/ui'

/** Deliver two old-coordinate momentum samples after the app rebases over a cold page. */
async function scrollThroughDelayedMomentum(page: Page, diffSeqs: string[]) {
  return chatScrollContainer(page).evaluate(async (container, diffSeqs) => {
    const { get, set } = Object.getOwnPropertyDescriptor(Element.prototype, 'scrollTop')!
    const top = (): number => get!.call(container) as number
    const viewportHeight = container.clientHeight
    const measuredDiffs = new Map<string, number>()
    const recordHeight = (event: Event) => {
      const { seq, source, height } = (event as CustomEvent<{ seq: string, source: string, height: number }>).detail
      if (source === 'premeasure' && diffSeqs.includes(seq))
        measuredDiffs.set(seq, height)
    }
    const deliverScroll = (rawTop: number) => {
      set!.call(container, rawTop)
      container.dispatchEvent(new Event('scroll'))
    }
    function replayMomentum(before: number, after: number) {
      const view = container.getBoundingClientRect()
      const row = Array.from(container.querySelectorAll<HTMLElement>('[data-seq]')).find((row) => {
        const box = row.getBoundingClientRect()
        return box.top < view.bottom && box.bottom > view.top && getComputedStyle(row).visibility !== 'hidden'
      })
      if (row === undefined)
        throw new Error('The view has no visible row after the rebase.')
      const rowTop = row.getBoundingClientRect().top
      const epoch = performance.now()
      const clock = Object.getOwnPropertyDescriptor(performance, 'now')
      let elapsedMs = 250
      Object.defineProperty(performance, 'now', {
        configurable: true,
        value: () => epoch + elapsedMs,
      })
      try {
        deliverScroll(before - 4)
        const first = top()
        // The event of the translator's own write reads the position it just wrote.
        container.dispatchEvent(new Event('scroll'))
        elapsedMs = 330
        deliverScroll(before - 12)
        const second = top()
        return {
          viewportHeight,
          rebase: after - before,
          first: first - after,
          second: second - after,
          rowKept: row.isConnected,
          rowTravel: row.getBoundingClientRect().top - rowTop,
          diffHeights: diffSeqs.map(seq => measuredDiffs.get(seq) ?? 0),
        }
      }
      finally {
        if (clock === undefined)
          Reflect.deleteProperty(performance, 'now')
        else
          Object.defineProperty(performance, 'now', clock)
      }
    }
    let rebase: { before: number, after: number } | undefined
    window.addEventListener('leapmux:chat-row-height', recordHeight)
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
    try {
      // Keep moving toward older history until its first tall diff joins the measured rows. The driver uses the
      // native setter, so the recorder sees only writes made by the app during the rebase.
      const nearTop = Math.floor(viewportHeight * 0.8)
      deliverScroll(nearTop)
      for (let frame = 0; frame < 360; frame++) {
        if (rebase !== undefined)
          return replayMomentum(rebase.before, rebase.after)
        await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
        if (rebase === undefined)
          deliverScroll(Math.max(16, Math.min(nearTop, top() - 1)))
      }
      const measuredSeqs = Array.from(measuredDiffs.keys()).join(', ')
      throw new Error(`No large rebase while measuring an older diff. Measured diff rows: ${measuredSeqs}`)
    }
    finally {
      Reflect.deleteProperty(container, 'scrollTop')
      window.removeEventListener('leapmux:chat-row-height', recordHeight)
    }
  }, diffSeqs)
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
  const edits = (await history()).filter(message => message.spanType === 'fileChange')
  const spans = new Map<string, bigint[]>()
  for (const edit of edits) {
    const span = spans.get(edit.spanId)
    if (span === undefined)
      spans.set(edit.spanId, [edit.seq])
    else
      span.push(edit.seq)
  }
  const editSpans = Array.from(spans.values())
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
  await clearStoredRowHeights(page)
  await clearStoredRenders(page)
  await page.setViewportSize({ width: 430, height: 650 })
  await page.goto(chatUrl)
  await expect(assistantBubbles(page).filter({ hasText: fillerTurn(lastTurn, false).answer })).toHaveCount(1)
  await expectAtBottom(page, 'after the cold reload')

  const result = await scrollThroughDelayedMomentum(page, editResults.map(String))
  expect(Math.max(...result.diffHeights), 'an older edit diff measured more than two viewports').toBeGreaterThan(2 * result.viewportHeight)
  expect(result.rebase).toBeGreaterThan(2 * result.viewportHeight)
  expect(result.first, 'the first old-coordinate sample moves the view by 4px').toBeCloseTo(-4, 0)
  expect(result.second, 'the later sample still moves the view by only 12px from the rebase').toBeCloseTo(-12, 0)
  expect(result.rowKept, 'the reader’s row stayed mounted').toBe(true)
  expect(result.rowTravel).toBeCloseTo(12, 0)
})

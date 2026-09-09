import type { Page } from '@playwright/test'
import { Buffer } from 'node:buffer'
import { getUserId } from './helpers/api'
import { COARSE_POINTER_METRICS, touchDragGripOnto } from './helpers/touch'
import { ARITHMETIC_PROMPT, loginViaToken, openWorkspace, sendMessage, waitForAgentIdle, waitForEditorDraft } from './helpers/ui'
import { ensureWorkerOnline, expect, restartWorker, processTest as test } from './process-control-fixtures'

const MOD = process.platform === 'darwin' ? 'Meta' : 'Control'

/**
 * Pause the queue, park two inputs in it, and hand back the locators that both
 * drag tests need.
 *
 * Kept in this spec rather than under `helpers/`, where the repo's
 * co-located-test rule would ask for a `.test.ts` beside a function that only
 * drives a browser.
 *
 * The row pattern is ANCHORED (`/^queued-input-/`). The two copies of this
 * setup had already drifted onto two different patterns, and the unanchored one
 * matches more than the row.
 */
async function seedTwoQueuedRows(page: Page) {
  await expect(page.locator('[data-testid="composer-editor"] .ProseMirror')).toBeVisible()
  await page.getByTestId('queue-pause-button').click()
  await sendMessage(page, 'first queued')
  await sendMessage(page, 'second queued')

  const rows = page.getByTestId(/^queued-input-/)
  await expect(rows).toHaveCount(2)
  await expect(rows.first()).toContainText('first queued')
  return {
    rows,
    source: rows.filter({ hasText: 'first queued' }),
    target: rows.filter({ hasText: 'second queued' }),
  }
}

test.describe('agent input queue', () => {
  test('persists paused input across clients, a reload, and a Worker restart, then supports queue changes', async ({ page, browser, authenticatedWorkspace, separateHubWorker }) => {
    await expect(page.locator('[data-testid="composer-editor"] .ProseMirror')).toBeVisible()
    // Establish a real provider session before the Worker restart. A fresh
    // Claude process reports an id before it stores a resumable conversation.
    await sendMessage(page, ARITHMETIC_PROMPT)
    await waitForAgentIdle(page)
    await page.getByTestId('queue-pause-button').click()
    await expect(page.getByTestId('queue-pause-button')).toHaveText('Resume Queue')

    const secondContext = await browser.newContext({ baseURL: separateHubWorker.hubUrl })
    const secondPage = await secondContext.newPage()
    await loginViaToken(secondPage, separateHubWorker.adminToken)
    await openWorkspace(secondPage, authenticatedWorkspace.workspaceId)
    await expect(secondPage.locator('[data-testid="composer-editor"] .ProseMirror')).toBeVisible()

    try {
      await expect(secondPage.getByTestId('queue-pause-button')).toHaveText('Resume Queue')

      const queuedFirst = `${'x'.repeat(1200)} full text tail`
      const queuedFirstPreview = queuedFirst.slice(0, 80)
      await page.getByTestId('file-input').setInputFiles({
        name: 'queued-input.txt',
        mimeType: 'text/plain',
        buffer: Buffer.from('queued attachment'),
      })
      await sendMessage(page, queuedFirst)
      await sendMessage(page, 'queued second')
      for (const clientPage of [page, secondPage]) {
        await expect(clientPage.getByTestId('agent-input-queue')).toContainText(queuedFirstPreview)
        await expect(clientPage.getByTestId('agent-input-queue')).not.toContainText('full text tail')
        await expect(clientPage.getByTestId('agent-input-queue')).toContainText('queued-input.txt')
        await expect(clientPage.getByTestId('agent-input-queue')).toContainText('queued second')
        await expect(clientPage.getByTestId('agent-input-queue')).toHaveCSS('overflow-y', 'auto')
      }

      await restartWorker(separateHubWorker)
      await ensureWorkerOnline(separateHubWorker)
      await page.reload()
      await expect(page.getByTestId('agent-input-queue')).toContainText(queuedFirstPreview)
      await expect(page.getByTestId('agent-input-queue')).toContainText('queued second')

      const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
      await editor.fill('normal draft')
      await page.getByTestId('file-input').setInputFiles({
        name: 'normal-draft.txt',
        mimeType: 'text/plain',
        buffer: Buffer.from('normal attachment'),
      })
      await expect(page.getByTestId('attachment-pill')).toContainText('normal-draft.txt')
      const first = page.getByTestId(/queued-input-/).filter({ hasText: queuedFirstPreview })
      await first.getByRole('button', { name: 'Edit', exact: true }).click()
      await expect(editor).toContainText('full text tail')
      await expect(page.getByTestId('attachment-pill')).toContainText('queued-input.txt')
      await editor.fill('edited first')
      await page.keyboard.press('Meta+Enter')
      for (const clientPage of [page, secondPage])
        await expect(clientPage.getByTestId('agent-input-queue')).toContainText('edited first')
      await expect(page.getByTestId('agent-input-queue')).toContainText('queued-input.txt')
      await expect(editor).toHaveText('normal draft')
      await expect(page.getByTestId('attachment-pill')).toContainText('normal-draft.txt')

      const edited = page.getByTestId(/queued-input-/).filter({ hasText: 'edited first' })
      await edited.getByRole('button', { name: 'Edit', exact: true }).click()
      await expect(editor).toHaveText('edited first')
      await editor.fill('unsaved queue edit')
      // The draft has to reach the store before the reload, or the reload
      // races the write and the assertion below fails for the wrong reason.
      // Drafts live in IndexedDB (see `~/lib/browserStorage`), so a
      // localStorage walk would poll a store the value never reaches and time
      // out. `waitForEditorDraft` scans this account's draft rows, which is
      // where a queue edit lands: its key carries the `editor-draft:` prefix.
      const adminUserId = await getUserId(separateHubWorker.hubUrl, separateHubWorker.adminToken)
      await waitForEditorDraft(page, adminUserId, 'unsaved queue edit')
      await page.reload()
      await expect(editor).toHaveText('unsaved queue edit')
      await expect(page.getByTestId('attachment-pill')).toContainText('queued-input.txt')
      const resumedEdit = page.getByTestId(/queued-input-/).filter({ hasText: 'edited first' })
      await resumedEdit.getByRole('button', { name: 'Cancel Edit' }).click()
      await expect(editor).toHaveText('normal draft')

      await edited.getByRole('button', { name: 'Edit', exact: true }).click()
      const secondClientItem = secondPage.getByTestId(/queued-input-/).filter({ hasText: 'edited first' })
      await secondClientItem.getByRole('button', { name: 'Take Over' }).click()
      await expect(secondPage.locator('[data-testid="composer-editor"] .ProseMirror')).toHaveText('edited first')
      await expect(editor).toHaveText('normal draft')
      await secondClientItem.getByRole('button', { name: 'Cancel Edit' }).click()

      const second = page.getByTestId(/queued-input-/).filter({ hasText: 'queued second' })
      await second.getByRole('button', { name: 'Move Up' }).click()
      const previews = page.getByTestId('agent-input-queue').locator('div').filter({ hasText: /^(edited first|queued second)$/ })
      await expect(previews.first()).toHaveText('queued second')

      // Delete arms on the first click and removes the input on the second, so
      // each of the two rows takes a pair.
      //
      // Each pass is pinned to ONE row's own test id, and the pass waits for
      // that row to go before starting the next. `.first()` would not do:
      // Playwright re-resolves a locator on every action, the removal is not
      // optimistic (the row survives until the Worker's snapshot returns), and
      // `ConfirmButton` returns to "Delete" the instant the confirming click
      // fires -- so a second pass through `.first()` can arm the row the first
      // pass already deleted, and then find no armed button once it goes.
      const rowIds = await page.getByTestId(/queued-input-/).evaluateAll(rows =>
        rows.map(row => row.getAttribute('data-testid')!),
      )
      expect(rowIds).toHaveLength(2)
      for (const rowId of rowIds) {
        const row = page.getByTestId(rowId)
        await row.getByRole('button', { name: 'Delete' }).click()
        await row.getByRole('button', { name: 'Confirm delete?' }).click()
        await expect(row).toHaveCount(0)
      }
      for (const clientPage of [page, secondPage])
        await expect(clientPage.getByTestId('agent-input-queue')).toHaveCount(0)
      await page.getByTestId('queue-pause-button').click()
      await expect(page.getByTestId('queue-pause-button')).toHaveText('Pause Queue')
      await expect(secondPage.getByTestId('queue-pause-button')).toHaveText('Pause Queue')
    }
    finally {
      await secondContext.close()
    }
  })

  test.describe('touch reorder (phone)', () => {
    // A coarse primary pointer is what shows the grip at all: below `sm` it is
    // the ONLY way to reorder, because a finger cannot drag a row body -- the
    // body's activators refuse touch so a swipe still scrolls the queue.
    test.use(COARSE_POINTER_METRICS)

    test('reorders a queued input by dragging its grip with a finger', async ({ page, authenticatedWorkspace }) => {
      void authenticatedWorkspace
      const { rows, source, target } = await seedTwoQueuedRows(page)
      const grip = source.locator('[data-drag-handle]')
      // The grip is visible ONLY here. On a fine pointer it is display:none,
      // which is the workspace list's own rule.
      await expect(grip).toBeVisible()
      const gripBox = (await grip.boundingBox())!
      const targetBox = (await target.boundingBox())!

      await touchDragGripOnto({
        page,
        grip: { x: gripBox.x + gripBox.width / 2, y: gripBox.y + gripBox.height / 2 },
        target: { x: targetBox.x + targetBox.width / 2, y: targetBox.y + targetBox.height / 2 },
        draggedRow: source,
        draggingClass: /itemDragging/,
      })

      // Polled: the Worker owns the order, so the reorder lands when the
      // snapshot confirms it, which can be after the lift.
      await expect.poll(async () => (await rows.first().textContent())?.includes('second queued')).toBe(true)
    })
  })

  test('shows no drag affordance when the queue contains one input', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    await expect(page.locator('[data-testid="composer-editor"] .ProseMirror')).toBeVisible()
    await page.getByTestId('queue-pause-button').click()
    await sendMessage(page, 'only queued input')

    const row = page.getByTestId(/^queued-input-/)
    await expect(row).toHaveCount(1)
    await expect(row).not.toHaveClass(/itemDraggable/)
    await expect(row.getByTestId(/^queue-drag-handle-/)).toHaveClass(/dragHandleInert/)
  })

  test('reorders a queued input by dragging its row', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    // A mouse is a FINE pointer, so the grip is hidden and the row body is what
    // drags -- exactly as it is in the workspace list. The grip carries the
    // touch path, which a mouse-driven browser cannot exercise.
    const { rows, source, target } = await seedTwoQueuedRows(page)
    const from = (await source.boundingBox())!
    const to = (await target.boundingBox())!
    await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2)
    await page.mouse.down()
    try {
      // Past the sensor's 10px activation distance, then onto the target's
      // centre in a second step so the collision detector sees the move.
      await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2 + 20)
      await expect(source).toHaveClass(/itemDragging/)
      // The dragged row travels on the queue's own axis alone, so it never
      // widens the scrollable area. Assert the OUTCOME -- nothing to scroll
      // sideways to -- rather than a declaration, so a row that starts drifting
      // sideways again fails here whatever produces it.
      await expect
        .poll(() => page.getByTestId('agent-input-queue')
          .evaluate(queue => queue.scrollWidth - queue.clientWidth))
        .toBe(0)
      await page.mouse.move(to.x + to.width / 2, to.y + to.height / 2)
    }
    finally {
      await page.mouse.up()
    }

    // The Worker owns the order, so the reorder is real only once it comes back.
    await expect(rows.first()).toContainText('second queued')
  })

  // `$mod+Enter` is bound to the queue steer, and the composer's own Cmd+Enter
  // sends. Both live on one chord, and only the emptiness context tells them
  // apart -- so the dangerous direction is the shortcut CLAIMING a keypress that
  // was meant to send. Every other spec in the suite sends with this chord, so a
  // regression there is loud; what needs its own case is the boundary.
  // `MOD` and not a literal `Meta`: tinykeys resolves `$mod` to Meta on an
  // Apple platform and to Control everywhere else, so a hardcoded Meta misses
  // the keybinding layer entirely on Linux and Windows and lands only on the
  // composer's own send plugin -- which accepts either modifier. Both
  // assertions below would then pass for the wrong reason, and the claim this
  // test exists to catch would go unexercised.
  test('leaves the send chord to the composer whenever there is something to send', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    const { rows } = await seedTwoQueuedRows(page)

    // Empty composer, no running turn: the head is not steerable, so the chord
    // is claimed and does nothing. Nothing is sent, and nothing is queued.
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await editor.click()
    await page.keyboard.press(`${MOD}+Enter`)
    await expect(rows).toHaveCount(2)

    // The same chord with text in the composer still sends, which is the whole
    // point of requiring an empty composer for the steer.
    await editor.click()
    await page.keyboard.type('typed then sent')
    await page.keyboard.press(`${MOD}+Enter`)
    await expect(editor).toHaveText('')
    await expect(rows).toHaveCount(3)
    await expect(rows.last()).toContainText('typed then sent')
  })

  test('offers Steer for input queued during a Claude turn', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace

    // Keep the first turn active long enough to put the next message in the
    // durable queue. The Interrupt button is the Worker's turn-state signal.
    await sendMessage(page, 'Write a 2,000-word technical report about Go concurrency. Do not use tools or stop early.')
    await expect(page.getByTestId('interrupt-button')).toBeVisible()

    await sendMessage(page, 'Stop the report now and reply with the single word STEERED.')
    const queued = page.getByTestId(/^queued-input-/).filter({ hasText: 'Stop the report' })
    await expect(queued).toBeVisible()
    const steer = queued.getByRole('button', { name: 'Steer' })
    await expect(steer).toBeVisible()

    await steer.click()
    await expect(queued).toHaveCount(0)
  })

  test('spaces the pause banner, the queue, the attachments and the composer alike', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    await expect(page.locator('[data-testid="composer-editor"] .ProseMirror')).toBeVisible()
    await page.getByTestId('queue-pause-button').click()
    await sendMessage(page, 'a queued input')
    await expect(page.getByTestId('agent-input-queue')).toContainText('a queued input')
    await page.getByTestId('file-input').setInputFiles({
      name: 'notes.txt',
      mimeType: 'text/plain',
      buffer: Buffer.from('an attachment'),
    })
    await expect(page.getByTestId('attachment-pill')).toContainText('notes.txt')

    // Measure the composer column's own flex children, not their contents: the
    // editor's ProseMirror host sits inside a bordered, padded container, so a
    // box taken from it reports that chrome as part of the gap.
    //
    // The gap between each neighbouring pair must be the SAME. `inputArea` owns
    // it with one `gap` and every child keeps its vertical padding at zero.
    // When each child owned its own spacing instead, two of them met and their
    // paddings ADDED -- padding never collapses the way margin does -- so one
    // boundary was double the rest, and its size changed with which optional
    // children happened to render.
    const measured = await page.getByTestId('agent-input-queue').evaluate((queue) => {
      const column = queue.parentElement!
      const children = Array.from(column.children).filter((child) => {
        const style = getComputedStyle(child)
        // The live region is absolutely positioned and the file input is
        // `display: none`. Neither is a flex item, so neither takes a gap.
        return style.display !== 'none' && style.position !== 'absolute'
      })
      const boxes = children.map(child => child.getBoundingClientRect())
      return {
        gaps: boxes.slice(1).map((rect, index) => rect.top - (boxes[index]!.top + boxes[index]!.height)),
        // The OTHER half of the contract, and the half a gap cannot see. A
        // child's own vertical padding lives INSIDE its border box, so
        // restoring it changes no measured gap at all while the visible
        // spacing goes uneven again -- which is the very regression this test
        // exists to catch.
        paddings: children.map((child) => {
          const style = getComputedStyle(child)
          return [style.paddingTop, style.paddingBottom]
        }),
      }
    })

    // Four children: the banner, the queue, the attachment strip, the composer.
    expect(measured.gaps).toHaveLength(3)
    // Rounded: sub-pixel layout puts a fraction on each measurement, and the
    // claim is that the gaps MATCH, not that they are integers.
    const rounded = measured.gaps.map(gap => Math.round(gap))
    // Equal AND non-zero: three collapsed gaps are equal too, and that is a
    // different bug rather than a pass.
    expect(rounded[0]).toBeGreaterThan(0)
    expect(rounded, `gaps between the composer column's children (raw: ${measured.gaps.join(', ')})`)
      .toEqual([rounded[0], rounded[0], rounded[0]])
    expect(measured.paddings, 'vertical padding of each composer column child')
      .toEqual(measured.paddings.map(() => ['0px', '0px']))
  })

  test('keeps the composer action row clear of the [+] button on a phone', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace // fixture trigger
    // Narrower than any phone this app targets, so the action row has the
    // least space it will ever have.
    await page.setViewportSize({ width: 320, height: 720 })
    await expect(page.locator('[data-testid="composer-editor"] .ProseMirror')).toBeVisible()

    const plus = page.getByTestId('composer-plus-trigger')
    const footer = page.getByTestId('composer-footer-slot')
    await expect(plus).toBeVisible()
    await expect(footer).toBeVisible()

    // The two slots are absolutely positioned on the same bottom line, one
    // anchored left and one anchored right, so nothing but the footer's
    // `max-width` stops them from meeting. The footer paints over the `[+]`
    // and its buttons take the clicks, which is what this guards.
    const plusBox = (await plus.boundingBox())!
    const footerBox = (await footer.boundingBox())!
    expect(footerBox.x).toBeGreaterThanOrEqual(plusBox.x + plusBox.width)

    // And the `[+]` still answers a click, which the overlap denied.
    await plus.click()
    await expect(page.getByTestId('composer-plus-popover')).toBeVisible()
  })

  test('goes icon-only and stays clear of the [+] when the composer is narrow on a wide viewport', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace // fixture trigger
    // A WIDE viewport with a NARROW composer, which the phone test above cannot
    // reach: a 320px viewport is already below `sm` on every measure. A split
    // pane or a floating window puts a ~260px composer on a 1200px display, and
    // a VIEWPORT media query calls that composer wide.
    await page.setViewportSize({ width: 1200, height: 800 })
    await expect(page.locator('[data-testid="composer-editor"] .ProseMirror')).toBeVisible()
    await page.addStyleTag({ content: '[data-testid="agent-editor-panel"] { max-width: 260px; }' })

    const plus = page.getByTestId('composer-plus-trigger')
    const footer = page.getByTestId('composer-footer-slot')
    const cluster = page.getByTestId('composer-actions')
    await expect(plus).toBeVisible()
    await expect(cluster).toBeVisible()
    // `hideInNarrowComposer` is a CONTAINER query on `inputArea`, so the labels
    // follow the COMPOSER's width and go away here, although the viewport is
    // 1200px wide. They stay reachable as the buttons' accessible names, which
    // is the whole contract of an icon-only control.
    //
    // The LABEL's visibility, not the button's text: `toHaveText` reads
    // `textContent`, which includes a `display: none` span, so it reports
    // "Send" for a button that renders nothing but an icon.
    await expect(page.getByTestId('send-button').locator('span')).toBeHidden()
    await expect(page.getByRole('button', { name: 'Send' })).toBeVisible()

    // Losing the word must not change the button's HEIGHT. A button is
    // `inline-flex`, so a flex container with no line-box strut takes the
    // height of its tallest ITEM: one line of text is 18px and the icon that
    // replaces it is 14px, so an unpinned button shrinks by 4px exactly here
    // and stops matching the `[+]` it shares the row with.
    const heightOf = (id: string) =>
      page.getByTestId(id).evaluate(el => el.getBoundingClientRect().height)
    const plusHeight = await heightOf('composer-plus-trigger')
    expect(plusHeight).toBeGreaterThan(0)
    for (const id of ['queue-pause-button', 'send-button'])
      expect(await heightOf(id), `${id} must match the [+] while icon-only`).toBe(plusHeight)

    // The CLUSTER's own box, not the slot's. The slot obeys its `max-width`
    // whatever its content does, so measuring the slot alone proves nothing --
    // a cluster that refuses to shrink simply overflows the slot's left edge
    // and paints across the `[+]` from there.
    const plusBox = (await plus.boundingBox())!
    const footerBox = (await footer.boundingBox())!
    const clusterBox = (await cluster.boundingBox())!
    const plusRight = plusBox.x + plusBox.width
    expect(footerBox.x, 'the footer slot must stop right of the [+]').toBeGreaterThanOrEqual(plusRight)
    expect(clusterBox.x, 'the action cluster must stop right of the [+]').toBeGreaterThanOrEqual(plusRight)

    // An EMPTY composer stays collapsed. The cap drives the available collapsed
    // width to zero here, and the expand test subtracts a margin from it, so a
    // text width of zero used to satisfy the comparison and open the tall
    // layout with nothing typed.
    await expect(page.getByTestId('composer-box')).not.toHaveAttribute('data-expanded')

    // And the `[+]` still answers a click, which the overlap denied.
    await plus.click()
    await expect(page.getByTestId('composer-plus-popover')).toBeVisible()
  })
})

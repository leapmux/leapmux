import type { Page } from '@playwright/test'
import { getUserId } from './helpers/api'
import { attachFile, attachmentPills, writeAttachmentFixture } from './helpers/attachments'
import { boxCenter, mouseDragOnto } from './helpers/drag'
import { withExtraClients } from './helpers/multiClient'
import { sendScriptedTurn } from './helpers/scriptedTurn'
import { COARSE_POINTER_METRICS, touchDragGripOnto } from './helpers/touch'
import { composerEditor, focusComposer, loginViaToken, openWorkspace, PLATFORM_MOD, queuePauseButton, resumePausedQueue, sendMessage, waitForEditorDraft } from './helpers/ui'
import { ensureWorkerOnline, expect, restartWorker, processTest as test } from './process-control-fixtures'

/**
 * Pause the queue and wait for its confirmation button.
 * The button label acknowledges the new state.
 * A send before that acknowledgement can reach the agent instead of the queue.
 * The resulting missing row can look like a rendering failure.
 * The queue must be running. A paused queue fails the call, because a click on the toggle would resume it.
 */
async function pauseQueue(page: Page) {
  const button = queuePauseButton(page)
  await expect(button).toHaveText('Pause Queue')
  await button.click()
  await expect(button).toHaveText('Resume Queue')
}

/**
 * Pause the queue and add two inputs for the drag tests.
 * Return their shared locators.
 * This local helper drives only the browser and needs no separate unit-test module.
 * Anchor the row pattern at /^queued-input-/.
 * An unanchored pattern can match a descendant instead of the input row.
 * Two previous copies used different patterns and produced that mismatch.
 */
async function seedTwoQueuedRows(page: Page) {
  await expect(composerEditor(page)).toBeVisible()
  await pauseQueue(page)
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
  test('persists paused input across clients, a reload, and a Worker restart, then supports queue changes', async ({ page, browser, authenticatedWorkspace, separateHubWorker, modelScript }) => {
    // Establish a provider session before the Worker restart. A fresh Claude
    // process reports an id before it stores a resumable conversation.
    await sendScriptedTurn(page, modelScript)
    await pauseQueue(page)

    await withExtraClients(browser, separateHubWorker, 1, async ([secondPage]) => {
      await loginViaToken(secondPage, separateHubWorker.adminToken)
      await openWorkspace(secondPage, authenticatedWorkspace.workspaceId)
      await expect(composerEditor(secondPage)).toBeVisible()
      await expect(queuePauseButton(secondPage)).toHaveText('Resume Queue')

      const queuedFirst = `${'x'.repeat(1200)} full text tail`
      const queuedFirstPreview = queuedFirst.slice(0, 80)
      await attachFile(page, writeAttachmentFixture('text', 'queued-input.txt'))
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

      const editor = composerEditor(page)
      await editor.fill('normal draft')
      await attachFile(page, writeAttachmentFixture('text', 'normal-draft.txt'))
      await expect(attachmentPills(page)).toContainText('normal-draft.txt')
      const first = page.getByTestId(/queued-input-/).filter({ hasText: queuedFirstPreview })
      await first.getByRole('button', { name: 'Edit', exact: true }).click()
      await expect(editor).toContainText('full text tail')
      await expect(attachmentPills(page)).toContainText('queued-input.txt')
      await editor.fill('edited first')
      await page.keyboard.press('Meta+Enter')
      for (const clientPage of [page, secondPage])
        await expect(clientPage.getByTestId('agent-input-queue')).toContainText('edited first')
      await expect(page.getByTestId('agent-input-queue')).toContainText('queued-input.txt')
      await expect(editor).toHaveText('normal draft')
      await expect(attachmentPills(page)).toContainText('normal-draft.txt')

      const edited = page.getByTestId(/queued-input-/).filter({ hasText: 'edited first' })
      await edited.getByRole('button', { name: 'Edit', exact: true }).click()
      await expect(editor).toHaveText('edited first')
      await editor.fill('unsaved queue edit')
      // Wait for the stored draft before reload. Otherwise reload can race its write and cause a false restoration failure.
      // Drafts use IndexedDB through ~/lib/browserStorage. A localStorage lookup cannot find them.
      // waitForEditorDraft reads this account's draft rows. Queue edits use the editor-draft: prefix.
      const adminUserId = await getUserId(separateHubWorker.hubUrl, separateHubWorker.adminToken)
      await waitForEditorDraft(page, adminUserId, 'unsaved queue edit')
      await page.reload()
      await expect(editor).toHaveText('unsaved queue edit')
      await expect(attachmentPills(page)).toContainText('queued-input.txt')
      const resumedEdit = page.getByTestId(/queued-input-/).filter({ hasText: 'edited first' })
      await resumedEdit.getByRole('button', { name: 'Cancel Edit' }).click()
      await expect(editor).toHaveText('normal draft')

      await edited.getByRole('button', { name: 'Edit', exact: true }).click()
      const secondClientItem = secondPage.getByTestId(/queued-input-/).filter({ hasText: 'edited first' })
      await secondClientItem.getByRole('button', { name: 'Take Over' }).click()
      await expect(composerEditor(secondPage)).toHaveText('edited first')
      await expect(editor).toHaveText('normal draft')
      await secondClientItem.getByRole('button', { name: 'Cancel Edit' }).click()

      const second = page.getByTestId(/queued-input-/).filter({ hasText: 'queued second' })
      await second.getByRole('button', { name: 'Move Up' }).click()
      const previews = page.getByTestId('agent-input-queue').locator('div').filter({ hasText: /^(edited first|queued second)$/ })
      await expect(previews.first()).toHaveText('queued second')

      // The first Delete click requests confirmation. The second click removes the input.
      // Select each row by its own test ID and wait for its removal before selecting the next row.
      // Playwright resolves the locator again for every action.
      // The row remains until the Worker returns its snapshot, and ConfirmButton restores Delete immediately after the confirmation click.
      // A second first() lookup can therefore select the old row again and request confirmation just before that row disappears.
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
      await resumePausedQueue(page)
      await expect(queuePauseButton(secondPage)).toHaveText('Pause Queue')
    })
  })

  test.describe('touch reorder (phone)', () => {
    // A coarse primary pointer shows the grip.
    // Below the sm threshold, the grip is the only control that can reorder an input.
    // The row body refuses touch dragging, so a finger swipe scrolls the queue.
    test.use(COARSE_POINTER_METRICS)

    test('reorders a queued input by dragging its grip with a finger', async ({ page, authenticatedWorkspace }) => {
      void authenticatedWorkspace
      const { rows, source, target } = await seedTwoQueuedRows(page)
      const grip = source.locator('[data-drag-handle]')
      // The coarse pointer shows the grip. A fine pointer hides it through display: none.
      // The workspace list uses the same rule.
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

      // Wait for the Worker snapshot to confirm the new order. It can arrive after the pointer lifts.
      await expect.poll(async () => (await rows.first().textContent())?.includes('second queued')).toBe(true)
    })
  })

  test('shows no drag affordance when the queue contains one input', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    await expect(composerEditor(page)).toBeVisible()
    await pauseQueue(page)
    await sendMessage(page, 'only queued input')

    const row = page.getByTestId(/^queued-input-/)
    await expect(row).toHaveCount(1)
    await expect(row).not.toHaveClass(/itemDraggable/)
    await expect(row.getByTestId(/^queue-drag-handle-/)).toHaveClass(/dragHandleInert/)
  })

  test('reorders a queued input by dragging its row', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    // A mouse is a fine pointer, so the row body handles the drag and the grip stays hidden.
    // The workspace list uses the same rule. A mouse cannot exercise the grip's touch path.
    const { rows, source, target } = await seedTwoQueuedRows(page)
    await mouseDragOnto(page, {
      from: await boxCenter(source),
      to: await boxCenter(target),
      dragged: { row: source, draggingClass: /itemDragging/ },
      // The row must move only along the queue axis and add no horizontal scroll area.
      // Check the actual scroll width so any source of sideways movement fails this assertion.
      // The activation move of the drag has a sideways part, so the check also covers a
      // pointer that leaves the axis.
      whileLifted: () => expect
        .poll(() => page.getByTestId('agent-input-queue')
          .evaluate(queue => queue.scrollWidth - queue.clientWidth))
        .toBe(0),
    })

    // Require the returned Worker order before accepting the reorder.
    await expect(rows.first()).toContainText('second queued')
  })

  // The queue steering shortcut and composer send control share the same key combination.
  // Only an empty composer permits the steering shortcut to consume it.
  // This case protects a send keypress from that shortcut.
  // Use PLATFORM_MOD because tinykeys maps $mod to Meta on Apple platforms and Control elsewhere.
  // A fixed Meta key would bypass the queue shortcut on Linux and Windows.
  // The composer accepts either modifier, so both assertions could pass without testing the intended shortcut.
  test('leaves the send chord to the composer whenever there is something to send', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    const { rows } = await seedTwoQueuedRows(page)

    // With an empty composer and no active turn, the first queued input cannot steer.
    // The shortcut consumes the keypress without sending or queueing anything.
    const editor = await focusComposer(page)
    await page.keyboard.press(`${PLATFORM_MOD}+Enter`)
    await expect(rows).toHaveCount(2)

    // With composer text present, the same key combination must send the prompt.
    await editor.click()
    await page.keyboard.type('typed then sent')
    await page.keyboard.press(`${PLATFORM_MOD}+Enter`)
    await expect(editor).toHaveText('')
    await expect(rows).toHaveCount(3)
    await expect(rows.last()).toContainText('typed then sent')
  })

  test('spaces the pause banner, the queue, the attachments and the composer alike', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    await expect(composerEditor(page)).toBeVisible()
    await pauseQueue(page)
    await sendMessage(page, 'a queued input')
    await expect(page.getByTestId('agent-input-queue')).toContainText('a queued input')
    await attachFile(page, writeAttachmentFixture('text', 'notes.txt'))
    await expect(attachmentPills(page)).toContainText('notes.txt')

    // Measure the composer column's flex children.
    // The inner ProseMirror element excludes its container border and padding, so its rectangle cannot measure the actual gap.
    // inputArea supplies one common gap, and each child has zero vertical padding.
    // Separate child padding adds at adjacent children because padding does not collapse like margins.
    // That previous design doubled one gap and changed its size when optional children appeared.
    const measured = await page.getByTestId('agent-input-queue').evaluate((queue) => {
      const column = queue.parentElement!
      const children = Array.from(column.children).filter((child) => {
        const style = getComputedStyle(child)
        // Exclude the absolute live region and hidden file input. Neither participates in flex spacing.
        return style.display !== 'none' && style.position !== 'absolute'
      })
      const boxes = children.map(child => child.getBoundingClientRect())
      return {
        gaps: boxes.slice(1).map((rect, index) => rect.top - (boxes[index]!.top + boxes[index]!.height)),
        // Check each child's vertical padding separately.
        // Padding inside its border box changes the visible spacing without changing the measured gaps.
        paddings: children.map((child) => {
          const style = getComputedStyle(child)
          return [style.paddingTop, style.paddingBottom]
        }),
      }
    })

    // The column contains these four children:
    // - The banner.
    // - The queue.
    // - The attachment strip.
    // - The composer.
    expect(measured.gaps).toHaveLength(3)
    // Round the subpixel measurements to compare equal gaps without requiring integer layout coordinates.
    const rounded = measured.gaps.map(gap => Math.round(gap))
    // Require a positive gap. Three collapsed gaps would also compare equal.
    expect(rounded[0]).toBeGreaterThan(0)
    expect(rounded, `gaps between the composer column's children (raw: ${measured.gaps.join(', ')})`)
      .toEqual([rounded[0], rounded[0], rounded[0]])
    expect(measured.paddings, 'vertical padding of each composer column child')
      .toEqual(measured.paddings.map(() => ['0px', '0px']))
  })

  test('keeps the composer action row clear of the [+] button on a phone', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace // fixture trigger
    // Use a viewport narrower than the supported phone sizes to test the action row's minimum available space.
    await page.setViewportSize({ width: 320, height: 720 })
    await expect(composerEditor(page)).toBeVisible()

    const plus = page.getByTestId('composer-plus-trigger')
    const footer = page.getByTestId('composer-footer-slot')
    await expect(plus).toBeVisible()
    await expect(footer).toBeVisible()

    // The two slots share the bottom line, with one at each side.
    // The footer's maximum width must prevent overlap.
    // An overlapping footer covers the plus button and intercepts its clicks.
    const plusBox = (await plus.boundingBox())!
    const footerBox = (await footer.boundingBox())!
    expect(footerBox.x).toBeGreaterThanOrEqual(plusBox.x + plusBox.width)

    // Require the plus button to accept a click after the geometry check.
    await plus.click()
    await expect(page.getByTestId('composer-plus-popover')).toBeVisible()
  })

  test('goes icon-only and stays clear of the [+] when the composer is narrow on a wide viewport', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace // fixture trigger
    // Test a narrow composer inside a wide viewport. The phone case cannot exercise that combination.
    // A 320px viewport is below the sm threshold. A split or floating pane can be about 260px wide on a 1200px display.
    // A viewport media query would classify that narrow composer as wide.
    await page.setViewportSize({ width: 1200, height: 800 })
    await expect(composerEditor(page)).toBeVisible()
    await page.addStyleTag({ content: '[data-testid="agent-editor-panel"] { max-width: 260px; }' })

    const plus = page.getByTestId('composer-plus-trigger')
    const footer = page.getByTestId('composer-footer-slot')
    const cluster = page.getByTestId('composer-actions')
    await expect(plus).toBeVisible()
    await expect(cluster).toBeVisible()
    // hideInNarrowComposer uses an inputArea container query.
    // It hides the labels according to the composer width even when the viewport remains 1200px wide.
    // The buttons retain those labels as accessible names.
    // Check the label visibility directly. toHaveText reads textContent and includes a span that uses display: none.
    // That assertion would still report Send when the button shows only its icon.
    await expect(page.getByTestId('send-button').locator('span')).toBeHidden()
    await expect(page.getByRole('button', { name: 'Send' })).toBeVisible()

    // Removing the label must preserve the button height.
    // An inline-flex container without a line-box strut uses the height of its tallest item.
    // Here, text is 18px high and the icon is 14px high.
    // Without a fixed height, the button would shrink by 4px and stop matching the plus button.
    const heightOf = (id: string) =>
      page.getByTestId(id).evaluate(el => el.getBoundingClientRect().height)
    const plusHeight = await heightOf('composer-plus-trigger')
    expect(plusHeight).toBeGreaterThan(0)
    for (const id of ['queue-pause-button', 'send-button'])
      expect(await heightOf(id), `${id} must match the [+] while icon-only`).toBe(plusHeight)

    // Measure the action cluster as well as its slot.
    // The slot can obey its maximum width while the cluster overflows its left edge and covers the plus button.
    const plusBox = (await plus.boundingBox())!
    const footerBox = (await footer.boundingBox())!
    const clusterBox = (await cluster.boundingBox())!
    const plusRight = plusBox.x + plusBox.width
    expect(footerBox.x, 'the footer slot must stop right of the [+]').toBeGreaterThanOrEqual(plusRight)
    expect(clusterBox.x, 'the action cluster must stop right of the [+]').toBeGreaterThanOrEqual(plusRight)

    // Require the empty composer to stay collapsed.
    // The width restriction reduces the available collapsed width to zero.
    // The expansion check subtracts a margin, so an empty zero-width string previously opened the tall layout.
    await expect(page.getByTestId('composer-box')).not.toHaveAttribute('data-expanded')

    // Require the plus button to accept a click after the geometry check.
    await plus.click()
    await expect(page.getByTestId('composer-plus-popover')).toBeVisible()
  })
})

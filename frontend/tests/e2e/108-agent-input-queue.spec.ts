import type { Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { localFieldValueOf } from '../../src/components/chat/composer/scheduleSend'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { getUserId } from './helpers/api'
import { nativeUserStrings } from './helpers/attachmentModelProbe'
import { attachFile, attachmentPills, writeAttachmentFixture } from './helpers/attachments'
import { finishCleanup, withCleanup } from './helpers/cleanup'
import { boxCenter, mouseDragOnto } from './helpers/drag'
import { withExtraClients } from './helpers/multiClient'
import { spawnSubagentToolCall } from './helpers/providerToolCalls'
import { sendScriptedTurn } from './helpers/scriptedTurn'
import { expectSteerAccepted, QUEUED_INPUT_ROW_TESTID, queuedInputRow, steerButton } from './helpers/steer'
import { expectRowBecomesFinal, requireRegistryRow } from './helpers/subagentRegistry'
import { COARSE_POINTER_METRICS, touchDragGripOnto } from './helpers/touch'
import { agentTabs, assistantBubbles, composerEditor, fillComposer, focusComposer, inputQueue, interruptButton, loginViaToken, openAgentViaUI, openWorkspace, PLATFORM_MOD, queuePauseButton, resumePausedQueue, sendMessage, userBubbles, waitForAgentIdle, waitForEditorDraft } from './helpers/ui'
import { ensureWorkerOnline, restartWorker, processTest as test } from './process-control-fixtures'

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

  const rows = page.getByTestId(QUEUED_INPUT_ROW_TESTID)
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
        await expect(inputQueue(clientPage)).toContainText(queuedFirstPreview)
        await expect(inputQueue(clientPage)).not.toContainText('full text tail')
        await expect(inputQueue(clientPage)).toContainText('queued-input.txt')
        await expect(inputQueue(clientPage)).toContainText('queued second')
        await expect(inputQueue(clientPage)).toHaveCSS('overflow-y', 'auto')
      }

      await restartWorker(separateHubWorker)
      await ensureWorkerOnline(separateHubWorker)
      await page.reload()
      await expect(inputQueue(page)).toContainText(queuedFirstPreview)
      await expect(inputQueue(page)).toContainText('queued second')

      const editor = composerEditor(page)
      await editor.fill('normal draft')
      await attachFile(page, writeAttachmentFixture('text', 'normal-draft.txt'))
      await expect(attachmentPills(page)).toContainText('normal-draft.txt')
      const first = queuedInputRow(page, queuedFirstPreview)
      await first.getByRole('button', { name: 'Edit', exact: true }).click()
      await expect(editor).toContainText('full text tail')
      await expect(attachmentPills(page)).toContainText('queued-input.txt')
      await editor.fill('edited first')
      await page.keyboard.press(`${PLATFORM_MOD}+Enter`)
      for (const clientPage of [page, secondPage])
        await expect(inputQueue(clientPage)).toContainText('edited first')
      await expect(inputQueue(page)).toContainText('queued-input.txt')
      await expect(editor).toHaveText('normal draft')
      await expect(attachmentPills(page)).toContainText('normal-draft.txt')

      const edited = queuedInputRow(page, 'edited first')
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
      const resumedEdit = queuedInputRow(page, 'edited first')
      await resumedEdit.getByRole('button', { name: 'Cancel Edit' }).click()
      await expect(editor).toHaveText('normal draft')

      await edited.getByRole('button', { name: 'Edit', exact: true }).click()
      const secondClientItem = queuedInputRow(secondPage, 'edited first')
      await secondClientItem.getByRole('button', { name: 'Take Over' }).click()
      await expect(composerEditor(secondPage)).toHaveText('edited first')
      await expect(editor).toHaveText('normal draft')
      await secondClientItem.getByRole('button', { name: 'Cancel Edit' }).click()

      const second = queuedInputRow(page, 'queued second')
      await second.getByRole('button', { name: 'Move Up' }).click()
      const previews = inputQueue(page).locator('div').filter({ hasText: /^(edited first|queued second)$/ })
      await expect(previews.first()).toHaveText('queued second')

      // The first Delete click requests confirmation. The second click removes the input.
      // Select each row by its own test ID and wait for its removal before selecting the next row.
      // Playwright resolves the locator again for every action.
      // The row remains until the Worker returns its snapshot, and ConfirmButton restores Delete immediately after the confirmation click.
      // A second first() lookup can therefore select the old row again and request confirmation just before that row disappears.
      const rowIds = await page.getByTestId(QUEUED_INPUT_ROW_TESTID).evaluateAll(rows =>
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
        await expect(inputQueue(clientPage)).toHaveCount(0)
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

      await touchDragGripOnto({
        page,
        grip: await boxCenter(grip),
        target: await boxCenter(target),
        draggedRow: source,
        draggingClass: /itemDragging/,
      })

      // Wait for the Worker snapshot to confirm the new order. It can arrive after the pointer lifts.
      await expect(rows.first()).toContainText('second queued')
    })
  })

  test('shows no drag affordance when the queue contains one input', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    await expect(composerEditor(page)).toBeVisible()
    await pauseQueue(page)
    await sendMessage(page, 'only queued input')

    const row = page.getByTestId(QUEUED_INPUT_ROW_TESTID)
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
        .poll(() => inputQueue(page)
          .evaluate(queue => queue.scrollWidth - queue.clientWidth))
        .toBe(0),
    })

    // Require the returned Worker order before accepting the reorder.
    await expect(rows.first()).toContainText('second queued')
  })

  // The queue shortcut and composer send control share the same key combination.
  // Only an empty composer permits the queue shortcut to consume it.
  // This case protects a send keypress from that shortcut, which would otherwise resume the paused queue.
  // Use PLATFORM_MOD because tinykeys maps $mod to Meta on Apple platforms and Control elsewhere.
  // A fixed Meta key would bypass the queue shortcut on Linux and Windows.
  // The composer accepts either modifier, so both assertions could pass without testing the intended shortcut.
  test('leaves the send chord to the composer whenever there is something to send', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    const { rows } = await seedTwoQueuedRows(page)

    // With composer text present, the key combination must send the prompt and leave the queue paused.
    const editor = await focusComposer(page)
    await page.keyboard.type('typed then sent')
    await page.keyboard.press(`${PLATFORM_MOD}+Enter`)
    await expect(editor).toHaveText('')
    await expect(rows).toHaveCount(3)
    await expect(rows.last()).toContainText('typed then sent')
    await expect(queuePauseButton(page)).toHaveText('Resume Queue')
  })

  // An empty queue dispatches nothing on resume, so the case needs no scripted turn.
  // A second press on the now-running idle queue must remain a no-op.
  test('resumes a paused queue with the send chord when the composer is empty', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    await expect(composerEditor(page)).toBeVisible()
    await pauseQueue(page)

    const editor = await focusComposer(page)
    await page.keyboard.press(`${PLATFORM_MOD}+Enter`)
    await expect(queuePauseButton(page)).toHaveText('Pause Queue')

    // On a running idle queue, the same empty-composer press must not toggle pause,
    // send an empty message, or insert a newline into the editor.
    await page.keyboard.press(`${PLATFORM_MOD}+Enter`)
    await expect(queuePauseButton(page)).toHaveText('Pause Queue')
    await expect(inputQueue(page)).toHaveCount(0)
    await expect(userBubbles(page)).toHaveCount(0)
    await expect(editor).toHaveText('')
  })

  // One press on a paused queue steers the head into the running turn and then resumes the queue.
  // The steer goes first, because the Worker steers through a pause.
  // The default agent of this suite is Claude Code, which steers.
  test('resumes a paused queue and steers with one send chord', async ({ page, authenticatedWorkspace, modelScript }) => {
    void authenticatedWorkspace
    const gate = 'queue-chord-steer-hold'
    await withCleanup(async () => {
      // The gate holds the first turn open, so the next message waits in the queue.
      const start = await modelScript.queue(
        { text: 'A report.', gate },
        { text: 'STEERED' },
      )
      await sendMessage(page, modelScript.prompt('Write a long technical report.'))
      await modelScript.waitForGate(gate)
      await expect(interruptButton(page)).toBeVisible()
      await pauseQueue(page)

      const steering = 'Stop the report now and reply with the single word STEERED.'
      const editor = await focusComposer(page)
      await page.keyboard.type(modelScript.prompt(steering))
      await page.keyboard.press(`${PLATFORM_MOD}+Enter`)
      const queued = queuedInputRow(page, 'Stop the report')
      await expect(queued).toBeVisible()
      await expect(steerButton(queued)).toBeVisible()
      await expect(editor).toHaveText('')
      await expect(queuePauseButton(page)).toHaveText('Resume Queue')

      // The composer is empty now, so the same chord steers and resumes.
      await page.keyboard.press(`${PLATFORM_MOD}+Enter`)
      await expectSteerAccepted(page, queued)
      await expect(queuePauseButton(page)).toHaveText('Pause Queue')

      await modelScript.releaseGate(gate)
      const request = await modelScript.requestAt(start + 1)
      expect(nativeUserStrings(request.body).join('\n')).toContain(steering)
      await waitForAgentIdle(page)
      await expect(assistantBubbles(page).filter({ hasText: 'STEERED' })).toHaveCount(1)
    }, async () => {
      await finishCleanup([
        modelScript.releaseGateIfHeld(gate),
      ])
    })
  })

  test('spaces the pause banner, the queue, the attachments and the composer alike', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    await expect(composerEditor(page)).toBeVisible()
    await pauseQueue(page)
    await sendMessage(page, 'a queued input')
    await expect(inputQueue(page)).toContainText('a queued input')
    await attachFile(page, writeAttachmentFixture('text', 'notes.txt'))
    await expect(attachmentPills(page)).toContainText('notes.txt')

    // Measure the composer column's flex children.
    // The inner ProseMirror element excludes its container border and padding, so its rectangle cannot measure the actual gap.
    // inputArea supplies one common gap, and each child has zero vertical padding.
    // Separate child padding adds at adjacent children because padding does not collapse like margins.
    // That previous design doubled one gap and changed its size when optional children appeared.
    const measured = await inputQueue(page).evaluate((queue) => {
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
    await expect(page.getByRole('button', { name: 'Send', exact: true })).toBeVisible()

    // Removing the label must preserve the button height.
    // An inline-flex container without a line-box strut uses the height of its tallest item.
    // Here, text is 18px high and the icon is 14px high.
    // Without a fixed height, the button would shrink by 4px and stop matching the plus button.
    const heightOf = (id: string) =>
      page.getByTestId(id).evaluate(el => el.getBoundingClientRect().height)
    const plusHeight = await heightOf('composer-plus-trigger')
    expect(plusHeight).toBeGreaterThan(0)
    for (const id of ['queue-pause-button', 'schedule-send-trigger', 'send-button'])
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

  test('holds queued input while a background task runs, drains when it finishes, and dispatches immediately on Steer', async ({ page, authenticatedWorkspace, modelScript }) => {
    void authenticatedWorkspace
    const drainGate = 'claude-bg-queue-drain-gate'
    const steerGate = 'claude-bg-queue-steer-gate'
    await withCleanup(async () => {
      // Part (a): hold queued input behind a running background subagent, then auto-drain when it finishes.
      await modelScript.rule(
        {
          name: 'claude background child holds for auto-drain',
          when: { user: 'BG_DRAIN_CHILD_MARKER' },
          respond: { gate: drainGate, text: 'BG_DRAIN_CHILD_DONE' },
          once: true,
        },
        {
          name: 'claude answers drained message after background task finishes',
          when: { user: 'queued behind background drain' },
          respond: { text: 'DRAINED_AFTER_BG_REPLY' },
          once: true,
        },
      )
      const drainStart = await modelScript.queue(
        {
          toolCalls: [
            spawnSubagentToolCall(AgentProvider.CLAUDE_CODE, 'bg-drain-spawn', {
              description: 'Background drain task',
              prompt: modelScript.prompt('Hold at BG_DRAIN_CHILD_MARKER.'),
              background: true,
            }),
          ],
        },
        { text: 'ROOT_DRAIN_TURN_DONE' },
      )
      await sendMessage(page, modelScript.prompt('Spawn a background task for queue hold test.'))
      await modelScript.waitForSteps(drainStart + 2)
      await modelScript.waitForGate(drainGate)
      await expect(assistantBubbles(page).filter({ hasText: 'ROOT_DRAIN_TURN_DONE' })).toHaveCount(1)
      const drainRow = await requireRegistryRow(page)
      await expect(drainRow).toHaveAttribute('data-status', 'running')

      await sendMessage(page, modelScript.prompt('queued behind background drain'))
      const heldRow = queuedInputRow(page, 'queued behind background drain')
      await expect(inputQueue(page)).toContainText('queued behind background drain')
      await expect(heldRow).toBeVisible()
      await expect(steerButton(heldRow)).toBeVisible()
      await expect(userBubbles(page).filter({ hasText: 'queued behind background drain' })).toHaveCount(0)

      await modelScript.rule({
        name: 'root handles background drain completion notification',
        when: { user: '<task-notification>' },
        respond: { text: 'ROOT_DRAIN_NOTICE_DONE' },
        once: true,
      })
      await modelScript.releaseGate(drainGate)
      await expectRowBecomesFinal(page, drainRow)
      await expect(userBubbles(page).filter({ hasText: 'queued behind background drain' })).toHaveCount(1)
      await expect(assistantBubbles(page).filter({ hasText: 'DRAINED_AFTER_BG_REPLY' })).toHaveCount(1)
      await expect(page.getByTestId(QUEUED_INPUT_ROW_TESTID)).toHaveCount(0)
      await waitForAgentIdle(page)

      // Part (b): repeat and click Steer while the background task still runs -> dispatches immediately.
      await modelScript.rule(
        {
          name: 'claude background child holds for steer bypass',
          when: { user: 'BG_STEER_CHILD_MARKER' },
          respond: { gate: steerGate, text: 'BG_STEER_CHILD_DONE' },
          once: true,
        },
        {
          name: 'claude answers steered message while background task still runs',
          when: { user: 'steered behind background task' },
          respond: { text: 'STEERED_WHILE_BG_RUNNING_REPLY' },
          once: true,
        },
      )
      const steerStart = await modelScript.queue(
        {
          toolCalls: [
            spawnSubagentToolCall(AgentProvider.CLAUDE_CODE, 'bg-steer-spawn', {
              description: 'Background steer task',
              prompt: modelScript.prompt('Hold at BG_STEER_CHILD_MARKER.'),
              background: true,
            }),
          ],
        },
        { text: 'ROOT_STEER_TURN_DONE' },
      )
      await sendMessage(page, modelScript.prompt('Spawn a second background task for Steer bypass test.'))
      await modelScript.waitForSteps(steerStart + 2)
      await modelScript.waitForGate(steerGate)
      await expect(assistantBubbles(page).filter({ hasText: 'ROOT_STEER_TURN_DONE' })).toHaveCount(1)

      await sendMessage(page, modelScript.prompt('steered behind background task'))
      const steerRow = queuedInputRow(page, 'steered behind background task')
      await expect(inputQueue(page)).toContainText('steered behind background task')
      await expect(steerRow).toBeVisible()
      await expect(userBubbles(page).filter({ hasText: 'steered behind background task' })).toHaveCount(0)

      await steerButton(steerRow).click()
      await expect(userBubbles(page).filter({ hasText: 'steered behind background task' })).toHaveCount(1)
      await expect(assistantBubbles(page).filter({ hasText: 'STEERED_WHILE_BG_RUNNING_REPLY' })).toHaveCount(1)
      await expect(page.getByTestId(QUEUED_INPUT_ROW_TESTID)).toHaveCount(0)

      await modelScript.rule({
        name: 'root handles background steer completion notification',
        when: { user: '<task-notification>' },
        respond: { text: 'ROOT_STEER_NOTICE_DONE' },
        once: true,
      })
      await modelScript.releaseGate(steerGate)
      await waitForAgentIdle(page)
    }, () => finishCleanup([
      modelScript.releaseGateIfHeld(drainGate),
      modelScript.releaseGateIfHeld(steerGate),
    ]))
  })
})

/**
 * Schedule the composer's next send through the real popover: open the
 * trigger, fill the field, apply. The Worker owns the clock, so the value the
 * browser computes only has to be far enough ahead for the row to render
 * before its deadline passes.
 */
async function scheduleNextSend(page: Page, offsetMs: number) {
  await page.getByTestId('schedule-send-trigger').click()
  // Wait for the popover before filling: a click during its opening position
  // lands outside it and dismisses it, losing the apply.
  await expect(page.getByTestId('schedule-send-popover')).toBeVisible()
  // Set the value programmatically. Playwright's `fill` requires the field to
  // read back exactly the value it typed, and the field writes the shortest
  // form of a time: it drops a zero fraction, and zero seconds with it. So
  // `fill` reports some valid values as malformed. The control's own onInput
  // handler carries the value either way.
  const value = localFieldValueOf(new Date(Date.now() + offsetMs))
  await page.getByTestId('schedule-send-field').evaluate((element: HTMLElement, setValue: string) => {
    ;(element as HTMLInputElement).value = setValue
    element.dispatchEvent(new Event('input', { bubbles: true }))
  }, value)
  await page.getByTestId('schedule-send-field').press('Enter')
  await expect(page.getByTestId('schedule-send-trigger')).toHaveAttribute('data-scheduled', 'true')
}

test.describe('scheduled input', () => {
  test('styles the schedule card as an Oat form with outline actions', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    await page.getByTestId('schedule-send-trigger').click()
    const popover = page.getByTestId('schedule-send-popover')
    await expect(popover).toBeVisible()
    const outlineColor = await queuePauseButton(page).evaluate(button => getComputedStyle(button).borderTopColor)
    for (const action of ['clear', 'cancel', 'apply'])
      await expect(page.getByTestId(`schedule-send-${action}`)).toHaveCSS('border-top-color', outlineColor)
    const foreground = await popover.evaluate(card => getComputedStyle(card).color)
    await expect(popover.locator('label')).toHaveCSS('color', foreground)
    await expect(page.getByTestId('schedule-send-field')).toHaveAccessibleDescription('Uses your local timezone.')
    await expect(page.getByTestId('schedule-send-clear')).toBeDisabled()
    await page.getByTestId('schedule-send-cancel').click()
    await scheduleNextSend(page, 300_000)
    await page.getByTestId('schedule-send-trigger').click()
    await expect(page.getByTestId('schedule-send-clear')).toBeEnabled()
    await expect(page.getByTestId('schedule-send-clear')).toHaveCSS('border-top-color', outlineColor)
    await page.getByTestId('schedule-send-field').fill('2000-01-01T09:00')
    await expect(page.getByTestId('schedule-send-apply')).toBeDisabled()
    await expect(page.getByTestId('schedule-send-apply')).toHaveCSS('border-top-color', outlineColor)
    await expect(page.getByTestId('schedule-send-error')).toHaveCSS('border-top-width', '0px')
    await expect(page.getByTestId('schedule-send-error')).toHaveCSS('padding-top', '0px')
    await expect(page.getByTestId('schedule-send-field')).toHaveAccessibleDescription('Uses your local timezone. Pick a time in the future.')
    await page.getByTestId('schedule-send-cancel').click()
  })

  test('restores the schedule of an unsent draft after a reload', async ({ page, authenticatedWorkspace, separateHubWorker }) => {
    void authenticatedWorkspace
    await scheduleNextSend(page, 300_000)
    await fillComposer(page, 'unsent scheduled draft')
    const userId = await getUserId(separateHubWorker.hubUrl, separateHubWorker.adminToken)
    await waitForEditorDraft(page, userId, 'unsent scheduled draft')
    await page.reload()
    await expect(composerEditor(page)).toHaveText('unsent scheduled draft')
    await expect(page.getByTestId('schedule-send-trigger')).toHaveAttribute('data-scheduled', 'true')
  })

  for (const clear of [false, true]) {
    test(`restores an unsaved ${clear ? 'cleared' : 'replacement'} schedule in a queue edit after a reload`, async ({ page, authenticatedWorkspace, separateHubWorker }) => {
      void authenticatedWorkspace
      await pauseQueue(page)
      await scheduleNextSend(page, 300_000)
      await sendMessage(page, 'stored scheduled input')
      const row = queuedInputRow(page, 'stored scheduled input')
      await row.getByRole('button', { name: 'Edit', exact: true }).click()
      await expect(composerEditor(page)).toHaveText('stored scheduled input')
      if (clear) {
        await page.getByTestId('schedule-send-trigger').click()
        await page.getByTestId('schedule-send-clear').click()
      }
      else {
        await scheduleNextSend(page, 600_000)
      }
      if (clear)
        await expect(page.getByTestId('schedule-send-trigger')).not.toHaveAttribute('data-scheduled')
      await page.getByTestId('schedule-send-trigger').click()
      await expect(page.getByTestId('schedule-send-popover')).toBeVisible()
      const expected = clear
        ? ''
        : await page.getByTestId('schedule-send-field').inputValue()
      await page.getByTestId('schedule-send-cancel').click()
      const text = `unsaved ${clear ? 'cleared' : 'replacement'} edit`
      await fillComposer(page, text)
      const userId = await getUserId(separateHubWorker.hubUrl, separateHubWorker.adminToken)
      await waitForEditorDraft(page, userId, text)
      await page.reload()
      await expect(composerEditor(page)).toHaveText(text)
      if (clear)
        await expect(page.getByTestId('schedule-send-trigger')).not.toHaveAttribute('data-scheduled')
      await page.getByTestId('schedule-send-trigger').click()
      await expect(page.getByTestId('schedule-send-popover')).toBeVisible()
      await expect(page.getByTestId('schedule-send-field')).toHaveValue(expected)
    })
  }

  test('refuses an incomplete native date without clearing the existing schedule', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    await scheduleNextSend(page, 300_000)
    await page.getByTestId('schedule-send-trigger').click()
    const field = page.getByTestId('schedule-send-field')
    await expect(page.getByTestId('schedule-send-popover')).toBeVisible()
    const original = await field.inputValue()
    await field.evaluate((element: HTMLInputElement) => {
      element.addEventListener('input', () => {
        element.dataset.nativeBadInput = String(element.validity.badInput)
      }, { capture: true })
    })
    // The first date segment is incomplete after Backspace. Its native value
    // is empty, but badInput distinguishes that state from deliberate clearing.
    const box = (await field.boundingBox())!
    await field.click({
      position: { x: 8, y: box.height / 2 },
    })
    await field.press('Backspace')
    await expect(field).toHaveAttribute('data-native-bad-input', 'true')
    await expect(page.getByTestId('schedule-send-apply')).toBeDisabled()
    await field.press('Enter')
    await expect(page.getByTestId('schedule-send-popover')).toBeVisible()
    await page.getByTestId('schedule-send-cancel').click()
    await page.getByTestId('schedule-send-trigger').click()
    await expect(field).toHaveValue(original)
  })

  test('saves an applied edit schedule after switching to another agent and back', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    await pauseQueue(page)
    await scheduleNextSend(page, 300_000)
    await sendMessage(page, 'round-trip scheduled input')
    await queuedInputRow(page, 'round-trip scheduled input').getByRole('button', { name: 'Edit', exact: true }).click()
    await expect(composerEditor(page)).toHaveText('round-trip scheduled input')
    await fillComposer(page, 'round-trip edited input')
    await scheduleNextSend(page, 600_000)
    await page.getByTestId('schedule-send-trigger').click()
    const applied = await page.getByTestId('schedule-send-field').inputValue()
    await page.getByTestId('schedule-send-cancel').click()

    await openAgentViaUI(page)
    await expect(composerEditor(page)).toHaveText('')
    await agentTabs(page).first().click()
    await expect(composerEditor(page)).toHaveText('round-trip edited input')
    await expect(page.getByTestId('schedule-send-trigger')).toHaveAttribute('data-scheduled')
    await page.locator('[data-testid="send-button"]:visible').click()

    const saved = queuedInputRow(page, 'round-trip edited input')
    await expect(saved).toContainText('Scheduled for')
    await expect(page.getByTestId(QUEUED_INPUT_ROW_TESTID)).toHaveCount(1)
    await saved.getByRole('button', { name: 'Edit', exact: true }).click()
    await expect(composerEditor(page)).toHaveText('round-trip edited input')
    await page.getByTestId('schedule-send-trigger').click()
    await expect(page.getByTestId('schedule-send-field')).toHaveValue(applied)
  })

  test('keeps a scheduled message through a reload, then reschedules and clears it in an edit', async ({ page, authenticatedWorkspace, modelScript }) => {
    void authenticatedWorkspace
    // Establish a live provider session first, as the file's first test does:
    // the cleared-schedule delivery at the end dispatches into a process that
    // is still answering, long after the fixture's idle auto-start would exit.
    await sendScriptedTurn(page, modelScript)
    // A far-enough deadline that the reload and the row assertions all happen
    // while the item is still future.
    await scheduleNextSend(page, 90_000)
    await sendMessage(page, 'scheduled hello')

    const row = queuedInputRow(page, 'scheduled hello')
    await expect(row).toContainText('Message')
    await expect(row).toContainText('Scheduled for')

    await page.reload()
    await expect(queuedInputRow(page, 'scheduled hello')).toContainText('Scheduled for')

    // Editing the schedule through the real queue edit moves the deadline
    // further out; clearing it delivers immediately through the ordinary drain.
    const cleared = await modelScript.queue({ text: `DELIVERED_${Date.now()}` })
    await row.getByRole('button', { name: 'Edit', exact: true }).click()
    await expect(composerEditor(page)).toHaveText('scheduled hello')
    // The edit changes the text and moves the schedule; both save together.
    await fillComposer(page, 'scheduled hello edited')
    await scheduleNextSend(page, 150_000)
    // The schedule popover held the focus; the save chord belongs to the
    // editor, so hand the focus back before pressing it.
    await composerEditor(page).click()
    await page.keyboard.press(`${PLATFORM_MOD}+Enter`)
    const edited = queuedInputRow(page, 'scheduled hello edited')
    await expect(edited).toContainText('Scheduled for')

    await edited.getByRole('button', { name: 'Edit', exact: true }).click()
    await expect(composerEditor(page)).toHaveText('scheduled hello edited')
    await page.getByTestId('schedule-send-trigger').click()
    await expect(page.getByTestId('schedule-send-popover')).toBeVisible()
    await page.getByTestId('schedule-send-clear').click()
    await composerEditor(page).click()
    await page.keyboard.press(`${PLATFORM_MOD}+Enter`)
    await modelScript.waitForSteps(cleared + 1)
    await expect(page.getByTestId(QUEUED_INPUT_ROW_TESTID)).toHaveCount(0)
  })

  test('delivers a scheduled message once its deadline passes', async ({ page, authenticatedWorkspace, modelScript }) => {
    void authenticatedWorkspace
    // The matured input dispatches into a process that is already answering.
    await sendScriptedTurn(page, modelScript)
    const delivered = await modelScript.queue({ text: `MATURED_${Date.now()}` })
    // Far enough out that the row renders as scheduled first. The Worker's
    // maturity timer, and nothing in this test, delivers it: the ordered step
    // waits on its own budget, which outlasts the deadline.
    await scheduleNextSend(page, 15_000)
    await sendMessage(page, 'deliver at maturity')
    await expect(queuedInputRow(page, 'deliver at maturity')).toContainText('Scheduled for')

    await modelScript.waitForSteps(delivered + 1)
    await expect(page.getByTestId(QUEUED_INPUT_ROW_TESTID)).toHaveCount(0)
  })

  test('holds a matured scheduled input while the queue is paused, then delivers it on resume', async ({ page, authenticatedWorkspace, modelScript }) => {
    void authenticatedWorkspace
    // Establish a live provider session first, as the file's first test does:
    // the matured input dispatches into a process that is already answering.
    await sendScriptedTurn(page, modelScript)
    await pauseQueue(page)
    await sendMessage(page, 'waits immediately')
    await scheduleNextSend(page, 10_000)
    await sendMessage(page, 'pause at maturity')
    const immediate = queuedInputRow(page, 'waits immediately')
    const scheduled = queuedInputRow(page, 'pause at maturity')
    await expect(scheduled).toContainText('Scheduled for')
    // A future row shares no reorder group with an immediate one.
    await expect(scheduled.getByRole('button', { name: 'Move Up' })).toBeDisabled()

    // Maturity moves the row into the immediate group, and the Worker
    // publishes that move even while the queue is paused. The two rows then
    // share one segment, so Move Up turns on: proof that the deadline passed.
    // The default expect budget outlasts the ten-second deadline.
    await expect(scheduled.getByRole('button', { name: 'Move Up' })).toBeEnabled()
    await expect(immediate.getByRole('button', { name: 'Move Down' })).toBeEnabled()
    // Nothing dispatched while paused: both rows still wait, and the matured
    // row keeps stating its schedule.
    await expect(page.getByTestId(QUEUED_INPUT_ROW_TESTID)).toHaveCount(2)
    await expect(scheduled).toContainText('Queued')
    await expect(scheduled).toContainText('Scheduled for')

    const resumed = await modelScript.queue(
      { text: `RESUMED_FIRST_${Date.now()}` },
      { text: `RESUMED_SECOND_${Date.now()}` },
    )
    await resumePausedQueue(page)
    await modelScript.waitForSteps(resumed + 2)
    await expect(page.getByTestId(QUEUED_INPUT_ROW_TESTID)).toHaveCount(0)
  })

  test('keeps a scheduled row reorderable only inside its own group', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    await expect(composerEditor(page)).toBeVisible()
    await pauseQueue(page)
    // One future item, one immediate: neither has a compatible neighbour, so
    // both arrow pairs refuse.
    await scheduleNextSend(page, 120_000)
    await sendMessage(page, 'the future one')
    await sendMessage(page, 'the immediate one')
    const future = queuedInputRow(page, 'the future one')
    const immediate = queuedInputRow(page, 'the immediate one')
    // The immediate row takes the effective first position although it was
    // queued second.
    await expect(page.getByTestId(QUEUED_INPUT_ROW_TESTID).first()).toContainText('the immediate one')
    for (const row of [future, immediate]) {
      await expect(row.getByRole('button', { name: 'Move Up' })).toBeDisabled()
      await expect(row.getByRole('button', { name: 'Move Down' })).toBeDisabled()
    }
  })
})

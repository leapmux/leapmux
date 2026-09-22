import type { Locator } from '@playwright/test'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest, expect } from './codex-fixtures'
import { bashToolCall, editToolCall } from './helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from './helpers/ui'

/**
 * The file-change statistics badge, on one file and on several.
 *
 * The changes arrive through Codex's own `apply_patch`, scripted at the mock
 * endpoint, so the rows carry exactly the shape the provider emits. An earlier
 * version wrote the `fileChange` messages straight into the worker database,
 * which made the fixture a second, hand-maintained copy of that shape.
 */
async function badgePresentation(badge: Locator) {
  await expect(badge).toBeVisible()
  return badge.evaluate((element) => {
    const style = globalThis.getComputedStyle(element)
    const title = element.parentElement
    const previous = element.previousElementSibling
    const box = element.getBoundingClientRect()
    const previousBox = previous?.getBoundingClientRect()
    return {
      fontFamily: style.fontFamily,
      fontSize: style.fontSize,
      lineHeight: style.lineHeight,
      titleGap: title ? globalThis.getComputedStyle(title).gap : null,
      gap: previousBox ? Math.round((box.left - previousBox.right) * 100) / 100 : null,
    }
  })
}

/**
 * The file-change row that names `path`.
 *
 * Both filters are load-bearing. `has` keeps the badge-carrying row: the user
 * prompt names the same file and comes first, so a text filter alone returns a
 * row with no badge in it. `:visible` keeps the on-screen copy, because ChatView
 * renders every unmeasured row twice.
 */
function changeRow(page: Parameters<typeof sendMessage>[0], path: string) {
  return page.locator('[data-seq]:visible')
    .filter({ has: page.getByTestId('git-diff-stats') })
    .filter({ hasText: path })
    .first()
}

codexTest('file-change statistics keep one presentation for one file and multiple files', async ({ page, authenticatedCodexWorkspace, modelScript }) => {
  void authenticatedCodexWorkspace

  // The files must exist before the patch, so the change is an UPDATE and the
  // diff has both sides to state.
  await modelScript.queue(
    { toolCalls: [bashToolCall(AgentProvider.CODEX, 'seed-files', 'printf "old\n" > single.ts; printf "old\n" > first.ts; printf "old\n" > second.ts; ls')] },
    { text: 'The files exist now.' },
  )
  await sendMessage(page, modelScript.prompt('Create the files.'))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)

  // One turn changes a single file, the next changes two. Codex reports each
  // changed file as its OWN `fileChange` item, so the second turn draws two
  // rows rather than one row naming two files — which is the shape this test
  // compares against the single-file row.
  await modelScript.queue(
    { toolCalls: [editToolCall(AgentProvider.CODEX, 'single-edit', { path: 'single.ts', before: 'old', after: 'new' })] },
    { text: 'I changed single.ts.' },
  )
  await sendMessage(page, modelScript.prompt('Change single.ts.'))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)

  await modelScript.queue(
    {
      toolCalls: [
        editToolCall(AgentProvider.CODEX, 'first-edit', { path: 'first.ts', before: 'old', after: 'new' }),
        editToolCall(AgentProvider.CODEX, 'second-edit', { path: 'second.ts', before: 'old', after: 'new' }),
      ],
    },
    { text: 'I changed first.ts and second.ts.' },
  )
  await sendMessage(page, modelScript.prompt('Change first.ts and second.ts.'))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)

  const single = await badgePresentation(changeRow(page, 'single.ts').getByTestId('git-diff-stats').first())
  const multiple = await badgePresentation(changeRow(page, 'first.ts').getByTestId('git-diff-stats').first())

  expect(single).toEqual(multiple)
  expect(single.titleGap).not.toBe('normal')
  expect(single.gap).toBeGreaterThan(0)
})

import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
/**
 * The typed background-task key keeps fresh native input and stored identity
 * apart. Two native Cursor call IDs that collide in a naive public-string
 * namespace -- one carrying the control characters Cursor's Run stream embeds
 * in a tool call id, one shaped exactly like LeapMux's own derived-key prefix
 * -- must open two distinct rows, address two distinct children, and leave no
 * control character in the DOM.
 */
import { expect } from '@playwright/test'
import { cursorTest } from './cursor-fixtures'
import { currentNativeAgent, toolTurnSteps } from './helpers/nativeScenario'
import { spawnSubagentToolCall } from './helpers/providerToolCalls'
import { expectSectionPersists } from './helpers/subagentRegistry'
import { assistantBubbles, sendMessage, waitForAgentIdle } from './helpers/ui'

const CONTROL_ID = 'call_CTRL\n_ID\t205'
const RESERVED_ID = 'leapmux-derived-key:call-reserved-shaped-205'

cursorTest('keeps two colliding native Cursor identities apart', async ({ native }) => {
  const { page, modelScript } = native
  const parent = await currentNativeAgent(native)
  if (!parent.workingDir)
    throw new Error('The key identity proof requires a private working directory.')
  const marker = 'CURSORKEY205'
  const file = join(parent.workingDir, 'key-identity-205.txt')
  writeFileSync(file, `${marker}\n`)

  const spawnControl = spawnSubagentToolCall(native.provider, CONTROL_ID, {
    description: 'Read the file with the control-character call id',
    prompt: modelScript.prompt('CURSORKEYCONTROL: reply with the single word CONTROLID.'),
    nativeExecution: { modelId: 'mock-grok' },
  })
  const spawnReserved = spawnSubagentToolCall(native.provider, RESERVED_ID, {
    description: 'Read the file with the reserved-shaped call id',
    prompt: modelScript.prompt('CURSORKEYRESERVED: reply with the single word RESERVEDID.'),
    nativeExecution: { modelId: 'mock-grok' },
  })
  await modelScript.rule(
    {
      name: 'the control-id child answers',
      when: { user: '^CURSORKEYCONTROL:' },
      once: true,
      respond: { text: 'CONTROLID' },
    },
    {
      name: 'the reserved-id child answers',
      when: { user: '^CURSORKEYRESERVED:' },
      once: true,
      respond: { text: 'RESERVEDID' },
    },
  )
  const start = await modelScript.queue(...toolTurnSteps([spawnControl, spawnReserved], { text: 'Both native children reported.' }, 'same-step'))
  await sendMessage(page, modelScript.prompt('Delegate one child per call id and report both words.'), 'type')
  await modelScript.waitForSteps(start + 1)
  await waitForAgentIdle(page)

  // Two child rows, two distinct stored identities, and no control byte in
  // the DOM. Cursor's parent turn also reports its own tool-call row, so the
  // child rows are the subagent-kind ones.
  const rows = native.page.locator('[data-testid="bg-task-row"][data-kind="subagent"]:visible')
  await expect(rows).toHaveCount(2)
  const html = await rows.evaluateAll(elements => elements.map(element => element.outerHTML).join(''))
  const hasControl = Array.from(html).some(ch => (ch.codePointAt(0) ?? 0) < 0x20)
  expect(hasControl, 'no row attribute may carry a control character').toBe(false)
  const ids = await rows.evaluateAll(elements => elements.map(element => element.getAttribute('data-task-id') ?? ''))
  expect(new Set(ids).size, 'the two native identities must not collapse into one row').toBe(2)
  expect(ids.some(id => id.startsWith('leapmux-derived-key:')), 'a reserved-shaped native key escapes, never passing through').toBe(false)
  // The DOM identity replaces each control character, so the raw key never
  // reaches a selector or a snapshot even while the registry keeps exact bytes.
  expect(ids).toContain('call_CTRL\uFFFD_ID\uFFFD205')

  // Each child's own word reached its own transcript through its own identity.
  await expect(assistantBubbles(page).filter({ hasText: 'CONTROLID' })).toBeVisible()
  await expect(assistantBubbles(page).filter({ hasText: 'RESERVEDID' })).toBeVisible()
  await expectSectionPersists(page)

  await page.reload()
  await waitForAgentIdle(page)
  await expect(native.page.locator('[data-testid="bg-task-row"][data-kind="subagent"]:visible')).toHaveCount(2)
})

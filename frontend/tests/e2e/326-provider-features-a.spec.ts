import type { Page } from '@playwright/test'
import type { AttachmentKind } from './helpers/attachments'
import type { MockModelRequestRecord } from './helpers/mockModelScript'
import type { ModelScript } from './helpers/modelScriptFixture'
import { formatTokenCount } from '../../src/components/chat/rendererUtils'
import { COPILOT_MODE, COPILOT_OPTION } from '../../src/generated/contracts/copilot-protocol'
import { CONTEXT_USAGE_FIELD } from '../../src/generated/contracts/session-info'
import { AgentOptionSettlementState, AgentProvider, UpdateAgentSettingsRequestSchema, UpdateAgentSettingsResponseSchema } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickNumber } from '../../src/lib/jsonPick'
import { codexTest } from './codex-fixtures'
import { COPILOT_E2E_SKIP_REASON, copilotTest } from './copilot-fixtures'
import { CURSOR_E2E_SKIP_REASON, cursorTest } from './cursor-fixtures'
import { GOOSE_E2E_SKIP_REASON, gooseTest } from './goose-fixtures'
import { getTestChannel } from './helpers/api'
import { expectNativeAttachmentProof, expectRefusedAttachmentsAbsent } from './helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment } from './helpers/attachments'
import { exerciseContextUsage, expectContextUsage } from './helpers/contextUsage'
import { watchAgentContextUsage } from './helpers/contextUsageEvents'
import { MOCK_MODELS } from './helpers/mockAgentEnvironment'
import { askUserQuestionToolCall, updateTodosToolCall } from './helpers/providerToolCalls'
import { expandGoalsAndTodosSection, goalsAndTodosSection } from './helpers/subagentRegistry'
import { assistantBubbles, chooseSettingsOption, expectSettingsChip, expectSettingsOptionChosen, expectUserMessage, openAgentInfoCard, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from './helpers/ui'
import { waitForAgentStartupViaAPI } from './helpers/worktree'
import { expect, KILO_E2E_SKIP_REASON, kiloTest } from './kilo-fixtures'
import { OPENCODE_E2E_SKIP_REASON, opencodeTest } from './opencode-fixtures'
import { PI_E2E_SKIP_REASON, piTest } from './pi-fixtures'
import { REASONIX_E2E_SKIP_REASON, reasonixTest } from './reasonix-fixtures'
import { ZCODE_E2E_SKIP_REASON, zcodeTest } from './zcode-fixtures'

async function proveAttachment(page: Page, modelScript: ModelScript, kind: AttachmentKind, filename: string): Promise<void> {
  await modelScript.queue({ text: 'Attachment received.' })
  const sourcePath = await expectAttachmentOutcome(page, kind, { supported: true, fileName: filename })
  await sendWithAttachment(page, modelScript.prompt('Read the attached file.'))
  const status = await modelScript.waitForSteps()
  await expectNativeAttachmentProof(page, status, kind, sourcePath)
  await waitForAgentIdle(page)
  await expectUserMessage(page, filename)
  await expect(assistantBubbles(page).filter({ hasText: 'Attachment received.' }).first()).toBeVisible()
}

kiloTest.describe('Kilo attachment support', () => {
  kiloTest.skip(!!KILO_E2E_SKIP_REASON, KILO_E2E_SKIP_REASON || '')
  for (const { kind, filename } of [
    { kind: 'text', filename: 'kilo-notes.txt' },
    { kind: 'image', filename: 'kilo-shot.png' },
    { kind: 'pdf', filename: 'kilo-doc.pdf' },
  ] satisfies Array<{ kind: AttachmentKind, filename: string }>) {
    kiloTest(`delivers ${kind === 'image' ? 'an' : 'a'} ${kind} attachment to the model`, async ({ authenticatedKiloWorkspace, page, modelScript }) => {
      void authenticatedKiloWorkspace
      await proveAttachment(page, modelScript, kind, filename)
    })
  }

  kiloTest('keeps a refused binary attachment out of the next request', async ({ authenticatedKiloWorkspace, page, modelScript }) => {
    void authenticatedKiloWorkspace
    const rejected = await expectAttachmentOutcome(page, 'binary', { supported: false, fileName: 'kilo-blob.bin' })
    await expectRefusedAttachmentsAbsent(page, modelScript, [rejected])
  })
})

codexTest.describe('Codex attachment support', () => {
  codexTest('delivers a text attachment to the model', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace
    await proveAttachment(page, modelScript, 'text', 'codex-notes.txt')
  })

  codexTest('delivers an image attachment to the model', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace
    await proveAttachment(page, modelScript, 'image', 'codex-shot.png')
  })
})

cursorTest.describe('Cursor attachment support', () => {
  cursorTest.skip(!!CURSOR_E2E_SKIP_REASON, CURSOR_E2E_SKIP_REASON || '')
  for (const { kind, filename } of [
    { kind: 'text', filename: 'cursor-notes.txt' },
    { kind: 'image', filename: 'cursor-shot.png' },
  ] satisfies Array<{ kind: AttachmentKind, filename: string }>) {
    cursorTest(`delivers ${kind === 'image' ? 'an' : 'a'} ${kind} attachment to the model`, async ({ authenticatedCursorWorkspace, page, modelScript }) => {
      void authenticatedCursorWorkspace
      await proveAttachment(page, modelScript, kind, filename)
    })
  }

  cursorTest('keeps refused PDF and binary attachments out of the next request', async ({ authenticatedCursorWorkspace, page, modelScript }) => {
    void authenticatedCursorWorkspace
    const pdf = await expectAttachmentOutcome(page, 'pdf', { supported: false, fileName: 'cursor-doc.pdf' })
    const binary = await expectAttachmentOutcome(page, 'binary', { supported: false, fileName: 'cursor-blob.bin' })
    await expectRefusedAttachmentsAbsent(page, modelScript, [pdf, binary])
  })
})

copilotTest.describe('Copilot attachment support', () => {
  copilotTest.skip(!!COPILOT_E2E_SKIP_REASON, COPILOT_E2E_SKIP_REASON || '')
  for (const { kind, filename } of [
    { kind: 'text', filename: 'copilot-notes.txt' },
    { kind: 'image', filename: 'copilot-shot.png' },
  ] satisfies Array<{ kind: AttachmentKind, filename: string }>) {
    copilotTest(`delivers ${kind === 'image' ? 'an' : 'a'} ${kind} attachment to the model`, async ({ authenticatedCopilotWorkspace, page, modelScript }) => {
      void authenticatedCopilotWorkspace
      await proveAttachment(page, modelScript, kind, filename)
    })
  }

  copilotTest('keeps a refused PDF attachment out of the next request', async ({ authenticatedCopilotWorkspace, page, modelScript }) => {
    void authenticatedCopilotWorkspace
    const rejected = await expectAttachmentOutcome(page, 'pdf', { supported: false, fileName: 'copilot-doc.pdf' })
    await expectRefusedAttachmentsAbsent(page, modelScript, [rejected])
  })

  copilotTest('keeps a refused binary attachment out of the next request', async ({ authenticatedCopilotWorkspace, page, modelScript }) => {
    void authenticatedCopilotWorkspace
    const rejected = await expectAttachmentOutcome(page, 'binary', { supported: false, fileName: 'copilot-blob.bin' })
    await expectRefusedAttachmentsAbsent(page, modelScript, [rejected])
  })
})

gooseTest.describe('Goose attachment support', () => {
  gooseTest.skip(!!GOOSE_E2E_SKIP_REASON, GOOSE_E2E_SKIP_REASON || '')
  for (const { kind, filename } of [
    { kind: 'text', filename: 'goose-notes.txt' },
    { kind: 'image', filename: 'goose-shot.png' },
  ] satisfies Array<{ kind: AttachmentKind, filename: string }>) {
    gooseTest(`delivers ${kind === 'image' ? 'an' : 'a'} ${kind} attachment to the model`, async ({ authenticatedGooseWorkspace, page, modelScript }) => {
      void authenticatedGooseWorkspace
      await proveAttachment(page, modelScript, kind, filename)
    })
  }

  gooseTest('keeps refused PDF and binary attachments out of the next request', async ({ authenticatedGooseWorkspace, page, modelScript }) => {
    void authenticatedGooseWorkspace
    const pdf = await expectAttachmentOutcome(page, 'pdf', { supported: false, fileName: 'goose-doc.pdf' })
    const binary = await expectAttachmentOutcome(page, 'binary', { supported: false, fileName: 'goose-blob.bin' })
    await expectRefusedAttachmentsAbsent(page, modelScript, [pdf, binary])
  })
})

piTest.describe('Pi attachment support', () => {
  piTest.skip(!!PI_E2E_SKIP_REASON, PI_E2E_SKIP_REASON || '')
  piTest('delivers a text attachment to the model', async ({ authenticatedPiWorkspace, page, modelScript }) => {
    void authenticatedPiWorkspace
    await proveAttachment(page, modelScript, 'text', 'pi-notes.txt')
  })
  piTest('delivers an image attachment to the model', async ({ authenticatedPiWorkspace, page, modelScript }) => {
    void authenticatedPiWorkspace
    await proveAttachment(page, modelScript, 'image', 'pi-shot.png')
  })
})

reasonixTest.describe('Reasonix attachment support', () => {
  reasonixTest.skip(!!REASONIX_E2E_SKIP_REASON, REASONIX_E2E_SKIP_REASON || '')
  reasonixTest('delivers a text attachment to the model', async ({ authenticatedReasonixWorkspace, page, modelScript }) => {
    void authenticatedReasonixWorkspace
    await proveAttachment(page, modelScript, 'text', 'reasonix-notes.txt')
  })
})

zcodeTest.describe('ZCode attachment support', () => {
  zcodeTest.skip(!!ZCODE_E2E_SKIP_REASON, ZCODE_E2E_SKIP_REASON || '')
  zcodeTest('delivers a text attachment to the model', async ({ authenticatedZCodeWorkspace, page, modelScript }) => {
    void authenticatedZCodeWorkspace
    await proveAttachment(page, modelScript, 'text', 'zcode-notes.txt')
  })
  zcodeTest('delivers an image attachment to the model', async ({ authenticatedZCodeWorkspace, page, modelScript }) => {
    void authenticatedZCodeWorkspace
    await proveAttachment(page, modelScript, 'image', 'zcode-shot.png')
  })
})

kiloTest.describe('Kilo sidebar', () => {
  kiloTest.skip(!!KILO_E2E_SKIP_REASON, KILO_E2E_SKIP_REASON || '')
  kiloTest('keeps the to-do list after a reload', async ({ authenticatedKiloWorkspace, page, modelScript }) => {
    void authenticatedKiloWorkspace
    await modelScript.queue(
      { toolCalls: [updateTodosToolCall(AgentProvider.KILO, 'kilo-todos', [
        { step: 'Inspect the repository', status: 'completed' },
        { step: 'Report the finding', status: 'in_progress' },
      ])] },
      { text: 'The list is ready.' },
    )
    await sendMessage(page, modelScript.prompt('Write a two-step to-do list.'))
    await modelScript.waitForSteps(2)
    await waitForAgentIdle(page)
    await expect(goalsAndTodosSection(page)).toBeVisible()
    await expandGoalsAndTodosSection(page)
    const list = page.locator('[data-testid="goals-and-todos"]:visible')
    await expect(list).toContainText('Inspect the repository')
    await expect(list).toContainText('Report the finding')
    await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(1)
    await expect(list.locator('[data-task-checkbox="in_progress"]')).toHaveCount(1)
    await page.reload()
    await expandGoalsAndTodosSection(page)
    await expect(list).toContainText('Report the finding')
  })

  kiloTest('shows the context usage that the model reports', async ({ authenticatedKiloWorkspace, page, modelScript }) => {
    void authenticatedKiloWorkspace
    const usage = { inputTokens: 12_000, outputTokens: 40 }
    await modelScript.queue({ text: 'Usage recorded.', usage })
    await sendMessage(page, modelScript.prompt('Reply once.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expectContextUsage(page, usage)
  })
})

cursorTest.describe('Cursor context usage', () => {
  cursorTest.skip(!!CURSOR_E2E_SKIP_REASON, CURSOR_E2E_SKIP_REASON || '')
  cursorTest('omits context usage when its ACP bridge sends no usage update', async ({ authenticatedCursorWorkspace, page, modelScript }) => {
    void authenticatedCursorWorkspace
    await modelScript.queue({ text: 'Usage recorded.', usage: { inputTokens: 12_000, outputTokens: 40 } })
    await sendMessage(page, modelScript.prompt('Reply once.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'Usage recorded.' }).first()).toBeVisible()
    const popover = await openAgentInfoCard(page)
    await expect(popover.getByText('Context', { exact: true })).toHaveCount(0)
  })
})

copilotTest.describe('Copilot context usage', () => {
  copilotTest.skip(!!COPILOT_E2E_SKIP_REASON, COPILOT_E2E_SKIP_REASON || '')
  copilotTest('shows the current tokens from its native usage event', async ({ authenticatedCopilotWorkspace, page, modelScript, leapmuxServer }) => {
    void authenticatedCopilotWorkspace
    const agentId = await page.locator('[data-testid="tab"][data-tab-type="agent"]').first().getAttribute('data-tab-id') ?? ''
    expect(agentId).not.toBe('')
    const watch = await watchAgentContextUsage(leapmuxServer, agentId)
    try {
      const nativeTokenReadings = () => watch.readings().filter((reading) => {
        const tokens = pickNumber(reading, CONTEXT_USAGE_FIELD.ContextTokens)
        return tokens !== null && tokens > 0
      })
      const before = pickNumber(nativeTokenReadings().at(-1), CONTEXT_USAGE_FIELD.ContextTokens) ?? 0
      const beforeCount = nativeTokenReadings().length
      await modelScript.queue({ text: 'Usage recorded.' })
      await sendMessage(page, modelScript.prompt('Reply once.'))
      await modelScript.waitForSteps()
      await waitForAgentIdle(page)
      await expect.poll(() => nativeTokenReadings().length).toBeGreaterThan(beforeCount)
      const usage = nativeTokenReadings().at(-1)
      const currentTokens = pickNumber(usage, CONTEXT_USAGE_FIELD.ContextTokens)
      if (currentTokens === null)
        throw new Error('The native Copilot usage update has no token count.')
      expect(currentTokens).toBeGreaterThan(before)
      const popover = await openAgentInfoCard(page)
      await expect(popover).toContainText(formatTokenCount(currentTokens))
      const tokenLimit = pickNumber(usage, CONTEXT_USAGE_FIELD.ContextWindow)
      if (tokenLimit !== null)
        await expect(popover).toContainText(formatTokenCount(tokenLimit))
    }
    finally {
      watch.cancel()
    }
  })
})

opencodeTest.describe('OpenCode context usage', () => {
  opencodeTest.skip(!!OPENCODE_E2E_SKIP_REASON, OPENCODE_E2E_SKIP_REASON || '')
  opencodeTest('shows the context usage that the model reports', async ({ authenticatedOpencodeWorkspace, page, modelScript }) => {
    void authenticatedOpencodeWorkspace
    await exerciseContextUsage(page, modelScript)
  })
})

gooseTest.describe('Goose context usage', () => {
  gooseTest.skip(!!GOOSE_E2E_SKIP_REASON, GOOSE_E2E_SKIP_REASON || '')
  gooseTest('shows the context usage that the model reports', async ({ authenticatedGooseWorkspace, page, modelScript }) => {
    void authenticatedGooseWorkspace
    await exerciseContextUsage(page, modelScript)
  })
})

piTest.describe('Pi context usage', () => {
  piTest.skip(!!PI_E2E_SKIP_REASON, PI_E2E_SKIP_REASON || '')
  piTest('shows the context usage that the model reports', async ({ authenticatedPiWorkspace, page, modelScript }) => {
    void authenticatedPiWorkspace
    await exerciseContextUsage(page, modelScript)
  })
})

reasonixTest.describe('Reasonix context usage', () => {
  reasonixTest.skip(!!REASONIX_E2E_SKIP_REASON, REASONIX_E2E_SKIP_REASON || '')
  reasonixTest('shows the context usage that the model reports', async ({ authenticatedReasonixWorkspace, page, modelScript }) => {
    void authenticatedReasonixWorkspace
    await exerciseContextUsage(page, modelScript)
  })
})

zcodeTest.describe('ZCode context usage', () => {
  zcodeTest.skip(!!ZCODE_E2E_SKIP_REASON, ZCODE_E2E_SKIP_REASON || '')
  zcodeTest('shows the context usage that the model reports', async ({ authenticatedZCodeWorkspace, page, modelScript }) => {
    void authenticatedZCodeWorkspace
    await exerciseContextUsage(page, modelScript)
  })
})

async function sendSettingsProbe(page: Page, modelScript: ModelScript): Promise<MockModelRequestRecord> {
  await modelScript.queue({ text: 'Settings applied.' })
  await sendMessage(page, modelScript.prompt('Reply once after the settings change.'))
  const status = await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  await expect(assistantBubbles(page).filter({ hasText: 'Settings applied.' }).first()).toBeVisible()
  const request = status.requests.find(record => record.stepIndex === status.stepCount - 1)
  if (!request)
    throw new Error('the settings probe did not reach the model')
  return request
}

function latestUserInput(request: MockModelRequestRecord): string {
  const body = isObject(request.body) ? request.body : null
  const messages = body?.messages
  if (!Array.isArray(messages))
    return ''
  const lastUser = messages.findLast(message => isObject(message) && message.role === 'user')
  return JSON.stringify(lastUser ?? '')
}

kiloTest.describe('Kilo settings', () => {
  kiloTest.skip(!!KILO_E2E_SKIP_REASON, KILO_E2E_SKIP_REASON || '')
  kiloTest('keeps its Plan mode and effort after a turn and reload', async ({ authenticatedKiloWorkspace, page, modelScript }) => {
    void authenticatedKiloWorkspace
    const buildRequest = await sendSettingsProbe(page, modelScript)
    await chooseSettingsOption(page, 'effort-low')
    await chooseSettingsOption(page, 'primaryAgent-plan')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Plan')
    await expectSettingsChip(page, 'Low')
    const planRequest = await sendSettingsProbe(page, modelScript)
    expect(planRequest.protocol).toBe('openai-chat-completions')
    expect(planRequest.body).toMatchObject({ reasoning_effort: 'low' })
    expect(latestUserInput(buildRequest)).not.toContain('# Native Plan Mode')
    expect(latestUserInput(planRequest)).toContain('# Native Plan Mode')
    await page.reload()
    await expectSettingsChip(page, 'Plan')
    await expectSettingsChip(page, 'Low')
  })
})

opencodeTest.describe('OpenCode settings', () => {
  opencodeTest.skip(!!OPENCODE_E2E_SKIP_REASON, OPENCODE_E2E_SKIP_REASON || '')
  opencodeTest('keeps its Plan mode and effort after a turn and reload', async ({ authenticatedOpencodeWorkspace, page, modelScript }) => {
    void authenticatedOpencodeWorkspace
    const buildRequest = await sendSettingsProbe(page, modelScript)
    await chooseSettingsOption(page, 'effort-low')
    await chooseSettingsOption(page, 'primaryAgent-plan')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Plan')
    await expectSettingsChip(page, 'Low')
    const planRequest = await sendSettingsProbe(page, modelScript)
    expect(planRequest.protocol).toBe('openai-chat-completions')
    expect(planRequest.body).toMatchObject({ reasoning_effort: 'low' })
    expect(latestUserInput(buildRequest)).not.toContain('# Plan Mode - System Reminder')
    expect(latestUserInput(planRequest)).toContain('# Plan Mode - System Reminder')
    await page.reload()
    await expectSettingsChip(page, 'Plan')
    await expectSettingsChip(page, 'Low')
  })
})

gooseTest.describe('Goose effort', () => {
  gooseTest.skip(!!GOOSE_E2E_SKIP_REASON, GOOSE_E2E_SKIP_REASON || '')
  gooseTest('keeps the high effort after a turn and reload', async ({ authenticatedGooseWorkspace, page, modelScript }) => {
    void authenticatedGooseWorkspace
    await chooseSettingsOption(page, `model-${MOCK_MODELS.gooseReasoning}`)
    await expectSettingsChip(page, MOCK_MODELS.gooseReasoning)
    await chooseSettingsOption(page, 'thinking_effort-high')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'High')
    const request = await sendSettingsProbe(page, modelScript)
    expect(request.protocol).toBe('openai-responses')
    expect(request.body).toMatchObject({ model: MOCK_MODELS.gooseReasoning, reasoning: { effort: 'high' } })
    await page.reload()
    await expectSettingsChip(page, MOCK_MODELS.gooseReasoning)
    await expectSettingsChip(page, 'High')
  })
})

piTest.describe('Pi effort', () => {
  piTest.skip(!!PI_E2E_SKIP_REASON, PI_E2E_SKIP_REASON || '')
  piTest('keeps the low effort after a turn and reload', async ({ authenticatedPiWorkspace, page, modelScript }) => {
    void authenticatedPiWorkspace
    await chooseSettingsOption(page, `model-${MOCK_MODELS.zai}`)
    await chooseSettingsOption(page, 'effort-low')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Low')
    const request = await sendSettingsProbe(page, modelScript)
    expect(request.protocol).toBe('openai-chat-completions')
    expect(request.body).toMatchObject({ model: MOCK_MODELS.zai, reasoning_effort: 'low' })
    await page.reload()
    await expectSettingsChip(page, 'Low')
  })
})

zcodeTest.describe('ZCode effort', () => {
  zcodeTest.skip(!!ZCODE_E2E_SKIP_REASON, ZCODE_E2E_SKIP_REASON || '')
  zcodeTest('keeps the low effort after a turn and reload', async ({ authenticatedZCodeWorkspace, page, modelScript }) => {
    void authenticatedZCodeWorkspace
    await chooseSettingsOption(page, 'effort-low')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Low')
    const request = await sendSettingsProbe(page, modelScript)
    expect(request.protocol).toBe('openai-chat-completions')
    expect(request.body).toMatchObject({ reasoning_effort: 'low' })
    await page.reload()
    await expectSettingsChip(page, 'Low')
  })
})

cursorTest.describe('Cursor model and mode', () => {
  cursorTest.skip(!!CURSOR_E2E_SKIP_REASON, CURSOR_E2E_SKIP_REASON || '')
  cursorTest('keeps a selected model and Plan mode after a turn and reload', async ({ authenticatedCursorWorkspace, page, modelScript }) => {
    void authenticatedCursorWorkspace
    const model = 'model-mock-grok[context=256k,reasoning_effort=low]'
    await chooseSettingsOption(page, model)
    await chooseSettingsOption(page, 'permissionMode-plan')
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, model)
    await expectSettingsChip(page, 'Plan')
    await sendSettingsProbe(page, modelScript)
    await page.reload()
    await expectSettingsOptionChosen(page, model)
    await expectSettingsChip(page, 'Plan')
  })

  cursorTest('confirms Plan mode through the native set-mode RPC', async ({ authenticatedCursorWorkspace, page, modelScript, leapmuxServer }) => {
    const agents = await waitForAgentStartupViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedCursorWorkspace.workspaceId)
    expect(agents).toHaveLength(1)
    await waitForSettingsHydrated(page)
    const agent = agents[0]
    if (!agent)
      throw new Error('Cursor has no running agent for the mode request')
    const channel = await getTestChannel(leapmuxServer.hubUrl, leapmuxServer.adminToken)
    const response = await channel.callWorker(leapmuxServer.workerId, 'UpdateAgentSettings', UpdateAgentSettingsRequestSchema, UpdateAgentSettingsResponseSchema, {
      agentId: agent.id,
      settings: { options: { permissionMode: 'plan' } },
    })
    expect(response.optionSettlements.permissionMode?.state).toBe(AgentOptionSettlementState.CONFIRMED)
    expect(response.optionSettlements.permissionMode?.value).toBe('plan')
    await page.reload()
    await expectSettingsChip(page, 'Plan')
    await sendSettingsProbe(page, modelScript)
    await page.reload()
    await expectSettingsChip(page, 'Plan')
  })
})

copilotTest.describe('Copilot mode and effort', () => {
  copilotTest.skip(!!COPILOT_E2E_SKIP_REASON, COPILOT_E2E_SKIP_REASON || '')
  copilotTest('keeps Plan mode and low effort after a turn and reload', async ({ authenticatedCopilotWorkspace, page, modelScript }) => {
    void authenticatedCopilotWorkspace
    const mode = `${COPILOT_OPTION.SessionMode}-${COPILOT_MODE.Plan}`
    await chooseSettingsOption(page, 'effort-low')
    await chooseSettingsOption(page, mode)
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, mode)
    await expectSettingsChip(page, 'Low')
    const request = await sendSettingsProbe(page, modelScript)
    expect(request.protocol).toBe('openai-chat-completions')
    expect(request.body).toMatchObject({
      reasoning_effort: 'low',
      tools: expect.arrayContaining([expect.objectContaining({ function: expect.objectContaining({ name: 'exit_plan_mode' }) })]),
    })
    await page.reload()
    await expectSettingsOptionChosen(page, mode)
    await expectSettingsChip(page, 'Low')
  })
})

async function answerOpenCodeQuestion(page: Page, modelScript: ModelScript, provider: AgentProvider): Promise<void> {
  await modelScript.queue(
    { toolCalls: [askUserQuestionToolCall(provider, 'color-question', [{
      question: 'Pick a color.',
      header: 'Color',
      options: [
        { label: 'Blue', description: 'Use blue.' },
        { label: 'Green', description: 'Use green.' },
      ],
    }])] },
    { text: 'The answer was recorded.' },
  )
  await sendMessage(page, modelScript.prompt('Ask me to pick a color.'))
  await modelScript.waitForSteps(1)
  const banner = page.getByTestId('control-banner').filter({ visible: true })
  await expect(banner).toContainText('Pick a color.')
  await banner.getByTestId('question-option-Green').click()
  await page.getByTestId('control-submit-btn').click()
  const status = await modelScript.waitForSteps(2)
  await waitForAgentIdle(page)
  const request = status.requests.find(record => record.stepIndex === 1)
  expect(request?.protocol).toBe('openai-chat-completions')
  const body = isObject(request?.body) ? request.body : null
  const messages = Array.isArray(body?.messages) ? body.messages : []
  const result = messages.find(message => isObject(message) && message.role === 'tool' && message.tool_call_id === 'color-question')
  expect(result, 'the native question result reached the model').toBeDefined()
  const answer = JSON.stringify(isObject(result) ? result.content : '')
  expect(answer).toContain('Green')
  expect(answer).not.toContain('Blue')
  await expect(assistantBubbles(page).filter({ hasText: 'The answer was recorded.' }).first()).toBeVisible()
  await expect(banner).toHaveCount(0)
}

kiloTest.describe('Kilo questions', () => {
  kiloTest.skip(!!KILO_E2E_SKIP_REASON, KILO_E2E_SKIP_REASON || '')
  kiloTest('answers a native question and resumes the turn', async ({ authenticatedKiloWorkspace, page, modelScript }) => {
    void authenticatedKiloWorkspace
    await answerOpenCodeQuestion(page, modelScript, AgentProvider.KILO)
  })
})

opencodeTest.describe('OpenCode questions', () => {
  opencodeTest.skip(!!OPENCODE_E2E_SKIP_REASON, OPENCODE_E2E_SKIP_REASON || '')
  opencodeTest('answers a native question and resumes the turn', async ({ authenticatedOpencodeWorkspace, page, modelScript }) => {
    void authenticatedOpencodeWorkspace
    await answerOpenCodeQuestion(page, modelScript, AgentProvider.OPENCODE)
  })
})

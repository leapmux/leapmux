import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { LETTA_MODE } from '../../../src/generated/contracts/letta-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { chooseNativeModeWithNotice } from '../helpers/nativeModeChange'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { currentNativeAgent, nativeToolOutcome } from '../helpers/nativeScenario'
import { expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { nativeToolResultAt, runNativeToolTurn } from '../helpers/nativeToolExecution'
import { bashToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument, uniqueMarker } from '../helpers/shellArguments'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, chooseSettingsOption, expectAssistantAnswer, expectNoControlBanner, expectSettingsChip, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForNativeSettingsHydrated, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest.describe('Letta Code modes', () => {
  lettaTest('the mode menu lists Standard, Accept Edits, Unrestricted and Strict', async ({ authenticatedLettaWorkspace, page }) => {
    void authenticatedLettaWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsOptionsOffered(page, 'permissionMode', Object.values(LETTA_MODE))
  })

  lettaTest('a mode change reaches the chip and survives a reload', async ({ authenticatedLettaWorkspace, page }) => {
    void authenticatedLettaWorkspace
    await waitForSettingsHydrated(page)

    await chooseSettingsOption(page, 'permissionMode-standard')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Standard')

    await chooseSettingsOption(page, 'permissionMode-acceptEdits')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Accept Edits')

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Accept Edits')
  })

  lettaTest('asks in Standard and runs a write in Unrestricted', async ({ askingLettaWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({
      page,
      modelScript,
      leapmuxServer,
      workspaceId: askingLettaWorkspace.workspaceId,
    })
    const file = join(askingLettaWorkspace.workingDir, 'letta-mode-proof.txt')
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Standard')

    await exerciseNativePermissionDecision(context, {
      toolCall: writeToolCall(AgentProvider.LETTA, 'standard-mode-write', { path: file, content: 'standard\n' }),
      decision: 'deny',
      beforeDecision: () => expect(existsSync(file)).toBe(false),
      nativeProof: () => expect(existsSync(file)).toBe(false),
    })
    await expectNoControlBanner(page)

    await chooseSettingsOption(page, 'permissionMode-unrestricted')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Unrestricted')
    // Unrestricted runs the write without a banner, so the scenario answers no control.
    const unrestrictedStep = await modelScript.queue(
      {
        toolCalls: [
          writeToolCall(AgentProvider.LETTA, 'unrestricted-mode-write', { path: file, content: 'unrestricted\n' }),
        ],
      },
      { text: 'The Unrestricted write finished.' },
    )
    await sendMessage(page, modelScript.prompt('Write the proof file without asking.'))
    await modelScript.waitForSteps(unrestrictedStep + 2)
    await waitForAgentIdle(page)
    await expectNoControlBanner(page)
    expect(readFileSync(file, 'utf8')).toBe('unrestricted\n')
    expect(await nativeToolResultAt(modelScript, unrestrictedStep + 1, 'unrestricted-mode-write')).toContain('letta-mode-proof.txt')
  })

  lettaTest('returns from Strict to Standard and proves native approval behavior after reload', async ({ native }) => {
    const { page } = native
    await waitForNativeSettingsHydrated(page)
    await expectSettingsOptionsOffered(page, 'permissionMode', Object.values(LETTA_MODE))
    await expectSettingsChip(page, 'Unrestricted')
    await sendNativeAnswer(native, 'Reply before the approval mode sequence.', 'The Letta mode sequence started.')
    const agent = await currentNativeAgent(native)
    const readPath = join(agent.workingDir, 'letta-mode-read-proof.txt')
    const readContent = `LETTAMODE${40 + 2}_${uniqueMarker('MODE')}\n`
    expect(existsSync(readPath)).toBe(false)
    writeFileSync(readPath, readContent)
    for (const mode of [
      { value: LETTA_MODE.Strict, label: 'Strict', previousLabel: 'Unrestricted' },
      { value: LETTA_MODE.Standard, label: 'Standard', previousLabel: 'Strict' },
    ]) {
      const notice = await chooseNativeModeWithNotice(native, { groupId: 'permissionMode', ...mode, noticeLabel: 'Permissions' })
      for (const reload of [false, true]) {
        if (reload) {
          await page.reload()
          await waitForNativeSettingsHydrated(page)
        }
        await expectSettingsChip(page, mode.label)
        await expectSettingsOptionChosen(page, `permissionMode-${mode.value}`)
        const callId = `native-mode-${mode.value}-${reload}`
        // Standard auto-approves a native read-only cat command. Shell arithmetic requires approval in both modes.
        const toolCall = bashToolCall(AgentProvider.LETTA, callId, `cat ${quotePosixShellArgument(readPath)}`)
        const prove = async (request: MockModelRequestRecord) => {
          const outcome = await nativeToolOutcome(native, request, callId)
          expect(outcome.text).toContain('LETTAMODE42')
          expect(outcome.text).toContain(readContent.trim())
          expect(readFileSync(readPath, 'utf8')).toBe(readContent)
        }
        if (mode.value === LETTA_MODE.Strict) {
          await exerciseNativePermissionDecision(native, {
            toolCall,
            decision: 'allow',
            beforeDecision: banner => expect(banner).toContainText('Bash'),
            nativeProof: prove,
          })
        }
        else {
          await expectNoNativeControl(native, {
            testId: 'control-banner',
            relatedProof: async () => {
              const { resultRequest } = await runNativeToolTurn(native, {
                toolCalls: [toolCall],
                prompt: 'Run the read-only command in Standard mode.',
                answer: `The Standard command ended after reload ${reload}.`,
                permissions: 'none',
              })
              await prove(resultRequest)
            },
          })
          const writeId = `${callId}-write`
          const path = join(agent.workingDir, `${writeId}.txt`)
          const content = `The Standard write ran after reload ${reload}.\n`
          expect(existsSync(path)).toBe(false)
          await exerciseNativePermissionDecision(native, {
            toolCall: writeToolCall(AgentProvider.LETTA, writeId, { path, content }),
            decision: 'allow',
            beforeDecision: async (banner) => {
              await expect(banner).toContainText(`${writeId}.txt`)
              expect(existsSync(path)).toBe(false)
            },
            nativeProof: async (request) => {
              expect(readFileSync(path, 'utf8')).toBe(content)
              expect((await nativeToolOutcome(native, request, writeId)).text).toContain(`${writeId}.txt`)
            },
          })
        }
        await notice.assertStored()
      }
    }
  })
})

lettaTest.describe('Letta Code settings', () => {
  lettaTest('applies a permission-mode change to the session', async ({ authenticatedLettaWorkspace, page, modelScript }) => {
    void authenticatedLettaWorkspace
    await waitForSettingsHydrated(page)
    const step = await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps(step + 1)
    await waitForAgentIdle(page)
    await expectAssistantAnswer(page)

    // A settings change reaches the running session. The chip follows the value.
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, 'permissionMode-strict')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Strict')
  })
})

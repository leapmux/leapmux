import type { MockModelScenarioStatus } from '../helpers/mockModelScript'
import { Buffer } from 'node:buffer'
import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { toJson } from '@bufbuild/protobuf'
import { expect } from '@playwright/test'
import { AgentGoalStatus, BackgroundTaskKind, BackgroundTaskStatus, ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { getTestChannel } from '../helpers/api'
import { finishCleanup, withCleanup } from '../helpers/cleanup'
import { clearGoal, expandGoalsAndTodosSection, expectEmptyGoalCard, expectGoalObjective, expectGoalStatus, goalAction, openGoalMenu, scriptedObjective, setGoal } from '../helpers/goalsAndTodos'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent, nativeAgentById, nativeModelContextText } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { isFileNameComponent } from '../helpers/runDirectory'
import { getGlobalState } from '../helpers/server'
import { uniqueMarker } from '../helpers/shellArguments'
import { expandBackgroundTasksSection, openChildTabFromRow } from '../helpers/subagentRegistry'
import { assistantBubbles, messageContents, openWorkspace, sendMessage, tabById, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { kiroTest } from '../kiro-fixtures'
import { kiroGoalCancellation, kiroGoalExecutionId, kiroGoalSessionId, readKiroGoalMessages } from './goalReceipt'
import { KIRO_AGENT, nativeContext } from './scenarios'

/** The word of the reason that Kiro states when a goal reaches its round limit. */
const KIRO_ROUND_LIMIT_WORD = 'maxIterations'

interface KiroPremeasureObservation {
  eventName: string
  events: { at: number, detail: unknown }[]
  omittedEvents: number
  invalidEvents: number
  listener: (event: Event) => void
}

declare global {
  interface Window {
    __kiroPremeasureObservation?: KiroPremeasureObservation
  }
}

kiroTest.describe('Kiro session goal', () => {
  // Kiro's AWS client does not forward the execution abort signal to its HTTP request.
  // Require the native cancelled executor before releasing and rejecting its old model answer.
  kiroTest('pauses, resumes and clears a running goal', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }, testInfo) => {
    const testStartedAt = Date.now()
    const marker = uniqueMarker()
    const setGate = `kiro-goal-set-${marker}`
    const resumeGate = `kiro-goal-resume-${marker}`
    const setRule = `kiro-goal-set-rule-${marker}`
    const resumeRule = `kiro-goal-resume-rule-${marker}`
    const objective = scriptedObjective(modelScript, 'Keep inspecting the repository.')
    const staleAnswer = 'Still working on the objective.'
    await withCleanup(async () => {
      const { agentId, workingDir } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, KIRO_AGENT, { optionValues: { policyPreset: 'allow-all' } })
      await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
      await waitForSettingsHydrated(page)
      const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
      const parent = await currentNativeAgent(context)
      expect(parent.id).toBe(agentId)
      const home = leapmuxServer.agentEnv.HOME
      if (!home || !parent.agentSessionId)
        throw new Error('The Kiro goal proof requires its private HOME and parent native session.')
      const installPremeasureObservation = () => page.evaluate((eventName) => {
        const previous = window.__kiroPremeasureObservation
        if (previous)
          window.removeEventListener(previous.eventName, previous.listener)
        const events: KiroPremeasureObservation['events'] = []
        const observation: KiroPremeasureObservation = {
          eventName,
          events,
          omittedEvents: 0,
          invalidEvents: 0,
          listener: (event) => {
            if (!(event instanceof CustomEvent)) {
              observation.invalidEvents++
              return
            }
            if (events.length >= 512) {
              observation.omittedEvents++
              return
            }
            const detail: unknown = event.detail
            events.push({ at: performance.now(), detail })
          },
        }
        window.__kiroPremeasureObservation = observation
        window.addEventListener(eventName, observation.listener)
      }, 'leapmux:chat-premeasure')
      await installPremeasureObservation()
      await modelScript.rule({ name: setRule, when: { protocol: 'aws-event-stream', user: 'original_user_request' }, respond: { text: staleAnswer, gate: setGate }, once: true })
      // Kiro can notify its parent when a work step ends. This fallback answers those native notification turns.
      await modelScript.fallback({ text: 'The native goal notification completed.' })
      modelScript.allowUnconsumed('Native Pause and Clear stop the held goal executions on purpose.')

      await setGoal(page, objective)
      const setStatus = await modelScript.waitForGate(setGate)
      const setRequests = setStatus.requests.filter(request => request.rule === setRule)
      expect(setRequests).toHaveLength(1)
      const setRequest = setRequests[0]
      if (!setRequest)
        throw new Error('The native Kiro goal reached no held Set request.')
      expect(setRequest.protocol).toBe('aws-event-stream')
      expect(nativeModelContextText(setRequest)).toContain(objective.text)
      expect(nativeModelContextText(setRequest)).toContain(objective.marker)
      await expect.poll(async () => (await modelScript.status()).requests.length).toBeGreaterThan(0)
      const sessionId = kiroGoalSessionId(setRequest)
      const initial = await readNativeSidebarSnapshot(context, agentId)
      expect(initial.goalLoaded).toBe(true)
      const workflowId = initial.goal?.nativeId
      if (!workflowId)
        throw new Error('The Worker goal snapshot contains no native workflow ID.')
      const step = initial.backgroundTasks.find(task => task.id === sessionId && task.kind === BackgroundTaskKind.SUBAGENT && task.groupKey === workflowId)
      if (!step?.childAgentId)
        throw new Error('The Worker goal snapshot contains no exact native step transcript.')
      expect(step.parentAgentId).toBe(agentId)
      const child = await nativeAgentById(context, step.childAgentId)
      expect(child?.parentAgentId).toBe(agentId)
      expect(child?.rootAgentId).toBe(agentId)
      expect(child?.spawnSpanId).toBe(sessionId)
      const identity = { home, runDir: getGlobalState().tmpDir, workingDir, sessionId, parentSessionId: parent.agentSessionId }
      const nativeMessages = () => readKiroGoalMessages(identity) ?? []
      const attachBarrierEvidence = async (stage: string, knownModel: MockModelScenarioStatus, answer: string) => {
        const evidence: Record<string, unknown> = { stage, agentId, childAgentId: step.childAgentId, parentSessionId: parent.agentSessionId, stepSessionId: sessionId, workingDir }
        const errorText = (error: unknown) => error instanceof Error ? error.message : String(error)
        // Diagnostics preserve raw native files, including malformed or incomplete text.
        // The feature reader instead requires complete records and the exact child-parent relation.
        evidence.nativeFiles = [parent.agentSessionId, sessionId].map((id) => {
          try {
            if (!isFileNameComponent(id))
              throw new Error('The Kiro diagnostic session must be one filename component.')
            assertPrivateNativePath(home, identity.runDir)
            const sessions = join(home, '.kiro', 'sessions')
            if (!existsSync(sessions))
              return { sessionId: id, files: [] }
            assertPrivateNativePath(sessions, home)
            const files = readdirSync(sessions, { withFileTypes: true }).filter(entry => entry.isDirectory()).map((entry) => {
              const directory = join(sessions, entry.name, id)
              const messageFile = join(directory, 'messages.jsonl')
              if (!existsSync(messageFile))
                return undefined
              assertPrivateNativePath(messageFile, home)
              const metadataFile = join(directory, 'session.json')
              let metadata: Record<string, unknown> | undefined
              if (existsSync(metadataFile)) {
                assertPrivateNativePath(metadataFile, home)
                const value: unknown = JSON.parse(readFileSync(metadataFile, 'utf8'))
                if (isObject(value))
                  metadata = { id: value.id, workspacePaths: value.workspacePaths, rootConversationId: value.rootConversationId }
              }
              return { path: messageFile, metadata, text: readFileSync(messageFile, 'utf8') }
            }).filter(file => file !== undefined)
            return { sessionId: id, files }
          }
          catch (error) {
            return { sessionId: id, readError: errorText(error) }
          }
        })
        evidence.model = knownModel
        // Save native bytes before a Worker RPC can wait on the missing-output defect.
        await testInfo.attach(`kiro-${stage}-native-evidence`, { body: Buffer.from(JSON.stringify(evidence)), contentType: 'application/json' })
        try {
          evidence.browser = await page.evaluate((expectedAnswer) => {
            const elementBox = (element: HTMLElement) => {
              const rect = element.getBoundingClientRect()
              const style = getComputedStyle(element)
              return {
                isConnected: element.isConnected,
                rect: { x: rect.x, y: rect.y, width: rect.width, height: rect.height },
                clientWidth: element.clientWidth,
                clientHeight: element.clientHeight,
                scrollWidth: element.scrollWidth,
                scrollHeight: element.scrollHeight,
                style: {
                  display: style.display,
                  visibility: style.visibility,
                  opacity: style.opacity,
                  position: style.position,
                  width: style.width,
                  height: style.height,
                  contain: style.contain,
                  transform: style.transform,
                },
              }
            }
            const bubbles = (element: HTMLElement) => Array.from(element.querySelectorAll<HTMLElement>('[data-testid="message-bubble"]')).map(bubble => ({
              sequence: bubble.getAttribute('data-message-seq'),
              role: bubble.getAttribute('data-role'),
              answerPresent: bubble.textContent?.includes(expectedAnswer) ?? false,
              text: bubble.textContent,
              box: elementBox(bubble),
            }))
            const chats = Array.from(document.querySelectorAll<HTMLElement>('[data-testid="chat-container"]')).filter((chat) => {
              const style = getComputedStyle(chat)
              return chat.getClientRects().length > 0 && style.display !== 'none' && style.visibility !== 'hidden'
            })
            const observation = window.__kiroPremeasureObservation
            return {
              at: performance.now(),
              visibilityState: document.visibilityState,
              viewport: { width: window.innerWidth, height: window.innerHeight },
              measurement: observation
                ? { events: [...observation.events], omittedEvents: observation.omittedEvents, invalidEvents: observation.invalidEvents }
                : { readError: 'The premeasure observation is absent.' },
              chats: chats.map(chat => ({
                chatInstanceId: chat.getAttribute('data-chat-instance-id'),
                box: elementBox(chat),
                mainRows: Array.from(chat.querySelectorAll<HTMLElement>('[data-seq]')).filter(row => !row.closest('[data-chat-premeasure-root="true"]')).map(row => ({
                  sequence: row.getAttribute('data-seq'),
                  band: row.getAttribute('data-band'),
                  box: elementBox(row),
                  bubbles: bubbles(row),
                })),
                premeasureRoots: Array.from(chat.querySelectorAll<HTMLElement>('[data-chat-premeasure-root="true"]')).map(root => ({
                  box: elementBox(root),
                  rows: Array.from(root.children).filter((row): row is HTMLElement => row instanceof HTMLElement).map(row => ({
                    band: row.getAttribute('data-band'),
                    box: elementBox(row),
                    bubbles: bubbles(row),
                  })),
                })),
              })),
            }
          }, answer)
        }
        catch (error) {
          evidence.browser = { readError: errorText(error) }
        }
        // Inspect both copies without changing their geometry or reveal state.
        await testInfo.attach(`kiro-${stage}-browser-evidence`, { body: Buffer.from(JSON.stringify(evidence)), contentType: 'application/json' })
        // Keep time for the original UI proof. The API abort cancels only this diagnostic request.
        const diagnosticBudget = Math.max(1, Math.min(30_000, testInfo.timeout - (Date.now() - testStartedAt) - 30_000))
        const diagnosticSignal = AbortSignal.timeout(diagnosticBudget)
        evidence.workerMessages = await Promise.all([agentId, step.childAgentId].map(async (id) => {
          try {
            const channel = await getTestChannel(leapmuxServer.hubUrl, leapmuxServer.adminToken)
            const snapshot = await channel.callWorker(leapmuxServer.workerId, 'ListAgentMessages', ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema, { agentId: id, limit: 100 }, { signal: diagnosticSignal })
            return { agentId: id, snapshot: toJson(ListAgentMessagesResponseSchema, snapshot) }
          }
          catch (error) {
            return { agentId: id, readError: errorText(error) }
          }
        }))
        await testInfo.attach(`kiro-${stage}-worker-evidence`, { body: Buffer.from(JSON.stringify(evidence)), contentType: 'application/json' })
      }
      const sendBarrier = async (stage: string, prompt: string, answer: string) => {
        const index = await modelScript.queue({ text: answer })
        await sendMessage(page, modelScript.prompt(prompt))
        let finished = await modelScript.waitForSteps(index + 1)
        await expect.poll(async () => {
          finished = await modelScript.status()
          return finished.requests.find(record => record.stepIndex === index)?.response?.status
        }).toBe(200)
        await attachBarrierEvidence(`${stage}-model-finished`, finished, answer)
        await waitForAgentIdle(page)
        await attachBarrierEvidence(`${stage}-worker-idle`, finished, answer)
        // Read the record after the turn. A native client states more of its request after the mock counts the step.
        const request = await modelScript.requestAt(index)
        expect(nativeModelContextText(request)).toContain(prompt)
        await expect(assistantBubbles(page).filter({ hasText: answer }).first()).toBeVisible()
        return request
      }
      // Kiro writes its session files while the test reads them, so each wait on them retries a read that throws.
      const setExecutionId = await retryUntilPass(() => {
        const id = kiroGoalExecutionId(nativeMessages())
        if (!id)
          throw new Error('The held native Kiro step contains no started execution.')
        return id
      })
      expect(kiroGoalCancellation(nativeMessages(), setExecutionId)).toBeUndefined()

      await openGoalMenu(page)
      await goalAction(page, 'pause').click()
      await expectGoalStatus(page, 'paused')
      const detail = page.locator('[data-testid="goal-status-detail"]:visible')
      await expect(detail.filter({ hasText: KIRO_ROUND_LIMIT_WORD }), 'the reader paused the goal, not the round limit').toHaveCount(0)
      await retryUntilPass(() => {
        expect(kiroGoalCancellation(nativeMessages(), setExecutionId)?.executionId, 'Kiro records the cancellation of the paused execution').toBe(setExecutionId)
      })
      await retryUntilPass(async () => {
        expect((await readNativeSidebarSnapshot(context, agentId)).backgroundTasks.find(task => task.id === sessionId)?.activeForm, 'the Worker marks the goal step as paused')
          .toMatch(/^Paused:/)
      })
      const paused = await readNativeSidebarSnapshot(context, agentId)
      expect(paused.goal?.status).toBe(AgentGoalStatus.PAUSED)
      expect(paused.goal?.nativeId).toBe(workflowId)
      expect((await modelScript.status()).pendingGates).toContain(setGate)
      const callsWhilePaused = (await modelScript.status()).requests.length
      // Release only after the native executor stops. The HTTP finish cannot satisfy that cancellation proof.
      await modelScript.releaseGate(setGate)
      await expect.poll(async () => (await modelScript.status()).requests.find(request => request.rule === setRule)?.response?.status).toBe(200)
      await expect.poll(async () => (await modelScript.status()).pendingGates.includes(setGate)).toBe(false)
      const barrier = await sendBarrier('pause', 'Reply once while the native goal stays paused.', `KIROPAUSEBARRIER${marker}`)
      expect(nativeModelContextText(barrier)).not.toContain(staleAnswer)
      expect(JSON.stringify(nativeMessages())).not.toContain(staleAnswer)
      const assertNoSavedStaleAnswer = async () => {
        for (const id of [agentId, step.childAgentId]) {
          const snapshot = await readNativeMessageSnapshot(context, id)
          for (const message of snapshot.messages)
            expect(JSON.stringify(nativeMessageBody(message))).not.toContain(staleAnswer)
        }
      }
      await assertNoSavedStaleAnswer()
      await expect(messageContents(page).filter({ hasText: staleAnswer })).toHaveCount(0)
      await expandBackgroundTasksSection(page)
      const stepRow = page.locator(`[data-testid="bg-task-row"]:visible[data-child-agent-id="${step.childAgentId}"]`).first()
      expect(await openChildTabFromRow(page, stepRow)).toBe(step.childAgentId)
      await expect(messageContents(page).filter({ hasText: staleAnswer })).toHaveCount(0)
      await tabById(page, agentId).click()
      await expectGoalStatus(page, 'paused')
      expect((await readNativeSidebarSnapshot(context, agentId)).goal?.nativeId).toBe(workflowId)
      expect(kiroGoalExecutionId(nativeMessages())).toBe(setExecutionId)

      await page.reload()
      await waitForSettingsHydrated(page)
      await installPremeasureObservation()
      await expandGoalsAndTodosSection(page)
      await expectGoalStatus(page, 'paused')
      await expectGoalObjective(page, objective)
      await expect(detail.filter({ hasText: KIRO_ROUND_LIMIT_WORD }), 'the reader paused the goal, not the round limit').toHaveCount(0)
      await assertNoSavedStaleAnswer()

      // Register this rule after cancellation and reload. An earlier rule could capture a retry before Resume.
      await modelScript.rule({ name: resumeRule, when: { protocol: 'aws-event-stream', user: 'original_user_request' }, respond: { text: staleAnswer, gate: resumeGate }, once: true })
      await openGoalMenu(page)
      const resumeBaseline = (await modelScript.status()).requests.length
      await goalAction(page, 'resume').click()
      await expectGoalStatus(page, 'active')
      await expect.poll(async () => (await modelScript.status()).requests.length, { message: 'the resumed run calls the model again' }).toBeGreaterThan(callsWhilePaused)
      await expect.poll(async () => (await modelScript.status()).requests.length).toBeGreaterThan(resumeBaseline)
      const resumeStatus = await modelScript.waitForGate(resumeGate)
      const resumedRequests = resumeStatus.requests.slice(resumeBaseline).filter(request => request.rule === resumeRule)
      expect(resumedRequests).toHaveLength(1)
      const resumeRequest = resumedRequests[0]
      if (!resumeRequest)
        throw new Error('The native Kiro goal reached no new held Resume request.')
      expect(resumeRequest.protocol).toBe('aws-event-stream')
      expect(nativeModelContextText(resumeRequest)).toContain(objective.text)
      expect(nativeModelContextText(resumeRequest)).toContain(objective.marker)
      expect(kiroGoalSessionId(resumeRequest)).toBe(sessionId)
      const resumeExecutionId = await retryUntilPass(() => {
        const id = kiroGoalExecutionId(nativeMessages())
        if (!id || id === setExecutionId)
          throw new Error('The resumed native Kiro step contains no new execution.')
        return id
      })
      expect(kiroGoalCancellation(nativeMessages(), resumeExecutionId)).toBeUndefined()

      await clearGoal(page)
      await expectEmptyGoalCard(page)
      await retryUntilPass(() => {
        expect(kiroGoalCancellation(nativeMessages(), resumeExecutionId)?.executionId, 'Kiro records the cancellation of the cleared execution').toBe(resumeExecutionId)
      })
      await retryUntilPass(async () => {
        expect((await readNativeSidebarSnapshot(context, agentId)).backgroundTasks.find(task => task.id === sessionId)?.status, 'the Worker stops the goal step')
          .toBe(BackgroundTaskStatus.STOPPED)
      })
      await modelScript.releaseGate(resumeGate)
      await expect.poll(async () => (await modelScript.status()).requests.find(request => request.rule === resumeRule)?.response?.status).toBe(200)
      await expect.poll(async () => (await modelScript.status()).pendingGates.includes(resumeGate)).toBe(false)
      const cleared = await sendBarrier('clear', 'Reply once after the native goal is cleared.', `KIROCLEARBARRIER${marker}`)
      expect(nativeModelContextText(cleared)).not.toContain(staleAnswer)
      expect(JSON.stringify(nativeMessages())).not.toContain(staleAnswer)
      expect(kiroGoalExecutionId(nativeMessages())).toBe(resumeExecutionId)
      await expectEmptyGoalCard(page)
      await assertNoSavedStaleAnswer()
      await expect(messageContents(page).filter({ hasText: staleAnswer })).toHaveCount(0)
    }, () => finishCleanup([
      modelScript.releaseGateIfHeld(setGate),
      modelScript.releaseGateIfHeld(resumeGate),
      page.isClosed()
        ? Promise.resolve()
        : page.evaluate(() => {
            const observation = window.__kiroPremeasureObservation
            if (observation)
              window.removeEventListener(observation.eventName, observation.listener)
            delete window.__kiroPremeasureObservation
          }),
    ]))
  })
})

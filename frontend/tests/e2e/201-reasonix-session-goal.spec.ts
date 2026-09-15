import type { AddressInfo } from 'node:net'
import { Buffer } from 'node:buffer'
import { writeFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, test } from './fixtures'
import { openAgentViaAPI } from './helpers/api'
import { createTestDirectory } from './helpers/runDirectory'
import { expandGoalsAndTodosSection, expectGoalStatus, goalAction, openGoalMenu } from './helpers/subagentRegistry'
import { openWorkspace } from './helpers/ui'

test('sets and clears a native Reasonix goal before and after cancellation', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
  const directory = createTestDirectory('reasonix-goal-protocol-')
  const requests: string[] = []
  const serverErrors: unknown[] = []
  const server = createServer(async (request, response) => {
    try {
      const chunks: Buffer[] = []
      for await (const chunk of request)
        chunks.push(Buffer.from(chunk))
      requests.push(Buffer.concat(chunks).toString())
      // Keep the native turn active until the browser cancels it.
    }
    catch (error) {
      if (!request.aborted)
        serverErrors.push(error)
      response.destroy()
    }
  })
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', resolve)
  })
  try {
    const port = (server.address() as AddressInfo).port
    writeFileSync(join(directory, 'reasonix.toml'), `default_model = "probe"\n[[providers]]\nname = "probe"\nkind = "openai"\nbase_url = "http://127.0.0.1:${port}/v1"\nmodel = "probe"\n`)
    writeFileSync(join(directory, 'AGENTS.md'), 'This directory contains a private protocol test. Do not run tools.\n')
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, directory, {
      agentProvider: AgentProvider.REASONIX,
      model: 'probe/probe',
    })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expandGoalsAndTodosSection(page)
    const objective = 'Keep the native objective until the browser clears it.'
    const setGoal = async () => {
      await goalAction(page, 'set').click()
      await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(objective)
      await page.locator('[data-testid="set-goal-submit"]:visible').click()
      await expect(page.locator('[data-testid="goal-objective"]:visible')).toContainText(objective)
      await expectGoalStatus(page, 'active')
      await expect(page.getByTestId('interrupt-button').filter({ visible: true })).toBeVisible()
    }
    await setGoal()
    await expect.poll(() => requests.length).toBe(1)
    expect(requests[0]).toContain(objective)
    await openGoalMenu(page)
    await goalAction(page, 'set').click()
    await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill('Retain this replacement after the refusal.')
    await page.locator('[data-testid="set-goal-submit"]:visible').click()
    await expect(page.getByText('agent is already running a turn', { exact: false })).toBeVisible()
    const refusedDialog = page.getByTestId('set-goal-dialog')
    await expect(refusedDialog).toBeVisible()
    await expect(refusedDialog.locator('.ProseMirror')).toContainText('Retain this replacement after the refusal.')
    await refusedDialog.getByRole('button', { name: 'Cancel', exact: true }).click()
    await expect(page.locator('[data-testid="goal-objective"]:visible')).toContainText(objective)
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expandGoalsAndTodosSection(page)
    await expect(page.locator('[data-testid="goal-objective"]:visible')).toContainText(objective)
    await openGoalMenu(page)
    await expect(goalAction(page, 'pause')).toHaveCount(0)
    await expect(goalAction(page, 'resume')).toHaveCount(0)
    await goalAction(page, 'clear').click()
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
    const interrupt = page.getByTestId('interrupt-button').filter({ visible: true })
    await expect(interrupt).toBeVisible()
    await interrupt.click()
    await expect(interrupt).toHaveCount(0)
    await setGoal()
    await expect.poll(() => requests.length).toBe(2)
    await interrupt.click()
    await expect(interrupt).toHaveCount(0)
    await expectGoalStatus(page, 'blocked')
    await openGoalMenu(page)
    await goalAction(page, 'clear').click()
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expandGoalsAndTodosSection(page)
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
    expect(serverErrors).toEqual([])
  }
  finally {
    await new Promise<void>((resolve, reject) => {
      server.close(error => error ? reject(error) : resolve())
      server.closeAllConnections()
    })
  }
})

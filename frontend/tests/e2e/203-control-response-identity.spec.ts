import { Buffer } from 'node:buffer'
import { execFileSync } from 'node:child_process'
import { join } from 'node:path'
import { AgentProvider, AgentStatus, MarkType } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { decompressContentToString } from '../../src/lib/decompress'
import { expect, test } from './fixtures'
import { openAgentViaAPI } from './helpers/api'
import { createTestDirectory } from './helpers/runDirectory'
import { listAgents } from './helpers/subagentRegistry'
import { openWorkspace } from './helpers/ui'
import { realAgentOpenOptions, realAgentSettings } from './realAgentSettings'

const sqlString = (value: string) => `'${value.replaceAll('\'', '\'\'')}'`

for (const provider of [AgentProvider.CODEX, AgentProvider.OPENCODE, AgentProvider.CURSOR]) {
  test(`preserves control response IDs through the browser for ${AgentProvider[provider]}`, async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])
    const pageErrors: string[] = []
    page.on('pageerror', error => pageErrors.push(error.message))
    await page.setViewportSize({ width: 780, height: 1000 })
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('control-id-'), {
      agentProvider: provider,
      ...realAgentOpenOptions(realAgentSettings(provider)),
    })
    // Startup clears stale controls before the live process accepts responses.
    await expect.poll(async () => {
      const agents = await listAgents(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, [agentId])
      return agents?.find(agent => agent.id === agentId)?.status
    }).toBe(AgentStatus.ACTIVE)
    const database = join(leapmuxServer.dataDir, 'worker', 'worker.db')
    const agent = sqlString(agentId)
    const readAnswers = () => {
      const rows = JSON.parse(execFileSync('sqlite3', ['-json', database, `SELECT hex(content) AS content, content_compression AS compression FROM messages WHERE agent_id=${agent} AND mark_type=${MarkType.CONTROL_RESPONSE} ORDER BY seq`], { encoding: 'utf8' }) || '[]') as Array<{ content: string, compression: number }>
      return rows.map(row => decompressContentToString(Buffer.from(row.content, 'hex'), row.compression) ?? '')
    }

    for (const [index, identity] of [
      { worker: 'jsonrpc:0', wire: '0' },
      { worker: 'jsonrpc:"0"', wire: '"0"' },
      { worker: 'jsonrpc:9007199254740993', wire: '9007199254740993' },
      { worker: 'jsonrpc:"001"', wire: '"001"' },
      { worker: 'jsonrpc:""', wire: '""' },
    ].entries()) {
      const questionIds = Array.from({ length: index === 0 ? 1 : 3 }, (_, question) => `q${question}`)
      const fields = provider === AgentProvider.CODEX
        ? { method: 'item/tool/requestUserInput', params: { questions: questionIds.map(id => ({ id, question: 'Choose the identity marker.', header: 'Marker', options: [{ label: 'Keep', description: 'Keep this marker.' }, { label: 'Change', description: 'Change this marker.' }] })) } }
        : provider === AgentProvider.CURSOR
          ? { method: 'cursor/ask_question', params: { toolCallId: `identity-${index}`, title: 'Choose the identity marker.', questions: questionIds.map(id => ({ id, prompt: 'Choose the identity marker.', allowMultiple: false, options: [{ id: 'keep-marker', label: 'Keep' }, { id: 'change-marker', label: 'Change' }] })) } }
          : { method: 'session/request_permission', params: { toolCall: { toolCallId: `identity-${index}`, title: 'Review the identity marker.', kind: 'read', rawInput: { path: '/project/marker.txt' } }, options: [{ optionId: 'once', name: 'Allow once', kind: 'allow_once' }, { optionId: 'session', name: 'Allow for session', kind: 'allow_always' }, { optionId: 'reject', name: 'Reject', kind: 'reject_once' }] } }
      const payload = `{"jsonrpc":"2.0","id":${identity.wire},${JSON.stringify(fields).slice(1)}`
      execFileSync('sqlite3', [database, `INSERT INTO control_requests (agent_id, agent_session_id, request_id, payload, claim_token) VALUES (${agent}, (SELECT agent_session_id FROM agents WHERE id=${agent}), ${sqlString(identity.worker)}, X'${Buffer.from(payload).toString('hex')}', ${sqlString(`identity-claim-${index}`)});`])
      await page.reload()
      await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
      const banner = page.getByTestId('control-banner').filter({ visible: true })
      await expect(banner).toBeVisible()
      if (index === 2) {
        await banner.hover()
        await banner.getByTestId('control-copy-json').click()
        await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toContain('9007199254740993')
      }
      await expect.poll(() => page.getByTestId('control-footer').evaluate((footer) => {
        const right = footer.getBoundingClientRect().right
        const rows: Array<{ top: number, bottom: number, right: number }> = []
        for (const button of footer.querySelectorAll('button')) {
          const box = button.getBoundingClientRect()
          if (box.width === 0 || box.height === 0)
            continue
          // Buttons on one visual row can have different heights.
          const row = rows.find(row => box.top < row.bottom && box.bottom > row.top)
          if (row) {
            row.top = Math.min(row.top, box.top)
            row.bottom = Math.max(row.bottom, box.bottom)
            row.right = Math.max(row.right, box.right)
          }
          else {
            rows.push({ top: box.top, bottom: box.bottom, right: box.right })
          }
        }
        return rows.length > 0 && rows.every(row => Math.abs(right - row.right) <= 2)
      })).toBe(true)
      await expect(page.getByTestId('queue-pause-button')).toHaveCount(0)
      if (provider !== AgentProvider.OPENCODE) {
        for (let question = 0; question < questionIds.length; question++)
          await banner.getByTestId('question-option-Keep').click()
        await expect(page.getByTestId('control-submit-btn')).toBeInViewport({ ratio: 1 })
        await page.getByTestId('control-submit-btn').click()
      }
      else {
        await expect(page.getByTestId('control-allow-btn')).toBeInViewport({ ratio: 1 })
        await expect(page.getByTestId('control-deny-btn')).toBeInViewport({ ratio: 1 })
        if (index === 0) {
          execFileSync('sqlite3', [database, `CREATE TRIGGER fail_control_response BEFORE INSERT ON messages WHEN NEW.agent_id=${agent} AND NEW.mark_type=${MarkType.CONTROL_RESPONSE} BEGIN SELECT RAISE(ABORT, 'response storage unavailable'); END;`])
          try {
            await page.getByTestId('control-allow-btn').click()
            await expect(banner.getByRole('alert')).toContainText('response storage unavailable')
            await expect(banner.getByRole('alert')).toHaveCount(1)
            await expect(page.getByRole('button', { name: 'Save response', exact: true })).toBeEnabled()
            await expect(page.getByTestId('control-allow-btn')).toHaveCount(0)
            expect(readAnswers()).toHaveLength(0)
          }
          finally {
            execFileSync('sqlite3', [database, 'DROP TRIGGER IF EXISTS fail_control_response'])
          }
          await page.getByRole('button', { name: 'Save response', exact: true }).click()
        }
        else {
          await page.getByTestId('control-allow-btn').click()
        }
      }
      await expect(banner).toHaveCount(0)
      await expect.poll(() => readAnswers().length).toBe(index + 1)
      const answer = readAnswers()[index]
      expect(answer).toContain(`"id":${identity.wire}`)
      const response = JSON.parse(answer)
      expect(response.result).toEqual(provider === AgentProvider.CODEX
        ? { answers: Object.fromEntries(questionIds.map(id => [id, { answers: ['Keep'] }])) }
        : provider === AgentProvider.CURSOR
          ? { outcome: { outcome: 'answered', answers: questionIds.map(questionId => ({ questionId, selectedOptionIds: ['keep-marker'] })) } }
          : { outcome: { outcome: 'selected', optionId: 'once' } })
    }

    await page.reload()
    expect(readAnswers()).toHaveLength(5)
    expect(pageErrors).toEqual([])
  })
}

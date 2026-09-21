import type { Locator } from '@playwright/test'
import { Buffer } from 'node:buffer'
import { execFileSync } from 'node:child_process'
import { join } from 'node:path'
import { AgentProvider, MarkType, MessageCompletion, MessageSource } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, test } from './fixtures'
import { openAgentViaAPI } from './helpers/api'
import { createTestDirectory } from './helpers/runDirectory'
import { openWorkspace } from './helpers/ui'
import { realAgentOpenOptions, realAgentSettings } from './realAgentSettings'

const sqlString = (value: string) => `'${value.replaceAll('\'', '\'\'')}'`
const blob = (value: unknown) => `X'${Buffer.from(JSON.stringify(value), 'utf8').toString('hex')}'`

function seedFileChanges(database: string, agentId: string): bigint[] {
  const agent = sqlString(agentId)
  const messages = [
    {
      id: 'single-file-change',
      spanId: 'single-file-change',
      changes: [{ path: '/project/a.ts', kind: { type: 'update' }, diff: '@@ -1 +1,3 @@\n-old\n+new\n+second\n+third\n' }],
    },
    {
      id: 'multiple-file-change',
      spanId: 'multiple-file-change',
      changes: [
        { path: '/project/a.ts', kind: { type: 'update' }, diff: '@@ -1 +1,3 @@\n-old\n+new\n+second\n+third\n' },
        { path: '/project/b.ts', kind: { type: 'update' }, diff: '@@ -1 +1 @@\n-before\n+after\n' },
      ],
    },
  ]
  const inserts = messages.map(message => `INSERT INTO messages
    (id, agent_id, seq, source, content, content_compression, agent_provider, span_id, span_type, supplemental_content, supplemental_content_compression, supplemental_revision, completion, agent_session_id, mark_type)
    VALUES (${sqlString(`${agentId}-${message.id}`)}, ${agent},
      (SELECT message_seq_hwm + 1 FROM agents WHERE id = ${agent}), ${MessageSource.AGENT},
      ${blob({ item: { id: message.id, type: 'fileChange', status: 'completed', changes: message.changes } })}, 1, ${AgentProvider.CODEX}, ${sqlString(message.spanId)}, 'fileChange',
      X'', 1, 0, ${MessageCompletion.UNSPECIFIED}, '', ${MarkType.UNSPECIFIED}) RETURNING seq;`)
  const output = execFileSync('sqlite3', ['-batch', '-noheader', '-list', '-bail', database], {
    encoding: 'utf8',
    input: `.timeout 10000\nBEGIN IMMEDIATE;\n${inserts.join('\n')}\nCOMMIT;\n`,
  })
  const sequences = output.trim()
  return sequences ? sequences.split('\n').map(value => BigInt(value)) : []
}

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

test('file-change statistics keep one presentation for one file and multiple files', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
  const agentId = await openAgentViaAPI(
    leapmuxServer.hubUrl,
    leapmuxServer.adminToken,
    leapmuxServer.workerId,
    authenticatedEmptyWorkspace.workspaceId,
    createTestDirectory('file-change-stat-'),
    {
      agentProvider: AgentProvider.CODEX,
      ...realAgentOpenOptions(realAgentSettings(AgentProvider.CODEX)),
    },
  )
  const sequences = seedFileChanges(join(leapmuxServer.dataDir, 'worker', 'worker.db'), agentId)
  expect(sequences).toHaveLength(2)
  const singleSequence = sequences[0]
  const multipleSequence = sequences[1]
  if (singleSequence === undefined || multipleSequence === undefined)
    throw new Error('The file-change fixtures did not return both message sequences.')

  await page.reload()
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  const singleRow = page.locator(`[data-seq="${singleSequence}"]`).filter({ visible: true })
  const multipleRow = page.locator(`[data-seq="${multipleSequence}"]`).filter({ visible: true })
  const single = await badgePresentation(singleRow.getByTestId('git-diff-stats'))
  const multiple = await badgePresentation(multipleRow.getByTestId('git-diff-stats').first())

  expect(single).toEqual(multiple)
  expect(single.titleGap).not.toBe('normal')
  expect(single.gap).toBeGreaterThan(0)
})

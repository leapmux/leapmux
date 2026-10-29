import type { McpProbeServer } from '../helpers/mcpProbeServer'
import type { NativeContextFixtures } from '../helpers/nativeScenario'
import { execFileSync } from 'node:child_process'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { elicitationFieldKey } from '../../../src/components/chat/controls/elicitationForm'
import { accountStorageKey, PREFIX_CONTROL_STATE } from '../../../src/lib/browserStorage'
import { isObject, pickObject } from '../../../src/lib/jsonPick'
import { agentOpenOptions } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { exerciseMcpProbeFormRoundTrip } from '../helpers/mcpProbeForm'
import { mcpServersConfig } from '../helpers/mcpProbeServer'
import { newNativeWorkingDir } from '../helpers/nativeAgentOpen'
import { hubSpawnEnv } from '../helpers/server'
import { waitForStoredEntry } from '../helpers/storage'
import { openWorkspace } from '../helpers/ui'
import { nativeContext } from './scenarios'

/** Open a fresh native session with one explicitly enabled project MCP server. */
export async function openReasonixMcpAgent(fixtures: NativeContextFixtures, prepare: (directory: string) => McpProbeServer) {
  const context = await nativeContext(fixtures)
  const directory = newNativeWorkingDir(context, 'reasonix-mcp-')
  const server = prepare(directory)
  writeFileSync(join(directory, '.mcp.json'), JSON.stringify(mcpServersConfig(server)))
  const { leapmuxServer } = context
  const enabled = execFileSync('reasonix', ['mcp', 'enable', server.name], {
    cwd: directory,
    env: hubSpawnEnv(leapmuxServer.agentEnv),
    encoding: 'utf8',
  })
  expect(enabled).toContain(`enabled MCP server ${JSON.stringify(server.name)} in this project`)
  await openAgentViaAPI(leapmuxServer, context.workspaceId, directory, agentOpenOptions(context.provider, {
    optionValues: { tool_approval: 'yolo' },
  }))
  await openWorkspace(context.page, context.workspaceId)
  return context
}

/**
 * Open a Reasonix agent whose project registers the probe form server, and answer the form through the browser after
 * a reload. The Yolo approval runs the MCP tool without a request, so the form is the only request of the call.
 */
export async function exerciseReasonixMcpForm(fixtures: NativeContextFixtures): Promise<void> {
  const context = await openReasonixMcpAgent(fixtures, directory => writeMcpFormServer(directory, 'form-server.mjs'))
  await exerciseMcpProbeFormRoundTrip(context, {
    callId: 'mcp-form',
    reloadBeforeSubmit: true,
    // Reasonix exposes each scalar as text and each choice by its display label.
    // Its MCP bridge restores the original integer, boolean, and enum values.
    form: {
      fill: async (page) => {
        const form = page.getByTestId('elicitation-form').filter({ visible: true })
        await expect(form).toBeVisible()
        await form.getByLabel('Count', { exact: true }).fill('0')
        await form.getByRole('button', { name: 'Enabled', exact: true }).click()
        await page.getByRole('menuitemradio', { name: 'No', exact: true }).click()
        await form.getByRole('button', { name: 'Color', exact: true }).click()
        await page.getByRole('menuitemradio', { name: 'Blue', exact: true }).click()
        return form
      },
      waitForDraft: (page, userId) => waitForStoredEntry(page, accountStorageKey(userId, PREFIX_CONTROL_STATE), reasonixMcpFormDraftSaved, 'the native Reasonix form answers must persist before reload'),
      expectRestored: async (form) => {
        await expect(form.getByLabel('Count', { exact: true })).toHaveValue('0')
        await expect(form.getByRole('button', { name: 'Enabled', exact: true })).toHaveText('No')
        await expect(form.getByRole('button', { name: 'Color', exact: true })).toHaveText('Blue')
      },
    },
  })
}

/** Require the native text and display-label answers in the durable form draft. */
export function reasonixMcpFormDraftSaved(record: unknown): boolean {
  const choices = isObject(record) ? pickObject(record, 'choices') : null
  return choices !== null
    && choices[elicitationFieldKey('count')] === '0'
    && choices[elicitationFieldKey('enabled')] === JSON.stringify('No')
    && choices[elicitationFieldKey('color')] === JSON.stringify('Blue')
}

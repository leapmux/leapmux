import { expect } from '@playwright/test'
import { test } from '../fixtures'
import { expectRegistryRow, expectRowBecomesFinal } from './subagentRegistry'

test('matches background task filters on each row before its nested content', async ({ page }) => {
  await page.setContent(`
    <div data-testid="section-header-background_tasks">Background tasks</div>
    <div id="shell-decoy" data-testid="bg-task-row" data-kind="shell" data-status="running">
      Shell task <span data-kind="subagent" data-status="succeeded">nested child label</span>
    </div>
    <div id="child-done" data-testid="bg-task-row" data-kind="subagent" data-status="succeeded">
      Completed child <span data-status="running">old progress text</span>
    </div>
    <div id="child-running" data-testid="bg-task-row" data-kind="subagent" data-status="running">Running child</div>
    <div id="workflow-done" data-testid="bg-task-row" data-kind="workflow" data-status="succeeded">Completed workflow</div>
    <div id="hidden-decoy" data-testid="bg-task-row">Hidden nested field <span style="display:none" data-kind="shell" data-status="paused">old state</span></div>
    <div id="legacy-row" data-testid="bg-task-row">Legacy row <span data-kind="shell" data-status="paused">nested fields</span></div>
  `)

  await expect(await expectRegistryRow(page, { kind: 'subagent' })).toHaveAttribute('id', 'child-done')
  await expect(await expectRegistryRow(page, { status: 'succeeded' })).toHaveAttribute('id', 'child-done')
  await expect(await expectRegistryRow(page, { kind: 'subagent', status: 'running' })).toHaveAttribute('id', 'child-running')
  await expect(await expectRegistryRow(page, { kind: 'workflow', status: 'succeeded' })).toHaveAttribute('id', 'workflow-done')
  await expect(await expectRegistryRow(page, { kind: 'shell', status: 'paused' })).toHaveAttribute('id', 'legacy-row')
  await expect(await expectRegistryRow(page, { kind: 'shell', status: 'paused', titleContains: 'Legacy row' })).toHaveAttribute('id', 'legacy-row')
})

test('accepts distinct success and unknown-outcome rows as final', async ({ page }) => {
  await page.setContent(`
    <div data-testid="section-header-background_tasks">Background tasks</div>
    <div id="success" data-testid="bg-task-row" data-kind="subagent" data-status="succeeded">
      <span data-testid="bg-task-status-dot" aria-label="Succeeded">●</span>
      <span data-testid="bg-task-secondary">Succeeded</span>
    </div>
    <div id="unknown" data-testid="bg-task-row" data-kind="subagent" data-status="ended_with_unknown_outcome">
      <span data-testid="bg-task-status-dot" aria-label="Ended with unknown outcome">●</span>
      <span data-testid="bg-task-secondary">Ended with unknown outcome</span>
    </div>
  `)
  for (const status of ['succeeded', 'ended_with_unknown_outcome']) {
    const row = await expectRegistryRow(page, { kind: 'subagent', status })
    await expectRowBecomesFinal(page, row)
  }
})

test('refuses a wrong status-dot label when the task title contains the expected end label', async ({ page }) => {
  await page.setContent(`
    <div data-testid="bg-task-row" data-kind="subagent" data-status="succeeded">
      <span data-testid="bg-task-title">Succeeded task</span>
      <span data-testid="bg-task-status-dot" aria-label="Failed">●</span>
      <span data-testid="bg-task-secondary">Succeeded</span>
    </div>
  `)
  const row = page.locator('[data-testid="bg-task-row"]:visible')
  await expect(expectRowBecomesFinal(page, row)).rejects.toThrow('aria-label')
})

test('refuses a wrong end-line label when the title and status dot contain the expected label', async ({ page }) => {
  await page.setContent(`
    <div data-testid="bg-task-row" data-kind="subagent" data-status="succeeded">
      <span data-testid="bg-task-title">Succeeded task</span>
      <span data-testid="bg-task-status-dot" aria-label="Succeeded">●</span>
      <span data-testid="bg-task-secondary">Failed</span>
    </div>
  `)
  const row = page.locator('[data-testid="bg-task-row"]:visible')
  await expect(expectRowBecomesFinal(page, row)).rejects.toThrow('toHaveText')
})

test('accepts the canonical title when the repeated end line is absent', async ({ page }) => {
  await page.setContent(`
    <div data-testid="bg-task-row" data-kind="subagent" data-status="succeeded">
      <span data-testid="bg-task-title">Succeeded</span>
      <span data-testid="bg-task-status-dot" aria-label="Succeeded">●</span>
    </div>
  `)
  const row = page.locator('[data-testid="bg-task-row"]:visible')
  await expectRowBecomesFinal(page, row)
  await expect(row.locator('[data-testid="bg-task-secondary"]:visible')).toHaveCount(0)
})

test('checks the secondary when a longer title contains the canonical word', async ({ page }) => {
  await page.setContent(`
    <div data-testid="bg-task-row" data-kind="subagent" data-status="succeeded">
      <span data-testid="bg-task-title">Succeeded task</span>
      <span data-testid="bg-task-status-dot" aria-label="Succeeded">●</span>
      <span data-testid="bg-task-secondary">Succeeded</span>
    </div>
  `)
  const row = page.locator('[data-testid="bg-task-row"]:visible')
  await expectRowBecomesFinal(page, row)
  await expect(row.locator('[data-testid="bg-task-secondary"]:visible')).toHaveText('Succeeded')
})

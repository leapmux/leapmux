import type { LocatorCheck } from '~/test-support/fakeLocator'
import { expect as playwrightExpect } from '@playwright/test'
import { describe, expect, it, vi } from 'vitest'
import { fakeLocator, fakeLocatorTree, recordingLocator } from '~/test-support/fakeLocator'

describe('fakeLocator', () => {
  it('passes each check by default, and Playwright reads it as a locator', async () => {
    const locator = fakeLocator()
    await playwrightExpect(locator).toBeVisible()
    await playwrightExpect(locator).toHaveAttribute('data-status', 'running')
    await playwrightExpect(locator).toHaveCount(2)
  })

  it('hands each check to the answer, with its expression and options', async () => {
    const checks: LocatorCheck[] = []
    const locator = fakeLocator((check) => {
      checks.push(check)
      return true
    })
    await playwrightExpect(locator).toHaveAttribute('data-kind', 'subagent')
    await playwrightExpect(locator).toHaveCount(3)
    expect(checks.map(check => check.expression)).toEqual(['to.have.attribute.value', 'to.have.count'])
    expect(checks[0]?.expressionArg).toBe('data-kind')
    expect(checks[0]?.expectedText?.[0]?.string).toBe('subagent')
    expect(checks[1]?.expectedNumber).toBe(3)
    expect(checks.every(check => !check.isNot)).toBe(true)
  })

  it('states the state, not the negation: a hidden element answers false, and its negated check passes', async () => {
    const hidden = fakeLocator(() => false)
    await playwrightExpect(hidden).not.toBeVisible()
    await expect(playwrightExpect(hidden).toBeVisible()).rejects.toThrow('toBeVisible')
    const shown = fakeLocator(() => true)
    await expect(playwrightExpect(shown).not.toBeVisible()).rejects.toThrow('toBeVisible')
  })

  it('prints the received value of an answer in the failure', async () => {
    const locator = fakeLocator(() => ({ matches: false, received: 'still' }))
    await expect(playwrightExpect(locator).toHaveClass(/dragging/)).rejects.toThrow('still')
  })

  it('waits for an answer that a promise gives', async () => {
    let release: (() => void) | undefined
    const answered = new Promise<boolean>((resolve) => {
      release = () => resolve(true)
    })
    const check = playwrightExpect(fakeLocator(() => answered)).toBeVisible()
    release?.()
    await check
  })

  it('keeps the members that the helper under test calls', async () => {
    const click = vi.fn(async () => {})
    const locator = fakeLocator(undefined, { click, path: 'row' })
    await locator.click()
    expect(click).toHaveBeenCalledOnce()
    expect(locator.path).toBe('row')
  })
})

describe('fakeLocatorTree', () => {
  /** The path of a node of the tree. */
  const pathOf = (locator: unknown) => (locator as { path: string }).path

  it('gives each node the path that built it', () => {
    const { page } = fakeLocatorTree({ log: [] })
    expect(pathOf(page)).toBe('page')
    expect(pathOf(page.locator('[data-testid="row"]').first())).toBe('page >> [data-testid="row"].first')
    expect(pathOf(page.getByTestId('chip').nth(2))).toBe('page >> testid=chip.nth(2)')
    expect(pathOf(page.getByRole('menuitem', { name: 'Delete', exact: true }))).toBe('page >> role=menuitem[name=Delete exact]')
    expect(pathOf(page.getByRole('dialog'))).toBe('page >> role=dialog')
    expect(pathOf(page.getByText('Saved', { exact: true }))).toBe('page >> text=Saved exact')
    expect(pathOf(page.getByLabel('Name'))).toBe('page >> label=Name')
    const row = page.locator('row')
    expect(pathOf(row.filter({ hasText: 'Alpha', has: page.locator('icon'), visible: true }))).toBe('page >> row[hasText=Alpha has=(page >> icon) visible=true]')
    expect(pathOf(row.or(page.locator('cell')))).toBe('(page >> row | page >> cell)')
  })

  it('logs each action and each check with the path, and answers from the path', async () => {
    const log: string[] = []
    const { page, node } = fakeLocatorTree({ log, answer: (expression, path) => !(expression === 'to.be.visible' && path === 'hidden') })
    await page.getByTestId('save').click()
    await page.getByLabel('Name').fill('Ada')
    await node('row').hover()
    await playwrightExpect(node('shown')).toBeVisible()
    await playwrightExpect(node('hidden')).not.toBeVisible()
    expect(await node('hidden').isVisible()).toBe(true)
    expect(log).toEqual([
      'click page >> testid=save',
      'fill page >> label=Name with Ada',
      'hover row',
      'to.be.visible shown',
      'not to.be.visible hidden',
    ])
  })

  it('reads attributes through the attribute hook, logs each read, and reads null by default', async () => {
    const log: string[] = []
    const { node } = fakeLocatorTree({ log, attribute: (name, path) => `${name} of ${path}` })
    expect(await node('row').getAttribute('data-id')).toBe('data-id of row')
    expect(await fakeLocatorTree({ log: [] }).node('row').getAttribute('data-id')).toBeNull()
    expect(log).toEqual(['read data-id row'])
  })

  it('adds the members of the root page', async () => {
    const reload = vi.fn(async () => null)
    const { page } = fakeLocatorTree({ log: [], page: { reload } })
    await page.reload()
    expect(reload).toHaveBeenCalledOnce()
  })
})

describe('recordingLocator', () => {
  it('logs each check with its name, and the count of a count check', async () => {
    const log: string[] = []
    const banner = recordingLocator('banner', log)
    await playwrightExpect(banner).toBeVisible()
    await playwrightExpect(banner).toHaveCount(0)
    expect(log).toEqual(['banner:to.be.visible', 'banner:to.have.count=0'])
  })

  it('answers through the given answer', async () => {
    const log: string[] = []
    await expect(playwrightExpect(recordingLocator('spinner', log, () => false)).toBeVisible()).rejects.toThrow('toBeVisible')
    expect(log).toEqual(['spinner:to.be.visible'])
  })
})

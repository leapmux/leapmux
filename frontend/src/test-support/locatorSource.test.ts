import { describe, expect, it } from 'vitest'
import { exportedFunctionBody, selectorsIn } from '~/test-support/locatorSource'

describe('selectorsIn', () => {
  // A minimum total selector count cannot prove that the parser reads both call forms,
  // so a sample with one locator of each form tests both parser paths.
  it('reads a getByTestId locator, not only a page.locator one', () => {
    const sample = `
      page.locator('[data-testid="goal-card"]:visible')
      page.getByTestId('agent-input-queue')
    `
    expect(selectorsIn(sample)).toEqual([
      '[data-testid="goal-card"]:visible',
      '[data-testid="agent-input-queue"]',
    ])
  })

  it('keeps a selector whole when it holds a quote of another kind', () => {
    expect(selectorsIn('page.locator(`[data-testid="goal-card"]:visible`)')).toEqual(['[data-testid="goal-card"]:visible'])
  })

  it('reads no selector from a locator of another root', () => {
    expect(selectorsIn('section.locator(\'> [role="button"]\')')).toEqual([])
  })
})

describe('exportedFunctionBody', () => {
  const source = [
    'export function plain(page) {',
    '  return page.locator(\'a\')',
    '}',
    'export async function waiting(page) {',
    '  await page.locator(\'b\').click()',
    '}',
    '',
  ].join('\n')

  it('reads an exported function and an exported async function to their closing braces', () => {
    expect(exportedFunctionBody(source, 'plain', 'sample.ts')).toBe('export function plain(page) {\n  return page.locator(\'a\')')
    expect(exportedFunctionBody(source, 'waiting', 'sample.ts')).toBe('export async function waiting(page) {\n  await page.locator(\'b\').click()')
  })

  it('names the module when the function is absent', () => {
    expect(() => exportedFunctionBody(source, 'absent', 'sample.ts')).toThrow('absent is no longer an exported function of sample.ts')
  })
})

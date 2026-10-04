import type { Locator, Page } from '@playwright/test'
import { randomUUID } from 'node:crypto'
import { expect } from '@playwright/test'

export interface NativeToolOutput {
  source: string
  text: string
  firstMarker: string
  omittedMarker: string
  lastMarker: string
}

/** Compute output in a native JavaScript process. Keep the complete markers outside its source. */
export function computedNativeToolOutput(options: { prefix?: string, lineCount?: number, padding?: number } = {}): NativeToolOutput {
  const prefix = options.prefix ?? `NATIVETOOLOUTPUT${randomUUID().replaceAll('-', '')}`
  const lineCount = options.lineCount ?? 3000
  const padding = options.padding ?? 0
  if (!/^[A-Z][A-Z0-9]{0,79}$/i.test(prefix))
    throw new Error('The native tool output prefix requires at most eighty ASCII letters and digits.')
  if (!Number.isSafeInteger(lineCount) || lineCount < 3 || lineCount > 20_000)
    throw new Error('The native tool output requires three through twenty thousand lines.')
  if (!Number.isSafeInteger(padding) || padding < 0 || padding > 100)
    throw new Error('The native tool output padding requires zero through one hundred characters.')
  const middle = Math.floor(lineCount / 2)
  const source = [
    `const outputFileLines = Array.from({length:${lineCount}}, (_, index) => ${JSON.stringify(prefix)} + "-line-" + index + ":" + "x".repeat(${padding}));`,
    `outputFileLines[${middle}] += "-middle-" + (70 + 7);`,
    `outputFileLines.push(${JSON.stringify(prefix)} + "-complete-" + (40 + 2));`,
    'const completeOutput = outputFileLines.join("\\n");',
  ].join(' ')
  const lines = Array.from({ length: lineCount }, (_, index) => `${prefix}-line-${index}:${'x'.repeat(padding)}`)
  lines[middle] += '-middle-77'
  const lastMarker = `${prefix}-complete-42`
  lines.push(lastMarker)
  return { source, text: lines.join('\n'), firstMarker: lines[0]!, omittedMarker: lines[middle]!, lastMarker }
}

/** Clear the clipboard and require the original native preview from Copy. */
export async function copyNativeToolOutputPreview(page: Page, result: Locator, expectedText: string): Promise<void> {
  if (!expectedText)
    throw new Error('The native tool output Copy proof requires nonempty native preview text.')
  await page.evaluate(async sentinel => navigator.clipboard.writeText(sentinel), `CLIPBOARD${randomUUID()}`)
  const view = result.locator('..')
  await view.hover()
  await view.getByRole('button', { name: 'Copy', exact: true }).click()
  await expect.poll(async () => {
    const actual = await page.evaluate(() => navigator.clipboard.readText())
    return { exact: actual === expectedText, utf16CodeUnits: actual.length }
  }).toEqual({ exact: true, utf16CodeUnits: expectedText.length })
}

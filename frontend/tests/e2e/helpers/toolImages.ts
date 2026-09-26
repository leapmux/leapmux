import type { Locator, Page } from '@playwright/test'
import { Buffer } from 'node:buffer'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'

/**
 * Image-in-tool-result fixtures and assertions.
 *
 * A provider's own `Read` (or equivalent) tool produces the image content when
 * the scripted call opens a real PNG in the working directory. The mock only
 * scripts the call; the picture in the tool row is proof that the CLI ran the
 * tool and that LeapMux rendered the result.
 */

/**
 * A 64x64 RGBA PNG: a teal field with a green marker square. Valid chunks and
 * CRCs, large enough that no decoder rejects it as a degenerate image.
 */
const TOOL_IMAGE_PNG_BASE64
  = 'iVBORw0KGgoAAAANSUhEUgAAAEAAAABACAYAAACqaXHeAAAAb0lEQVR42u3YMREAIAwEwZcYicjBFSigykwatjgDW15S63wdAAAAAAAAAODdTi8AAAAAAAAAAAAAAAAAAAAAgCECAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAJjqArCUycOeoJLSAAAAAElFTkSuQmCC'

/**
 * Write a PNG into `workingDir` and return its file name.
 *
 * The name carries a marker the prompt never states, so a tool row that names
 * it can only come from the tool call the script issued.
 */
export function writeToolImage(workingDir: string, marker: string): string {
  const name = `tool-image-${marker}.png`
  writeFileSync(join(workingDir, name), Buffer.from(TOOL_IMAGE_PNG_BASE64, 'base64'))
  return name
}

/** Every visible tool row. Scope to `:visible` — a premeasure copy is hidden. */
export function toolRows(page: Page): Locator {
  return page.locator('[data-tool-message]:visible')
}

/**
 * The inline image of a tool result, in either row layout.
 *
 * Every tool picture draws through the shared image-result view, whose open
 * control wraps the image. A lookup for that control reaches the picture of a
 * merged row and of a split request/result pair alike.
 */
function toolResultImages(page: Page): Locator {
  return page.locator('button[aria-label="Open image"] img')
}

/**
 * Assert a tool call of `fileName` drew an inline image.
 *
 * Two row layouts exist. A provider that merges the call and its result into one
 * row draws the picture inside the row that names the file. A provider that keeps
 * a request row beside a result row names the file in the request row and draws
 * the picture in the result row, which drops its header -- and with it the
 * `data-tool-message` hook. A text placeholder has no image element, so the file
 * name alone cannot satisfy this check.
 */
export async function expectToolRowImage(page: Page, fileName: string): Promise<void> {
  await expect(toolRows(page).filter({ hasText: fileName }).first()).toBeVisible()
  const image = toolResultImages(page).first()
  await expect(image).toBeVisible()
  // An `img` element alone proves nothing: a broken picture keeps the element
  // and draws a placeholder. A decoded image reports its pixel width.
  await expect.poll(() => image.evaluate(el => (el as HTMLImageElement).naturalWidth)).toBeGreaterThan(0)
}

/**
 * Assert a tool call of `fileName` ran and drew no picture.
 *
 * Codewhale and Cline build tool results as text only (matrix note 3). A
 * provider that restores a picture from another store may also draw none on
 * this path. The name alone proves the tool ran.
 */
export async function expectToolRowWithoutImage(page: Page, fileName: string): Promise<void> {
  await expect(toolRows(page).filter({ hasText: fileName }).first()).toBeVisible()
  await expect(toolResultImages(page)).toHaveCount(0)
}

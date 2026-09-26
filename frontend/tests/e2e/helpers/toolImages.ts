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

/** A 2x2 PNG with distinct corner pixels. Decodable, large enough to see. */
const TINY_PNG_BASE64
  = 'iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAYAAABytg0kAAAAFElEQVR42mP8z8DwHwAFBQIAX8jx0gAAAABJRU5ErkJggg=='

/**
 * Write a PNG into `workingDir` and return its file name.
 *
 * The name carries a marker the prompt never states, so a tool row that names
 * it can only come from the tool call the script issued.
 */
export function writeToolImage(workingDir: string, marker: string): string {
  const name = `tool-image-${marker}.png`
  writeFileSync(join(workingDir, name), Buffer.from(TINY_PNG_BASE64, 'base64'))
  return name
}

/** Every visible tool row. Scope to `:visible` — a premeasure copy is hidden. */
export function toolRows(page: Page): Locator {
  return page.locator('[data-tool-message]:visible')
}

/**
 * Assert a tool row carries an inline image.
 *
 * The row's `<img>` is the only element the chat renders for a picture in a
 * tool result. A text placeholder has no `img`, so this check is not satisfied
 * by the file name alone.
 */
export async function expectToolRowImage(page: Page, fileName: string): Promise<void> {
  const row = toolRows(page).filter({ hasText: fileName }).first()
  await expect(row).toBeVisible()
  await expect(row.locator('img').first()).toBeVisible()
}

/**
 * Assert a tool row holds the file name but draws no picture.
 *
 * Codewhale and Cline build tool results as text only (matrix note 3). A
 * provider that restores a picture from another store may also draw none on
 * this path. The name alone proves the tool ran.
 */
export async function expectToolRowWithoutImage(page: Page, fileName: string): Promise<void> {
  const row = toolRows(page).filter({ hasText: fileName }).first()
  await expect(row).toBeVisible()
  await expect(row.locator('img')).toHaveCount(0)
}

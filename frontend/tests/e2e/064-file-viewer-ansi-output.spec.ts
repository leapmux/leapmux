import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import { createTestDirectory } from './helpers/runDirectory'
import { treeRow } from './helpers/ui'
import { showWorkspaceWithAgents } from './helpers/workspace'

test.describe('file viewer ANSI output', () => {
  /**
   * A `.log` file is ANSI output. The main thread colours it with the synchronous Shiki highlighter, which holds no
   * grammar: Shiki colours ANSI without one. This drives that highlighter in a real browser, with the theme pair that the
   * app registers on it at boot.
   */
  test('colours the ANSI escapes of a log file', async ({ page, leapmuxServer }) => {
    const dir = createTestDirectory('leapmux-ansi-log-')
    writeFileSync(join(dir, 'build.log'), 'plain start\n\x1B[31mERROR failed\x1B[0m\nplain end\n')
    await showWorkspaceWithAgents(page, leapmuxServer, 'ANSI Log', { workingDir: dir })

    await treeRow(page, 'build.log').click()

    const error = page.locator('span[data-shiki-token]:visible', { hasText: 'ERROR' })
    const plain = page.locator('span[data-shiki-token]:visible', { hasText: 'plain start' })
    await expect(error).toHaveCount(1)
    await expect(plain).toHaveCount(1)
    // The escapes are gone from the text, and the red run has a colour of its own.
    await expect(error).toHaveText('ERROR failed')
    const colourOf = (token: typeof error) => token.evaluate(element => getComputedStyle(element).color)
    expect(await colourOf(error)).not.toBe(await colourOf(plain))
  })
})

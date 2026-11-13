import { rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import { createTestDirectory } from './helpers/runDirectory'
import { openFileInDiffMode, visibleText } from './helpers/ui'
import { showWorkspaceWithAgents } from './helpers/workspace'
import { commitFile, initGitRepo } from './helpers/worktree'

/** `count` numbered lines that start with `side`, each ending in a newline. */
function numberedLines(side: string, count: number): string {
  return Array.from({ length: count }, (_, index) => `${side} line ${String(index + 1).padStart(4, '0')}\n`).join('')
}

/**
 * A rewrite changes every line, so the line diff needs more edits than the diff worker
 * searches for, and the view draws the whole old text removed and the whole new text
 * added. The view must say so, or a reader takes the diff for a real change to every
 * line. The file viewer mounts one copy of the diff, so it draws all 6,000 lines, more
 * than the line limit of a chat row.
 */
test('states that a rewritten file draws as a whole replacement, and draws every line of it', async ({ page, leapmuxServer }) => {
  const dir = createTestDirectory('leapmux-e2e-git-rewrite-')
  try {
    initGitRepo(dir)
    commitFile(dir, 'rewritten.txt', numberedLines('old', 3000), 'initial')
    writeFileSync(join(dir, 'rewritten.txt'), numberedLines('new', 3000))

    await showWorkspaceWithAgents(page, leapmuxServer, 'Rewritten File', { workingDir: dir })
    await openFileInDiffMode(page, 'changed', 'rewritten.txt')

    await expect(page.getByTestId('diff-replacement-notice').filter({ visible: true })).toHaveText(
      'Too many lines changed to compare them one by one. This diff removes every old line and adds every new line.',
    )
    await expect(visibleText(page, 'old line 0001')).toBeVisible()
    await expect(visibleText(page, 'old line 3000')).toBeVisible()
    await expect(visibleText(page, 'new line 0001')).toBeVisible()
    await expect(visibleText(page, 'new line 3000')).toBeVisible()
    await expect(page.getByTestId('diff-truncation-notice')).toHaveCount(0)
  }
  finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

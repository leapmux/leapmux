import { expect, test } from './fixtures'
import { loginViaUI } from './helpers/ui'
import { openNewWorkspaceDialog } from './helpers/worktree'

/**
 * The directory picker is rooted at the FILESYSTEM ROOT, not the home
 * directory.
 *
 * The picker used to root at the literal `~`, which the Worker expanded, so
 * nothing outside the home directory could be reached by clicking. These tests
 * cover the two halves of the change: the root is now reachable, and the home
 * directory is still one glance away rather than several clicks down.
 *
 * The CI Worker runs Linux, so the Windows drive selector gets no coverage
 * here. `DriveSelector.test.tsx` and `DirectorySelector.test.tsx` carry it.
 */
test.describe('Directory picker root', () => {
  test('roots the tree at the filesystem root', async ({ page }) => {
    await loginViaUI(page)
    await openNewWorkspaceDialog(page)

    const rootNode = page.locator('[data-testid="tree-root-node"]:visible')
    await expect(rootNode).toBeVisible()
    await expect(rootNode.locator('[data-testid="tree-row-name"]')).toHaveText('/')
  })

  test('can browse to a directory outside the home directory', async ({ page }) => {
    await loginViaUI(page)
    await openNewWorkspaceDialog(page)

    await expect(page.locator('[data-testid="tree-root-node"]:visible')).toBeVisible()

    // `/tmp` exists on every POSIX host the suite runs on and is never under
    // `$HOME`, so reaching it by clicking is exactly what the old root made
    // impossible.
    //
    // The label may carry more than one segment. The tree asks for
    // `max_depth: 5`, and the worker merges a directory holding exactly one
    // child into its parent's row, so `/tmp` with a single subdirectory
    // renders as `tmp/<child>`. Match the leading segment, not the whole
    // label.
    const tmpRow = page.locator('[data-testid="tree-row"]:visible')
      .filter({ has: page.locator('[data-testid="tree-row-name"]', { hasText: /^(private|tmp)(\/|$)/ }) })
      .first()
    await expect(tmpRow).toBeVisible()
    await tmpRow.click()

    // Selecting it writes the working directory, which the path box shows as
    // an absolute path -- it is not under home, so it cannot tildify.
    const pathBox = page.getByPlaceholder('Enter path...')
    await expect(pathBox).toHaveValue(/^\/(private|tmp)/)
  })

  test('reveals the home directory without a click', async ({ page }) => {
    await loginViaUI(page)
    await openNewWorkspaceDialog(page)

    await expect(page.locator('[data-testid="tree-root-node"]:visible')).toBeVisible()

    // The dialog opens with no selection, so the tree walks itself open toward
    // the Worker's home directory. Its own row is visible with zero clicks,
    // and nothing is selected: `revealPath` expands, it never selects.
    //
    // The leading segment only, for the merge reason the test above states: a
    // single-user host renders `/home` as `home/<user>` in one row.
    const homeRow = page.locator('[data-testid="tree-row"]:visible')
      .filter({ has: page.locator('[data-testid="tree-row-name"]', { hasText: /^(home|Users|root)(\/|$)/ }) })
      .first()
    await expect(homeRow).toBeVisible()
    await expect(page.locator('[data-testid="tree-row"][data-active="true"]:visible')).toHaveCount(0)
  })

  test('the home button selects the home directory', async ({ page }) => {
    await loginViaUI(page)
    await openNewWorkspaceDialog(page)

    await expect(page.locator('[data-testid="tree-root-node"]:visible')).toBeVisible()
    // The dialog opens unarmed -- the test above states why.
    await expect(page.locator('[data-testid="tree-row"][data-active="true"]:visible')).toHaveCount(0)

    await page.locator('[data-testid="directory-selector-home"]:visible').click()

    // The button SELECTS, unlike the reveal that opened the dialog. The path
    // box tildifies a path under the home directory, and the home directory
    // itself is the shortest such path.
    await expect(page.getByPlaceholder('Enter path...')).toHaveValue(/^(~|\/(home|Users|root)\/)/)
    await expect(page.locator('[data-testid="tree-row"][data-active="true"]:visible')).toHaveCount(1)

    // That the button also OPENS the directory is asserted in
    // `DirectoryTree.test.tsx`, against the tree's expansion state. Here it
    // would need the Worker's home directory to hold a child, which no CI host
    // guarantees.
  })
})

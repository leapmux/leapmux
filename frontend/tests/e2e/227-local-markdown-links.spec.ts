import type { Page } from '@playwright/test'
import type { ModelScript } from './helpers/modelScriptFixture'
import { Buffer } from 'node:buffer'
import { mkdirSync, realpathSync, rmSync, writeFileSync } from 'node:fs'
import { basename, join } from 'node:path'
import { expect } from '@playwright/test'
import { test as base } from './fixtures'
import { cssAttributeValue } from './helpers/cssAttribute'
import { createTestDirectory } from './helpers/runDirectory'
import { clearRecordedToasts, expectToastRecorded, getRecordedToasts } from './helpers/toast'
import {
  agentTabs,
  assistantBubbles,
  sendMessage,
  sidebarSectionHeader,
  treeRow,
  waitForAgentIdle,
} from './helpers/ui'
import { waitForAgentStartupViaAPI } from './helpers/workerTabs'

/** Valid 1x1 PNG so Chromium decodes it with naturalWidth === 1. */
const TINY_PNG_BYTES = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=',
  'base64',
)

const TINY_SVG_SOURCE = '<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16"><rect width="16" height="16" fill="#3b82f6"/></svg>'

const test = base.extend({
  // eslint-disable-next-line no-empty-pattern -- Playwright requires a destructuring pattern for the fixture argument
  agentWorkingDir: async ({}, use) => {
    const dir = realpathSync(createTestDirectory('local-md-repo-'))
    mkdirSync(join(dir, 'docs', 'nested'), { recursive: true })
    writeFileSync(
      join(dir, 'README.md'),
      '# Workspace Root Readme\n\nRoot content marker.\n',
    )
    writeFileSync(
      join(dir, 'docs', 'guide.md'),
      [
        '# Local Guide Heading',
        '',
        'Guide line two marker.',
        '',
        '![Architecture](./arch.png)',
        '',
        '![Icon](./icon.svg)',
        '',
        'Back to [Root note](../README.md).',
      ].join('\n'),
    )
    writeFileSync(join(dir, 'docs', 'arch.png'), TINY_PNG_BYTES)
    writeFileSync(join(dir, 'docs', 'icon.svg'), TINY_SVG_SOURCE)
    writeFileSync(join(dir, 'docs', 'nested', 'info.txt'), 'nested file\n')
    try {
      await use(dir)
    }
    finally {
      rmSync(dir, { recursive: true, force: true })
    }
  },
  authenticatedWorkspace: async ({ authenticatedWorkspace, leapmuxServer }, use) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    await waitForAgentStartupViaAPI(hubUrl, adminToken, workerId, authenticatedWorkspace.workspaceId)
    await use(authenticatedWorkspace)
  },
})

async function runMarkdownReply(
  page: Page,
  modelScript: ModelScript,
  promptLabel: string,
  markdownReply: string,
): Promise<void> {
  const step = await modelScript.queue({ text: markdownReply })
  await sendMessage(page, modelScript.prompt(promptLabel))
  await modelScript.waitForSteps(step + 1)
  await waitForAgentIdle(page)
}

test.describe('local Markdown links, directory reveal, and inline local images', () => {
  test('opens local file links and line-suffixed links from chat with resolved-path hover tooltip', async ({ page, authenticatedWorkspace, modelScript }) => {
    const workingDir = authenticatedWorkspace.workingDir!
    await runMarkdownReply(
      page,
      modelScript,
      'Reply with local file links.',
      'Read [Open guide](./docs/guide.md) or jump to [Line ref](docs/guide.md:2).',
    )

    const guideLink = assistantBubbles(page).locator('[data-local-link="./docs/guide.md"]').first()
    const lineRefLink = assistantBubbles(page).locator('[data-local-link="docs/guide.md:2"]').first()
    await expect(guideLink).toBeVisible()
    await expect(guideLink).toHaveAttribute('role', 'button')
    await expect(guideLink).not.toHaveAttribute('title')

    // Hovering displays the resolved absolute path in a role="tooltip" popover.
    await guideLink.hover()
    const tooltip = page.getByRole('tooltip')
    await expect(tooltip).toBeVisible()
    await expect(tooltip).toHaveText(join(workingDir, 'docs', 'guide.md'))

    // Clicking opens docs/guide.md in a File Viewer tab in rendered Markdown mode.
    await guideLink.click()
    const fileContent = page.locator('[data-testid="file-viewer-content"]:visible').first()
    await expect(fileContent).toBeVisible()
    await expect(fileContent.getByRole('heading', { level: 1, name: 'Local Guide Heading' })).toBeVisible()

    // Return to the agent tab and click the line-suffixed link (docs/guide.md:2).
    await agentTabs(page).first().click()
    await expect(lineRefLink).toBeVisible()
    await lineRefLink.click()
    await expect(fileContent.getByRole('heading', { level: 1, name: 'Local Guide Heading' })).toBeVisible()
  })

  test('renders inline local PNG and SVG images in FileViewer and resolves relative file links', async ({ page, authenticatedWorkspace, modelScript }) => {
    void authenticatedWorkspace
    await runMarkdownReply(
      page,
      modelScript,
      'Link the guide.',
      'Inspect [Open guide](./docs/guide.md).',
    )

    await assistantBubbles(page).locator('[data-local-link="./docs/guide.md"]').first().click()
    const fileContent = page.locator('[data-testid="file-viewer-content"]:visible').first()
    await expect(fileContent.getByRole('heading', { level: 1, name: 'Local Guide Heading' })).toBeVisible()

    // Inline local PNG and SVG load over worker E2EE RPC into blob: URLs with real dimensions.
    const pngImg = fileContent.locator('[data-local-image="./arch.png"] img')
    const svgImg = fileContent.locator('[data-local-image="./icon.svg"] img')
    await expect(pngImg).toBeVisible()
    await expect(svgImg).toBeVisible()
    await expect(pngImg).toHaveAttribute('src', /^blob:/u)
    await expect(svgImg).toHaveAttribute('src', /^blob:/u)
    await expect.poll(() => pngImg.evaluate(el => (el as HTMLImageElement).naturalWidth)).toBeGreaterThan(0)
    await expect.poll(() => svgImg.evaluate(el => (el as HTMLImageElement).naturalWidth)).toBeGreaterThan(0)

    // Clicking the standalone PNG opens arch.png in a File Viewer image tab.
    await pngImg.click()
    const imageTab = page.locator('[data-testid="tab"][data-tab-type="file"]').filter({ hasText: 'arch.png' }).first()
    await expect(imageTab).toBeVisible()
    const viewerImg = page.locator('[data-testid="file-viewer-content"]:visible img').first()
    await expect(viewerImg).toBeVisible()
    await expect(viewerImg).toHaveAttribute('src', /^blob:/u)

    // Switch back to guide.md and follow the parent-relative ../README.md link.
    await page.locator('[data-testid="tab"][data-tab-type="file"]').filter({ hasText: 'guide.md' }).first().click()
    const rootNoteLink = fileContent.locator('[data-local-link="../README.md"]').first()
    await expect(rootNoteLink).toBeVisible()
    await rootNoteLink.click()

    await expect(fileContent.getByRole('heading', { level: 1, name: 'Workspace Root Readme' })).toBeVisible()
    await expect(fileContent).toContainText('Root content marker.')
  })

  test('expands a collapsed sidebar and Files section, focuses the target directory, and pulses the visual cue', async ({ page, authenticatedWorkspace, modelScript }) => {
    void authenticatedWorkspace
    await runMarkdownReply(
      page,
      modelScript,
      'Link the nested directory.',
      'Check [Docs folder](./docs/nested) for files.',
    )

    // Ensure the Files tree and Goals & To-dos section are ready first (Files is
    // collapsible only when another section is visible in the right sidebar).
    const filesHeader = sidebarSectionHeader(page, 'files')
    await expect(filesHeader).toBeVisible()
    await expect(sidebarSectionHeader(page, 'todos')).toBeVisible()
    await expect(treeRow(page, 'docs')).toBeVisible()

    // Click the Files title text rather than the summary center, which overlaps
    // the Collapse all action button inside the header.
    await filesHeader.getByTestId('section-header-files-summary').getByText('Files', { exact: true }).click()
    await expect(filesHeader).toHaveAttribute('data-closed', '')

    const toggleRightSidebar = page.getByRole('button', { name: /Toggle right sidebar/u })
    await toggleRightSidebar.click()
    await expect(filesHeader).toBeHidden()

    // Click the local directory link in the chat message.
    await assistantBubbles(page).locator('[data-local-link="./docs/nested"]').first().click()

    // Check the transient 1400ms reveal cue first so parallel shard load does not
    // expire it while awaiting the persistent sidebar and expansion assertions.
    const docsRow = treeRow(page, 'docs')
    const nestedRow = treeRow(page, 'nested')
    await expect(nestedRow).toHaveAttribute('data-reveal-cue', 'true')

    await expect(filesHeader).toBeVisible()
    await expect(filesHeader).not.toHaveAttribute('data-closed')
    await expect(docsRow).toBeVisible()
    await expect(docsRow).toHaveAttribute('aria-expanded', 'true')
    await expect(nestedRow).toBeVisible()
    await expect(nestedRow).toHaveAttribute('aria-expanded', 'true')
    await expect(nestedRow).toBeFocused()

    // Visual cue clears automatically after the cue window.
    await expect(nestedRow).not.toHaveAttribute('data-reveal-cue')
  })

  test('shows a single-line warning toast for a directory outside the active file tree without re-rooting', async ({ page, authenticatedWorkspace, modelScript }) => {
    const workingDir = authenticatedWorkspace.workingDir!
    const outsideDir = realpathSync(createTestDirectory('local-md-outside-'))
    const markdownOutsideDest = `<${outsideDir.replace(/\\/g, '%5C').replace(/>/g, '%3E')}>`
    const expectedAttrHref = outsideDir.replace(/\\/g, '%5C').replace(/>/g, '%3E')
    try {
      await runMarkdownReply(
        page,
        modelScript,
        'Link an outside directory.',
        `See [Outside dir](${markdownOutsideDest}).`,
      )

      const rootNode = page.locator('[data-testid="tree-root-node"]:visible').first()
      await expect(rootNode).toContainText(basename(workingDir))

      await clearRecordedToasts(page)
      await assistantBubbles(page).locator(`[data-local-link="${cssAttributeValue(expectedAttrHref)}"]`).first().click()

      await expectToastRecorded(page, `Directory is outside the current file tree: ${outsideDir}`)
      const toasts = await getRecordedToasts(page)
      const warning = toasts.find(t => t.message.includes('Directory is outside the current file tree:'))
      // showWarnToast renders with data-variant="danger" (see Toast.tsx).
      expect(warning?.variant).toBe('danger')
      expect(warning?.message).not.toContain('\n')

      // Files tree stays rooted at the workspace directory.
      await expect(rootNode).toContainText(basename(workingDir))
    }
    finally {
      rmSync(outsideDir, { recursive: true, force: true })
    }
  })

  test('keeps dangerous schemes, UNC paths, and remote file URIs stripped to plain text', async ({ page, authenticatedWorkspace, modelScript }) => {
    void authenticatedWorkspace
    await runMarkdownReply(
      page,
      modelScript,
      'Reply with dangerous link targets.',
      'Links: [bad](javascript:alert(1)), [unc](\\\\\\\\evil.example\\\\share\\\\a.txt), and [file-unc](file:////evil.example/share/a.txt).',
    )

    const bubble = assistantBubbles(page).filter({ hasText: 'Links:' }).first()
    await expect(bubble).toContainText('bad')
    await expect(bubble).toContainText('unc')
    await expect(bubble).toContainText('file-unc')
    await expect(bubble.locator('a')).toHaveCount(0)
    await expect(bubble.locator('[data-local-link]')).toHaveCount(0)
  })
})

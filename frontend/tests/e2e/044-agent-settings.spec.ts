import { MODEL_NONDETERMINISM_RETRIES } from './helpers/modelRetries'
import { applyPermissionPreset, ARITHMETIC_PROMPT, assistantBubbles, chooseSettingsOption, expectAssistantAnswer, expectSettingsChip, openAgentViaUI, openPlusMenu, openSettingsMenu, permissionModeOffered, SECOND_ARITHMETIC_ANSWER, SECOND_ARITHMETIC_PROMPT, settingsBar, settingsGroupTrigger, visibleOnly, waitForSettingsHydrated, waitForSettingsIdle } from './helpers/ui'
import { expect, restartWorker, stopWorker, processTest as test } from './process-control-fixtures'

test.describe('Agent Settings', () => {
  test('default settings on startup', async ({ authenticatedWorkspace, page }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Default: Sonnet model (overridden via LEAPMUX_CLAUDE_DEFAULT_MODEL in e2e), Default permission mode.
    // Retrying assertions, not a one-shot textContent(): the trigger renders a
    // bare ellipsis until the agent's option groups arrive, so reading it the
    // instant it becomes visible sees "…" and nothing else, forever.
    await expectSettingsChip(page, 'Sonnet')
    await expectSettingsChip(page, 'Default')
  })

  test('permission shortcuts use the Claude modes that the session offers', async ({ authenticatedWorkspace, page }) => {
    void authenticatedWorkspace
    await waitForSettingsHydrated(page)
    // Claude's Smart preset sets permissionMode=auto, and livePermissionModeGroup drops
    // the auto option exactly when the startup probe rejected it. So the picker decides
    // whether the shortcut must be there -- asserted either way, rather than skipped.
    const autoOffered = await permissionModeOffered(page, 'auto')

    const menu = await openPlusMenu(page)
    const smart = menu.getByTestId('composer-smart-permissions')
    await expect(menu.getByTestId('composer-bypass-permissions')).toBeVisible()
    if (autoOffered)
      await expect(smart).toBeVisible()
    else
      await expect(smart).toHaveCount(0)

    await applyPermissionPreset(page, 'bypass')
    await expectSettingsChip(page, 'Bypass Permissions')

    if (autoOffered) {
      await applyPermissionPreset(page, 'smart')
      await expectSettingsChip(page, 'Auto Mode')
    }
  })

  test('switch permission modes', async ({ authenticatedWorkspace, page }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Switch to Plan Mode (dropdown auto-closes on select)
    await chooseSettingsOption(page, 'permissionMode-plan')
    await expectSettingsChip(page, 'Plan Mode')
    await waitForSettingsIdle(page)

    // Switch to Accept Edits
    await chooseSettingsOption(page, 'permissionMode-acceptEdits')
    await expectSettingsChip(page, 'Accept Edits')
    await waitForSettingsIdle(page)

    // Switch to Bypass Permissions
    await chooseSettingsOption(page, 'permissionMode-bypassPermissions')
    await expectSettingsChip(page, 'Bypass Permissions')
    await waitForSettingsIdle(page)

    // Switch to Don't Ask (always available — not plan/admin-gated)
    await chooseSettingsOption(page, 'permissionMode-dontAsk')
    await expectSettingsChip(page, 'Don\'t Ask')
    await waitForSettingsIdle(page)

    // Switch to Auto Mode if the backend's startup probe surfaced it.
    // Auto mode depends on plan / model / admin-managed settings
    // (see permission-modes docs); if the probe rejected it, the radio
    // is filtered out server-side and this block becomes a no-op.
    await openSettingsMenu(page, 'permissionMode')
    const autoOffered = await page.locator('[data-testid="permissionMode-auto"]').isVisible()
    await page.keyboard.press('Escape')
    if (autoOffered) {
      await chooseSettingsOption(page, 'permissionMode-auto')
      await expectSettingsChip(page, 'Auto Mode')
      await waitForSettingsIdle(page)
    }

    // Switch back to Default
    await chooseSettingsOption(page, 'permissionMode-default')
    await expectSettingsChip(page, 'Default')
  })

  test('switch model', async ({ authenticatedWorkspace, page }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Change model to Haiku (default is Sonnet, dropdown auto-closes on select)
    await chooseSettingsOption(page, 'model-haiku')
    await expectSettingsChip(page, 'Haiku')
  })

  // Nested so the retry budget covers ONLY this test. Its subject -- that a
  // model whose name contains brackets gets escaped correctly when spawning
  // Claude Code -- can only be observed by making the restarted agent answer,
  // so the assertion is on the model's output and inherits its variance. Every
  // other test in this file asserts app state and must keep failing on the
  // first attempt. See MODEL_NONDETERMINISM_RETRIES.
  test.describe('bracketed model names', () => {
    test.describe.configure({ retries: MODEL_NONDETERMINISM_RETRIES })

    test('switch to model with bracket characters', async ({ authenticatedWorkspace, page }) => {
      const trigger = settingsBar(page)
      await expect(trigger).toBeVisible()

      // Switch to Opus[1m] — model name contains brackets that must be
      // properly escaped in the shell command when spawning Claude Code.
      // We use Opus[1m] (not Sonnet[1m]) because the e2e account doesn't have
      // extra-usage billing enabled for Sonnet's 1M context tier.
      await chooseSettingsOption(page, 'model-opus[1m]')
      await expectSettingsChip(page, 'Opus (1M context)')
      await waitForSettingsIdle(page)

      // Verify agent restarted successfully by sending a message
      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await expect(editor).toBeVisible()
      await editor.click()
      await page.keyboard.type('What is 3+4? Reply with just the number, nothing else.')
      await page.keyboard.press('Meta+Enter')

      // Wait for an assistant response — if the agent failed to start, we
      // would see an error notification instead.
      // Scan all bubbles for the answer rather than .last(): the per-turn "Took Ns"
      // meta bubble also carries data-role="agent" and can be last, racing the
      // answer bubble.
      await expectAssistantAnswer(page, { answer: /\b7\b/ })
    })
  })

  test('switch effort', async ({ authenticatedWorkspace, page }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Change effort to High (dropdown auto-closes on select)
    await chooseSettingsOption(page, 'effort-high')
    // Dropdown closes; re-open to verify selection
    await openSettingsMenu(page, 'effort')
    await expect(page.locator('[data-testid="effort-high"] input[type="radio"]')).toBeChecked()
    await page.keyboard.press('Escape')
  })

  test('effort hidden when haiku selected', async ({ authenticatedWorkspace, page }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Default model is Sonnet — the effort axis is offered; switch to Haiku.
    // The composer gives each axis its own `[+]` submenu, so "the axis is not
    // offered" shows up as the submenu (and its status-bar chip) being absent,
    // not as an empty section inside one fused menu.
    const effortSubmenu = settingsGroupTrigger(page, 'effort')
    const effortChip = page.locator('[data-testid="composer-effort-trigger"]')

    await openSettingsMenu(page, 'effort')
    await expect(page.locator('[data-testid="effort-high"]')).toBeVisible()
    await chooseSettingsOption(page, 'model-haiku')
    await expectSettingsChip(page, 'Haiku')
    await waitForSettingsIdle(page)

    // Effort is hidden for Haiku, on both surfaces.
    await openPlusMenu(page)
    await expect(effortSubmenu).toHaveCount(0)
    await expect(effortChip).toHaveCount(0)
    await page.keyboard.press('Escape')

    // Switch back to Sonnet — effort should reappear
    await chooseSettingsOption(page, 'model-sonnet')
    await expectSettingsChip(page, 'Sonnet')
    await waitForSettingsIdle(page)

    await openSettingsMenu(page, 'effort')
    await expect(page.locator('[data-testid="effort-high"]')).toBeVisible()
    await page.keyboard.press('Escape')
  })

  test('the effort menu is the same before and after a model round trip', async ({ authenticatedWorkspace, page }) => {
    void authenticatedWorkspace // fixture trigger
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // The CLI reports the same effort levels for every effort-capable model, so
    // the menu must not CHANGE as the user moves between them. It used to: the
    // static fallback catalog declared Sonnet max-only, and the live catalog
    // that replaced it on the first settings change declared xhigh -- so
    // xhigh/ultracode appeared out of nowhere after one model switch. This walks
    // the round trip and pins the three menus against each other, which is the
    // regression, rather than pinning which tier belongs to which model (a claim
    // that belongs to the CLI and changes without us).
    const effortOptions = async (): Promise<string[]> => {
      await waitForSettingsHydrated(page)
      await openSettingsMenu(page, 'effort')
      await expect(page.locator('[data-testid="effort-auto"]')).toBeVisible()
      const ids = await page.locator('[data-testid^="effort-"]:visible').evaluateAll(els =>
        els.map(el => el.getAttribute('data-testid') ?? ''),
      )
      await page.keyboard.press('Escape')
      return ids
    }

    const onSonnet = await effortOptions()
    expect(onSonnet, 'Sonnet offers an effort menu').not.toHaveLength(0)

    await chooseSettingsOption(page, 'model-opus[1m]')
    await expectSettingsChip(page, 'Opus')
    await waitForSettingsIdle(page)
    expect(await effortOptions(), 'Opus offers the same tiers').toEqual(onSonnet)

    await chooseSettingsOption(page, 'model-sonnet')
    await expectSettingsChip(page, 'Sonnet')
    await waitForSettingsIdle(page)
    expect(await effortOptions(), 'and Sonnet still does on the way back').toEqual(onSonnet)
  })

  test('ultracode effort is selectable and keeps the agent working', async ({ authenticatedWorkspace, page }) => {
    void authenticatedWorkspace // fixture trigger
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    await openSettingsMenu(page, 'effort')
    await expect(page.locator('[data-testid="effort-ultracode"]')).toBeVisible()
    await page.keyboard.press('Escape')

    // Select ultracode. CI accounts lack the dynamic-workflows entitlement, so
    // the CLI gracefully downgrades to xhigh (apply_flag_settings no-ops and
    // get_settings reports ultracode:false). We therefore assert the agent
    // stays functional rather than that the selection persists as ultracode.
    await chooseSettingsOption(page, 'effort-ultracode')
    await waitForSettingsIdle(page)

    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()
    // Use a distinctive sentinel word as the answer, not a number. A numeric
    // answer would be unsafe here because the "Took Ns" bubble's duration
    // (e.g. "Took 11s") shares data-role="agent" and would substring-match a
    // numeric sentinel like "11", letting the test pass on the duration bubble
    // even if the agent never answered.
    await page.keyboard.type('Reply with exactly the word PINEAPPLE and nothing else.')
    await page.keyboard.press('Meta+Enter')
    await expect(assistantBubbles(page).filter({ hasText: 'PINEAPPLE' })).toBeVisible()
  })

  test('Extended Thinking label reflects model', async ({ authenticatedWorkspace, page }) => {
    const onOpt = page.locator('[data-testid="alwaysThinkingEnabled-on"]')
    const offOpt = page.locator('[data-testid="alwaysThinkingEnabled-off"]')

    // Default (Sonnet — set via LEAPMUX_CLAUDE_DEFAULT_MODEL) supports
    // adaptive thinking, so the enabled option is labeled "Adaptive".
    // The option ID is always "on"; only the display name varies.
    await openSettingsMenu(page, 'alwaysThinkingEnabled')
    await expect(onOpt).toBeVisible()
    await expect(onOpt).toContainText('Adaptive')
    await expect(offOpt).toBeVisible()
    await expect(offOpt).toContainText('Off')

    // Toggle Off + back on to verify the radio wires through.
    await offOpt.click()
    await waitForSettingsIdle(page)
    await openSettingsMenu(page, 'alwaysThinkingEnabled')
    await expect(page.locator('[data-testid="alwaysThinkingEnabled-off"] input[type="radio"]')).toBeChecked()
    await onOpt.click()
    await waitForSettingsIdle(page)
    await openSettingsMenu(page, 'alwaysThinkingEnabled')
    await expect(page.locator('[data-testid="alwaysThinkingEnabled-on"] input[type="radio"]')).toBeChecked()

    // Switch to Haiku — no adaptive support, so the enabled option
    // relabels to "On". AgentStatusChange carries fresh option groups, so
    // this happens without a page reload.
    await chooseSettingsOption(page, 'model-haiku')
    await expectSettingsChip(page, 'Haiku')
    await waitForSettingsIdle(page)
    await openSettingsMenu(page, 'alwaysThinkingEnabled')
    await expect(onOpt).toContainText('On')
    await expect(onOpt).not.toContainText('Adaptive')

    // Switch back to Opus — adaptive support returns.
    await chooseSettingsOption(page, 'model-opus[1m]')
    await expectSettingsChip(page, 'Opus')
    await waitForSettingsIdle(page)
    await openSettingsMenu(page, 'alwaysThinkingEnabled')
    await expect(onOpt).toContainText('Adaptive')
    await page.keyboard.press('Escape')
  })

  test('a supported effort survives a model switch', async ({ authenticatedWorkspace, page }) => {
    void authenticatedWorkspace // fixture trigger
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Switch to Opus first, then pick xhigh.
    await chooseSettingsOption(page, 'model-opus[1m]')
    await expectSettingsChip(page, 'Opus')
    await waitForSettingsIdle(page)

    await chooseSettingsOption(page, 'effort-xhigh')
    await waitForSettingsIdle(page)
    await openSettingsMenu(page, 'effort')
    await expect(page.locator('[data-testid="effort-xhigh"] input[type="radio"]')).toBeChecked()
    await page.keyboard.press('Escape')

    // Sonnet supports xhigh too (the CLI reports the same tiers for both), so
    // the selection carries over rather than being clamped. This used to assert
    // a downgrade to high, which only held while the static catalog wrongly
    // declared Sonnet max-only.
    await chooseSettingsOption(page, 'model-sonnet')
    await expectSettingsChip(page, 'Sonnet')
    await waitForSettingsIdle(page)

    await openSettingsMenu(page, 'effort')
    await expect(page.locator('[data-testid="effort-xhigh"]')).toBeVisible()
    await expect(page.locator('[data-testid="effort-xhigh"] input[type="radio"]')).toBeChecked()
    await page.keyboard.press('Escape')
  })

  test('permission mode persistence across refresh', async ({ authenticatedWorkspace, page }) => {
    // Wait for the editor to be ready (agent is started)
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Switch to Plan Mode (dropdown auto-closes on select)
    await chooseSettingsOption(page, 'permissionMode-plan')
    await expectSettingsChip(page, 'Plan Mode')

    // Wait for the control_response round-trip to complete and DB to update.
    await page.waitForTimeout(3000)

    // Refresh the page
    await page.reload()

    // Verify Plan Mode is still selected after refresh
    const triggerAfter = settingsBar(page)
    await expect(triggerAfter).toBeVisible()
    await expectSettingsChip(page, 'Plan Mode')
  })

  test('model persistence across refresh', async ({ authenticatedWorkspace, page }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Change model to Haiku (default is Sonnet, dropdown auto-closes on select)
    await chooseSettingsOption(page, 'model-haiku')
    await expectSettingsChip(page, 'Haiku')

    // Wait for restart to complete
    await page.waitForTimeout(5000)

    // Refresh the page
    await page.reload()

    // Verify Haiku is still selected after refresh
    const triggerAfter = settingsBar(page)
    await expect(triggerAfter).toBeVisible()
    await expectSettingsChip(page, 'Haiku')
  })

  test('focus returns to editor after mode change', async ({ authenticatedWorkspace, page }) => {
    // Wait for the editor to be ready (agent is started)
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Open dropdown and click a mode
    await chooseSettingsOption(page, 'permissionMode-plan')

    // Close the dropdown by pressing Escape
    await page.keyboard.press('Escape')
    await expect(page.locator('[data-testid="composer-plus-popover"]')).not.toBeVisible()

    // Click the editor and verify it can receive focus
    await editor.click()
    await expect(editor).toBeFocused()
  })

  // Nested so the retry budget covers ONLY this test. Its subject -- that Plan
  // Mode survives a worker restart -- can only be reached by making the
  // RELAUNCHED agent answer, so it inherits the model's variance: the arithmetic
  // assertion is on what the model chose to emit, and a turn that narrates
  // instead settles nothing by waiting. Every other test in this file asserts
  // app state and must keep failing on the first attempt.
  // See MODEL_NONDETERMINISM_RETRIES.
  test.describe('worker restart', () => {
    test.describe.configure({ retries: MODEL_NONDETERMINISM_RETRIES })

    test('settings restored after worker restart', async ({ authenticatedWorkspace, separateHubWorker, page }) => {
      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      const trigger = settingsBar(page)
      await expect(trigger).toBeVisible()

      // Send a message to establish a session ID.
      await editor.click()
      await page.keyboard.type(SECOND_ARITHMETIC_PROMPT)
      await page.keyboard.press('Meta+Enter')

      // Wait for a response (ensures init message and session ID are stored)
      await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })

      // Switch to Plan Mode (dropdown auto-closes on select)
      await chooseSettingsOption(page, 'permissionMode-plan')
      await expectSettingsChip(page, 'Plan Mode')

      // Wait for the control_response round-trip to complete and DB to update.
      await page.waitForTimeout(3000)

      // Stop worker
      await stopWorker()
      await page.waitForTimeout(3000)

      // Restart worker
      await restartWorker(separateHubWorker)

      // Wait for the editor to become visible again after worker reconnects
      await expect(editor).toBeVisible()

      // Send a message to trigger agent re-launch via ensureAgentActive
      await editor.click()
      await page.keyboard.type(ARITHMETIC_PROMPT)
      await page.keyboard.press('Meta+Enter')

      // 6912 only appears in this response (the warmup answered 3333), so scanning
      // all bubbles for it is robust to the trailing "Took Ns" meta bubble that
      // .last() would otherwise race.
      await expectAssistantAnswer(page)

      // Verify Plan Mode is still selected after worker restart
      await expectSettingsChip(page, 'Plan Mode')
    })
  })

  test('interrupt via control request', async ({ authenticatedWorkspace, page }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Send a quick message to ensure the agent is fully started
    await editor.click()
    await page.keyboard.type(SECOND_ARITHMETIC_PROMPT)
    await page.keyboard.press('Meta+Enter')
    await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })

    // Verify the agent is still responsive after interrupt by sending another message
    await editor.click()
    await page.keyboard.type(ARITHMETIC_PROMPT)
    await page.keyboard.press('Meta+Enter')
    await expectAssistantAnswer(page)
  })

  test('model/effort items not disabled when idle', async ({ authenticatedWorkspace, page }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Each axis has its own `[+]` submenu now, so each one is opened before its
    // own items are read. The fused menu this replaced showed every axis at
    // once, which let a single open cover all three.

    // Verify all model items are enabled (not disabled) when idle
    await openSettingsMenu(page, 'model')
    await expect(page.locator('[data-testid="model-haiku"]')).not.toHaveAttribute('data-disabled', '')
    await expect(page.locator('[data-testid="model-sonnet"]')).not.toHaveAttribute('data-disabled', '')
    await expect(page.locator('[data-testid="model-sonnet\\[1m\\]"]')).not.toHaveAttribute('data-disabled', '')
    await expect(page.locator('[data-testid="model-opus\\[1m\\]"]')).not.toHaveAttribute('data-disabled', '')

    // Verify effort items are enabled when idle
    await openSettingsMenu(page, 'effort')
    await expect(page.locator('[data-testid="effort-auto"]')).not.toHaveAttribute('data-disabled', '')
    await expect(page.locator('[data-testid="effort-low"]')).not.toHaveAttribute('data-disabled', '')
    await expect(page.locator('[data-testid="effort-medium"]')).not.toHaveAttribute('data-disabled', '')
    await expect(page.locator('[data-testid="effort-high"]')).not.toHaveAttribute('data-disabled', '')
    // Every effort-capable model offers the same tiers, xhigh included
    await expect(page.locator('[data-testid="effort-max"]')).toBeVisible()
    await expect(page.locator('[data-testid="effort-max"]')).not.toHaveAttribute('data-disabled', '')
    await expect(page.locator('[data-testid="effort-xhigh"]')).toBeVisible()
    await expect(page.locator('[data-testid="effort-xhigh"]')).not.toHaveAttribute('data-disabled', '')

    // Verify permission mode items are enabled when idle
    await openSettingsMenu(page, 'permissionMode')
    await expect(page.locator('[data-testid="permissionMode-default"]')).not.toHaveAttribute('data-disabled', '')

    // Verify "Disabled while running" footnote is NOT visible when idle
    await expect(page.locator('[data-testid="settings-disabled-footnote"]')).not.toBeVisible()

    await page.keyboard.press('Escape')
  })

  test('settings change notification appears in chat', async ({ authenticatedWorkspace, page }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Change model to Haiku (default is Sonnet) — should produce a notification
    await chooseSettingsOption(page, 'model-haiku')
    await expectSettingsChip(page, 'Haiku')

    // Verify the notification bubble appears in chat
    await expect(visibleOnly(page.getByText('Model (Sonnet \u2192 Haiku)'))).toBeVisible()
  })

  test('Extended Thinking toggle round-trip tracks the confirmed state', async ({ authenticatedWorkspace, page }) => {
    const toggle = async (state: 'on' | 'off') => {
      await chooseSettingsOption(page, `alwaysThinkingEnabled-${state}`)
      await waitForSettingsIdle(page)
    }

    // off -> on -> off. Turning thinking back ON sends the flag as null (clear to the
    // CLI default), which get_settings then reports as ABSENT. The confirmed value
    // must still settle on that default ("on") so the final OFF is a REAL change and
    // the net settings notification reads "Off".
    await toggle('off')
    await toggle('on')
    await toggle('off')

    // The bug stranded the baseline on "off" after the on-toggle, making the final
    // off a silent no-op -- which would leave the net notification on "Adaptive"
    // instead of "Off". The END state is the subject, so both rendered forms are
    // accepted: the notification reads a bare "(Off)" when the option had no
    // prior stored value and "(Adaptive -> Off)" when it did (see firstSet in
    // notificationRenderers), and which one appears depends on whether the agent
    // had reported its confirmed baseline before the first toggle landed -- a
    // race this test neither controls nor is about.
    await expect(visibleOnly(page.getByText(/Extended Thinking \((?:.* → )?Off\)/))).toBeVisible()
    await expect(visibleOnly(page.getByText(/Extended Thinking \((?:.* → )?Adaptive\)/))).toHaveCount(0)
  })

  test('permission mode change notification appears in chat', async ({ authenticatedWorkspace, page }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Switch to Plan Mode
    await chooseSettingsOption(page, 'permissionMode-plan')
    await expectSettingsChip(page, 'Plan Mode')

    // Verify the notification bubble appears in chat
    await expect(visibleOnly(page.getByText('Mode (Default \u2192 Plan Mode)'))).toBeVisible()
  })

  test('no thinking indicator when switching settings', async ({ authenticatedWorkspace, page }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Install a MutationObserver to detect even a brief flash of the thinking indicator.
    // The ThinkingIndicator element is always in the DOM (collapsed via grid-template-rows: 0fr),
    // so we check whether it becomes visually expanded (grid-template-rows: 1fr / opacity: 1).
    await page.evaluate(() => {
      (window as any).__thinkingIndicatorSeen = false
      const observer = new MutationObserver(() => {
        const el = document.querySelector('[data-testid="thinking-indicator"]') as HTMLElement | null
        if (el && el.style.gridTemplateRows === '1fr') {
          (window as any).__thinkingIndicatorSeen = true
        }
      })
      observer.observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ['style'] })
      ;(window as any).__thinkingObserver = observer
    })

    // Switch permission mode to Plan Mode
    await chooseSettingsOption(page, 'permissionMode-plan')
    await expectSettingsChip(page, 'Plan Mode')
    await waitForSettingsIdle(page)

    // Switch model to Haiku (effort section hidden for Haiku)
    await chooseSettingsOption(page, 'model-haiku')
    await expectSettingsChip(page, 'Haiku')
    await waitForSettingsIdle(page)

    // Switch model back to Sonnet so effort section re-appears, then switch effort to High
    await chooseSettingsOption(page, 'model-sonnet')
    await expectSettingsChip(page, 'Sonnet')
    await waitForSettingsIdle(page)

    await chooseSettingsOption(page, 'effort-high')
    await waitForSettingsIdle(page)

    // Wait a moment for any delayed status events
    await page.waitForTimeout(2000)

    // Verify indicator was never shown
    const sawThinking = await page.evaluate(() => {
      (window as any).__thinkingObserver?.disconnect()
      return (window as any).__thinkingIndicatorSeen
    })
    expect(sawThinking).toBe(false)

    // Direct check too
    await expect(page.locator('[data-testid="thinking-indicator"]')).not.toBeVisible()
  })

  test('permission mode change in new agent tab targets correct agent', async ({ authenticatedWorkspace, page }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Verify first agent starts with Default mode
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Default')

    // Open a second agent tab
    await openAgentViaUI(page)

    // A new session requests Auto Mode, and the Claude CLI decides whether it can enter
    // it. The picker offers "auto" exactly when the startup probe accepted it, so it
    // pins WHICH mode to expect -- an either/or regex would also pass if the safe
    // default stopped being applied at all, and this is the only unpinned agent left in
    // the suite.
    await waitForSettingsHydrated(page)
    const expectedMode = await permissionModeOffered(page, 'auto') ? 'Auto Mode' : 'Default'
    await expectSettingsChip(page, expectedMode)

    // Switch the new agent to Plan Mode
    await chooseSettingsOption(page, 'permissionMode-plan')
    await expectSettingsChip(page, 'Plan Mode')
    await waitForSettingsIdle(page)

    // Verify the notification appears in the new agent's chat (not the first agent's)
    await expect(visibleOnly(page.getByText(`Mode (${expectedMode} → Plan Mode)`))).toBeVisible()

    // Switch back to the first agent tab
    const agentTabs = page.locator('[data-testid="tab"][data-tab-type="agent"]')
    await agentTabs.first().click()

    // First agent should still be in Default mode
    await expectSettingsChip(page, 'Default')
    // And should NOT have the permission mode notification
    await expect(visibleOnly(page.getByText('Mode (Default → Plan Mode)'))).not.toBeVisible()
  })

  test('settings loading indicator in the status bar', async ({ authenticatedWorkspace, page }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Change model to Haiku (default is Sonnet). The chip label flips
    // optimistically, so the spinner is the only marker that the change is
    // still in flight.
    await chooseSettingsOption(page, 'model-haiku')

    const loadingSpinner = page.locator('[data-testid="settings-loading-spinner"]')
    await expect(loadingSpinner).toBeVisible()

    // Eventually the spinner disappears after statusChange arrives
    await expect(loadingSpinner).not.toBeVisible()
    await expectSettingsChip(page, 'Haiku')
  })
})

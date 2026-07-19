---
title: "Settings & Preferences"
description: "Every setting in the Preferences and Profile dialogs: appearance, terminal theme, diff view, turn-end sound, fonts, profile, password, and OAuth links."
type: docs
weight: 10
---

LeapMux keeps your personal settings in two dialogs reached from the user (avatar) menu: **Preferences** (appearance, terminal theme, diff view, turn-end sound, debug logging, and custom fonts) and **Profile** (username, display name, email, password, and linked OAuth accounts). This chapter covers every setting in both dialogs, the additional browser-only toggles that live elsewhere in the app, and how each preference is stored and resolved.

## Opening Preferences and Profile

Both dialogs open from the **user menu** — the avatar dropdown in the app shell. The menu contains:

- **Profile...** — opens the Profile dialog. Shown only when you are **not** in solo mode.
- **About...** (labeled **About LeapMux Desktop...** in the desktop app) — opens the About dialog.
- **Preferences...** — opens the Preferences dialog. Always shown. The menu item displays the keyboard shortcut hint next to its label.

### Keyboard shortcut

Press the Open Preferences shortcut to open the dialog from anywhere:

| Platform | Shortcut |
|---|---|
| macOS | `⌘,` |
| Windows / Linux | `Ctrl+,` |

This is the `app.openPreferences` command (default binding `$mod+Comma`). See [Keyboard Shortcuts](/docs/using/keyboard-shortcuts/) for the full shortcut system and how to customize bindings.

### Desktop native menu

In the Tauri desktop app, the native menu also has a **Preferences...** item that opens the same dialog. On macOS the menu item shows the `⌘,` accelerator; other desktop platforms render their own title-bar controls instead.

> **Note:** Both dialogs are tall modal dialogs with a titled header (**Preferences** or **Profile**) and a **Close** control. Press `Escape` to close, or click outside the dialog body.

## The Preferences dialog

What you see in the Preferences dialog depends on whether your Hub runs in distributed mode or [solo mode](/docs/operating/running-leapmux/).

### Distributed mode: two scopes

In distributed mode the dialog has two tabs:

- **This Browser** — per-browser overrides. Settings here apply only to the browser (or desktop app install) you are currently using. They do not follow you to other devices.
- **Account Defaults** — your account-wide defaults, stored on the Hub. These apply on every device where you sign in, unless a browser override takes precedence on that device.

Every appearance setting on the **This Browser** tab includes an extra **Use account default** option. Selecting it removes the browser override so the setting falls back to your account default. The **Account Defaults** tab has the same options minus **Use account default**, and every change there saves to the Hub immediately.

The resolution order for any setting is:

```
This Browser override (if set)  →  Account default  →  built-in default
```

### Solo mode

In solo mode there are no per-browser overrides and no Profile dialog. The Preferences dialog shows only the account-level **Appearance** settings followed by **Fonts**, with no tabs.

## Appearance settings

Each appearance setting is a labeled group of pill buttons; the active option is highlighted, and selecting another applies it immediately.

### Theme

Controls the overall light/dark palette of the app.

| Option | Effect |
|---|---|
| **Use account default** | (This Browser tab only) Falls back to your account default. |
| **Dark** | Forces the dark palette. |
| **Light** | Forces the light palette. |
| **System** | Follows your OS `prefers-color-scheme` setting and switches live when the OS does. |

The built-in default is **System**.

### Terminal Theme

Controls the color scheme of terminal tabs (see [Terminals](/docs/using/terminals/)).

| Option | Effect |
|---|---|
| **Use account default** | (This Browser tab only) Falls back to your account default. |
| **Match UI** | Follows the resolved app theme (dark/light, including OS when the UI theme is **System**). |
| **Dark** | Always uses the dark terminal palette. |
| **Light** | Always uses the light terminal palette. |

The built-in default is **Match UI**.

### Diff View

Controls how file diffs render in chat tool results and the file viewer (see [File Browser](/docs/using/file-browser/)).

| Option | Effect |
|---|---|
| **Use account default** | (This Browser tab only) Falls back to your account default. |
| **Unified** | Single-column unified diff. |
| **Side-by-Side** | Two-column split diff. |

The built-in default is **Unified**.

> **Tip:** This setting is the *default* for new diffs. Individual diffs have their own per-diff control so you can flip a single diff between unified and split without changing your preference. In chat the control is a columns/rows icon button; in the file viewer it is a pair of text buttons labeled **Unified** and **Split**.

### Turn End Sound

Plays a notification sound when a coding agent finishes a turn.

| Option | Effect |
|---|---|
| **Use account default** | (This Browser tab only) Falls back to your account default. |
| **None** | No sound. |
| **Ding Dong** | Plays a doorbell chime when a turn ends. |

The built-in default is **Ding Dong**.

The sound is intentionally restrained. The chime is active-client gated — only the focused client plays it, so it does not double across tabs or devices — and it is also skipped for single-exchange turns and rate-limited to at most one chime per minute. See [Device Sync & Presence](/docs/using/collaboration/) for why and how that gating works.

#### Volume

When the turn-end sound is set to anything other than **None**, a **Volume** control appears.

- On the **Account Defaults** tab: a slider from 0 to 100 with a percentage readout. The new volume saves when you release the slider.
- On the **This Browser** tab: a toggle that reads **Use account default** or **Custom volume**. Click it to switch to a per-browser custom volume; a slider then appears, seeded with your current account volume.

The built-in default volume is **100%**.

### Debug Logging

Enables verbose client-side debug logging in the browser console — useful when reporting an issue.

| Option | Effect |
|---|---|
| **Use account default** | (This Browser tab only) Falls back to your account default. |
| **On** | Verbose logging enabled. |
| **Off** | Verbose logging disabled. |

The built-in default is **Off**.

## Font settings

Fonts are **account-level only** (stored on the Hub) and appear under the **Account Defaults** tab, or directly in solo mode. The section has two master switches:

- **Custom UI fonts** — when on, a **UI Fonts** list editor appears. These fonts apply to the app interface.
- **Custom monospace fonts** — when on, a **Monospace Fonts** list editor appears. These fonts apply to code, diffs, and terminals.

Both switches are off by default, in which case LeapMux uses its bundled defaults. The default monospace stack is `"Hack NF", Hack, "SF Mono", Consolas, monospace`, and the default UI stack falls back to the system sans-serif font. LeapMux bundles **Hack NF** (Hack Nerd Font) as a web font, so glyph-rich agent output renders correctly out of the box.

### Editing a font list

Both editors work the same way, but their fallbacks differ. For monospace, your custom fonts are tried first, in order, and the bundled `"Hack NF", Hack, "SF Mono", Consolas, monospace` stack is appended after them as a fallback. For UI fonts, only your custom fonts are used — the system sans-serif fallback applies only when no custom UI font is set, not after a non-empty custom list.

- **Add** a font: type a name in the **Font name** field and press Enter, or click the **+** button.
- **Reorder** fonts: drag a row by its handle (`⠿`). Order is priority — the first installed font wins.
- **Edit** a font: double-click its name. Enter commits, Escape cancels.
- **Remove** a font: click the **×** button on its row.
- When a list is empty it shows **No fonts configured**.

Font names are limited to 128 characters; the characters `"`, `\`, `$`, and `%` and control characters are stripped, and an empty name is rejected. Every add, remove, reorder, edit, or toggle saves to the Hub immediately (**Font preferences saved.** on success).

> **Tip:** Custom fonts only take effect for font families actually installed on the machine running the browser. List several fallbacks so the app degrades gracefully on devices that lack your first choice.

## The Profile dialog

The Profile dialog manages your account identity. It is **not available in solo mode**. For the broader account lifecycle — sign-up, login, OAuth, email verification, sessions — see [Accounts & Authentication](/docs/using/accounts/). Each section below shows inline success and error messages and saves independently.

### Username and Display Name

- **Username** — your login name and personal-organization slug. Changing it shows the warning **"Changing your username will also rename your personal organization."** Usernames are 1–32 characters, lowercase letters, digits, and hyphens only, with no leading, trailing, or consecutive hyphens. `solo` is always reserved; `admin` is reserved on most paths.
- **Display Name** — an optional friendly name. If left empty it falls back to your username. Limited to 128 characters.
- Click **Save Profile** to apply. The button is disabled until something changes and is valid; it reads **Saving...** while in flight. Success shows **Profile updated.**

### Email

The **Email** section shows your **Current Email** (or **Not set**) with a **(verified)** or **(unverified)** badge. If you have requested a change that is awaiting verification, a **"Pending email change to … — check your inbox to verify."** notice appears.

To change it, enter a new address in **New Email** and click **Change Email**:

- If the Hub requires email verification, you will see **"Verification email sent. Check your inbox."** and must confirm the new address (see [Accounts & Authentication](/docs/using/accounts/)).
- Otherwise the change applies immediately (**Email updated.**).

The button is disabled when the field is empty or equals your current email. Addresses must be valid (contain `@` with a dotted domain) and at most 254 characters.

### Password

The **Password** section uses a shared password form with a live strength meter (**Weak / Fair / Good / Strong**). Passwords must be 8–128 characters; there is no complexity requirement, so the meter is advisory.

- If you already have a password, a **Current Password** field appears and is required. The button reads **Change Password**.
- If you signed up via OAuth and have no password yet, no current-password field is shown and the button reads **Set Password**.

A mismatch between the new password and its confirmation shows **"Passwords do not match."** Success shows **Password changed.** (or **Password set.**).

> **Warning:** Changing your password signs out all your *other* sessions and revokes API/delegation tokens. Your current session stays signed in.

### Linked Accounts

If you have linked one or more OAuth/OIDC providers (GitHub, Google, Apple, or a generic OIDC provider configured by your operator — see [Authentication Providers](/docs/operating/authentication-providers/)), each appears in **Linked Accounts** with an **Unlink** button. This section is hidden when you have no linked providers.

> **Note:** You cannot unlink your only sign-in method. If a provider is your only way in and you have no password, set a password first.

## Browser-only preferences set elsewhere

A handful of per-browser preferences are stored alongside the others but are toggled in context rather than in the Preferences dialog:

| Preference | Default | Where to toggle | What it does |
|---|---|---|---|
| **Expand agent thoughts** | On | Tab bar dropdown menu, under **Advanced** | Whether agent thinking/reasoning bubbles start expanded. |
| **Show hidden messages** | Off | Tab bar dropdown menu, under **Advanced** | Developer view that reveals hidden chat messages. |
| **Reveal in file manager after save** | On (desktop only) | Checkbox in the file viewer's save action | Reveals a downloaded file in Finder / Explorer / Files after saving. |
| **Enter-key send mode** | `⌘`/`Ctrl` sends | Composer toolbar toggle (**Enter sends** / **⌘⏎ sends**) | Whether plain Enter sends a chat message or inserts a newline. |
| Terminal renderer | Auto | Not surfaced in any dialog | Renderer backend for terminals (auto / WebGL / canvas). |

The composer toggle reads **Enter sends** when plain Enter sends a message, and otherwise shows the modifier-Enter label for your platform: **⌘⏎ sends** on macOS, **Ctrl+⏎ sends** on Windows/Linux. The Enter-key send mode interacts with the chat editor's send keys; see [Coding Agents](/docs/using/coding-agents/) and [Keyboard Shortcuts](/docs/using/keyboard-shortcuts/). The terminal renderer preference has no UI control — it defaults to automatic selection (and avoids WebGL on Linux desktop).

## Custom keyboard shortcuts

Custom keybindings are stored **account-level** (as JSON on the Hub), not in the Preferences dialog and not per-browser. There is no graphical keybinding editor. See [Keyboard Shortcuts](/docs/using/keyboard-shortcuts/) for the default bindings and how overrides work.

## How preferences persist

### Per-browser (This Browser)

Browser overrides live entirely on the device you are using, in the browser's local storage, under a single consolidated key. The value carries a 1-year expiry that is refreshed every time you open the app, so a browser you use within any rolling one-year window keeps its overrides indefinitely. Setting an option to **Use account default** removes that override so the account default takes over. Theme changes also propagate instantly to your other open tabs in the same browser.

The settings stored per-browser are Theme, Terminal Theme, Diff View, Turn End Sound, Volume, and Debug Logging, plus the browser-only toggles above (Expand agent thoughts, Show hidden messages, Enter-key send mode, Terminal renderer, and Reveal in file manager after save).

> **Note:** Clearing your browser's site data wipes all **This Browser** overrides. Your **Account Defaults** are unaffected — they live on the Hub and reappear after you sign in again.

### Account-wide (Account Defaults)

Account defaults are stored on the Hub and loaded when the app starts. The account-level settings are Theme, Terminal Theme, the custom UI/monospace font toggles and lists, Diff View, Turn End Sound, Volume, Debug Logging, and your custom keybindings. They follow you to every device where you sign in.

### Built-in defaults

When neither a browser override nor an account default is set, LeapMux uses these built-in defaults:

| Setting | Default |
|---|---|
| Theme | System |
| Terminal Theme | Match UI |
| Diff View | Unified |
| Turn End Sound | Ding Dong |
| Turn-end volume | 100% |
| Debug Logging | Off |
| Custom UI / monospace fonts | Off (empty) |

## Related chapters

- [Accounts & Authentication](/docs/using/accounts/) — sign-up, login, OAuth, email verification, sessions.
- [Keyboard Shortcuts](/docs/using/keyboard-shortcuts/) — the shortcut system, default bindings, and customization.
- [Running LeapMux](/docs/operating/running-leapmux/) — solo vs. distributed mode.
- [File Browser](/docs/using/file-browser/) and [Terminals](/docs/using/terminals/) — features the Diff View and Terminal Theme settings affect.

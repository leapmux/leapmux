---
title: "Settings & Preferences"
description: "Every setting in the Preferences dialog: appearance, notifications, chat, terminal, files, keyboard shortcuts, account, fonts, and the hub administration panels."
type: docs
weight: 10
---

LeapMux keeps your settings in one **Preferences** dialog reached from the user (avatar) menu. It is a large, searchable, categorized dialog covering every user, browser, and (for hub administrators) instance setting in one place. This chapter covers every category, the additional in-context toggles, and how each preference is stored and resolved.

## Opening Preferences

The dialog opens from the **user menu** — the avatar dropdown in the app shell — via **Preferences...**, or with its keyboard shortcut:

| Platform | Shortcut |
|---|---|
| macOS | `⌘,` |
| Windows / Linux | `Ctrl+,` |

This is the `app.openPreferences` command (default binding `$mod+Comma`). In the Tauri desktop app the native menu also has a **Preferences...** item that opens the same dialog.

> **Note:** The dialog is a tall modal with a titled header. Press `Escape` to close, or click outside the dialog body.

## The dialog

The dialog's left side is a category navigation; the right side shows the selected category's rows. Press `/` to move focus to the **Search settings** box — the navigation is replaced by flat results across every category while you type, each result labeled with its `Category › Setting` breadcrumb. `Escape` clears the search before it closes the dialog.

Every row shows a label, a one-line description, and a control. Rows that exist at two tiers additionally show a **scope chip** naming the tier that currently wins:

- **Account** — the setting follows you to every device where you sign in.
- **This device** — the setting is overridden in this browser (or desktop install) only.

Opening the chip offers **Use account default** or **Override on this device**. Single-tier rows show the tier as static text instead of a chip.

The categories:

| Category | Covers |
|---|---|
| **Appearance** | Theme, terminal theme, diff view, UI fonts, monospace fonts. |
| **Notifications** | Turn-end sound and volume, terminal OS notifications. |
| **Chat & Composer** | Expand agent thoughts, show hidden messages, Enter key behavior, composer status bar. |
| **Terminal** | Terminal renderer. |
| **Files & Editors** | Preferred editor (desktop), reveal after download (desktop), hidden files in directory picker. |
| **Keyboard Shortcuts** | The keybinding editor (see below). |
| **Advanced** | Debug logging, trusted worker keys, reset all browser overrides. |
| **Account** | Username, display name, email, password, linked OAuth accounts. Hidden in solo mode. |

Administrators additionally see an **ADMINISTRATION** section in the navigation. These rows administer the hub itself, so the hub authenticates and validates every write. Most rows apply without a restart. The hub that serves the write applies the change at once, and another hub on the same database picks it up within ~30 seconds. The two rows in the ADMINISTRATION **Advanced** category are the exception; they apply only after a hub restart. Solo mode omits the categories a single-user hub has no use for:

| Category | Covers | In solo mode |
|---|---|---|
| **General** | Public base URL, session duration, secure cookies. | Public base URL only |
| **Sign-up & Access** | Open sign-up, require verified email. | Hidden |
| **Email (SMTP)** | Relay host and port, credentials, sender address, TLS mode. | Hidden |
| **Bot Protection** | Captcha provider and its parameters. | Hidden |
| **Rate Limits** | Failed-attempt limits, per operation. | Hidden |
| **Limits & Timeouts** | API, agent-startup, and worktree-create timeouts; per-user connection and worker caps. | Shown |
| **Advanced** | Maximum message size, queue budgets. Both apply only after a hub restart, and both rows carry a **Requires Restart** badge. | Shown |

See [Configuration](/docs/operating/configuration/) for what each instance setting does, and [`admin` — hub administration over RPC](/docs/operating/control-cli/#admin--hub-administration-over-rpc) for the CLI surface over the same settings.

## Appearance

### Theme

The overall light/dark palette: **Dark**, **Light**, or **System** (follows your OS `prefers-color-scheme` and switches live). A dual-tier setting; the built-in default is **System**.

### Terminal theme

The color scheme of terminal tabs: **Match UI** (follows the resolved app theme), **Dark**, or **Light**. A dual-tier setting; the built-in default is **Match UI**. See [Terminals](/docs/using/terminals/).

### Diff view

How file diffs render in chat tool results and the file viewer: **Unified** (single column) or **Side by side** (two columns). A dual-tier setting; the built-in default is **Unified**.

> **Tip:** This setting is the *default* for new diffs. Individual diffs keep their own per-diff control so you can flip a single diff without changing your preference.

### Fonts

Fonts are a dual-tier setting like the other appearance rows — the account default stack follows you across devices, and either family can be overridden per device. Each family (**Custom UI fonts**, **Custom monospace fonts**) has a master switch and, when enabled, an ordered list editor:

- **Add** a font: type a name in the add box (**Add UI font** or **Add monospace font**) and press Enter, or click the **+** button.
- **Reorder** fonts: drag a row by its handle (`⠿`). Order is priority — the first installed font wins.
- **Edit** a font: double-click its name. Enter commits, Escape cancels.
- **Remove** a font: click the **×** button on its row.

The override unit is the whole family configuration (switch + list together), so a device override can never end up half-applied.

LeapMux limits a font name to 128 UTF-8 bytes and a family to 32 names, strips the characters `"`, `\`, `$`, and `%` and the control characters, and refuses an empty name. 128 bytes holds 128 plain ASCII characters, but only approximately 42 CJK characters. For monospace, your custom fonts are tried first and the bundled `"Hack NF", Hack, "SF Mono", Consolas, monospace` stack is appended as a fallback; for UI fonts only your custom list applies. LeapMux bundles **Hack NF** (Hack Nerd Font) as a web font, so glyph-rich agent output renders correctly out of the box.

> **Tip:** Custom fonts only take effect for families actually installed on the machine running the browser. List several fallbacks so the app degrades gracefully on devices that lack your first choice.

## Notifications

### Turn-end sound

Plays a notification sound when a coding agent finishes a turn: **None** or **Ding dong** (the built-in default). The sound is intentionally restrained — only the focused client plays it, so it does not double across tabs or devices, and it is skipped for single-exchange turns and rate-limited to at most one chime per minute. See [Device Sync & Presence](/docs/using/collaboration/).

### Turn-end volume

A 0–100% slider (built-in default 100%), shown when the turn-end sound is not **None**. A dual-tier setting.

### Terminal OS notifications

Whether terminal alerts raise OS-level notifications (browser-only; the browser asks for notification permission when you enable it). Off by default.

## Chat & Composer, Terminal, Files & Editors

These categories hold the per-device toggles that used to be scattered across in-app menus. The in-context controls stay exactly where they were — the tab-bar **Advanced** menu, the composer **[+]** menu, the file viewer's save action — and both routes change the same stored value, so a toggle in one place is reflected everywhere.

| Setting | Default | Also toggled from | What it does |
|---|---|---|---|
| **Expand agent thoughts** | On | Tab bar menu | Whether agent thinking/reasoning bubbles start expanded. |
| **Show hidden messages** | Off | Tab bar menu | Developer view that reveals hidden chat messages. |
| **Enter key behavior** | **Cmd/Ctrl+Enter sends** | Composer **[+]** menu (**Send with ⌘⏎**) | Whether plain Enter sends a chat message or inserts a newline. The other choice is **Enter sends**. |
| **Composer status bar** | On | Composer **[+]** menu | Whether the branch/model/effort/mode chips show beneath the editor box. |
| **Terminal renderer** | Auto | — | Renderer backend for terminals (auto / WebGL / canvas). Automatic selection avoids WebGL on Linux desktop. |
| **Preferred editor** | First detected (desktop only) | The editor menu on **Open in Editor** | Which external editor opens files. |
| **Reveal after download** | On (desktop only) | The file viewer's save action | Reveals a downloaded file in Finder / Explorer / Files after saving. |
| **Hidden files in directory picker** | On | The directory picker itself | Whether the directory picker lists dotfiles. |

## Keyboard shortcuts

The **Keyboard Shortcuts** category is a table of every command with its default binding and source (**Default** or **Custom**). Click a binding to capture a new chord; a chord already bound in the same context is refused with the name of the conflicting command; **Reset** on a customized row returns it to its default. Overrides are stored account-level (up to 200 of them) and follow you to every device. See [Keyboard Shortcuts](/docs/using/keyboard-shortcuts/) for the command catalogue.

## Advanced

- **Debug logging** — verbose client-side logging in the browser console; a dual-tier setting, off by default.
- **Trusted worker keys** — the list of worker keys your browser has trusted (TOFU). Remove individual pins or clear them all; the next connect re-prompts.
- **Reset all browser overrides** — the **Reset overrides** button removes every **This device** override at once, returning every dual-tier setting to its account default.

## Account

The Account category carries what the old Profile dialog managed. It is **not available in solo mode** (a solo deployment has one local identity). For the broader account lifecycle — sign-up, login, OAuth, email verification, sessions — see [Accounts & Authentication](/docs/using/accounts/).

- **Username and display name** — usernames are 1–32 characters, lowercase letters, digits, and hyphens only; `solo` is always reserved. Display names are up to 128 UTF-8 bytes and fall back to the username when empty.
- **Email** — changing it may require verification (an operator-configured policy); a pending change shows a notice until confirmed.
- **Password** — 8–128 printable ASCII characters, spaces included (see [Password requirements](/docs/using/accounts/#password-requirements)) with a live strength meter. Changing it signs out all your *other* sessions and revokes API/delegation tokens; your current session stays signed in. OAuth-only accounts can set a first password here.
- **Linked accounts** — your linked OAuth/OIDC providers with **Unlink** buttons. You cannot unlink your only sign-in method without a password set.

## How preferences persist

### Per-key account storage

Account settings are stored on the Hub **one key at a time**. Each change sends only the changed key, and the Hub merges it under a row lock — so **two devices (or two tabs) editing different settings no longer overwrite each other**, and a rejected change never partially applies. A value the Hub considers invalid is refused with the row's error; your other settings are untouched.

### Device overrides

Device (**This device**) overrides live entirely in the browser's local storage under one consolidated key with a 1-year rolling expiry — a browser you use within any rolling one-year window keeps its overrides indefinitely. Choosing **Use account default** (or **Reset all browser overrides**) removes the override so the account default takes over. Clearing your browser's site data wipes all device overrides; your account settings are unaffected and reappear after you sign in again.

### Resolution order

For any dual-tier setting:

```
Device override (if set)  →  Account default  →  built-in default
```

### Built-in defaults

| Setting | Default |
|---|---|
| Theme | System |
| Terminal theme | Match UI |
| Diff view | Unified |
| Turn-end sound | Ding dong |
| Turn-end volume | 100% |
| Debug logging | Off |
| Custom UI / monospace fonts | Off (empty) |
| Keybinding overrides | None |

## Related chapters

- [Accounts & Authentication](/docs/using/accounts/) — sign-up, login, OAuth, email verification, sessions.
- [Keyboard Shortcuts](/docs/using/keyboard-shortcuts/) — the shortcut system, default bindings, and customization.
- [Configuration](/docs/operating/configuration/) — the hub instance settings the administration panels manage.
- [`leapmux control admin`](/docs/operating/control-cli/) — the CLI surface over the same hub settings.
- [Running LeapMux](/docs/operating/running-leapmux/) — solo vs. distributed mode.

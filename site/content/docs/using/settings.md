---
title: "Settings & Preferences"
description: "Every Preferences dialog setting: appearance, notifications, chat, terminal, files, fonts, keyboard shortcuts, account, apps, and hub administration panels."
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

Every row shows a label, a one-line description, and a control. Rows that exist at two tiers additionally show a **scope chip** that identifies the tier which currently wins:

- **Account** — the setting follows you to every device where you sign in.
- **This device** — the setting is overridden in this browser (or desktop install) only.

Opening the chip offers **Use account default** or **Override on this device**. Single-tier rows show the tier as static text instead of a chip.

The categories:

| Category | Covers |
|---|---|
| **Account** | Profile name, email, password, passkeys, linked accounts, connected apps. The section the dialog opens on. Solo mode hides every row but **Connected apps**, because a solo Hub authorizes apps like any other and its owner must be able to disconnect one. |
| **Apps** | The app registrations you own. An ordinary account may register an app for itself; an administrator's registrations are visible to everybody. See [App Authorization](/docs/operating/app-authorization/). |
| **Appearance** | Theme (palette + light/dark), terminal theme, syntax theme, diff view, UI fonts, monospace fonts. |
| **Notifications** | Turn-end sound and volume, terminal OS notifications. |
| **Chat & Composer** | Expand agent thoughts, show hidden messages, Enter key behavior, composer status bar. |
| **Terminal** | Terminal renderer. |
| **Files & Editors** | Preferred editor (desktop), reveal after download (desktop), hidden files in directory picker. |
| **Keyboard Shortcuts** | The keybinding editor (see below). |
| **Advanced** | Debug logging, trusted worker keys, reset all browser overrides. |

Administrators additionally see an **ADMINISTRATION** section in the navigation. These rows administer the hub itself, so the hub authenticates and validates every write — and every write needs a **verified session**, because several of these keys are the hub's own security controls. The first change in a sitting opens a **Verify your identity** dialog and then lands on its own; see [Session elevation](/docs/operating/security/#session-elevation). Most rows apply without a restart. The hub that serves the write applies the change at once, and another hub on the same database picks it up within ~30 seconds. The dialog re-reads the hub's own state after every accepted write, so a key that decides what the rest of the app offers converges immediately: publish the hub's URL and **Add passkey** in the **Account** section stops being disabled, with no page reload. The two rows in the ADMINISTRATION **Advanced** category are the exception; they apply only after a hub restart. Solo mode omits the categories a single-user hub has no use for:

| Category | Covers | In solo mode |
|---|---|---|
| **General** | Public base URL, session duration, secure cookies. | Public base URL only |
| **Sign-up & Access** | Open sign-up, require verified email. | Hidden |
| **Email (SMTP)** | Relay host and port, credentials, sender address, TLS mode. | Hidden |
| **Bot Protection** | Captcha provider and its parameters. | Hidden |
| **Rate Limits** | Failed-attempt limits, per operation. | The anonymous authorization-server limit only |
| **Limits & Timeouts** | API, agent-startup, and worktree-create timeouts; per-user connection and worker caps. | Shown |
| **Apps** | Open app registration (RFC 7591), off by default. | Shown |
| **Advanced** | Maximum message size, queue budgets. Both apply only after a hub restart, and both rows carry a **Requires Restart** badge. | Shown |

See [Configuration](/docs/operating/configuration/) for what each instance setting does, and [`admin` — hub administration over RPC](/docs/operating/admin-cli/) for the CLI surface over the same settings.

## Appearance

### Theme

Two choices on one line: a **color palette**, and whether it follows the system or is pinned.

The palette drop-down lists LeapMux's own palette and ten adapted from permissively licensed community themes, which `NOTICE` credits. Some of them offer more than one look, and a second drop-down appears beside the palette when they do.

The tri-switch picks **System** (follows your OS `prefers-color-scheme` and switches live), **Light**, or **Dark**.

The palette and the mode are one setting, so one scope chip and one reset cover both. A dual-tier setting; the built-in default is **Default** at **System**.

The same control appears once outside the dialog, so a new user can set the look before anything else: on the empty state that offers to create your first workspace. It writes the same preference, so a choice made there is the one this dialog shows afterwards.

The screens shown *before* you sign in carry no theme control: the desktop launcher, the sign-in page, and first-run setup. Your theme is stored per account, so there is nothing to read until LeapMux knows who you are. Those screens paint the **Default** palette and follow your system light/dark setting, and your own theme applies as soon as you are signed in.

### Terminal theme

The same two choices, for terminal tabs, plus one more palette: **Match UI**. Choosing it hands the whole row to the app, so the mode pills grey out and report the mode the app is on. Choosing any other palette detaches the row and hands the pills back, starting from the app's own mode — so the terminal looks the same until you change it.

The palettes are the same ones the app offers. Each supplies its own sixteen ANSI colors, and the terminal's background, foreground, cursor and selection come from that same palette, so a terminal on a theme other than the app's is still coherent in itself. Where a palette's ANSI set belongs to another project, the entry identifies that project beside the palette, so the scheme that you already see stays findable under its own name.

A dual-tier setting; the built-in default is **Match UI**. That default is why the theme picker on the empty state moves the terminal too — there is only one choice to make until you come here and detach it. See [Terminals](/docs/using/terminals/).

### Syntax theme

Colors for highlighted code, in chat, the editor, diffs and file views. The same control and the same **Match UI** default as the terminal theme, and independent of both other settings.

Each palette highlights with its own project's editor theme, credited in `NOTICE`. Where a palette has no editor theme of its own, its entry identifies the one it borrows, the same way the terminal list does.

Unlike the palette and the mode, this one is not free to change: highlighting bakes each color into the code as it is tokenized, so switching re-highlights and code repaints as you scroll back through it.

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

A family holds up to 32 names, and the panel reports a name it cannot use. For monospace, LeapMux tries your custom fonts first and appends the bundled `"Hack NF", Hack, "SF Mono", Consolas, monospace` stack as a fallback; for UI fonts only your custom list applies. LeapMux bundles **Hack NF** (Hack Nerd Font) as a web font, so glyph-rich agent output renders correctly with no configuration.

> **Tip:** Custom fonts only take effect for families actually installed on the machine running the browser. List several fallbacks so the app degrades gracefully on devices that lack your first choice.

## Notifications

### Turn-end sound

Plays a notification sound when a coding agent finishes a turn: **None** or **Ding dong** (the built-in default). The sound is intentionally restrained — only the focused client plays it, so it does not double across tabs or devices, and it is skipped for single-exchange turns and rate-limited to at most one chime per minute. See [Device Sync](/docs/using/device-sync/).

### Turn-end volume

A 0–100% slider (built-in default 100%), shown when the turn-end sound is not **None**. A dual-tier setting.

### Terminal OS notifications

Whether terminal alerts raise OS-level notifications (browser-only; the browser asks for notification permission when you enable it). Off by default.

## Chat & Composer, Terminal, Files & Editors

These categories hold the per-device toggles. The in-context controls — the tab-bar **Advanced** menu, the composer **[+]** menu, the file viewer's save action — change the same stored value, so a toggle in one place is reflected everywhere.

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

The **Keyboard Shortcuts** category is a table of every command with its default binding and source (**Default** or **Custom**). Click a binding to capture a new chord; the panel refuses a chord already bound in the same context and gives the name of the conflicting command; **Reset** on a customized row returns it to its default. Overrides are stored account-level (up to 200 of them) and follow you to every device. See [Keyboard Shortcuts](/docs/using/keyboard-shortcuts/) for the command catalogue.

## Advanced

- **Debug logging** — verbose client-side logging in the browser console; a dual-tier setting, off by default.
- **Trusted worker keys** — the list of worker keys that your browser trusts (TOFU). Remove individual pins or clear them all; the next connect re-prompts.
- **Reset all browser overrides** — the **Reset overrides** button removes every **This device** override at once, returning every dual-tier setting to its account default.

## Account

The Account category carries your account settings, as one row per concern. It leads the navigation. Solo mode hides every row here but **Connected apps** — a solo deployment has one local identity, so there is no password, address or provider to manage, but it authorizes apps like any other Hub. For the broader account lifecycle — sign-up, login, OAuth, email verification, sessions — see [Accounts & Authentication](/docs/using/accounts/).

- **Profile** — your username and display name, saved together. A username is a lowercase slug, and `solo` is always reserved. A display name falls back to the username when empty.
- **Email** — changing it may require verification (an operator-configured policy); a pending change shows a notice until confirmed.
- **Password** — 8–128 printable ASCII characters, spaces included (see [Password requirements](/docs/using/accounts/#password-requirements)) with a live strength meter. Changing it signs out all your *other* sessions and disconnects every app; your current session stays signed in. OAuth-only accounts can set a first password here.
- **Passkeys** — the credentials registered to this account, with add, rename and remove. See [Managing passkeys in your profile](/docs/using/accounts/#managing-passkeys-in-your-profile).
- **Linked accounts** — your linked OAuth/OIDC providers, each with an **Unlink** button. You cannot detach your only sign-in method without a password set.
- **Connected apps** — every app holding access to your account, grouped by app, with the permissions each was granted. **Disconnect** on the app's line ends every machine it runs on; **Revoke** on one row ends that machine alone. See [Connected Apps](/docs/using/connected-apps/).

Four of those six rows need a **verified session**: email, password, passkeys, and linked accounts. Your profile name and your connected apps do not. Disconnecting an app only ever *reduces* what it can reach, and demanding a fresh factor from somebody who just realized an app is malicious is the wrong failure mode. While the session is verified, a panel at the top of the section says so and offers **End now**. Every **ADMINISTRATION** section takes the same rule and shows the same panel. See [Session elevation](/docs/operating/security/#session-elevation).

## Apps

**App registrations** lists the apps you registered, with **Register app**, and per row the permission ceiling it may ask for, whether an administrator vouched for it, **Edit** (the name, home page, redirect addresses and that ceiling), **Allow step-up** or **Refuse step-up**, and **Retire**. Narrowing that ceiling takes the permission from every credential the app already holds, so it is the lever to reach for short of retiring the app outright. Yours are visible to you alone; an administrator's are visible to everybody and only an administrator can edit them. Administrators also get **Vouch** and **Withdraw vouch** on every row. See [App Authorization](/docs/operating/app-authorization/) for what each field decides.

This row needs a **verified session**, and **Connected apps** one category up does not. The asymmetry is the point: disconnecting reduces access, while editing a registration rewrites **where a consent redirects** — the single most dangerous write in the feature, because it diverts an authorization code already in flight to an address the editor chose. Registering an app, allowing it the step-up ceremony, and vouching for it take the same rule.

Administrators additionally see **Apps** under **ADMINISTRATION**, which holds the Hub's own app setting: whether [RFC 7591 open registration](/docs/operating/app-authorization/#open-registration) accepts anonymous callers. It is off by default.

## How preferences persist

### Per-key account storage

Account settings are stored on the Hub **one key at a time**. Each change sends only the changed key, and the Hub merges it under a row lock — so **two devices (or two tabs) can edit different settings without overwriting each other**, and a rejected change never partially applies. A value the Hub considers invalid is refused with the row's error; your other settings are untouched.

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
| Theme | Default palette, System mode |
| Terminal theme | Match UI |
| Syntax theme | Match UI |
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

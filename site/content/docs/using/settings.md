---
title: "Settings & Preferences"
description: "Every Preferences dialog category in order: Account, Apps, Appearance, Notifications, Chat & Composer, Terminal, Desktop, Files & Editors, Keyboard Shortcuts, Advanced — and the Administration group hub administrators see."
type: docs
weight: 10
---

LeapMux keeps your settings in one **Preferences** dialog reached from the user (avatar) menu. It is a large, searchable, categorized dialog covering every user, browser, and (for hub administrators) instance setting in one place. This chapter covers every category in the dialog's own order, the additional in-context toggles, and how each preference is stored and resolved.

## Opening Preferences

The dialog opens from the **user menu** — the avatar dropdown in the app shell — via **Preferences...**, or with its keyboard shortcut:

| Platform | Shortcut |
|---|---|
| macOS | `⌘,` |
| Windows / Linux | `Ctrl+,` |

This is the `app.openPreferences` command (default binding `$mod+Comma`). On macOS, the desktop app's native menu also has a **Preferences...** item that opens the same dialog.

{{< callout type="info" >}}
The dialog is a tall modal with a titled header. Press `Escape` to close, or click outside the dialog body.
{{< /callout >}}

## The dialog

The dialog's left side is a category navigation; the right side shows the selected category's rows. Press `/` to move focus to the **Search settings** box — the navigation is replaced by flat results across every category while you type, each result labeled with its `Category › Setting` breadcrumb. `Escape` clears the search before it closes the dialog.

Every row shows a label, a one-line description, and a control. Rows that exist at two tiers additionally show a **scope chip** that identifies the tier which currently wins:

- **Account** — the setting follows you to every device where you sign in.
- **This device** — the setting is overridden in this browser (or desktop install) only.

Opening the chip offers **Use account default** or **Override on this device**. Single-tier rows show the tier as static text instead of a chip.

The user categories, in navigation order:

| Category | Covers |
|---|---|
| **Account** | Profile name, email, password, passkeys, linked accounts. The section the dialog opens on. Solo mode hides every row, and the category disappears from the dialog. |
| **Apps** | The apps feature in two rows: **Connected apps** (what your account authorized) and the app registrations you own. An ordinary account may register an app for itself; an administrator's registrations are visible to everybody. Both rows stay in solo mode, because a solo Hub authorizes apps like any other. See [App Authorization](/docs/admin/app-authorization/). |
| **Appearance** | Theme (palette + light/dark), terminal theme, syntax theme, diff view, UI fonts, monospace fonts. |
| **Notifications** | Turn-end sound and volume, terminal OS notifications. |
| **Chat & Composer** | Expand agent thoughts, show hidden messages, Enter key behavior, composer status bar. |
| **Terminal** | Terminal renderer. |
| **Desktop** | The tray (menu bar) icon, what closing and minimizing a window do, and the login launch. The desktop app only; in a browser the category disappears from the dialog. |
| **Files & Editors** | Preferred editor (desktop), reveal after download (desktop), hidden files in directory picker. |
| **Keyboard Shortcuts** | The keybinding editor (see below). |
| **Advanced** | Debug logging, trusted worker keys, reset all browser overrides. |

Administrators additionally see an **ADMINISTRATION** group in the navigation, below these categories — see [Administration](#administration) below.

## Account

The Account category carries your account settings, as one row per concern. It leads the navigation. Solo mode hides every row here — a solo deployment has one local identity, so there is no password, address or provider to manage — and the category disappears from the dialog. For the broader account lifecycle — sign-up, login, OAuth, email verification, sessions — see [Accounts & Authentication](/docs/using/accounts/).

- **Profile** — your username and display name, saved together. A username is a lowercase slug, and `solo` is always reserved. A display name falls back to the username when empty.
- **Email** — changing it requires verification when the Hub has a mail relay configured (see [Email (SMTP)](#email-smtp) below); a pending change shows a notice until confirmed.
- **Password** — 8–128 printable ASCII characters, spaces included (see [Password requirements](/docs/using/accounts/#password-requirements)) with a live strength meter. Changing it signs out all your *other* sessions and disconnects every app; your current session stays signed in. OAuth-only accounts can set a first password here.
- **Passkeys** — the credentials registered to this account, with add, rename and remove. See [Managing passkeys in your profile](/docs/using/accounts/#managing-passkeys-in-your-profile).
- **Linked accounts** — your linked OAuth/OIDC providers, each with an **Unlink** button. You cannot detach your only sign-in method without a password set.

Four of those five rows need a **verified session**: email, password, passkeys, and linked accounts. Your profile name does not. While the session is verified, a panel at the top of the section says so and offers **End now**. Every **ADMINISTRATION** section takes the same rule and shows the same panel. See [Session elevation](/docs/admin/security/#session-elevation).

## Apps

The category holds the whole apps feature in two rows, authorization first.

**Connected apps** lists every app holding access to your account, grouped by app, with the permissions each was granted. **Disconnect** on the app's line ends every machine it runs on; **Revoke** on one row ends that machine alone. See [Connected Apps](/docs/using/connected-apps/).

**App registrations** lists the apps you registered, with **Register an app**, and per row the permission ceiling it may ask for, whether an administrator vouched for it, **Edit** (the name, home page, redirect addresses and that ceiling), **Allow step-up** or **Refuse step-up**, and **Retire**.

Narrowing that ceiling also removes that permission from every credential the app already holds; retiring the app is the only stronger action. Yours are visible to you alone; an administrator's are visible to everybody and only an administrator can edit them. Administrators also get **Vouch** and **Withdraw vouch** on every row. See [App Authorization](/docs/admin/app-authorization/) for what each field decides.

**App registrations** needs a **verified session**; **Connected apps**, the row above it, does not. Disconnecting reduces access. Editing a registration rewrites **where a consent redirects**, so it can divert an authorization code already in flight to an address the editor chose — which is why that row needs the proof. Registering an app, allowing it the step-up ceremony, and vouching for it take the same rule.

Administrators additionally see the **Hub-wide Apps** category under **ADMINISTRATION** — see [Hub-wide Apps](#hub-wide-apps) below.

## Appearance

### Theme

Two choices on one line: a **color palette**, and whether it follows the system or is pinned.

The palette drop-down lists LeapMux's own palette and ten adapted from permissively licensed community themes, which `NOTICE` credits. Some of them offer more than one look, and a second drop-down appears beside the palette when they do.

The tri-switch picks **System** (follows your OS `prefers-color-scheme` and switches live), **Light**, or **Dark**.

The palette and the mode are one setting, so one scope chip and one reset cover both. A dual-tier setting; the built-in default is **Default** at **System**.

The same control appears once outside the dialog, so a new user can set the look before anything else: on the empty state that offers to create your first workspace. It writes the same preference, so a choice made there is the one this dialog shows afterwards.

The screens shown *before* you sign in carry no theme control: the desktop launcher, the sign-in page, and first-run setup. Your theme is stored per account, so there is nothing to read until LeapMux knows who you are. Those screens paint the **Default** palette and follow your system light/dark setting, and your own theme applies as soon as you are signed in.

### Terminal theme

The same two choices, for terminal tabs, plus one more palette: **Match UI**. Choosing **Match UI** links the row to the app theme: the mode pills are disabled and show the app's mode. Choosing any other palette unlinks the row. The pills then start from the app's mode, so the terminal keeps its current look.

The palettes are the same ones the app offers. Each supplies its own sixteen ANSI colors, and the terminal's background, foreground, cursor and selection come from that same palette, so a terminal on a theme other than the app's stays consistent. Where a palette's ANSI set belongs to another project, the entry names that project beside the palette.

A dual-tier setting; the built-in default is **Match UI**. That default is why the theme picker on the empty state moves the terminal too — there is only one choice to make until you come here and detach it. See [Terminals](/docs/using/terminals/).

### Syntax theme

Colors for highlighted code, in chat, the editor, diffs and file views. The same control and the same **Match UI** default as the terminal theme, and independent of both other settings.

Each palette highlights with its own project's editor theme, credited in `NOTICE`. Where a palette has no editor theme of its own, its entry identifies the one it borrows, the same way the terminal list does.

Unlike the palette and the mode, this one costs more to change: highlighting stores each token's color, so a switch re-highlights the code and it repaints as you scroll back through it.

### Diff view

How file diffs render in chat tool results and the file viewer: **Unified** (single column) or **Side by side** (two columns). A dual-tier setting; the built-in default is **Unified**.

{{< callout >}}
This setting is the *default* for new diffs. Individual diffs keep their own per-diff control so you can flip a single diff without changing your preference.
{{< /callout >}}

### Fonts

Fonts are a dual-tier setting like the other appearance rows — the account default stack follows you across devices, and either family can be overridden per device. Each family (**Custom UI fonts**, **Custom monospace fonts**) has a master switch and, when enabled, an ordered list editor:

- **Add** a font: type a name in the add box (**Add UI font** or **Add monospace font**) and press Enter, or click the **+** button.
- **Reorder** fonts: drag a row by its handle (`⠿`). Order is priority — the first installed font wins.
- **Edit** a font: double-click its name. Enter commits, Escape cancels.
- **Remove** a font: click the **×** button on its row.

The override unit is the whole family configuration (switch + list together), so a device override can never end up half-applied.

A family holds up to 32 names, and the panel reports a name it cannot use. For monospace, LeapMux tries your custom fonts first and appends the bundled `"Hack NF", Hack, "SF Mono", Consolas, monospace` stack as a fallback; for UI fonts only your custom list applies. LeapMux bundles **Hack NF** (Hack Nerd Font) as a web font, so glyph-rich agent output renders correctly with no configuration.

{{< callout >}}
Custom fonts only take effect for families actually installed on the machine running the browser. List several fallbacks so the app degrades gracefully on devices that lack your first choice.
{{< /callout >}}

## Notifications

### Turn-end sound

Plays a notification sound when a coding agent finishes a turn: **None** or **Ding dong** (the built-in default). The sound is intentionally restrained. Only the focused client plays it, so it does not double across tabs or devices. Single-exchange turns are skipped, and it plays at most once per minute. See [Device Sync](/docs/using/device-sync/).

### Turn-end volume

A 0–100% slider (built-in default 100%), shown unless the sound is **None** on both this device and the account. A dual-tier setting.

### Terminal OS notifications

Whether terminal alerts raise OS-level notifications (browser-only; the browser asks for notification permission when you enable it). Off by default.

## Chat & Composer

Per-device toggles for the chat surface. The in-context controls — the tab-bar **Advanced** menu and the composer **[+]** menu — change the same stored value, so a toggle in one place is reflected everywhere.

| Setting | Default | Also toggled from | What it does |
|---|---|---|---|
| **Expand agent thoughts** | On | Tab bar menu | Whether agent thinking/reasoning bubbles start expanded. |
| **Show hidden messages** | Off | Tab bar menu | Developer view that reveals hidden chat messages. |
| **Enter key behavior** | **Cmd/Ctrl+Enter sends** | Composer **[+]** menu (**Send with ⌘/Ctrl+⏎**) | Whether plain Enter sends a chat message or inserts a newline. The other choice is **Enter sends**. |
| **Composer status bar** | On | Composer **[+]** menu | Whether the branch/model/effort/mode chips show beneath the editor box. |

## Terminal

| Setting | Default | What it does |
|---|---|---|
| **Terminal renderer** | Auto | Renderer backend for terminals (auto / WebGL / canvas). Automatic selection avoids WebGL on Linux desktop. |

## Desktop

The **Desktop** category appears in the desktop app only. A browser has no tray, no menu bar item and no login items, so the whole category disappears from the dialog there.

Every row is a dual-tier setting: the account default follows you to each machine you sign in on, and any machine can override it.

### Tray icon

On Linux and Windows this row is **Tray icon** and the icon sits in the system tray. On macOS it is **Menu bar icon** and the icon sits in the menu bar. Turn it on to keep LeapMux available when no window is open. The built-in default is **off**.

The next two rows apply only while this one is on, so the dialog hides them while the tray is off on both tiers.

{{< callout type="info" >}}
On Linux the tray needs a status-icon library (`libayatana-appindicator3`). The `.deb` package recommends it rather than requiring it, because many desktop environments have no tray at all. Where the library is absent, the **Tray icon** row reports that LeapMux could not create the icon. LeapMux then never hides a window: closing and minimizing keep their ordinary behaviour.
{{< /callout >}}

### When you close the window

What LeapMux does when you close the last window: **Hide to the tray** (**Hide to the menu bar** on macOS), or **Quit LeapMux**. The built-in default is to hide to the tray.

### When you minimize the window

What LeapMux does when you minimize a window: **Hide to the tray**, or **Keep in the taskbar**. On macOS the two options read **Hide to the menu bar** and **Keep in the Dock**. The built-in default keeps it in the taskbar.

On macOS the window plays its minimize animation into the Dock before the tile disappears. macOS gives an application no way to intercept a minimize before it happens. Every Mac app with this feature shows the same behaviour.

### Start at login

Registers LeapMux with your operating system's login items, so it starts when you sign in to the computer. Off by default. Some systems ask you to approve the login item the first time. A system that declines it reports so on this row.

### Window at login

Applies to the **login launch only** — starting LeapMux yourself always shows a window. **Show the window** is the built-in default. **Hide the window** starts LeapMux in the tray (menu bar) when the tray icon is on. It starts LeapMux minimized when the tray icon is off. The dialog hides this row while **Start at login** is off on both tiers.

{{< callout type="info" >}}
The desktop app must decide the window state before it can read your preferences. It therefore keeps a copy of the tray and login-launch settings on the machine. LeapMux rewrites the copy whenever a setting changes, and your account stays the source of truth. The copy belongs to the operating-system user, not to a LeapMux account. On a machine that two LeapMux accounts sign in on, LeapMux starts with the settings of the account that signed in last. A change made on another device reaches this machine when you next sign in there, so one launch can still follow the previous choice.
{{< /callout >}}

## Files & Editors

Per-device toggles for files and external editors. As with the chat toggles, the in-context controls — the editor menu on **Open in …**, the file viewer's save action, the directory picker — change the same stored value as these rows.

| Setting | Default | Also toggled from | What it does |
|---|---|---|---|
| **Preferred editor** | First detected (desktop only) | The editor menu on **Open in …** | Which external editor opens files. |
| **Reveal after download** | On (desktop only) | The file viewer's save action | Reveals a downloaded file in Finder / Explorer / Files after saving. |
| **Hidden files in directory picker** | On | The directory picker itself | Whether the directory picker lists dotfiles. |

## Keyboard Shortcuts

The **Keyboard Shortcuts** category is a table of every command with its default binding and source (**Default** or **Custom**). Click a binding to capture a new chord; the panel refuses a chord already bound in the same context and gives the name of the conflicting command; **Reset** on a customized row returns it to its default. Overrides are stored account-level (up to 200 of them) and follow you to every device. See [Keyboard Shortcuts](/docs/using/keyboard-shortcuts/) for the command catalogue.

## Advanced

- **Debug logging** — verbose client-side logging in the browser console; a dual-tier setting, off by default.
- **Trusted worker keys** — the list of worker keys that your browser trusts (TOFU). Remove individual pins or clear them all; the next connect re-prompts.
- **Reset all browser overrides** — the **Reset overrides** button removes every **This device** override at once. Dual-tier settings return to their account default, and browser-only settings return to their built-in default. Trusted worker keys are kept.

## Administration

Administrators see a second group in the navigation, **ADMINISTRATION**, below the user categories. Its rows are the Hub's own instance settings: the Hub authenticates and validates every write, and every write needs a **verified session** — several of these keys are the Hub's own security controls. The first change in a sitting opens a **Verify your identity** dialog and then lands on its own; see [Session elevation](/docs/admin/security/#session-elevation).

Most rows apply without a restart. The Hub that serves the write applies the change at once, and another Hub on the same database picks it up within ~30 seconds. The dialog re-reads the Hub's state after every accepted write. For example, publish the Hub's URL and **Add passkey** in the **Account** category stops being disabled, with no page reload. The two rows that apply only after a Hub restart carry a **Requires Restart** badge. Solo mode omits the categories a single-user Hub has no use for; each category below states what stays.

### General

- **Public base URL** — the browser-facing URL when the Hub runs behind a TLS-terminating reverse proxy (scheme and host only). Mail links, the CLI's login endpoints, and passkey ceremonies all derive from it. See [Passkeys](/docs/admin/configuration/#passkeys).
- **Session duration** — how long a session lives after your last activity. See [Sessions and signing out](/docs/using/accounts/#sessions-and-signing-out).
- **Secure cookies** — serve the session cookie with the `__Host-` prefix behind TLS; changing it signs everybody out.

In solo mode only **Public base URL** stays.

### Sign-up & Access

One row: **Open sign-up**, off by default. With it off, new accounts come only from an administrator or from a linked OAuth identity; with it on, the `/signup` page works. Email verification has no row of its own — it follows the mail relay: configuring **Email (SMTP)** below is what turns verification on. Hidden entirely in solo mode.

### Email (SMTP)

One row, **SMTP relay**: host, port, username, from address, TLS mode, and a password kept in the row's secret half. Configuring it enables verification emails and account recovery; with no relay, sign-ups skip verification entirely. Hidden entirely in solo mode.

### Hub-wide Apps

- **Open app registration** — whether anonymous callers may register an app through [RFC 7591 dynamic registration](/docs/admin/app-authorization/#open-registration). Off by default.
- **Hub-wide app registrations** — the registrations an administrator creates for every account on the Hub to authorize. Registering one is an administrator's act, so it asks for a fresh proof first.

Shown in solo mode: a solo Hub authorizes apps like any other.

### Bot Protection

- **Bot protection enabled** — whether captcha verification runs on the protected forms: sign-in, sign-up, account recovery, and email verification. The honeypot check stays active either way.
- **Provider** — the active provider: the built-in **ALTCHA** (the default), **Google reCAPTCHA v3**, or **Cloudflare Turnstile**.
- **ALTCHA parameters**, **Google reCAPTCHA v3**, **Cloudflare Turnstile** — one row per provider's key fields. Every provider's fields are visible at all times, so an administrator fills one in and then switches **Provider** to it.

See [Bot protection](/docs/admin/configuration/#bot-protection-captcha--rate-limits) for where the built-in ALTCHA runs and what the parameters cost. Hidden entirely in solo mode.

### Rate Limits

One row per counted operation:

- **Rate limit - elevation** — failed attempts to verify your identity for a sensitive change: 5 per 15 minutes, per user.
- **Rate limit - oauth_anonymous** — the authorization server's anonymous endpoints: 600 per 10 minutes, per client address.

In solo mode only the anonymous limit stays.

### Limits & Timeouts

- **Timeouts** — the API timeout, the agent-startup timeout, and the worktree-create timeout, each in seconds.
- **Per-user caps** — a user's maximum simultaneous connections (default 32) and Workers (default 64); `0` means unlimited.

Shown in solo mode.

### Advanced

Two rows, both carrying a **Requires Restart** badge:

- **Maximum message size** — the application payload ceiling.
- **Queue budgets** — memory budgets for the Hub's outbound queue pools; `0` auto-sizes them.

Shown in solo mode.

See [Configuration](/docs/admin/configuration/) for what each instance setting does, and [Admin CLI](/docs/admin/admin-cli/) for the CLI surface over the same settings.

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
| Tray / menu bar icon | Off |
| When you close the window | Hide to the tray |
| When you minimize the window | Keep in the taskbar |
| Start at login | Off |
| Window at login | Show the window |
| Custom UI / monospace fonts | Off (empty) |
| Keybinding overrides | None |

## Related chapters

- [Accounts & Authentication](/docs/using/accounts/) — sign-up, login, OAuth, email verification, sessions.
- [Keyboard Shortcuts](/docs/using/keyboard-shortcuts/) — the shortcut system, default bindings, and customization.
- [Configuration](/docs/admin/configuration/) — the hub instance settings the administration panels manage.
- [`leapmux control admin`](/docs/using/control-cli/) — the CLI surface over the same hub settings.
- [Running LeapMux](/docs/admin/running-leapmux/) — solo vs. distributed mode.

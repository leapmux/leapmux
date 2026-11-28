---
title: "Settings & Preferences"
description: "Configure LeapMux in the Preferences dialog: account, apps, themes, fonts, notifications, chat, terminal, desktop app behavior, files, shortcuts, and device overrides."
type: docs
weight: 10
---

All personal and device preferences live in the searchable **Preferences** dialog. Open it from the avatar menu (**Preferences...**), the macOS desktop menu bar, or by pressing `⌘,` / `Ctrl+,`. Press `Esc` or click outside the modal to close it.

## Navigating and scope tiers

{{< screenshot src="preferences-dialog" alt="LeapMux Preferences dialog showing category navigation, search box, Appearance theme and diff controls, and Account default scope chips" caption="The Preferences dialog organizes every user and device setting with live search and Account vs. This device scope chips." width="1280" height="856" >}}

Press `/` anywhere in the dialog to focus **Search settings** and filter every category at once (`Esc` clears the search before closing the dialog).

Settings resolve in two tiers:

```text
This device override (if set)  →  Account default  →  Built-in default
```

- **Account** — stored on the Hub and synced to every browser and desktop app where you sign in. Different settings edited on two devices merge independently without overwriting each other.
- **This device** — overrides a dual-tier setting in the current browser or desktop install only. Click the row's scope chip to switch between **Use account default** and **Override on this device**. Clearing browser site data removes local overrides while keeping your account defaults intact.

{{< callout type="info" >}}
**Hub administrators** also see an **ADMINISTRATION** section below the user categories (**General**, **Sign-up & Access**, **Network access**, **Email (SMTP)**, **Hub-wide Apps**, **Bot Protection**, **Rate Limits**, **Limits & Timeouts**, and **Advanced**). For all instance-level Hub settings, see [Configuration Guide](/docs/admin/configuration/) and [Configuration Reference](/docs/admin/configuration-reference/).
{{< /callout >}}

## Account and Apps

- **Account** — manages your username and display name (**Profile**), **Email**, **Password**, **Passkeys**, and **Linked accounts** (OAuth/OIDC). Solo mode hides every row except **Password**. Email, password, passkeys, and unlinking providers require a verified session.
- **Apps** — displays **Connected apps** (authorized CLIs and integrations, with one-click **Revoke** or **Disconnect**) and **App registrations** (private OAuth apps you register for your own scripts and tools).

{{< screenshot src="preferences-apps" alt="LeapMux Preferences Apps panel showing Connected apps and App registrations with verified badges, permission scope chips, and management actions" caption="Manage Connected apps and private App registrations under Preferences → Apps." width="1280" height="856" >}}

See [Accounts & Connected Apps](/docs/using/accounts-and-apps/) for the full walkthrough of both categories.

## Appearance

Every row in **Appearance** is dual-tier (account default with optional per-device override):

- **Theme** — picks a color palette (LeapMux's default or ten adapted community palettes, some with multiple variants) and light/dark mode (**System**, **Light**, or **Dark**). Built-in default: **Default** at **System**. Pre-login screens always use the default palette following your OS mode until you sign in.
- **Terminal theme** — defaults to **Match UI**, following your app theme automatically. Pick any other palette to detach terminal colors (all sixteen ANSI colors, background, foreground, cursor, and selection stay coordinated).
- **Syntax theme** — controls code highlighting in chat, diffs, and the file viewer. Defaults to **Match UI** and can be detached independently.
- **Diff view** — sets the default layout for new diffs (**Unified** or **Side by side**; default **Unified**). Individual diffs still keep their own toggle.
- **Custom UI fonts / Custom monospace fonts** — enable an ordered priority list (up to 32 fonts per family; drag handles reorder, double-click edits). Monospace appends LeapMux's bundled **Hack NF** stack (`"Hack NF", Hack, "SF Mono", Consolas, monospace`) as a fallback so Nerd Font icons render by default.

## Notifications

- **Turn-end sound** (dual-tier) — plays **Ding dong** (default) or **None** when an agent finishes a non-trivial turn (at most once per minute, on your active client only; see [Workspaces](/docs/using/workspaces/#live-sync-across-devices)).
- **Turn-end volume** (dual-tier) — 0–100% slider (default 100%), hidden when the sound is **None** on both tiers.
- **Desktop notifications** (device-only, default off) — requests browser notification permission to surface terminal alerts (OSC 9 / 777 / 99) and `leapmux notify` messages while the window is hidden.

## Chat & Composer

These settings control chat display, composer behavior, and Worker-installed Agent Skills:

| Setting | Scope | Default | Also controlled from | What it does |
| --- | --- | --- | --- | --- |
| **Expand agent thoughts** | This device | On | Tab bar **+** → **Advanced** | Starts agent thinking blocks expanded. |
| **Show hidden messages** | This device | Off | Tab bar **+** → **Advanced** | Reveals hidden developer/bookkeeping chat rows. |
| **Enter key behavior** | This device | `⌘Enter` / `Ctrl+Enter` sends | Composer **[+]** menu | Switches between `⌘Enter` / `Ctrl+Enter` sends and plain `Enter` sends. |
| **Composer status bar** | This device | On | Composer **[+]** menu | Shows or hides the branch, model, effort, and mode chips below the composer. |
| **Agent skills** | Account (per Worker) | None enabled | `leapmux skill` CLI | Installs and optionally auto-syncs curated LeapMux skills on each connected Worker across `~/.agents/skills`, `~/.claude/skills`, `~/.codex/skills`, `~/.cursor/skills`, and `~/.gemini/skills`. See [Agent Skills](/docs/using/agent-skills/). |

## Terminal

| Setting | Scope | Default | What it does |
| --- | --- | --- | --- |
| **Terminal renderer** | This device | Auto | Picks Auto, WebGL, or Canvas rendering (Auto avoids WebGL on Linux desktop). |
| **Quake terminal position** | Dual-tier | Top | Edge the Quake panel slides from: **Top**, **Bottom**, **Left**, or **Right**. |
| **Quake terminal size** | Dual-tier | 65% | Share of the center canvas covered by the Quake panel. |
| **Quake terminal animation** | Dual-tier | 200 ms | Slide duration (disabled automatically when OS reduced motion is active). |
| **Quake terminal background opacity** | Dual-tier | 0.9 | Panel background opacity (terminal text remains fully opaque). |

## Desktop

Shown in **LeapMux Desktop** only (hidden in web browsers). All five rows are dual-tier:

- **Tray icon** (Linux/Windows) / **Menu bar icon** (macOS) — default **Off**. Keeps LeapMux accessible when no window is open. On Linux, requires `libayatana-appindicator3`; if unavailable, closing and minimizing keep standard window behavior.
- **When you close the window** — **Hide to the tray / menu bar** (default) or **Quit LeapMux** (shown when the tray icon is enabled).
- **When you minimize the window** — **Keep in the taskbar / Dock** (default) or **Hide to the tray / menu bar** (shown when the tray icon is enabled).
- **Start at login** — default **Off**. Registers LeapMux with your OS login items.
- **Window at login** — **Show the window** (default) or **Hide the window** (shown when **Start at login** is enabled; applies only to login launches).

{{< callout type="info" >}}
LeapMux Desktop caches tray and login-launch choices locally per OS user so it can apply window state before signing in to your Hub; your account preference remains the source of truth once signed in.
{{< /callout >}}

## Files & Applications

Device-local settings for external editors, downloads, and the directory picker:

| Setting | Control | Default | What it does |
| --- | --- | --- | --- |
| **Preferred application** (desktop only) | Validated text input (also set from **Open in …**) | Blank (first detected editor) | Accepts a supported editor or file-manager ID (for example, `vscode`) used by `⇧⌘E` / `Ctrl+Shift+E`; leave blank to fall back to the first detected editor. |
| **Reveal after download** (desktop only) | Toggle (also in save dialog) | On | Highlights saved files in Finder, Explorer, or your Linux file manager. |
| **Hidden files in directory picker** | Toggle (also in directory picker) | On | Lists dotfiles inside the **Working Directory** picker. |

## Keyboard Shortcuts and Advanced

{{< screenshot src="preferences-keyboard-shortcuts" alt="LeapMux Preferences Keyboard Shortcuts table showing commands, bindings, and Default or Custom source badges" caption="Remap workspace keybindings under Preferences → Keyboard Shortcuts." width="1280" height="856" >}}

- **Keyboard Shortcuts** — account-synced keybinding editor supporting single shortcuts and two-step chords with conflict detection (up to 200 custom bindings). See [Keyboard Shortcuts](/docs/using/keyboard-shortcuts/).
- **Advanced:**
  - **Debug logging** (dual-tier, default off) — enables verbose client diagnostics in the browser console.
  - **Trusted worker keys** — lists pinned Worker public keys trusted by this browser (trust on first use) so you can remove individual pins or clear all pins.
  - **Reset all browser overrides** — clears every **This device** override at once (restoring account or built-in defaults) without removing trusted Worker keys.

## Related chapters

- [Accounts & Connected Apps](/docs/using/accounts-and-apps/) — profile, passkeys, recovery, and app authorization.
- [Keyboard Shortcuts](/docs/using/keyboard-shortcuts/) — complete shortcut catalog.
- [Configuration Guide](/docs/admin/configuration/) — Hub administrator settings.
- [Configuration Reference](/docs/admin/configuration-reference/) — flags, environment variables, and Hub setting keys.

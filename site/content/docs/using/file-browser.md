---
title: "File Browser"
description: "Browse the active tab's working directory on any Worker with live git status, syntax-highlighted previews, Markdown and image viewers, and inline diffs."
type: docs
weight: 6
---

LeapMux includes a git-aware file browser and read-only file viewer in the right sidebar's **Files** section. It displays the directory tree of the active tab's working directory with live git status colors and diff-stat badges. Click any file to open it as a tab, inspect its contents or inline diffs against `HEAD` or the staging index, and quote lines directly into your agent chat.

All directory listings, file reads, and git metadata come directly from the Worker hosting the active tab over the end-to-end encrypted (E2EE) channel, so the Hub never sees your paths or file contents. See [Security & Authentication](/docs/admin/security-and-auth/).

{{< callout type="info" >}}
The file viewer is strictly read-only. You can inspect, quote, mention, and download files, while edits happen through your coding agents and terminals.
{{< /callout >}}

## The Files section

{{< screenshot src="file-browser-diff" alt="LeapMux file browser with Changed git filter and inline syntax-highlighted unified diff viewer" caption="The right-hand Files sidebar filters modified files and opens unified or split diffs beside your agents." width="722" height="504" >}}

The **Files** panel always tracks the **active tab**, updating automatically when you switch tabs, Workers, or directories. While a new tab is still creating a worktree, the panel displays `Starting…` until the directory exists.

### Header controls

| Button | When shown | What it does |
| --- | --- | --- |
| **Tree view** / **Flat list** | When a git filter is active | Switches between the nested tree and a flat list of repo-relative changed paths. |
| **Locate active file** | When a file tab is active | Scrolls to and highlights the active file in the tree. |
| **Collapse all** | Always | Collapses every expanded directory except the root. |
| **Show hidden files** / **Hide hidden files** | Always | Toggles dotfiles and OS-hidden entries (`⌘⇧H` / `Ctrl+Shift+H`). |
| **Sort by** | Always | Picks sort criterion (**Name**, **Last modified**, **Size**, or **Type**) and direction. Directories always stay above files. |
| **Refresh** | Always | Reloads git status and expanded directories (`⌘R` / `Ctrl+R`, or `F5`). |

Hidden-file visibility and sort order persist per Worker and working directory. Directories are lazy-loaded on expand, fold single-child chains (such as `src/main/java`), and refresh automatically when an agent finishes a turn. Directories with more than 256 entries truncate to the first 256 names and display an inline truncation notice.

### File and folder context menu

{{< screenshot src="file-context-menu" alt="LeapMux file context menu in the Files sidebar showing file size, modification time, Mention in chat, Copy path, Copy relative path, and Download" caption="Open a file or folder's context menu to inspect metadata, @-mention it in chat, copy its path, or save it." width="297" height="363" >}}

Open the **`⋯`** menu on any row (or in the file viewer header) for quick actions:

- **Size & Modified** — shows entry metadata; clicking the block copies path, size, and timestamp as JSON.
- **Mention in chat** — inserts an `@`-mention into the active agent composer.
- **Open a terminal tab here** — (directories only) opens a terminal tab rooted in that directory.
- **Copy path** / **Copy relative path** — copies the full or workspace-relative path.
- **Download** (web) or **Save as...** / **Save to Downloads** (desktop app) — saves files to your local machine.

## Git status, badges, and filters

Inside a git repository, LeapMux overlays live git status onto icons and adds diff-stat badges (`+N -M *U` for added lines, deleted lines, and untracked file count, aggregated upward to parent folders and the root):

| Status | Icon color | Meaning |
| --- | --- | --- |
| Conflict / unmerged | Red | Merge conflict in the file. |
| Untracked | Green | New file not yet tracked by git. |
| Staged only | Green | Staged changes with no further unstaged edits. |
| Modified / unstaged | Amber | Unstaged edits in the working copy. |
| Changed directory | Dimmed amber | Contains one or more changed descendants. |

Four filter tabs appear above the tree: **All**, **Changed**, **Staged**, and **Unstaged**. Choosing a filtered tab hides unchanged directories and lets you switch to **Flat list**.

Opening a file from a git filter picks its initial viewer mode automatically:

- From **Staged** → inline diff (`HEAD` vs. staged copy).
- From **Changed** or **Unstaged** → inline diff (`HEAD` vs. working copy).
- From **All** → plain working-copy view.

## Viewing files

Clicking a file opens it as a tab in the focused tile (or activates the existing tab if already open on that Worker). File tab paths sync across your devices over the encrypted Worker channel; see [Workspaces](/docs/using/workspaces/#live-sync-across-devices).

| Content type | How it displays |
| --- | --- |
| **Text & code** | Line-numbered, syntax-highlighted view up to **256 KiB** (larger files show the first 256 KiB with a truncation notice). Syntax highlighting is skipped for files exceeding **1,000 lines or 20,000 characters** (such as minified bundles), while keeping plain text readable. |
| **Markdown** (`.md`, `.markdown`, `.mdx`) | Top-right toggle between **Rendered**, **Side-by-side** (with synchronized scrolling), and **Source**. |
| **Images** (`.png`, `.jpg`, `.jpeg`, `.gif`, `.bmp`, `.webp`, `.svg`, `.ico`, `.avif`) | Zoomable viewer (25%–500%, fit to view, actual size, continuous Ctrl+wheel zoom, checkerboard behind transparency) up to **5.25 MiB** (`5,505,024` bytes). SVGs also offer **Rendered / Side-by-side / Source**. |
| **Binary or oversize** | Fallback card with **Download** / **Save** actions and an optional **Show anyway** 16-byte offset/hex/ASCII inspector (hidden for empty files). |

{{< screenshot src="file-viewer-markdown" alt="LeapMux file viewer displaying README.md in side-by-side mode with rendered Markdown beside syntax-highlighted source and the Rendered, Side-by-side, and Source toggle" caption="Markdown and SVG files offer a top-right toggle between Rendered, Side-by-side, and Source views." width="714" height="491" >}}

### Quoting selections into chat

Highlight any lines in a text or source view to reveal a floating toolbar with **Quote** (inserts the selected lines and line range into the active agent's composer) and **Copy**.

## Inline diffs

{{< screenshot src="file-browser-split-diff" alt="LeapMux file viewer in Split diff mode displaying the HEAD, Working, Unified, Split, vs Working, and vs Staged toolbar beside the Files sidebar" caption="Switch between HEAD, Working, Unified, and Split diffs, or toggle vs Working and vs Staged when a file has both staged and unstaged edits." width="714" height="491" >}}

When a file has git changes, the viewer toolbar offers four views: **HEAD**, **Working**, **Unified**, and **Split** (and auto-scrolls to the first change).

When a file has both staged and unstaged changes, an extra sub-toggle lets you choose:

- **vs Working** — compares `HEAD` against the on-disk working copy.
- **vs Staged** — compares `HEAD` against the staged index copy.

Binary diffs display a one-line status summary (for example, `Binary file logo.png was added in the working copy.`), and files missing at a revision show `File does not exist at this ref`. If changes are reverted while viewing a diff, the tab switches back to **Working** automatically.

{{< callout type="info" >}}
Your default diff style (**Unified** or **Side by side**) is controlled in **Preferences → Appearance → Diff view** (see [Settings & Preferences](/docs/using/settings/#appearance)).
{{< /callout >}}

## Related chapters

- [Worktrees & Branches](/docs/using/worktrees-and-branches/) — switch branches, create worktrees, and push changes.
- [Coding Agents](/docs/using/coding-agents/) — quote code spans and `@`-mention files in chat.
- [Tabs & Layout](/docs/using/tabs-and-layout/) — tile, split, and float file tabs alongside agents.
- [Keyboard Shortcuts](/docs/using/keyboard-shortcuts/) — customize `⌘⇧H` / `Ctrl+Shift+H` and `⌘R` / `Ctrl+R`.

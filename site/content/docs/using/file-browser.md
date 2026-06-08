---
title: "File Browser"
description: "The git-aware file browser and read-only viewer in LeapMux: browse the tree with live git status, open files as tabs, and view inline diffs, even remotely."
type: docs
weight: 8
---

LeapMux includes a git-aware file browser and a read-only file viewer. The browser lives in the workspace sidebar's **Files** section and shows the directory tree of the active tab's working directory, with live git-status colors and diff-stat badges. Clicking a file opens it as a tab in the main tile area, where you can read its contents, view inline diffs against `HEAD` or the index, preview images and Markdown, and quote selections into a chat.

Everything the file browser shows — directory listings, file contents, and git status — comes from the Worker that owns the active tab. When that Worker runs on a remote machine, all of this data streams over the end-to-end-encrypted Worker channel; the Hub never sees your paths or file contents. See [Security & Threat Model](/docs/operating/security/) for the trust boundaries.

> **Note:** The file viewer is strictly read-only. LeapMux has no file-editing or file-writing capability — there is no "save" that writes back to disk. The viewer's only output actions are downloading, quoting, and mentioning files. Edits happen through your coding agents and terminals, not through this browser.

## The Files section

The **Files** section is a collapsible panel in the workspace sidebar, marked with a folder-tree icon. It is open by default and can be reordered or collapsed like any other sidebar section.

The section always tracks the **active tab**. It renders the directory tree rooted at that tab's working directory and reflects that tab's Worker, working directory, home directory, and git status. Switching tabs re-roots the tree. If no tab is selected, the section shows `No tab selected`.

When the active tab is still starting up — for example, an agent that is creating a new worktree on disk — the tree shows a `Starting…` spinner and suppresses listing until the working directory exists.


**The Files panel in the sidebar:**

```text
┌──────────────────────────────────────────────────┐
│ Files                collapse · hidden · refresh │
│ Enter path...                          ~/project │
│                                                  │
│ project/                                         │
│ ├── src/                                         │
│ │   ├── app.ts                                   │
│ │   └── utils.ts                                 │
│ ├── docs/                                        │
│ ├── notes.txt                                    │
│ └── README.md                                    │
└──────────────────────────────────────────────────┘
```

### Section header buttons

The Files section header carries a row of action buttons, shown on the right. Some appear only in certain states.

| Button | When shown | What it does |
|--------|-----------|--------------|
| **Tree view** / **Flat list** | Only when a git filter is active | Toggles between the directory tree and a flat list of changed files. |
| **Locate active file** | Only when a file tab is active | Selects and scrolls to the active file's path in the tree. |
| **Collapse all** | Always | Collapses every expanded directory except the root. |
| **Show hidden files** / **Hide hidden files** | Always | Shows or hides hidden files (dotfiles and OS-hidden entries). Bound to `$mod+Shift+h`. |
| **Refresh** | Always | Re-fetches git status and reloads the directory tree. Bound to `$mod+r` and `F5`. |

The show/hidden and refresh buttons have keyboard shortcuts that work anywhere in the workspace. See [Keyboard Shortcuts](/docs/using/keyboard-shortcuts/) for the full keybindings table and how to customize them.

> **Tip:** The hidden-files setting is remembered per Worker and working directory, so each repo keeps its own preference across reloads.

## The directory tree

The tree shows the active tab's working directory and its contents.

- **Path input.** A text box at the top of the tree shows the selected path, abbreviated with `~` for your home directory. You can type a path and press Enter (or click away) to navigate there; `~` is expanded automatically. The placeholder is `Enter path...`. If you type an absolute path whose style does not match the Worker's OS, a hint appears, for example: `This looks like a POSIX path but the worker expects Windows paths.`
- **Root row.** The first row is the working directory itself, shown with an open-folder icon, the directory's base name, and a diff-stats badge summarizing the whole repo.
- **Directories and files.** Directories show a chevron that rotates when expanded. Clicking a directory expands or collapses it; clicking a file selects it and opens it as a file tab.
- **Single-child folding.** Long single-child chains like `src/main/java` are collapsed into one row to save space. Row labels always use `/` separators regardless of the Worker's OS.
- **Lazy loading.** A directory's children are fetched the first time you expand it. While fetching, the row shows `Loading...`; an empty directory shows `Empty`.
- **Large directories.** A directory with more than 256 entries is truncated, and the tree shows an inline `<N>+ entries, listing truncated` row.

The tree refreshes itself automatically: it silently re-fetches expanded directories whenever an agent finishes a turn, and reloads in full when you click **Refresh**. Old contents stay visible during the refresh so the view never flickers blank.

> **Note:** The tree's expanded/collapsed state and cached listings are remembered per working directory, so re-opening a workspace restores your place in the tree. This is session-scoped: it survives reloads within the same browser tab but is cleared when that tab is closed.

### File and folder context menu

Each tree row has a three-dot menu (and the file viewer exposes the same menu). The items shown depend on whether the row is a file or a directory, and whether you are on the web or the desktop app:

| Item | When shown | What it does |
|------|-----------|--------------|
| **Mention in chat** | When a chat target is available | Inserts a `@`-mention of the path into the active agent's chat editor. |
| **Open a terminal tab here** | Directories only | Opens a new terminal tab rooted at that directory. |
| **Copy path** | Always | Copies the absolute path. |
| **Copy relative path** | When the Worker's working directory is known | Copies the path relative to the Worker's working directory. |
| **Download** | Files, on the web | Downloads the file through your browser. |
| **Save as...** / **Save to Downloads** | Files, on the desktop app | Saves the file to a chosen location or to your Downloads folder. |

## Git status and filters

When the active tab's working directory is a git repository, the file browser overlays live git status onto the tree and adds a filter bar. Git status is recomputed on the Worker — from `git status` plus diff statistics — and refreshes when an agent finishes a turn, when you click **Refresh**, when you switch branches, and when you switch to a tab on a different Worker or working directory. (It never fires while a tab is still starting.)

### Status colors

File and folder icons are tinted by git status:

| State | Color | Meaning |
|-------|-------|---------|
| Conflict / unmerged | Red | The file is in a merge conflict. |
| Untracked | Green | A new file git is not yet tracking. |
| Staged only | Green | Changes are staged and the working copy has no further unstaged changes. |
| Modified / unstaged | Amber | The working copy has unstaged changes. |
| Changed directory | Amber (dimmed) | A folder containing changed files somewhere below it. |

### Diff-stat badges

Each changed row shows a diff-stat badge in the form `+N -M *U`:

- `+N` — lines added (green).
- `-M` — lines deleted (red).
- `*U` — number of untracked files (amber). Untracked files count toward `*U` but contribute no added/deleted line counts.

A directory's badge aggregates the stats of everything changed beneath it, and the root row's badge summarizes the whole repository. A badge with all-zero counts is hidden.

Putting the colors and badges together, a small repo with one staged file, one unstaged file, and one untracked file renders roughly like this (status shown in parentheses; in the UI it is conveyed by the icon tint):

**A git-aware file tree with status badges:**

```text
myproject                 +12 -4 *1   (root summary)
├── src                   +12 -4      (changed directory)
│   ├── app.ts            +8 -3       (modified / unstaged)
│   └── utils.ts          +4 -1       (staged only)
├── notes.txt             *1          (untracked)
└── README.md                         (unchanged, no badge)
```

### Filter tabs

When the working directory is a git repo, a filter bar appears above the tree with four tabs:

| Tab | Shows |
|-----|-------|
| **All** | The full directory tree (the default). |
| **Changed** | Files with staged or unstaged changes. |
| **Staged** | Files with staged changes. |
| **Unstaged** | Files with unstaged changes. |

Choosing any filter other than **All** restricts the view to changed files. In a filtered tree, directories with no changed descendants are hidden, and an empty result shows `No changes`. While a filter is active you can switch to a **Flat list** (via the header toggle) that lists each changed file by its repo-relative path, with the same status colors and diff badges.

> **Tip:** The filter you open a file from also sets the file viewer's initial mode. Opening a file from **Staged**, **Changed**, or **Unstaged** drops you straight into the inline diff; opening from **All** shows the plain working-copy content. See "Opening a file" below.

## Opening a file

Clicking a file in the tree or flat list opens it as a file tab in the focused tile. If a tab for that file (on the same Worker) already exists, LeapMux activates it instead of opening a duplicate. The tab's title is the file's base name.

The initial view depends on the filter you opened from:

| Opened from | Initial view |
|-------------|--------------|
| **Staged** | Inline diff of `HEAD` vs the staged copy. |
| **Changed** or **Unstaged** | Inline diff of `HEAD` vs the working copy. |
| **All** (or an unfiltered tree) | The plain working-copy content. |

File tabs participate in the workspace's tiling and layout system like any other tab, so you can split, float, and rearrange them. See [Tabs & Layout](/docs/using/tabs-and-layout/). The file's path is registered with the Worker over the encrypted channel so that collaborators in the same workspace can resolve the same file — see [Collaboration & Presence](/docs/using/collaboration/).

## Viewing files

The file viewer auto-detects the content type and renders it accordingly.

| Content | How it renders |
|---------|----------------|
| Text | Syntax-highlighted with line numbers. Highlighting is skipped for files over 1000 lines (the text still displays). |
| Markdown (`.md`, `.markdown`, `.mdx`) | A rendered/source/side-by-side toggle. |
| Images (`.png`, `.jpg`, `.jpeg`, `.gif`, `.bmp`, `.webp`, `.svg`, `.ico`, `.avif`) | A zoomable image viewer. SVGs also get the rendered/source/side-by-side toggle. |
| Binary or oversize | A fallback card with download/save actions and an optional hex view. |

### Markdown and SVG view toggle

Markdown files and SVG images show a segmented toggle in the top-right corner:

- **Rendered view** — the rendered document or image.
- **Side-by-side view** — rendered and source panes together, with synchronized scrolling for Markdown.
- **Source view** — the raw text.

### Image viewer

Images open at a fit-to-view zoom. The image toolbar provides **Zoom out**, a zoom percentage, **Zoom in**, **Fit to view**, and an **Actual size (100%)** button. Zoom ranges from 25% to 500%. The **Zoom in** and **Zoom out** buttons step in 25% increments, while pinch-zoom (Ctrl+wheel) toward the cursor is continuous within the same range. A checkerboard background sits behind transparent images.

### Binary and oversize files

The viewer reads at most 256 KiB of a file. Binary files (detected by extension or by sniffing for non-printable bytes) and oversize images show a fallback card instead of inline content:

- **This image is too large to preview.** — for an oversize image.
- **This file cannot be displayed inline.** — for a binary file.

From the card you can **Download** (web) or **Save as...** / **Save to Downloads** (desktop), and you can choose **Show anyway** to open a hex view of the raw bytes. On the desktop app the card also offers a **Reveal in file manager after save** checkbox. The hex view shows the classic offset / 16-byte / ASCII layout, with a **Show download view** button to return to the card.

> **Note:** The **Show anyway** button is hidden for empty (zero-byte) files, since there are no bytes to inspect.

### Status bar and truncation

In plain (non-diff) views, a status bar at the bottom shows the file size. When a file is larger than the 256 KiB read window, the viewer shows only the first 256 KiB and displays a `Truncated at 256.0 KB` warning. Your scroll position is remembered per file and view mode.

### Quoting and copying selections

Selecting text in a text, source, or Markdown-source view pops up a small toolbar with two actions:

- **Quote** — inserts a quote of the selected lines (with their line range) into the active agent's chat editor.
- **Copy** — copies the selection to the clipboard.

This is the fastest way to point an agent at a specific span of code without retyping it. See [Coding Agents](/docs/using/coding-agents/) for how quotes appear in the chat.

## Inline diffs

When a file has git changes, the viewer offers diff modes alongside the plain content. A toolbar at the top of the viewer switches between them:

| Mode | Shows |
|------|-------|
| **HEAD** | The file's committed `HEAD` revision (read-only text). |
| **Working** | The on-disk working copy. |
| **Unified** | An inline unified diff. Shown only when the file has git changes. |
| **Split** | A side-by-side diff. Shown only when the file has git changes. |

When a file has both staged and unstaged changes, an extra sub-toggle appears so you can choose which diff to view:

- **vs Working** — `HEAD` compared against the on-disk working copy.
- **vs Staged** — `HEAD` compared against the staged (index) copy.

The committed and staged versions are read from git on the Worker; the diff itself is computed on your side and rendered in unified or split layout. Entering a diff auto-scrolls to center the first change.

A couple of edge cases:

- **Binary diffs** can't be shown as text. Instead you get a one-line description derived from git status, for example: `Binary file logo.png was added in the working copy.`
- If a file does not exist at the chosen revision, the `HEAD` or staged view shows `File does not exist at this ref`.
- If a file has no diff to show, the viewer falls back to the **Working** view automatically so you are never stranded on an empty diff.

## Working against a remote Worker

The file browser works identically whether the Worker is local or remote, because all of its data comes from the Worker over RPCs:

- **Directory listings, file reads, and file stats** for the tree and the viewer.
- **Git status** (file states and diff stats) for the colors, badges, and filters.
- **Committed and staged file contents** for the inline diffs.

When the Worker is on another machine, every one of these requests and responses travels over the end-to-end-encrypted Worker channel. The Hub relays the encrypted traffic but cannot read your paths or file contents. This is the same channel your agents and terminals use. For how remote Workers are registered, approved, and selected, see [Managing Workers](/docs/operating/managing-workers/); for the encryption details, see [Security & Threat Model](/docs/operating/security/).

> **Note:** Git error messages surfaced in the browser (for example, a dubious-ownership warning) appear in English.

## Related chapters

- [Worktrees & Branches](/docs/using/worktrees-and-branches/) — open a tab in a worktree, switch/create/delete branches, push, and the dirty-worktree protections. The file browser reflects the branch and working directory of whatever tab is active, including linked worktrees.
- [Terminals](/docs/using/terminals/) — the "Open a terminal tab here" action opens a terminal rooted at the selected directory.
- [Coding Agents](/docs/using/coding-agents/) — quoting and mentioning files feeds the active agent's chat.
- [Tabs & Layout](/docs/using/tabs-and-layout/) — file tabs tile, split, and float like any other tab.

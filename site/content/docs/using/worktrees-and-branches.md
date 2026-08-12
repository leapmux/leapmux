---
title: "Worktrees & Branches"
description: "Run many agents on one repo without conflicts using git worktrees: pick a branch or worktree when opening a tab, change it, push, and protect uncommitted work."
type: docs
weight: 6
---

LeapMux is built to run several coding agents at once against the same repository. The thing that keeps them from clobbering each other's changes is **git worktrees** — each agent (or terminal) can work in its own linked worktree, on its own branch, with its own working copy. This chapter explains how to choose a branch or worktree when you open a tab, how to change or delete branches later, how to push your work, and how LeapMux protects you from losing uncommitted changes when you close a tab.

For the content that lives inside tabs, see [Coding Agents](/docs/using/coding-agents/) and [Terminals](/docs/using/terminals/). For the git-aware file tree and inline diffs, see [File Browser](/docs/using/file-browser/). For the tiling canvas the tabs live in, see [Tabs & Layout](/docs/using/tabs-and-layout/).

## Why per-agent worktrees matter

A single git checkout has one working copy and one current branch. Two agents that share it share that working copy. Each agent sees the other's edits, staged files, and branch switches. A `git checkout` by one agent changes the files under the other.

A **linked worktree** is a second working directory attached to the same repository, checked out on a different branch. With a worktree per agent, each agent gets:

- An isolated working copy — files one agent edits do not appear in another's tree.
- An independent branch — switching or committing in one worktree does not touch another.
- A contained cleanup — delete a worktree, and its branch, when that line of work ends. The main checkout stays untouched.

LeapMux makes this the default mental model: tabs are grouped in the sidebar by repository and then by branch, and the open-time **Git options** let you spin up a fresh worktree without ever touching the terminal.

> **Note:** Worktrees are optional. You can also keep working in your repository's main checkout ("Use current state") — useful for quick one-off tasks where isolation does not matter.

## The Repo → Branch sidebar tree

Open tabs are grouped in the workspace sidebar into a two-level tree:

```text
Repo group   (Repo label)
└─ Branch group   (Branch name + diff-stats badge)
   ├─ tab
   └─ tab
```

- The **repo group** header shows the repository, with the origin URL (or the toplevel path for a local repo with no origin) in its tooltip.
- Each **branch group** header shows the branch name and a diff-stats badge summarizing changes in that working directory.
- LeapMux groups tabs by branch name, Worker, and repository path together, so two clones of the same repo on the same branch stay in separate groups.

A working directory with no current branch carries a state label instead. **`(no branch)`** means a repository with no commits yet, or a tab LeapMux has not yet stamped with its git state — a new tab shows it for a moment, then picks up its real branch. A detached HEAD carries the **short commit SHA** (e.g. `a1b2c3d`). **Create new branch** moves either one onto a real branch.

### The branch context menu

Each branch row has a **`...`** context menu with exactly two items:

| Item | What it does |
|---|---|
| **Change branch...** | Opens the [Change branch dialog](#changing-the-branch-on-a-tab). |
| **Delete branch...** | Opens the [Delete branch dialog](#deleting-a-branch) (styled in red). |

Both items act through the Worker that hosts the repository. LeapMux greys them out, with the reason on hover, while that Worker is offline. The `(no branch)` row carries no menu at all, because it has no branch to change or delete.

> **Note:** A detached-HEAD row keeps its menu, because its short-SHA label is a real label. **Delete branch** fails there: the row names a commit and not a branch, so the Worker has no branch to force-delete. It reports that failure only after it already switched the working directory to the branch you picked. Use **Create new branch** first to get onto a real branch.

### The branch chip in the composer

The composer's status bar carries the same two items behind a branch-name chip, so you can change or delete the branch without opening the sidebar. The chip appears when the focused agent reports a branch. Hiding the status bar (**[+]** ▸ **Show status bar**) hides the chip; the sidebar's branch row keeps both actions.

## Choosing a branch or worktree when you open a tab

When you open a new agent, a new terminal, or a new workspace against a git repository, the dialog shows a **`Git options`** panel with five modes (select one with the radio buttons):

| Mode | What it does | Fields |
|---|---|---|
| **Use current state** | Keeps the current branch and working copy. The default for new tabs. | Shows *Currently on branch: \<branch\>* |
| **Switch to branch** | Checks out an existing branch in this working directory. | A branch selector |
| **Create new branch** | Creates a new branch from a base and checks it out here. | **Branch Name**, **Base Branch** |
| **Create new worktree** | Creates a new linked worktree on a new branch (isolation). | **Branch Name**, **Base Branch**, **Worktree path:** preview |
| **Use existing worktree** | Opens the tab in a worktree that already exists. | A worktree selector |

### Use current state

No fields. The tab opens in the repository's current working directory on its current branch. When a current branch exists, the panel shows *Currently on branch: \<branch\>*.

### Switch to branch

Pick a branch from the selector. The list has a **Local** and a **Remote** option group, and ` (current)` marks the branch you are already on.

The panel warns you about three cases. You picked the branch you are already on. You picked a remote branch, and LeapMux checks out the same-named local branch instead. Or the working copy holds uncommitted changes, which can make the switch fail or discard them.

### Create new branch

- **Branch Name** — type a name, or click the **Generate random name** button to fill in a three-word kebab-case slug (e.g. `brave-amber-otter`). The input placeholder is `feature-branch`.
- **Base Branch** — the branch to start from. It is seeded to the current branch once branches load. Leaving it empty is allowed — the Worker defaults to the current HEAD, which lets you create a branch even on a detached or unborn HEAD.

LeapMux validates the name against its own approximation of git's `check-ref-format` rules. It rejects an empty name, a name over 256 characters, control characters, the characters space `~ ^ : ? * [ ] \`, a name that starts with `/ . - @`, a name that ends with `/`, `.`, or `.lock`, and a name that holds `..`, `//`, or `/.`. A rejected name, or one an existing branch already uses, shows the reason below the input.

A new branch here carries the working copy with it, uncommitted changes included. The panel states this when it finds any.

### Create new worktree

Same **Branch Name** and **Base Branch** fields as Create new branch, plus a read-only **Worktree path:** preview. LeapMux always places a new worktree at a fixed location next to the repository:

```
<repo-parent>/<repo-dirname>-worktrees/<branch>
```

For example, a repository at `~/code/leapmux` with a branch `fix-login` produces `~/code/leapmux-worktrees/fix-login`. The preview is tilde-abbreviated, with the full path in a tooltip. If that path already exists on disk, the operation is rejected.

So the worktrees live in a sibling directory next to the main checkout, one subdirectory per branch:

**On-disk worktree layout:**

```text
~/code/                            ◄── repo parent
├── leapmux/                       ◄── main checkout (current branch)
│   └── .git/
└── leapmux-worktrees/             ◄── sibling worktrees directory
    ├── fix-login/      ◄── agent / terminal tab opens here
    │   └── (working copy on branch fix-login)
    └── add-search/     ◄── agent / terminal tab opens here
        └── (working copy on branch add-search)
```

Each agent or terminal tab that uses a worktree opens in one of these branch directories, so its edits stay isolated from the main checkout and from the other worktrees.

A new worktree starts from committed state only. Uncommitted changes in the source working copy stay where they are. The panel states this when it finds any.

### Use existing worktree

Pick a worktree from the selector. Each option carries the label `<branch> — <tilde-path>`. The selector lists **linked** worktrees only. It leaves out the repository's main working tree, so you cannot adopt the main checkout as a managed worktree by accident.

> **Tip:** Create new worktree is the right choice for "start a fresh task in isolation." Use existing worktree is for re-attaching a tab to work you (or another agent) already set up.

## Changing the branch on a tab

Open the branch row's **`...`** menu and choose **Change branch...** to open the **Change branch** dialog. It works on one repository working directory. It offers three of the five modes: **Switch to branch** (the default), **Create new branch**, and **Create new worktree**.

What each mode does on **Apply**:

| Mode | Effect |
|---|---|
| **Switch to branch** | Checks out the chosen branch in this working directory. Every tab in the group is relabelled to the new branch. |
| **Create new branch** | Creates the branch from the chosen base and checks it out here. Tabs relabelled to the new branch. |
| **Create new worktree** | Opens a **brand-new tab** in the new worktree — your current tabs stay where they are. |

Switch and Create-branch change the working directory under the tabs already in it. An agent or a terminal there does not stop, and from that point it reads the new branch's files. The dialog states this before you apply.

When you pick **Create new worktree** in this dialog, an extra **Open as** selector appears with two choices:

- **Agent** — shows an agent provider picker and opens an agent tab in the new worktree.
- **Terminal** — shows a **Shell** picker and opens a terminal tab in the new worktree.

The sidebar labels update as soon as the change completes. The file browser's git status refreshes too, when it shows the repository you changed.

> **Warning:** Switching branches with uncommitted changes can fail or discard work. If the dialog reports uncommitted changes, commit or push them first (see [Pushing a branch](#pushing-a-branch)).

## Deleting a branch

Open the branch row's **`...`** menu and choose **Delete branch...** to open the **Delete branch** dialog. It shows a [branch status block](#branch-status-indicators) and a sentence describing which tabs are affected. The primary action is the red **Delete branch** button; there is also a **Cancel** button and, when there is pushable work, a [Push](#pushing-a-branch) button.

Deletion behaves differently depending on whether the branch is a linked worktree.

### Deleting a linked worktree

There is no "switch to" picker, and the status block notes that the group's tabs *will be stopped*. **Delete branch** closes every tab in the group and removes the worktree. Once the last tab that points at that worktree is gone, the Worker runs `git worktree remove`, deletes the branch, and drops its record. It skips the branch delete when another worktree still has that branch checked out, so a branch you added to two worktrees survives the first removal.

The dialog checks that git accepts the removal, then closes and leaves the work running on the Worker. The Worker needs a moment to stop an agent and delete a large working copy, so the directory disappears shortly after the tabs do. A worktree that another tab still uses, or one LeapMux does not track (a directory you created yourself with `git worktree add`), stays on disk.

If git refuses the removal outright — the worktree is locked, for example — the dialog stays open with the reason and closes nothing.

### Deleting a regular branch

For a branch in the main checkout, you must tell LeapMux where to leave HEAD. The dialog shows **Switch this working directory to:** and a branch selector listing every branch except the one being deleted. On **Delete branch**, the Worker checks out your chosen target, then force-deletes the doomed branch. Tabs keep running on the switched-to branch.

If the branch you are deleting is the **only** branch, the selector is replaced by the error **Cannot delete the only branch. Create another branch first.** and the button stays disabled.

> **Warning:** Branch deletion is a force-delete (`git branch -D`). Unmerged commits on the deleted branch that have not been pushed are gone. If the status block shows unpushed commits, push first.

## Pushing a branch

The Delete branch dialog and the Close last tab dialog both offer a push button when the branch has work to push. The Delete branch dialog also needs a tab in the group that carries a working directory, which is the directory it pushes from. The label adapts:

| Branch state | Button label |
|---|---|
| Has uncommitted changes | **Commit and Push** |
| Clean working copy, but unpushed commits or no remote branch | **Push** |

**Commit and Push** stages everything (`git add -A`) and makes a `WIP` commit before pushing. **Push** just pushes. If the branch has no upstream yet, LeapMux sets one up (`git push -u origin <branch>`). LeapMux abandons a push that does not complete within 60 seconds.

A push needs an `origin` remote and a real branch name, so LeapMux cannot push a detached HEAD.

> **Tip:** Use **Commit and Push** as a quick "save my work before I switch or delete" before changing or deleting a branch. The `WIP` commit captures everything so nothing is lost; you can reword or squash it later.

## Branch status indicators

The Delete branch and Close last tab dialogs share a status block that summarizes the branch's git state. Depending on the state, it shows some of:

- **Worktree:** `path` — only for a linked worktree.
- **Branch:** `name`.
- **Uncommitted changes:** with a diff-stats badge — when the working copy is dirty.
- ***N* commit(s) not pushed.** — when there are unpushed commits.
- **Branch not pushed to remote.** — when the branch has no remote counterpart.
- **No uncommitted changes or unpushed commits.** — when everything is committed and pushed.
- A sentence describing the affected tabs (for example, *2 agents and 1 terminal will be stopped, 1 file will be closed.*).

The sidebar branch-group header also carries a diff-stats badge (`+N -M *U`) so you can see at a glance which branches have changes. For the full meaning of those badges and the per-file git status colors, see [File Browser](/docs/using/file-browser/).

## Dirty-worktree protection when closing tabs

Closing tabs is where you are most likely to lose work, so LeapMux guards the last tab of a worktree or branch. When you close the **last** tab of a worktree, or the last non-worktree tab on a branch that has uncommitted changes, unpushed commits, or a missing remote, the **Close last tab** dialog appears.

It identifies what you are about to close — the worktree path, or the branch — and shows the same [branch status block](#branch-status-indicators). Its buttons:

| Button | Effect |
|---|---|
| **Cancel** | Aborts the close — nothing happens. |
| **Push** / **Commit and Push** | Pushes your work first (shown only when there is pushable work). |
| **Delete** (worktree targets only) | Closes the tabs and schedules the worktree for removal. |
| **Close anyway** | Closes the tab(s) but keeps the worktree on disk. |

If git refuses the removal — the worktree is locked, for example — **Delete** is unavailable and the reason appears above the buttons. **Close anyway** still closes the tab.

LeapMux removes a worktree only as part of closing the tabs that point at it, so no removal takes one away from a live tab. **Delete** here covers the last tab on that worktree. [**Delete branch...**](#deleting-a-linked-worktree) on the branch row covers the whole group at once.

> **Warning:** **Close anyway** does not push and does not delete. It closes the tab. Any uncommitted changes stay on disk in the worktree, but you lose the tab that points at it. Use **Push** / **Commit and Push** first if the status block shows work you want to keep.

> **Note:** This dialog needs the Worker, because only the Worker reads the branch's git state. With that Worker offline the tab closes without the dialog, and the worktree stays on disk while the Worker is down. This is **not** the same as **Close anyway**: that choice keeps the worktree whatever its state, but a close made while the Worker was offline leaves the worktree unreferenced. When the Worker returns, its housekeeping pass reclaims an unreferenced worktree — including the directory and the branch. It leaves any worktree that holds uncommitted or unpushed work for you, so your work is safe either way.

## Where git operations run

Every git command runs on the **Worker** that owns the working directory — the machine where your repository actually lives — not in your browser and not on the Hub. So the Worker computes all the branch and worktree state you see (branches, worktrees, diff stats, ahead/behind) and streams it back over the end-to-end-encrypted Worker channel.

The Worker runs each git command with a fixed English/C locale and with terminal prompts disabled, so no command blocks and waits for credentials. A push against a private remote therefore fails instead of hanging. Configure a credential helper or an SSH agent on the Worker.

For more on workers and how they are selected, see [Managing Workers](/docs/operating/managing-workers/). For the run modes that host workers, see [Running LeapMux](/docs/operating/running-leapmux/).

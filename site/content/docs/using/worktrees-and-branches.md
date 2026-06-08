---
title: "Worktrees & Branches"
type: docs
weight: 6
---

LeapMux is built to run several coding agents at once against the same repository. The thing that keeps them from clobbering each other's changes is **git worktrees** — each agent (or terminal) can work in its own linked worktree, on its own branch, with its own working copy. This chapter explains how to choose a branch or worktree when you open a tab, how to change or delete branches later, how to push your work, and how LeapMux protects you from losing uncommitted changes when you close a tab.

For the content that lives inside tabs, see [Coding Agents](/docs/using/coding-agents/) and [Terminals](/docs/using/terminals/). For the git-aware file tree and inline diffs, see [File Browser](/docs/using/file-browser/). For the tiling canvas the tabs live in, see [Tabs & Layout](/docs/using/tabs-and-layout/).

## Why per-agent worktrees matter

A single git checkout has one working copy and one current branch. If two agents share it, they share that working copy: one agent's edits, staged files, and branch switches are visible to the other, and a `git checkout` triggered by one can yank the rug out from under the other.

A **linked worktree** is a second working directory attached to the same repository, checked out on a different branch. With a worktree per agent, each agent gets:

- An isolated working copy — files one agent edits do not appear in another's tree.
- An independent branch — switching or committing in one worktree does not touch another.
- A clean blast radius — you can delete a worktree (and its branch) when you are done with that line of work without disturbing the main checkout.

LeapMux makes this the default mental model: tabs are grouped in the sidebar by repository and then by branch, and the open-time **Git options** let you spin up a fresh worktree without ever touching the terminal.

> **Note:** Worktrees are optional. You can also keep working in your repository's main checkout ("Use current state") — useful for quick one-off tasks where isolation does not matter.

## The Repo → Branch sidebar tree

Open tabs are grouped in the workspace sidebar into a two-level tree:

**The Repo → Branch sidebar tree:**

```text
Repo group   (Repo label)
└─ Branch group   (Branch name + diff-stats badge)
   ├─ tab
   └─ tab
```

- The **repo group** header shows the repository, with the origin URL (or the toplevel path for a local repo with no origin) in its tooltip.
- Each **branch group** header shows the branch name and a diff-stats badge summarizing changes in that working directory.
- Tabs are bucketed by the combination of branch name, Worker, and git toplevel path, so two clones of the same repo on the same branch stay in separate groups.

How a group with no normal current branch is labelled depends on its exact state:

- An **unborn HEAD** — a freshly-initialized repo with no commits yet, or a tab that has not been git-stamped — has no resolvable branch. It has no current branch and is labelled **`(no branch)`**.
- A **detached HEAD that has at least one commit** is labelled with its **short commit SHA** (e.g. `a1b2c3d`). This is a real label, not the `(no branch)` bucket.

> **Note:** The `(no branch)` group has no context menu — branch operations like change and delete cannot run there, because there is no branch to act on. A detached-HEAD-with-commits row, by contrast, keeps its `...` menu (it has a short-SHA label), but **Delete branch** there will fail on the Worker: there is no branch to force-delete, only a commit, so `git branch -D <short-sha>` is refused. To get a real branch, use **Create new branch** (it works on a detached or unborn HEAD).

### The branch context menu

Each branch row has a **`...`** context menu with exactly two items:

| Item | What it does |
|---|---|
| **Change branch...** | Opens the [Change branch dialog](#changing-the-branch-on-a-tab). |
| **Delete branch...** | Opens the [Delete branch dialog](#deleting-a-branch) (styled in red). |

The menu is hidden when the row is read-only, when the group has no current branch at all (the `(no branch)` group — an unborn HEAD or an un-stamped tab), or when the tab has not yet been stamped with a git toplevel path. A detached-HEAD-with-commits row keeps its menu, since it carries a short-SHA label.

## Choosing a branch or worktree when you open a tab

When you open a new agent, a new terminal, or a new workspace against a git repository, the dialog shows a **`Git options`** panel. While LeapMux probes the repository it shows a **Loading branch info** spinner; if the directory is not a git repository or the probe fails, it shows **Git probe failed: *hint*** instead.

The panel offers five modes (select one with the radio buttons):

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

Pick a branch from the selector. The list is split into **Local** and **Remote** option groups; the current branch is suffixed ` (current)`. The selector starts with a **Select a branch...** prompt.

LeapMux warns you about a few situations:

- Picking the branch you are already on: *Working directory is already on this branch.* (or *Working directory is already on local branch "\<cur\>".*).
- Picking a remote branch when a same-named local branch already exists: *Local branch "\<localName\>" already exists and will be checked out instead.*
- A dirty working copy: *The working copy has uncommitted changes. Switching branches may fail or discard changes.*

### Create new branch

- **Branch Name** — type a name, or click the **Generate random name** button to fill in a three-word kebab-case slug (e.g. `brave-amber-otter`). The input placeholder is `feature-branch`.
- **Base Branch** — the branch to start from. It is seeded to the current branch once branches load. Leaving it empty is allowed — the Worker defaults to the current HEAD, which lets you create a branch even on a detached or unborn HEAD.

If the name fails validation, or collides with an existing branch (*A branch with this name already exists*), the error shows below the input. A dirty working copy shows: *The working copy has uncommitted changes. Creating a new branch will include them.*

> **Note:** LeapMux validates branch names with a `check-ref-format`-style ruleset. It rejects empty names, names over 256 characters, control characters, the characters space `~ ^ : ? * [ ] \`, names that start with `/ . - @`, names that end with `/`, `.`, or `.lock`, and names containing `..`, `//`, or `/.`.

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

A dirty working copy shows: *The selected working copy has uncommitted changes that will not be transferred to the new worktree.* — the new worktree starts from committed state only.

### Use existing worktree

Pick a worktree from the selector (prompt: **Select a worktree...**). Each option is labelled `<branch> — <tilde-path>`. Only **linked** worktrees are listed — the repository's main working tree is filtered out, so you can never accidentally adopt the main checkout as a managed worktree. While loading it shows **Loading worktrees...**; with none it shows **No worktrees found**.

> **Tip:** Create new worktree is the right choice for "start a fresh task in isolation." Use existing worktree is for re-attaching a tab to work you (or another agent) already set up.

## Changing the branch on a tab

Open the branch row's **`...`** menu and choose **Change branch...** to open the **Change branch** dialog. It operates on one repository working directory and offers a restricted set of modes — **Switch to branch**, **Create new branch**, and **Create new worktree**. (*Use current state* is intentionally excluded; there is nothing to change.) The default mode is **Switch to branch**.

The footer has a **Cancel** button and an **Apply** button (which reads **Applying...** while it runs).

What each mode does on **Apply**:

| Mode | Effect |
|---|---|
| **Switch to branch** | Checks out the chosen branch in this working directory. Every tab in the group is relabelled to the new branch. |
| **Create new branch** | Creates the branch from the chosen base and checks it out here. Tabs relabelled to the new branch. |
| **Create new worktree** | Opens a **brand-new tab** in the new worktree — your current tabs stay where they are. |

For Switch and Create-branch, the dialog warns: *Running agents and terminals will continue on the new branch.* The same working directory changes underneath them, so a long-running agent or terminal keeps running but now sees the new branch's files.

When you pick **Create new worktree** in this dialog, an extra **Open as** selector appears with two choices:

- **Agent** — shows an agent provider picker (or *No agent providers configured for this worker.* when there are none) and opens an agent tab in the new worktree.
- **Terminal** — shows a **Shell** picker and opens a terminal tab in the new worktree.

> **Warning:** Switching branches with uncommitted changes can fail or discard work. If the dialog reports uncommitted changes, commit or push them first (see [Pushing a branch](#pushing-a-branch)).

After a successful switch or create, every tab in the same `(worker, working-directory)` group is relabelled in the sidebar and the file browser's git status refreshes if it is the active repository.

## Deleting a branch

Open the branch row's **`...`** menu and choose **Delete branch...** to open the **Delete branch** dialog. While it inspects the branch it shows **Inspecting branch state**. The dialog always shows a [branch status block](#branch-status-indicators) and a sentence describing which tabs are affected. The primary action is the red **Delete branch** button; there is also a **Cancel** button and, when there is pushable work, a [Push](#pushing-a-branch) button.

Deletion behaves differently depending on whether the branch is a linked worktree.

### Deleting a linked worktree

There is no "switch to" picker. The status block notes that the group's tabs *will be stopped*. Clicking **Delete branch** closes **every** tab in the group and asks the Worker to remove the worktree. The Worker reference-counts the worktree and, when the last tab referencing it closes, runs `git worktree remove`, deletes the branch (if no other worktree uses it), and removes its tracking record.

You will see one of these outcomes as a toast:

| Outcome | Toast |
|---|---|
| Worktree removed | **Worktree removed** |
| Still open elsewhere | **Tabs closed; worktree still in use elsewhere** |
| Could not confirm removal | **Tabs closed; could not confirm worktree removal** |
| Worktree was not tracked by LeapMux | **Tabs closed (worktree was not tracked)** |
| Tracked but nothing removed | **Tabs closed; worktree not removed** |

> **Note:** A worktree created outside LeapMux (a raw `git worktree add`) has no tracking record. LeapMux closes the tabs but leaves the directory on disk for you to remove manually.

### Deleting a regular branch

For a branch in the main checkout, you must tell LeapMux where to leave HEAD. The dialog shows **Switch this working directory to:** and a branch selector listing every branch except the one being deleted. On **Delete branch**, the Worker checks out your chosen target, then force-deletes the doomed branch. Tabs keep running on the switched-to branch. Success toast: **Branch deleted**.

If the branch you are deleting is the **only** branch, the selector is replaced by the error **Cannot delete the only branch. Create another branch first.** and the button stays disabled.

> **Warning:** Branch deletion is a force-delete (`git branch -D`). Unmerged commits on the deleted branch that have not been pushed are gone. If the status block shows unpushed commits, push first.

## Pushing a branch

Both the Delete branch dialog and the close-last-tab confirmation surface a push control whenever the branch has pushable work and a pushable (agent or terminal) tab exists. The button label adapts:

| Branch state | Button label |
|---|---|
| Has uncommitted changes | **Commit and Push** |
| Clean working copy, but unpushed commits or no remote branch | **Push** |

**Commit and Push** stages everything (`git add -A`) and makes a `WIP` commit before pushing. **Push** just pushes. If the branch has no upstream yet, LeapMux sets one up (`git push -u origin <branch>`). The push is bounded at 60 seconds.

| Result | Toast |
|---|---|
| Success | **Branch pushed successfully** |
| Failure | **Failed to push branch** |

Pushing requires an `origin` remote and a real branch name — a detached HEAD cannot be pushed. If there is no remote, the push is refused.

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

> **Note:** These dialogs report unpushed *commit counts* rather than git's ahead/behind numbers. The ahead/behind figures feed the agent's own status indicator, not these dialogs.

## Dirty-worktree protection when closing tabs

Closing tabs is where you are most likely to lose work, so LeapMux guards the last tab of a worktree or branch. When you close the **last** tab of a worktree, or the last non-worktree tab on a branch that has uncommitted changes, unpushed commits, or a missing remote, the **Close last tab** dialog appears.

It tells you what you are closing:

- Worktree: *You are closing the last tab for worktree `path`.*
- Branch: *You are closing the last non-worktree tab for branch `name`.*

and shows the same [branch status block](#branch-status-indicators). Its buttons:

| Button | Effect |
|---|---|
| **Cancel** | Aborts the close — nothing happens. |
| **Push** / **Commit and Push** | Pushes your work first (shown only when there is pushable work). |
| **Delete** (worktree targets only) | Closes the tabs and schedules the worktree for removal (toast: **Worktree will be removed**). |
| **Close anyway** | Closes the tab(s) but keeps the worktree on disk. |

> **Warning:** **Close anyway** does not push or delete — it just closes the tab. Any uncommitted changes stay on disk in the worktree, but you lose the tab pointing at it. Use **Push** / **Commit and Push** first if the status block shows work you care about.

> **Note:** Worktree removal is always tied to closing tabs — there is no separate "remove worktree" command. A worktree is removed only when its last tab closes with **Delete**. If the Worker is unreachable when you close, LeapMux skips the dialog and removes the tab locally with the toast *Worker is unreachable; removing the tab without closing it.*

## Where git operations run

Every git command runs on the **Worker** that owns the working directory — the machine where your repository actually lives — not in your browser and not on the Hub. Worker git commands run with a fixed English/C locale and with terminal prompts disabled, so they never block waiting for credentials. Because of this, all the branch and worktree state you see (branches, worktrees, diff stats, ahead/behind) is computed remotely and streamed back over the end-to-end-encrypted Worker channel.

For more on workers and how they are selected, see [Managing Workers](/docs/operating/managing-workers/). For the run modes that host workers, see [Running LeapMux](/docs/operating/running-leapmux/).

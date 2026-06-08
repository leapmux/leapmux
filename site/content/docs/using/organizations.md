---
title: "Organizations & Members"
description: "What an organization is in LeapMux, how it appears in the URL, the Owner, Admin, and Member roles and what each can do, and how to manage members and orgs."
type: docs
weight: 2
---

An **organization** (org) is the top-level tenant boundary in LeapMux. Every account belongs to at least one org, and everything you do — workspaces, Workers, members, presence — lives inside an org. This chapter explains what an org is, how it shows up in the URL, how to create one, the three roles and exactly what each can do, how to manage members, and how to switch between orgs.

> **Note:** Organization management is only available in **Hub / multi-user mode**. In solo mode the org-switcher, the Profile menu item, and the Organization Settings page are all hidden or disabled — a solo install runs as a single built-in `solo` org. See [Running LeapMux](/docs/operating/running-leapmux/) for the difference between solo and distributed deployments.

## What an organization is

An org groups users and the workspaces they share. Key facts:

- **Multi-tenant by design.** A user can belong to several orgs at once, each with its own members, workspaces, and Workers.
- **Personal org for everyone.** When your account is created, LeapMux automatically creates a **personal** org named after your username, and makes you its **Owner**. This is your default home org. See [Accounts & Authentication](/docs/using/accounts/) for account creation.
- **Team orgs are explicit.** Beyond your personal org, you create additional **team** orgs to collaborate with others.
- **The org name is a slug.** Each org is identified by a globally unique, GitHub-style slug (lowercase letters, digits, and hyphens). That slug is the org's name; there is no separate display name.

For how orgs fit into the broader architecture (Hub, Worker, Frontend, workspaces, tiles), see [Concepts](/docs/getting-started/concepts/).

## Organizations in the URL

The whole application is namespaced under the org slug:

```
/o/<orgSlug>
```

For example, the dashboard for the `acme` org is `/o/acme`, and an individual workspace is `/o/acme/workspace/<workspaceId>`. The current org is determined purely by the slug in the URL — LeapMux matches it against the list of orgs you belong to. If the slug does not match any org you are a member of, you get a "not found" page with a link to go back to the dashboard.

When you log in, LeapMux sends you to your personal org's dashboard at `/o/<yourUsername>`.

## Roles: Owner, Admin, Member

Every membership carries exactly one of three roles. The role gates which org-management actions you can perform. In short: an **Owner** has full control, including deleting the org (new orgs start with their creator as the sole Owner); an **Admin** handles day-to-day administration — rename, manage members and roles — but cannot delete the org; and a **Member** participates in and views the org, but cannot administer it. The permission matrix below is the authoritative reference for what each role can do.

### Permission matrix

The following table lists exactly what each role can do. (Internally, the gate for administrative actions passes only for Owner or Admin; deletion is further restricted to Owner.)

| Action | Member | Admin | Owner |
| --- | :---: | :---: | :---: |
| View org details | Yes | Yes | Yes |
| List org members | Yes | Yes | Yes |
| List the orgs you belong to | Yes | Yes | Yes |
| Open / work inside the org | Yes | Yes | Yes |
| Create a brand-new org (becoming its Owner) | Yes | Yes | Yes |
| Rename the org | No | Yes | Yes |
| Invite (add) members | No | Yes | Yes |
| Remove members | No | Yes | Yes |
| Change member roles (incl. promote to Owner) | No | Yes | Yes |
| Delete the org | No | No | Yes |

Notes on the matrix:

- **Any member can create a new org.** Creating an org is not an administrative action against an existing org — it makes a fresh org with you as its sole Owner. So even a plain Member can spin up their own team org.
- **Admins can promote to Owner.** An Admin can raise another member all the way to Owner and can demote others.
- **Only Owners delete.** Deleting an org requires Owner role on that org. (A system-wide platform administrator can also delete an org regardless of role; that operator-level flag is separate from org roles and is covered in [Admin CLI](/docs/operating/admin-cli/).)

If a Member attempts an admin-only action, LeapMux returns a permission-denied error ("insufficient permissions"). If you are not a member of the org at all, it returns "not a member of this organization".

### Last-owner protection

To avoid orphaning an org, LeapMux refuses to leave an org with zero Owners:

- **Removing the last Owner** is rejected with "cannot remove the last owner".
- **Demoting the last Owner** (changing the only Owner's role to something other than Owner) is rejected with "cannot change role of the last owner".

To remove or demote the current sole Owner, first promote a second member to Owner.

## Creating an organization

You create a team org from the [Organization Settings page](#the-organization-settings-page) (the **Create Organization** card). Enter a name and click **Create**. On success you see a confirmation like `Organization "acme" created.`, and you become the new org's Owner.

### Slug rules

The org name is validated as a slug before it is created. The same rules apply when you rename an org.

| Rule | Detail |
| --- | --- |
| Length | 1 to 32 characters |
| Allowed characters | lowercase letters, digits, and hyphens (`a`-`z`, `0`-`9`, `-`) |
| Case | input is trimmed and lowercased automatically |
| Leading hyphen | not allowed |
| Trailing hyphen | not allowed |
| Consecutive hyphens | `--` not allowed |
| Uniqueness | must not already be taken by another (non-deleted) org |

If a name breaks one of these rules, creation (or rename) fails with a message naming the offending constraint (for example, that the name must not contain consecutive hyphens). If the name is already in use, creation fails with `organization name "<name>" is already taken`.

> **Tip:** Because input is lowercased and trimmed, `Acme ` and `acme` resolve to the same slug `acme`. Pick a short, hyphenated name like `acme-platform`.

## The Organization Settings page

Org administration happens on the **Organization Settings** page, at:

```
/o/<orgSlug>/org
```

> **Warning:** There is currently no in-app button or menu item that links to this page. You reach it by typing the URL directly — replace `<orgSlug>` with your org's slug (for example, `/o/acme/org`). The user menu offers org *switching* only, not a link to settings. In solo mode this page immediately redirects you back to the dashboard.

The page header has a `<- Dashboard` back link (to `/o/<orgSlug>`) and a title, **Organization Settings**. It is divided into the following cards.

### General

Shows the org's **Name**.

- For a **team org**, the name is an editable text input with a **Save** button. Save is disabled while the action is in progress (and when there is nothing to save). On success you see "Organization name updated." Saving runs the new name through the slug rules above.
- For a **personal org**, the name is shown read-only, plus a **Type** row showing **Personal**. Personal orgs cannot be renamed; the backend rejects the attempt with "cannot update a personal organization".

### Members

Lists the org's members and lets Owners/Admins manage them.

**Invite form** (top of the card):

- **Username** text input.
- A role `<select>` with options **Member** / **Admin** / **Owner** (default **Member**).
- An **Invite** button (disabled while the invite is in progress). On success you see a message naming the user and the role you chose, like "Invited alice as member." (or "...as admin." / "...as owner." if you picked a higher role).

**Members table** with columns:

| Column | Notes |
| --- | --- |
| Username | the member's login name |
| Display Name | falls back to `-` when not set |
| Role | a per-row `<select>` (Member / Admin / Owner) that changes the role on change |
| Joined | the date the member joined |
| Actions | a **Remove** button (danger styling) |

The role `<select>` and **Remove** button are **disabled for your own row**, so you cannot accidentally demote or remove yourself from the settings page.

The table shows a placeholder while members load and a message when the org has no members to display.

### Create Organization

An **Organization name** text input plus a **Create** button (disabled while the action is in progress). This is the card you use to create new team orgs (see [Creating an organization](#creating-an-organization)).

### Danger Zone

Shown only for team orgs (never for personal orgs). It explains: "Permanently delete this organization and all its data. This action cannot be undone." and offers a **Delete Organization** button (danger styling, disabled while the action is in progress).

### Confirmation dialogs

Destructive actions prompt for confirmation:

- **Remove a member** — title "Remove member", confirm button "Remove" (danger), body "Remove `<username>` from this organization?"
- **Delete the org** — title "Delete organization", confirm button "Delete" (danger), body `Are you sure you want to delete "<name>"? This cannot be undone.`

## Adding, changing, and removing members

All member management is performed from the **Members** card by an Owner or Admin.

### Adding (inviting) a member

LeapMux's "invite" adds an **existing LeapMux user** to the org directly — there is **no email invitation flow**. The person you add must already have a LeapMux account.

1. In the invite form, type the user's **Username**.
2. Choose a role (Member / Admin / Owner).
3. Click **Invite**.

If no user with that username exists, you get "user not found". If you omit a role, the new member is added as a Member. Re-inviting someone who is already a member fails (the operation errors out rather than showing a friendly "already a member" message); use their existing row's **Role** `<select>` to change their role instead.

### Changing a member's role

Use the **Role** `<select>` on the member's row. Changing the value immediately applies the new role. Admins and Owners can promote (including to Owner) and demote, subject to the [last-owner protection](#last-owner-protection).

### Removing a member

Click **Remove** on the member's row and confirm. Removing a member also **revokes that user's Worker-access grants in this org** (their access grants in *other* orgs are preserved). For more on Worker access, see [Managing Workers](/docs/operating/managing-workers/).

> **Note:** The disabled self-row controls are a UI safeguard, not the only guard. The last-owner rule still applies at the backend: the sole Owner cannot be removed or demoted by anyone.

## Switching between organizations

The org you are working in is whatever org slug is in the URL. To switch:

1. Open the **user menu** (your avatar/account menu in the app shell).
2. Under the **Switch Organization** section, click the org you want.

Each org you belong to appears as a button that navigates to `/o/<orgName>`. The currently active org is highlighted, and **personal orgs show a `(personal)` tag** next to their name. This section — along with the **Log out** action — is hidden in solo mode.

The org list in this menu is sorted by org name.

## How workspace access relates to org membership

This is the single most important thing to understand about orgs and sharing: **org membership alone does not grant access to any workspace.** Being a Member, Admin, or even Owner of an org does **not** let you see other people's workspaces in that org.

Workspace access is layered separately on top of org membership:

- Every workspace has a **single owner** — the user who created it.
- A user can read a workspace **only if** they are its owner **or** they have an explicit per-user share grant for that workspace.
- Shared workspaces appear in the recipient's **Shared** section in the sidebar.

In other words, org membership defines *who can be granted access*; the workspace owner decides *who actually gets it*, one user at a time. The workspace owner manages this from the **Workspace sharing** dialog. See [Workspaces](/docs/using/workspaces/) for the full sharing workflow and [Collaboration & Presence](/docs/using/collaboration/) for what live state syncs between collaborators.

> **Warning:** The Workspace sharing dialog currently offers three options — **Private**, **All org members**, and **Specific members** — but the **All org members** option is not implemented server-side and saving it fails with "invalid share mode". Use **Private** or **Specific members**. To share with several members today, choose **Specific members** and check each user individually.

> **Note:** A share grant authorizes the Hub to *route* a recipient's traffic to the workspace's Workers; it does not by itself decrypt anything. To actually read agent and terminal content, the recipient still opens their own end-to-end-encrypted channel to each Worker. The Hub is a blind relay and never sees plaintext. See [Security & Threat Model](/docs/operating/security/).

## Operator and CLI notes

There is no full org-management surface in the CLI; member and role management happen through the web app. The only org-related admin CLI command is read-only:

```bash
leapmux admin org list
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--query` | `""` | prefix match on org name |
| `--limit` | `50` | maximum rows to return |
| `--cursor` | `""` | pagination cursor (a `created_at` timestamp) |

The output columns are `ID`, `NAME`, `PERSONAL` (yes/no), and `CREATED`; it prints "No organizations found." when there are none. There is **no** admin CLI command to create or delete orgs or to manage membership and roles — use the web app for those. See [Admin CLI](/docs/operating/admin-cli/) for the complete reference.

## Related chapters

- [Accounts & Authentication](/docs/using/accounts/) — sign up, log in, profile, and personal-org creation.
- [Workspaces](/docs/using/workspaces/) — creating, sharing, and managing workspaces.
- [Collaboration & Presence](/docs/using/collaboration/) — what syncs live between collaborators.
- [Managing Workers](/docs/operating/managing-workers/) — Worker access grants (revoked on member removal).
- [Running LeapMux](/docs/operating/running-leapmux/) — solo vs distributed mode.
- [Security & Threat Model](/docs/operating/security/) — the routing-vs-reading distinction and E2EE.
- [Admin CLI](/docs/operating/admin-cli/) — the read-only `org list` command and operator-level deletion.

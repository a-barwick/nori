# Providers: Microsoft, iCloud, HEY (backend)

Branch `claude/nori-assistant-backend-9c520e`. None of the three has been run against a real account. Each needs the credential named in its section.

| Provider | Commit | Auth | Built | Blocked on |
| --- | --- | --- | --- | --- |
| Microsoft (Outlook) | 9acf840 | OAuth, Microsoft identity platform v2, `common` endpoint | calendar (list, events, get, free/busy, create, update, cancel), mail (search, read, draft, send draft, organize), contacts and people search | `MICROSOFT_CLIENT_ID`, `MICROSOFT_CLIENT_SECRET` from an Entra app registration for "any organizational directory and personal Microsoft accounts", with `NORI_OAUTH_REDIRECT_URL` as a Web redirect URI |
| iCloud Calendar | 46608a2 (+ b765587 for the connect path) | Apple ID + app-specific password (no OAuth exists) | calendar list, events (server-expanded), create, delete | A test Apple ID with two-factor authentication and an app-specific password |
| HEY | none | HEY OAuth with PKCE at app.hey.com | nothing | A HEY OAuth client registered for Nori by 37signals (see below) |

## Microsoft: personal vs work or school accounts

- One integration serves both. After sign-in, the account label ends "(personal)" or "(work or school)". The check is `GET /organization`, which only Entra accounts can read.
- Free/busy for other people (`getSchedule`) is its own capability, `calendar.availability`. Its stated reason is that only work or school accounts have it. For a personal account the call is refused before it runs.
- In some work tenants an admin must consent before users can grant these permissions. The sign-in then fails with the provider's admin-consent error.
- Microsoft offers no way to revoke a single app's delegated refresh token. Disconnect deletes Nori's copy, and the user removes the grant at account.live.com/consent/Manage (personal) or myapps.microsoft.com (work or school).
- Mail semantics differ from Gmail: a message lives in one folder (archiving is a move), Outlook has categories instead of labels, and flags exist. Sending returns 202, so it is recorded as `submitted`.
- Interrupted creates (events, drafts) are found again by an extended-property key and never repeated. Event changes carry the etag read at preparation.

## iCloud: app-specific password lifecycle

- Connect and repair take `username` and `password` in the connection action. These are proposed contract additions: write-only, never in receipts or projections. The pair is verified by finding the CalDAV principal; a 401 comes back as "rejected password" (400 validation_failed).
- When the user revokes that app-specific password, or changes the Apple ID password (which revokes every app-specific password), CalDAV returns 401. The connection then needs sign-in, and repair takes a new app-specific password. Repair refuses credentials that belong to a different principal than the connection's.
- Credentials go only to https `*.icloud.com` hosts, including across discovery redirects.
- iCloud Mail is IMAP-only and iCloud Contacts would need CardDAV. Neither is built; both are listed as unsupported capabilities.

## HEY: supported path and the gap

Source: the official 37signals HEY CLI (basecamp/hey-cli 1.2.1, cached at `~/.claude/plugins/cache/37signals/hey/1.2.1`), its `API-COVERAGE.md`, and `internal/auth/auth.go`.

- HEY has a JSON API at app.hey.com covering mail boxes, threads, search, drafts, labels (folders), collections, the Screener, contacts and calendars. The official Go SDK is `github.com/basecamp/hey-sdk/go` (v0.28.0 in the CLI's go.mod).
- Auth is HEY's own OAuth server with PKCE: `/oauth/authorizations/new` and `/oauth/tokens`. The CLI uses a public client ID registered for itself, with a fixed loopback redirect (`127.0.0.1:8976/callback`).
- **The gap:** I found no published way for a third-party app to register its own HEY OAuth client. Reusing the CLI's client ID would impersonate the CLI, so Nori must not.
- **Supported path:** 37signals registers a Nori OAuth client for Nori's redirect URL. Then a native HEY connector on hey-sdk/go fits the existing OAuth integration shape.
- Exact credential needed: a HEY OAuth client ID (and secret, if HEY issues confidential clients) registered for `NORI_OAUTH_REDIRECT_URL`.
- Gmail is not a stand-in for HEY.
- A personal-only interim exists but is not built: the user runs the HEY CLI themselves and exposes it through a local stdio MCP server declared with `NORI_MCP_COMMANDS` (local stack only). It would need an MCP wrapper around the CLI and is not a product path.

## W8: connectors through MCP packages (bf2efb2)

Nori's MCP client supports `none` and `configured` auth (a bearer token stored per server). MCP OAuth sign-in is not built yet. The SDK's interactive handler does not fit the web callback flow, and most providers below lack dynamic client registration anyway.

Research came from each provider's own pages (late 2026). Points marked "unconfirmed" come only from search summaries.

| Provider | First-party remote server | Auth usable by Nori now | Capabilities (read / write) | Preconditions | Packaged | Source |
| --- | --- | --- | --- | --- | --- | --- |
| Linear | `https://mcp.linear.app/mcp` (read-only: `/mcp/readonly`) | Yes: Linear API key as bearer. OAuth 2.1 with dynamic client registration also exists. | Find issues, projects, comments / create and update them | Workspace account | `linear/mcp` | linear.app/docs/mcp |
| GitHub | `https://api.githubcopilot.com/mcp/` (toolsets: `/mcp/x/{toolset}`; read-only: `/readonly`) | Yes: fine-grained PAT as bearer | Repos, issues, PRs, Actions / per toolset | Copilot Business and Enterprise orgs must allow MCP servers. Enterprise Managed Users have PATs off by default. | `github/mcp` | github/github-mcp-server docs/remote-server.md; docs.github.com |
| Notion | `https://mcp.notion.com/mcp` | Business and Enterprise only: an MCP token from a workspace owner | Search / create and edit pages, manage databases | Members only, not guests. Admins can restrict clients. | `notion/mcp` | developers.notion.com/docs/mcp |
| Slack | `https://mcp.slack.com/mcp` | No. Needs a confidential OAuth app (own client ID and secret); no dynamic client registration. | Search messages, files, users, channels; read history / send, schedule, draft messages; reactions; canvases | App must be internal or in the Marketplace, and the workspace admin approves it | No | docs.slack.dev/ai/mcp-server |
| Google Drive | `https://drivemcp.googleapis.com/mcp/v1` | No. Needs an OAuth client from your own Cloud project (`drive.readonly`, `drive.file`). | Search, list, metadata, permissions, read and download / create and copy files | Developer Preview Program membership; Drive API and Drive MCP API enabled | No | developers.google.com/workspace/drive/api/guides/configure-mcp-server |
| Dropbox | `https://mcp.dropbox.com/mcp` | No. Dynamic client registration is only for Dropbox's listed clients; others need their own Dropbox app. | List, search, read, metadata, links, revisions / create, copy, move, delete, share, restore | Beta. Team admins may block app registration. | No | help.dropbox.com/integrations/connect-dropbox-mcp-server |
| Microsoft Teams | Agent 365: `https://agent365.svc.cloud.microsoft/agents/tenants/{tenantId}/servers/mcp_TeamsServer` | No. Entra, scope `McpServers.Teams.All`; the generic-client flow is unconfirmed. | List and get chats and messages / post, reply, create and update chats, channels, members | Preview. Microsoft marks app-specific M365 MCP tools as backward-compatibility only and points to Work IQ. A Copilot license requirement is unconfirmed. | No | learn.microsoft.com/microsoft-agent-365/mcp-server-reference/teams |
| OneDrive | Agent 365: `.../servers/mcp_OneDriveRemoteServer` | No (same as Teams) | Find, list, metadata, read small files / create, rename, delete, copy, move, share (5 MB limit) | Preview; same caveat as Teams. Frontier access and tenant admin control are unconfirmed. | No | learn.microsoft.com/microsoft-agent-365/mcp-server-reference/onedrive |

What unblocks the rest:
1. MCP OAuth in the MCP client (authorization code with PKCE, a `resource` parameter, refresh), using a pre-registered client per provider.
2. Registered apps:
   - Slack: an internal app approved by the workspace admin.
   - Google: a Cloud OAuth client plus Developer Preview Program membership.
   - Dropbox: a Dropbox app.
   - Teams and OneDrive: an Entra app registration with Agent 365 access. Microsoft's own steer is toward Work IQ rather than app-specific servers.

OneDrive mail and calendar through Graph are already covered natively by the Microsoft connector (9acf840). OneDrive files are not.

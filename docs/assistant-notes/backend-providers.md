# Providers: Microsoft, iCloud, HEY (backend)

Branch `claude/nori-assistant-backend-9c520e`. None of the three has been run against a real account. Each needs the credential named in its section.

| Provider | Commit | Auth | Built | Blocked on |
| --- | --- | --- | --- | --- |
| Microsoft (Outlook) | 9acf840 | OAuth, Microsoft identity platform v2, `common` endpoint | calendar (list, events, get, free/busy, create, update, cancel), mail (search, read, draft, send draft, organize), contacts and people search | `MICROSOFT_CLIENT_ID`, `MICROSOFT_CLIENT_SECRET` from an Entra app registration for "any organizational directory and personal Microsoft accounts", with `NORI_OAUTH_REDIRECT_URL` as a Web redirect URI |
| iCloud Calendar | 46608a2 (+ b765587 for the connect path) | Apple ID + app-specific password (no OAuth exists) | calendar list, events (server-expanded), create, delete | A test Apple ID with two-factor authentication and an app-specific password |
| HEY | none | HEY OAuth 2 at app.hey.com (PKCE, device flow; scopes read, write, send) | nothing (no connector, by instruction) | A client Nori may use: third-party registration is advertised but switched off (checked 2026-11-10, see below) |

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

## HEY: checked 2026-11-10 (replaces the first run's assessment)

The first run read a cached copy of the HEY CLI 1.2.1 on the old laptop. This section is re-checked from public sources on 2026-11-10, on the new machine. Nothing was signed in to, no HEY client id was used, nothing was sent to `/oauth/clients`.

### On this machine

- No `hey` binary on the PATH, no HEY app in `/Applications`, no HEY or 37signals plugin or marketplace under `~/.claude/plugins` (only `claude-plugins-official`). The CLI is not installed here.

### Public sources (read 2026-11-10)

| Fact | Source |
| --- | --- |
| HEY CLI latest is v1.13.1, published 2026-11-10 01:58 UTC (1.2.1 was the first run's copy). | `gh api repos/basecamp/hey-cli/releases/latest` |
| The CLI signs in with OAuth + PKCE against `app.hey.com/oauth/authorizations/new` and `/oauth/tokens`, loopback redirect on `127.0.0.1:<ephemeral port>`, with a client id hard-coded for the CLI (`internal/auth/auth.go:20`, `oauthClientID`). It also accepts a pasted token (`hey auth login --token`, `HEY_TOKEN`) or a web session cookie. Tokens go to the OS keyring. | github.com/basecamp/hey-cli at v1.13.1: `internal/auth/auth.go`, `docs/architecture/auth-and-setup.md` |
| `hey auth login --mcp` uses a second first-party client, "HEY MCP (local)", scopes `read write send`, device flow where available. | `internal/config/mcp.go`, `internal/auth/mcp.go` at v1.13.1 (as reported by the research agent; not re-read line by line) |
| Read-only commands: `hey box list`, `hey box view <imbox\|feedbox\|…>`, `hey thread read <id>`, `hey search`, `hey label list/view`, `hey collection list/view`, `hey screener list`, `hey contact list/show`, `hey draft list/show`, `hey attachment list/save`, `hey account list`. `hey mcp --read-only` drops write tools. | hey-cli `README.md`, `docs/cli.md`, `API-COVERAGE.md`, `docs/agents.md` at v1.13.1 |
| API coverage is JSON on app.hey.com: boxes, topics and entries (cursor paging in the `Link` header), messages, advanced search, folders/labels, collections, Screener clearances, contacts, calendars, identity, a changes feed; writes are postings, drafts and sending. | hey-cli `API-COVERAGE.md` at v1.13.1 |
| Go SDK `github.com/basecamp/hey-sdk/go`, latest v0.35.0 (2026-11-08). Auth: any bearer token (`StaticTokenProvider`) or OAuth with PKCE and refresh; RFC 8414 discovery. Default client id is the CLI's. No personal access token feature is described. | github.com/basecamp/hey-sdk README at v0.35.0; `gh api repos/basecamp/hey-sdk/releases/latest` |
| HEY's live OAuth metadata lists `registration_endpoint: https://app.hey.com/oauth/clients`, `client_id_metadata_document_supported: true`, scopes `read`, `write`, `send`, a device authorization endpoint, and client auth `none`, `client_secret_basic`, `client_secret_post`. | `GET https://app.hey.com/.well-known/oauth-authorization-server`, fetched 2026-11-10 by this session |
| The SDK maintainers describe `POST /oauth/clients` as RFC 7591 dynamic registration "for third-party connectors", answering a bare 404 "while switched off". Merged 2026-11-08. | github.com/basecamp/hey-sdk/pull/300, read by this session with `gh api` |
| A hosted HEY MCP server exists at `https://mcp.hey.com/mcp` (GET answers 401 with an OAuth protected-resource challenge). hey.com/ai describes it as a preview for 37signals staff and invited reviewers, "coming to every HEY account", no date. | `GET https://mcp.hey.com/mcp` (401, this session); www.hey.com/ai (research agent) |
| No developer portal or third-party registration page: `hey.com/api` and `hey.com/developers` are 404; dev.37signals.com has nothing on a HEY API; no GitHub issue asks for third-party clients (Discussions are off on both repos). | research agent, 2026-11-10 |
| No IMAP or POP. HEY offers an MBOX export (email), vCard (contacts), .ics (calendars), and forwarding of all non-spam mail to another address. | help.hey.com articles 718 (2025-07-24), 730 (2025-08-01), 1055 (2026-03-27), research agent |
| The official Claude Code plugin `hey` is in the 37signals marketplace (`basecamp/plugins`), sourced from hey-cli; it ships a skill and a session hook that drive the CLI, no MCP server. | github.com/basecamp/plugins `.claude-plugin/marketplace.json`, research agent |

### What this means for Nori (backend's reading, not a decision)

- **Still no client Nori may use today.** Every working way in belongs to 37signals' own clients (the CLI id, the "HEY MCP (local)" id) or is a pasted token or cookie. Reusing those would impersonate HEY's software, so Nori must not.
- **Changed since the first run:** HEY now advertises dynamic client registration and client-ID metadata documents, and its SDK calls registration a path for "third-party connectors" that is switched off for now. When it is switched on, Nori could register its own client (or publish a client metadata document at a Nori URL) with the `read` scope, and a native connector on hey-sdk/go would fit the existing OAuth integration shape. The hosted MCP at mcp.hey.com is the other likely route once it leaves preview: Nori's MCP client already does OAuth with discovery and dynamic registration (c6cbeec), so it may work without new code, but only once HEY opens registration to outside clients.
- Not tried, because each would mean sending a request on Austin's behalf or registering a client without his say: a `POST /oauth/clients`, an authorize request with a client metadata document URL.
- **Interim, no new code:** HEY can forward all non-spam mail to an address Nori already reads (for example his Google account), which would put HEY mail on the Google path the first slice is proving. That is Austin's choice and changes his mail setup.
- No HEY connector and no MCP wrapper is built (instruction of 2026-11-10).

### The cheapest honest check against Austin's account (for Austin to run himself)

This tells us whether his HEY account works with HEY's own tooling and which scopes and boxes it exposes. It uses 37signals' client, on his machine, signed in by him, read-only. Nori is not involved and no session sees the token.

1. Install the official CLI, for example `brew install --cask basecamp/tap/hey` or `go install github.com/basecamp/hey-cli/cmd/hey@v1.13.1` (hey-cli `docs/install.md`). `hey doctor` checks the install.
2. Run `hey auth login` and complete the sign-in in the browser HEY opens.
3. Run, and note only whether each works and roughly what it lists (no message contents needed): `hey account list`, `hey box list`, `hey box view imbox`, `hey search "<a word you know is in a recent email>"`.
4. `hey auth status` shows the sign-in state.
5. Afterwards, if he does not want to keep it: `hey auth logout`, and remove the CLI's access under HEY's Connected Apps.
6. Write the results (works / fails with what message) in his reply or have a session record them in the coordinator's notes.

This proves his account reachable through HEY's API, which no recording can. It does not give Nori access; that still waits on HEY opening third-party registration or the hosted MCP, or on forwarding.

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

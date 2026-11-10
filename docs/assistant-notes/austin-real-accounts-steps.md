# Connecting your real accounts to local Nori

Everything is set up except the parts only you can do: signing in, approving access, and one Microsoft app registration. Nori runs on your Mac at **http://127.0.0.1:3213**, with its own database and an encrypted local credential vault. Production is not touched.

## Google (about 2 minutes)

1. Open http://127.0.0.1:3213/apps, then Google → Connect.
2. Choose your Google address and approve the access shown.
   - If Google says **"Access blocked: Nori has not completed the Google verification process"**, open Google Cloud Console for the project that owns the Nori OAuth client (client ID starting `8273801370-`). Go to **Google Auth Platform → Audience → Test users → Add users**, add your Google address, save, and repeat step 1.
   - If it says **"Google hasn't verified this app"**, choose Continue. It's your own app in testing.
3. Create a calendar just for these checks: calendar.google.com → Other calendars **+** → Create new calendar, named **Nori test**. Nori writes only there.

Checked before writing this: Google accepts the OAuth client and the redirect URI (the consent page shows "Choose an account to continue to Nori").

## iCloud (about 2 minutes)

1. At account.apple.com → Sign-In and Security → **App-Specific Passwords**, generate one named "Nori local".
2. In Nori, go to Apps → iCloud Calendar → Connect. Enter your Apple ID and that password.

The password goes straight into Nori's local vault. Nori never shows it again, and no one else sees it.

## Microsoft (about 5 minutes, one time)

1. Sign in at **entra.microsoft.com** with your Microsoft address.
   - If it asks you to create a directory or an Azure account first, a free one is enough.
2. Go to **Applications → App registrations → New registration**:
   - Name: Nori local.
   - Supported account types: **Personal Microsoft accounts only**.
   - Redirect URI: platform **Web**, address **http://127.0.0.1:3213/api/v1/connections/oauth/callback**.
   - Click Register.
3. On the app's Overview, copy **Application (client) ID**.
4. Go to **Certificates & secrets → New client secret** (24 months) and copy its **Value**. It is shown once.
5. Add three lines to `/Users/austinbarwick/Documents/dev/agents/nori/.env`:
   - `MICROSOFT_CLIENT_ID=<the client ID>`
   - `MICROSOFT_CLIENT_SECRET=<the secret value>`
   - `MICROSOFT_TENANT=consumers`

   Then tell me; I restart the local core.
6. In Nori, go to Apps → Microsoft → Connect, sign in, and approve.

Nori asks Microsoft for these delegated permissions: openid, email, offline_access, User.Read, Calendars.Read, Calendars.ReadWrite, Mail.Read, Mail.ReadWrite, Mail.Send, Contacts.Read, People.Read. You consent at sign-in; nothing needs adding under API permissions.

If the portal refuses `http://127.0.0.1` as a Web redirect URI, tell me. Nori's redirect address would then move to `http://localhost:3213/...` for all providers.

## Rules for the real runs

- Writes go only to the **Nori test** calendar. Each is reversible, and integration deletes what it made at the end.
- Email goes only to your Google address. Every send waits for an approval you click yourself.
- Rows are marked "provider verified" only for what actually ran against your account.
- After you sign in, integration runs A01–A03, A15–A23 and A27 against Google, iCloud and Microsoft.

## Turning it off

Disconnect each account in Apps. Google also lists Nori at myaccount.google.com/connections, and Microsoft at account.live.com/consent/Manage.

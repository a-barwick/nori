import type { Metadata } from "next";
import { LegalDocument } from "./LegalDocument";

export const metadata: Metadata = {
  title: "Privacy Policy · Nori",
  description: "How Nori collects, uses, protects, and deletes your data.",
};

export default function PrivacyPage() {
  return (
    <LegalDocument
      title="Privacy Policy"
      effectiveDate="Effective November 2026"
      companionHref="/terms"
      companionLabel="Terms of Service"
      sections={[
        {
          heading: "Information Nori collects",
          body: (
            <>
              <p>
                When you sign in to Nori, we store your email address, display name, and sign-in account identifier.
                Inside Nori, we store your timezone, preferences, standing permissions, conversations, memories,
                tasks, routines, uploaded files, and metadata for apps or MCP servers you connect.
              </p>
              <p>
                If you bring your own model API keys (BYOK), your keys are encrypted at rest using Google Cloud KMS
                and used solely to authenticate requests to the AI providers you select.
              </p>
            </>
          ),
        },
        {
          heading: "Third-party model providers and connected apps",
          body: (
            <p>
              When you message Nori or run tasks, the relevant conversation context, files, and tool outputs are sent
              to the third-party AI model providers you choose (such as OpenAI, Anthropic, Google, Vercel AI Gateway, or
              OpenRouter) and to any external apps or MCP servers you authorize. Those services handle data under their
              own privacy policies and terms.
            </p>
          ),
        },
        {
          heading: "Google user data",
          body: (
            <>
              <p>When you connect Google, Nori asks only for the access needed by the services you turn on:</p>
              <ul className="list-disc space-y-1 pl-5">
                <li>
                  <strong className="font-semibold text-ink">Drive:</strong> the files you choose in the Google file
                  picker and files Nori creates for you. Nori can&apos;t see the rest of your Drive.
                </li>
                <li>
                  <strong className="font-semibold text-ink">Calendar:</strong> reading your calendars and events, and
                  creating or changing events when you ask.
                </li>
                <li>
                  <strong className="font-semibold text-ink">Gmail:</strong> sending email you approve. Nori can&apos;t
                  read your inbox, and each send needs your approval of its recipients, body, and attachments.
                </li>
                <li>
                  <strong className="font-semibold text-ink">Account:</strong> your Google email address and account
                  identifier, so Nori can show which account is connected.
                </li>
              </ul>
              <p>
                Nori stores your Google access token encrypted in its database, along with the names and IDs of the files
                you choose. File contents and event details that Nori reads for a request become part of that
                conversation or task, and are sent to the AI model provider you selected so it can answer.
              </p>
              <p>
                Nori uses Google user data only to provide the features you request. Nori doesn&apos;t sell it, use it
                for advertising, or use it to train AI models. People at Nori don&apos;t view it unless the law
                requires it.
              </p>
              <p>
                You can disconnect Google from <strong className="font-semibold text-ink">More → Apps &amp; MCP</strong>.
                Nori then revokes its access with Google and deletes the stored token. You can also remove Nori&apos;s
                access from your{" "}
                <a href="https://myaccount.google.com/permissions" className="text-accent-text hover:underline">
                  Google Account permissions
                </a>
                .
              </p>
              <p>
                Nori&apos;s use and transfer of information received from Google APIs to any other app will adhere to the{" "}
                <a
                  href="https://developers.google.com/terms/api-services-user-data-policy"
                  className="text-accent-text hover:underline"
                >
                  Google API Services User Data Policy
                </a>
                , including the Limited Use requirements.
              </p>
            </>
          ),
        },
        {
          heading: "Telemetry and diagnostics",
          body: (
            <p>
              Nori uses Sentry for error reporting and Arize for operational traces so we can diagnose crashes and
              reliability issues. Credentials, API keys, authorization tokens, and sensitive message content are
              redacted before diagnostic telemetry is sent.
            </p>
          ),
        },
        {
          heading: "Data retention",
          body: (
            <p>
              Your conversations, memories, tasks, files, and settings remain stored while your account is active so
              Nori can maintain continuity across sessions. Operational logs and diagnostics are kept for a limited
              period to support security and reliability.
            </p>
          ),
        },
        {
          heading: "Resetting data and deleting your account",
          body: (
            <>
              <p>
                You can clear your conversations, memories, work history, uploaded files, schedules, preferences, saved
                API keys, and connected apps at any time from <strong className="font-semibold text-ink">Settings → Reset data</strong>.
              </p>
              <p>
                To permanently delete your Nori account and associated records, use account deletion in Settings or
                contact your workspace administrator.
              </p>
            </>
          ),
        },
      ]}
    />
  );
}

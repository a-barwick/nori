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

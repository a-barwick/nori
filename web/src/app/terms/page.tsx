import type { Metadata } from "next";
import Link from "next/link";
import { LegalDocument } from "../privacy/LegalDocument";

export const metadata: Metadata = {
  title: "Terms of Service · Nori",
  description: "Terms governing use of Nori.",
};

export default function TermsPage() {
  return (
    <LegalDocument
      title="Terms of Service"
      effectiveDate="Effective November 2026"
      companionHref="/privacy"
      companionLabel="Privacy Policy"
      sections={[
        {
          heading: "Using Nori",
          body: (
            <p>
              Nori is a personal assistant engine for managing conversations, tasks, routines, and connected tools. You
              must be authorized to access the workspace and are responsible for maintaining the security of your
              sign-in credentials and connected accounts.
            </p>
          ),
        },
        {
          heading: "Your content and connected services",
          body: (
            <p>
              You retain ownership of the messages, files, and instructions you provide to Nori. When you connect
              third-party apps, MCP servers, or bring-your-own model API keys (BYOK), you authorize Nori to send
              requests to those services on your behalf according to your standing permissions and approvals.
            </p>
          ),
        },
        {
          heading: "AI outputs and approvals",
          body: (
            <p>
              Responses and actions produced by third-party AI models may contain errors or omissions. Review proposed
              actions carefully before approving external sends, edits, or recurring routines, and set standing
              permissions and task budgets in Settings that fit your workflow.
            </p>
          ),
        },
        {
          heading: "Acceptable use",
          body: (
            <p>
              Use Nori lawfully and in compliance with the terms of your chosen AI model providers and connected
              services. Respect workspace boundaries and refrain from disrupting service availability or attempting
              unauthorized access to other accounts.
            </p>
          ),
        },
        {
          heading: "Data reset, account deletion, and availability",
          body: (
            <p>
              You can clear your workspace data at any time in{" "}
              <strong className="font-semibold text-ink">Settings → Reset data</strong> or request permanent account
              deletion. Nori is provided as-is and may be updated or modified over time to maintain reliability and
              security.
            </p>
          ),
        },
        {
          heading: "Privacy",
          body: (
            <p>
              Our{" "}
              <Link href="/privacy" className="text-accent-text hover:underline">
                Privacy Policy
              </Link>{" "}
              explains what data Nori collects, how third-party model providers and telemetry work, and how you control
              your data.
            </p>
          ),
        },
      ]}
    />
  );
}

import Link from "next/link";
import type { ReactNode } from "react";
import { NoriMark } from "@/components/mark/NoriMark";

export type LegalSection = {
  heading: string;
  body: ReactNode;
};

export function LegalDocument({
  title,
  effectiveDate,
  companionHref,
  companionLabel,
  sections,
}: {
  title: string;
  effectiveDate: string;
  companionHref: string;
  companionLabel: string;
  sections: LegalSection[];
}) {
  return (
    <div className="scrollbar-quiet h-full overflow-y-auto bg-canvas text-ink">
      <header className="nori-top-wash border-b border-hairline">
        <div className="nori-page-col flex h-[60px] items-center justify-between gap-4">
          <Link href="/" className="inline-flex items-center gap-2.5 text-section font-semibold text-ink">
            <NoriMark size={26} />
            <span>Nori</span>
          </Link>
          <nav aria-label="Legal navigation" className="flex items-center gap-5 text-meta font-medium">
            <Link href={companionHref} className="text-accent-text hover:underline">
              {companionLabel}
            </Link>
          </nav>
        </div>
      </header>

      <main className="nori-page-col py-10 sm:py-14">
        <article className="max-w-2xl space-y-8">
          <div className="space-y-2">
            <h1 className="text-[28px] leading-9 font-bold tracking-[-0.025em] text-ink sm:text-display">
              {title}
            </h1>
            <p className="text-meta text-ink-3">{effectiveDate}</p>
          </div>

          <div className="space-y-6 rounded-card border border-card-border bg-surface p-6 sm:p-8">
            {sections.map((section) => (
              <section key={section.heading} className="space-y-2">
                <h2 className="text-section font-semibold text-ink">{section.heading}</h2>
                <div className="space-y-2.5 text-read text-ink-2">{section.body}</div>
              </section>
            ))}
          </div>
        </article>
      </main>
    </div>
  );
}

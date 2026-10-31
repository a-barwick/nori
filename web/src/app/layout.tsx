import type { Metadata } from "next";
import { Figtree, IBM_Plex_Mono } from "next/font/google";
import { connection } from "next/server";
import { browserObservabilityConfig } from "@/lib/observability/config.server";
import "./globals.css";

const figtree = Figtree({
  variable: "--font-figtree",
  subsets: ["latin"],
  display: "swap",
});

const plexMono = IBM_Plex_Mono({
  variable: "--font-plex-mono",
  subsets: ["latin"],
  weight: ["400", "500"],
  display: "swap",
});

export const metadata: Metadata = {
  title: "Nori · Nova",
  description: "Nori, the assistant inside Nova. One conversation, everything moving forward.",
};

// Runtime configuration must arrive before browser instrumentation and the
// interactive app, so this document cannot use a prerendered shell.
export const instant = false;

export default async function RootLayout({ children }: LayoutProps<"/">) {
  // Read the promoted image's runtime settings, rather than freezing them into
  // prerendered HTML during the image build.
  await connection();
  return (
    <html lang="en" className={`${figtree.variable} ${plexMono.variable} h-full`}
      data-nori-observability={JSON.stringify(browserObservabilityConfig())}>
      <body className="h-full overflow-clip">
        {children}
      </body>
    </html>
  );
}

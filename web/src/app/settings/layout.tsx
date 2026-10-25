import { AppShell } from "@/components/shell/AppShell";

export default function LegacyLayout({ children }: { children: React.ReactNode }) {
  return <AppShell>{children}</AppShell>;
}

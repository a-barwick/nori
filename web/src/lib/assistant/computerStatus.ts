import type { ComputerStatus } from "@/lib/contracts/assistant";

export function computerStatusPill(
  status: ComputerStatus["status"],
): { tone: "ok" | "working" | "neutral" | "danger" | "warning"; label: string } {
  switch (status) {
    case "ready":
      return { tone: "ok", label: "Active" };
    case "sleeping":
      return { tone: "neutral", label: "Asleep" };
    case "provisioning":
    case "resetting":
      return { tone: "working", label: status === "provisioning" ? "Starting" : "Resetting" };
    case "deleting":
    case "deleted":
      return { tone: "neutral", label: "Removing" };
    case "error":
      return { tone: "danger", label: "Needs attention" };
    default:
      return { tone: "neutral", label: "Ready to start" };
  }
}

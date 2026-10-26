import { NovaPreview } from "@/components/nova/NovaPreview";
import { notFound } from "next/navigation";

export default function RedesignPage() {
  if (process.env.NODE_ENV !== "development") notFound();
  return <NovaPreview />;
}

import { initServerObservability } from "@/lib/observability/config.server";
import { ignoredRequestSpans } from "@/lib/observability/privacy";

initServerObservability(ignoredRequestSpans);

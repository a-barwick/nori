import type { Effect, PermissionRule, PermissionRuleEditable } from "@/lib/contracts/assistant";

export type RuleEffect = PermissionRule["effect"];
export type RuleFilter = "all" | RuleEffect;

export const RULE_PRECEDENCE_NOTE =
  "Never allow beats everything; Ask first always overrides Allow automatically.";

export const RULE_EFFECT_ORDER: readonly RuleEffect[] = ["deny", "ask", "allow"];

export const RULE_EFFECT_META: Record<
  RuleEffect,
  {
    label: string;
    groupTitle: string;
    tone: "ok" | "warning" | "danger";
  }
> = {
  allow: {
    label: "Allow automatically",
    groupTitle: "Allow automatically",
    tone: "ok",
  },
  ask: {
    label: "Ask first",
    groupTitle: "Ask first",
    tone: "warning",
  },
  deny: {
    label: "Never allow",
    groupTitle: "Never allow",
    tone: "danger",
  },
};

export type RuleGroup = {
  id: "deny" | "ask" | "allow" | "scoped";
  title: string;
  rules: PermissionRule[];
};

export function countRulesByEffect(rules: PermissionRule[]): Record<RuleEffect, number> {
  const counts: Record<RuleEffect, number> = { allow: 0, ask: 0, deny: 0 };
  for (const rule of rules) counts[rule.effect] += 1;
  return counts;
}

export function groupPermissionRules(rules: PermissionRule[], filter: RuleFilter = "all"): RuleGroup[] {
  const filtered = filter === "all" ? rules : rules.filter((rule) => rule.effect === filter);
  return [
    { id: "deny", title: RULE_EFFECT_META.deny.groupTitle, rules: filtered.filter((rule) => rule.effect === "deny") },
    { id: "ask", title: RULE_EFFECT_META.ask.groupTitle, rules: filtered.filter((rule) => rule.effect === "ask") },
    {
      id: "allow",
      title: RULE_EFFECT_META.allow.groupTitle,
      rules: filtered.filter((rule) => rule.effect === "allow" && !rule.automation_id && !rule.goal_id),
    },
    {
      id: "scoped",
      title: "For automations and goals",
      rules: filtered.filter((rule) => rule.effect === "allow" && (rule.automation_id || rule.goal_id)),
    },
  ];
}

const ACTION_PHRASE: Record<Effect, string> = {
  read: "read",
  reversible_write: "make undoable changes",
  sensitive_send: "send",
  unknown: "use tools that don’t say what they do",
  control: "run control actions",
};

export function describePermissionRulePreview({
  effect,
  classes,
  targetLabel,
  recipients = "",
}: {
  effect: RuleEffect;
  classes: Effect[];
  targetLabel: string;
  recipients?: string;
}): string {
  const actions = classes.map((effectClass) => ACTION_PHRASE[effectClass]).filter(Boolean).join(" or ") || "act";
  const trimmedRecipients = recipients.trim();
  const recipientClause = classes.includes("sensitive_send") && trimmedRecipients ? ` to ${trimmedRecipients}` : "";
  switch (effect) {
    case "allow":
      return `Nori will automatically ${actions} with ${targetLabel}${recipientClause}.`;
    case "ask":
      return `Nori will always ask first before it can ${actions} with ${targetLabel}${recipientClause}.`;
    case "deny":
      return `Nori will never ${actions} with ${targetLabel}${recipientClause}.`;
  }
}

export function buildPermissionRuleEditable({
  effect,
  account,
  classes,
  recipients = "",
  expires = "",
}: {
  effect: RuleEffect;
  account: string;
  classes: Effect[];
  recipients?: string;
  expires?: string;
}): PermissionRuleEditable {
  const parsedRecipients = classes.includes("sensitive_send")
    ? recipients.split(",").map((value) => value.trim()).filter(Boolean)
    : [];
  return {
    effect,
    tool_ids: ["*"],
    connection_ids: [account],
    resource_ids: ["*"],
    action_classes: classes,
    ...(parsedRecipients.length ? { recipients: parsedRecipients } : {}),
    ...(expires ? { expires_at: new Date(`${expires}T23:59:59`).toISOString() } : {}),
  };
}

export function revokeRuleConfirmationBody(rule: PermissionRule): string {
  switch (rule.effect) {
    case "deny":
      return "Nori will no longer automatically block this action.";
    case "ask":
      return "Nori will fall back to your other rules and defaults for this action.";
    case "allow":
      return rule.automation_id
        ? "The automation will ask you before doing this from now on."
        : "Nori will ask you before doing this from now on.";
  }
}

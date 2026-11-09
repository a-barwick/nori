"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useRef, useState } from "react";
import { Download, Play, Plus, Upload } from "lucide-react";
import { Button } from "@/components/ui/primitives";
import { mutate, newRequestID } from "@/lib/assistant/api";
import { useAssistant } from "@/lib/assistant/context";
import { invalidate, useAction, useResource } from "@/lib/assistant/useResource";
import type { SkillEditable, SkillView } from "@/lib/contracts/assistant";
import { cn } from "@/lib/utils";
import { Confirm, Empty, Failure, Field, inputBase, inputClass, Loading, Notice, NotYet, Pill, Row, Rows, Screen, Section } from "./ui";

type List<T> = { items: T[] };

const editableOf = (skill: SkillView): SkillEditable => ({
  title: skill.title, description: skill.description, instructions: skill.instructions, inputs: skill.inputs ?? {},
  required_tool_ids: skill.required_tool_ids ?? [], examples: skill.examples, scope: skill.scope ?? "user",
});

/** Starts a deliberate run: Chat opens with the skill named in an editable draft. */
export function runSkill(skill: Pick<SkillView, "title">, setDraft: (text: string) => void, push: (href: string) => void) {
  setDraft(`Use my skill “${skill.title}”: `);
  push("/");
}

function download(skill: SkillView) {
  const url = URL.createObjectURL(new Blob([JSON.stringify(editableOf(skill), null, 2)], { type: "application/json" }));
  const link = document.createElement("a");
  link.href = url; link.download = `${skill.title.replace(/[^\p{L}\p{N} -]/gu, "").slice(0, 60) || "skill"}.skill.json`; link.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

/** Reads an exported skill file and checks it has the fields a skill needs. */
async function readSkillFile(file: File): Promise<SkillEditable> {
  if (file.size > 64 * 1024) throw new Error("That file is too large to be a skill.");
  let value: Partial<SkillEditable>;
  try { value = JSON.parse(await file.text()); } catch { throw new Error("That file isn’t a skill export."); }
  if (typeof value.title !== "string" || typeof value.instructions !== "string" || !value.title.trim() || !value.instructions.trim()) throw new Error("A skill needs a title and instructions.");
  return { title: value.title, description: typeof value.description === "string" ? value.description : "", instructions: value.instructions,
    inputs: value.inputs && typeof value.inputs === "object" ? value.inputs : {}, required_tool_ids: Array.isArray(value.required_tool_ids) ? value.required_tool_ids.filter((id) => typeof id === "string") : [],
    examples: Array.isArray(value.examples) ? value.examples.filter((example) => typeof example === "string") : undefined, scope: value.scope === "chat" || value.scope === "project" ? value.scope : "user" };
}

export function SkillsList() {
  const { has, runtime } = useAssistant();
  const router = useRouter();
  const skills = useResource<List<SkillView>>(has("skills") ? "/skills" : null, { poll: 30_000 });
  const action = useAction();
  const picker = useRef<HTMLInputElement>(null);
  if (!has("skills")) return <NotYet what="Skills" />;
  if (skills.loading) return <Loading />;
  const items = skills.data?.items ?? [];
  const importFile = (file: File) => action.run("import", async () => {
    const value = await readSkillFile(file);
    const receipt = await mutate<SkillView>("/skills", { request_id: newRequestID(), expected_revision: 0, value });
    invalidate("/skills");
    router.push(`/apps/skills/${encodeURIComponent((receipt.resource as SkillView).id)}`);
  });
  return <Section actions={<div className="flex gap-2">
    <input ref={picker} type="file" accept=".json,application/json" hidden aria-label="Choose a skill file" onChange={(event) => { const file = event.target.files?.[0]; event.target.value = ""; if (file) void importFile(file); }} />
    <Button size="sm" onClick={() => picker.current?.click()} disabled={!!action.pending}><Upload size={14} aria-hidden />{action.pending === "import" ? "Importing…" : "Import"}</Button>
    <Link href="/apps/skills/new" className="inline-flex h-8 items-center gap-1.5 rounded-full bg-accent px-3 text-meta font-medium text-on-accent hover:bg-accent-hover"><Plus size={14} aria-hidden />New skill</Link>
  </div>}>
    {action.error && <div className="mb-3"><Notice tone="danger">{action.error}</Notice></div>}
    {skills.error && <div className="mb-3"><Failure error={skills.error} onRetry={() => void skills.reload()} /></div>}
    {items.length ? <Rows label="Skills">{items.map((skill) => <Row key={skill.id} href={`/apps/skills/${encodeURIComponent(skill.id)}`} title={skill.title}
      detail={[skill.description, skill.installation_id ? "From a plugin" : `Version ${skill.ref.version}`].filter(Boolean).join(" · ")}
      trailing={<Button size="sm" variant="quiet" onClick={(event) => { event.preventDefault(); runSkill(skill, runtime.setDraft, router.push); }}><Play size={13} aria-hidden />Run</Button>} />)}</Rows>
      : <Empty>No skills yet. A skill is a reusable set of instructions you can run from Chat.</Empty>}
  </Section>;
}

export function SkillScreen({ id }: { id?: string }) {
  const { has } = useAssistant();
  const skill = useResource<SkillView>(id && has("skills") ? `/skills/${encodeURIComponent(id)}` : null, { poll: 0 });
  if (!has("skills")) return <Screen title="Skill" back="/apps?tab=skills"><NotYet what="Skills" /></Screen>;
  if (id && !skill.data) return <Screen title="Skill" back="/apps?tab=skills">{skill.error ? <Failure error={skill.error} /> : <Loading />}</Screen>;
  return <SkillEditor key={skill.data?.revision ?? "new"} existing={skill.data} />;
}

function SkillEditor({ existing }: { existing?: SkillView }) {
  const { runtime } = useAssistant();
  const router = useRouter();
  const action = useAction();
  const start = existing ? editableOf(existing) : { title: "", description: "", instructions: "", inputs: {}, required_tool_ids: [], scope: "user" as const };
  const [value, setValue] = useState<SkillEditable>(start);
  const [examples, setExamples] = useState((start.examples ?? []).join("\n"));
  const readOnly = !!existing?.installation_id;
  const dirty = JSON.stringify({ ...value, examples: examples.split("\n").map((line) => line.trim()).filter(Boolean) }) !== JSON.stringify({ ...start, examples: start.examples ?? [] });
  const save = () => action.run("save", async () => {
    const body = { ...value, title: value.title.trim(), instructions: value.instructions.trim(), examples: examples.split("\n").map((line) => line.trim()).filter(Boolean) };
    const receipt = existing
      ? await mutate<SkillView>(`/skills/${encodeURIComponent(existing.id)}`, { request_id: newRequestID(), expected_revision: existing.revision, value: body }, "PUT")
      : await mutate<SkillView>("/skills", { request_id: newRequestID(), expected_revision: 0, value: body });
    invalidate("/skills");
    const saved = receipt.resource as SkillView;
    if (existing) router.refresh(); else router.replace(`/apps/skills/${encodeURIComponent(saved.id)}`);
  });
  const remove = () => action.run("delete", async () => {
    await mutate(`/skills/${encodeURIComponent(existing!.id)}/actions`, { request_id: newRequestID(), expected_revision: existing!.revision, value: { action: "delete" } });
    invalidate("/skills"); router.push("/apps?tab=skills");
  });
  return <Screen title={existing ? existing.title : "New skill"} back="/apps?tab=skills"
    actions={existing && <>{readOnly ? <Pill>From a plugin</Pill> : <Pill>Version {existing.ref.version}</Pill>}<Button size="sm" variant="primary" onClick={() => runSkill(existing, runtime.setDraft, router.push)}><Play size={13} aria-hidden />Run</Button><Button size="sm" onClick={() => download(existing)}><Download size={13} aria-hidden />Export</Button></>}>
    <form className="space-y-5" onSubmit={(event) => { event.preventDefault(); if (!readOnly) void save(); }}>
      <fieldset disabled={readOnly} className="space-y-5">
        <Field label="Name"><input value={value.title} onChange={(event) => setValue({ ...value, title: event.target.value })} className={inputClass} placeholder="Weekly expenses summary" required /></Field>
        <Field label="What it’s for"><input value={value.description} onChange={(event) => setValue({ ...value, description: event.target.value })} className={inputClass} placeholder="Summarizes receipts from the past week" /></Field>
        <Field label="Instructions"><textarea value={value.instructions} onChange={(event) => setValue({ ...value, instructions: event.target.value })} rows={8} maxLength={32 * 1024} className={cn(inputClass, "resize-y font-mono text-code")} placeholder="Find receipts in my mail from the last 7 days…" required /></Field>
        <Field label="Example requests (one per line)"><textarea value={examples} onChange={(event) => setExamples(event.target.value)} rows={3} className={cn(inputClass, "resize-y")} /></Field>
        <Field label="Available in"><select value={value.scope} onChange={(event) => setValue({ ...value, scope: event.target.value as SkillEditable["scope"] })} className={cn(inputBase, "w-auto")}><option value="user">All chats</option><option value="chat">One chat</option><option value="project">One project</option></select></Field>
      </fieldset>
      {existing?.required_tool_ids.length ? <Section title="Uses"><p className="text-meta text-ink-2 [overflow-wrap:anywhere]">{existing.required_tool_ids.join(", ")}</p></Section> : null}
      {action.error && <Notice tone="danger">{action.error}</Notice>}
      {!readOnly && <div className="flex flex-wrap gap-2">
        <Button type="submit" variant="primary" disabled={!value.title.trim() || !value.instructions.trim() || !dirty || !!action.pending}>{action.pending === "save" ? "Saving…" : existing ? "Save new version" : "Create skill"}</Button>
        {existing && <Confirm danger title="Delete this skill?" body="Runs that already used it keep their record." confirm="Delete" onConfirm={() => void remove()} trigger={<Button variant="quiet" className="text-danger!" disabled={!!action.pending}>Delete</Button>} />}
      </div>}
    </form>
  </Screen>;
}

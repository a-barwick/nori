"use client";

import { useRef, useState, type ReactNode } from "react";
import { Dialog } from "radix-ui";
import { ArrowRight, ArrowUp, ArrowUpRight, CalendarDays, Check, ChevronRight, Cloud, FileText, Globe, Mail, RotateCcw, ShieldCheck, TriangleAlert, X } from "lucide-react";
import { Medallion } from "@/components/mark/NoriMark";
import { NovaShell } from "./NovaShell";

type Scene = "morning" | "followup" | "renewals";
type Detail = "brief" | "forecast" | "contoso" | "fabrikam" | "dashboard" | null;

const scenes: { id: Scene; time: string; label: string }[] = [
  { id: "morning", time: "8:00 AM", label: "Your day, prepared" },
  { id: "followup", time: "11:00 AM", label: "The follow-up" },
  { id: "renewals", time: "3:00 PM", label: "Built with Nori" },
];

const meetings: { time: string; name: string; type: string; status: string; detail: Detail; risk?: boolean }[] = [
  { time: "9:00", name: "Forecast call", type: "with Sam", status: "Notes ready", detail: "forecast" },
  { time: "10:00", name: "Northwind Health", type: "renewal", status: "Brief ready", detail: "brief", risk: true },
  { time: "1:00", name: "Contoso Bank", type: "discovery", status: "Agenda sent", detail: "contoso" },
  { time: "3:30", name: "Fabrikam", type: "QBR", status: "Deck drafted", detail: "fabrikam" },
];

const accounts = [
  { name: "Northwind Health", arr: "$480K", days: 62, risk: "High" },
  { name: "Woodgrove Bank", arr: "$310K", days: 45, risk: "Medium" },
  { name: "Tailspin Toys", arr: "$265K", days: 88, risk: "Medium" },
  { name: "Contoso Bank", arr: "$520K", days: 120, risk: "Low" },
];

function AssistantMessage({ children }: { children: ReactNode }) {
  return <div className="nova-assistant-message"><Medallion size={38} /><div>{children}</div></div>;
}

function RenewalChart() {
  return <div className="nova-chart" role="img" aria-label="Illustrative renewal risk: Northwind highest, followed by Woodgrove, Tailspin, and Contoso">
    {[100, 70, 56, 34].map((height, index) => <span key={height} style={{ height: `${height}%` }} className={index === 0 ? "nova-chart-risk" : ""} />)}
  </div>;
}

function RenewalDashboard() {
  return <div className="nova-dashboard">
    <div className="nova-dashboard-heading"><div><span className="nova-eyebrow">MAYA’S WORKSPACE</span><h3>My renewals</h3></div><span className="nova-pill nova-success"><span className="nova-dot" />Sample data</span></div>
    <div className="nova-metrics"><div><strong>$2.4M</strong><span>Renewing</span></div><div><strong>9</strong><span>Accounts</span></div><div><strong className="nova-danger">3</strong><span>At risk</span></div></div>
    <div className="nova-table-scroll"><table><caption className="sr-only">Selected renewals, ordered by risk. Fictional sample accounts.</caption><thead><tr><th>Account</th><th>ARR</th><th>Renews</th><th>Risk</th></tr></thead><tbody>{accounts.map((account) => <tr key={account.name}><th scope="row">{account.name}</th><td>{account.arr}</td><td>{account.days} days</td><td><span className={`nova-pill nova-risk-${account.risk.toLowerCase()}`}>{account.risk}</span></td></tr>)}</tbody></table></div>
    <p className="nova-dashboard-footnote">Showing 4 of 9 sample accounts · Shared with Sam in this concept</p>
  </div>;
}

/** Explicitly fictional, in-memory interactions. Never calls a runtime or external service. */
export function NovaPreview() {
  const [scene, setScene] = useState<Scene>("morning");
  const [detail, setDetail] = useState<Detail>(null);
  const [draft, setDraft] = useState("");
  const [sent, setSent] = useState(false);
  const [approved, setApproved] = useState(false);
  const [editing, setEditing] = useState(false);
  const [emailBody, setEmailBody] = useState("Usage report by Friday\nTwo-year pricing options\nSecurity review with Priya on the 14th");
  const [savedBody, setSavedBody] = useState(emailBody);
  const [messages, setMessages] = useState<{ id: number; text: string }[]>([]);
  const [notice, setNotice] = useState("");
  const scrollArea = useRef<HTMLDivElement>(null);
  const nextMessageId = useRef(0);

  function changeScene(next: Scene) {
    setScene(next);
    setMessages([]);
    setNotice("");
    scrollArea.current?.scrollTo({ top: 0 });
  }

  function reset() {
    changeScene("morning");
    setSent(false);
    setApproved(false);
    setEditing(false);
    setDraft("");
    setEmailBody("Usage report by Friday\nTwo-year pricing options\nSecurity review with Priya on the 14th");
    setSavedBody("Usage report by Friday\nTwo-year pricing options\nSecurity review with Priya on the 14th");
  }

  function sendMessage() {
    const text = draft.trim();
    if (!text) return;
    if (/renewal|dashboard|build/i.test(text)) changeScene("renewals");
    else if (/recap|follow.?up|priya|email/i.test(text)) changeScene("followup");
    else if (/morning|meeting|calendar|today/i.test(text)) changeScene("morning");
    setMessages((previous) => [...previous, { id: nextMessageId.current++, text }]);
    setNotice("Message added to this preview. Explore the prepared examples above; live tasks run in the connected app.");
    setDraft("");
    requestAnimationFrame(() => scrollArea.current?.scrollTo({ top: scrollArea.current.scrollHeight, behavior: "smooth" }));
  }

  const detailTitle = detail === "dashboard" ? "My renewals" : detail === "brief" ? "Northwind Health · Renewal brief" : detail === "forecast" ? "Forecast call · Notes" : detail === "contoso" ? "Contoso Bank · Discovery agenda" : "Fabrikam · QBR outline";

  return <NovaShell preview actions={<><span className="nova-preview-badge">Design preview</span><button type="button" className="nova-icon-button" onClick={reset} aria-label="Reset preview" title="Reset preview"><RotateCcw size={17} /></button></>}>
    <nav className="nova-scenes" aria-label="Explore a day with Nori">
      {scenes.map((item) => <button type="button" key={item.id} aria-current={scene === item.id ? "step" : undefined} onClick={() => changeScene(item.id)}><span className="nova-scene-time">{item.time}</span><span>{item.label}</span></button>)}
    </nav>
    <main className="nova-preview-main" aria-label="Nori design preview">
      <div className="nova-preview-scroll scrollbar-quiet" ref={scrollArea}>
        <div className="nova-conversation" key={scene}>
          <div className="nova-day-label"><span />{scene === "morning" ? "TODAY, A LITTLE AHEAD" : scene === "followup" ? "AFTER THE NORTHWIND CALL" : "YOUR IDEAS, PUT TO WORK"}<span /></div>
          {scene === "morning" && <>
            <AssistantMessage><h1>Morning, Maya.</h1><p>Four meetings today. Start with Northwind.</p></AssistantMessage>
            <section className="nova-agenda nova-card" aria-label="Today's prepared meetings">
              <div className="nova-card-heading"><span><CalendarDays size={17} />YOUR DAY AT A GLANCE</span><span className="nova-muted">4 meetings</span></div>
              {meetings.map((meeting) => <button type="button" key={meeting.name} className={`nova-meeting ${meeting.risk ? "nova-meeting-risk" : ""}`} onClick={() => setDetail(meeting.detail)}>
                <span className="nova-meeting-time">{meeting.time}</span>
                <span className="nova-meeting-description"><span><strong>{meeting.name}</strong><span className="nova-muted"> {meeting.name === "Forecast call" ? "" : "· "}{meeting.type}</span></span>{meeting.risk && <span className="nova-risk-note"><TriangleAlert size={16} />Usage down 18% · champion changed roles</span>}</span>
                <span className="nova-pill">{meeting.status}</span><ChevronRight size={16} className="nova-row-arrow" />
              </button>)}
            </section>
            <div className="nova-source-note"><Check size={14} />Prepared from your calendar, email, and Salesforce<span>Concept data</span></div>
            <AssistantMessage><p>I’ve pulled together the account history and what’s changed. Your Northwind brief is ready when you are.</p></AssistantMessage>
            <div className="nova-suggestions"><button type="button" onClick={() => setDetail("brief")}><FileText size={16} />Open the Northwind brief<ArrowUpRight size={15} /></button><button type="button" onClick={() => changeScene("followup")}>See the follow-up<ArrowRight size={15} /></button></div>
          </>}
          {scene === "followup" && <>
            <AssistantMessage><h1>The follow-up is ready.</h1><p>Northwind recap and Salesforce update are ready.</p></AssistantMessage>
            <div className="nova-user-message">Also loop in Priya from Legal.</div>
            <AssistantMessage><p>Added Priya. Take a look before anything goes out.</p></AssistantMessage>
            <div className="nova-action-grid">
              <section className="nova-card nova-action-card">
                <div className="nova-card-label"><Mail size={18} />RECAP EMAIL</div>
                <h2>Next steps on your renewal</h2>
                <p className="nova-recipients">To Dana, Lee <span className="nova-pill nova-success">+ Priya, Legal</span></p>
                {editing ? <label className="nova-edit-label">Email recap<textarea autoFocus value={emailBody} onChange={(event) => setEmailBody(event.target.value)} rows={5} /></label> : <ul className="nova-recap-list">{savedBody.split("\n").filter(Boolean).map((line, index) => <li key={index}>{line}</li>)}</ul>}
                <div className="nova-card-actions">{editing ? <><button type="button" className="nova-button" disabled={!emailBody.trim()} onClick={() => { setSavedBody(emailBody); setEditing(false); }}>Save changes</button><button type="button" className="nova-button nova-button-secondary" onClick={() => { setEmailBody(savedBody); setEditing(false); }}>Cancel</button></> : <><button type="button" className={`nova-button ${sent ? "nova-button-done" : ""}`} disabled={sent} onClick={() => { setSent(true); setNotice("Preview only: the recap is marked as sent. No email was sent."); }}>{sent ? <><Check size={16} />Sent</> : "Send"}</button><button type="button" className="nova-button nova-button-secondary" disabled={sent} onClick={() => setEditing(true)}>Edit</button></>}</div>
              </section>
              <section className="nova-card nova-action-card">
                <div className="nova-card-label"><Cloud size={19} />SALESFORCE</div>
                <dl className="nova-update"><dt>Stage</dt><dd>Discovery <ArrowRight size={18} /> Proposal</dd><dt>Next step</dt><dd>Pricing options by Fri</dd></dl>
                <div className="nova-card-actions"><button type="button" className={`nova-button ${approved ? "nova-button-done" : ""}`} disabled={approved} onClick={() => { setApproved(true); setNotice("Preview only: the update is marked as approved. Salesforce was not changed."); }}>{approved ? <><Check size={16} />Approved</> : "Approve"}</button></div>
              </section>
            </div>
            <div className="nova-source-note"><ShieldCheck size={15} />You’re in control. Nori asks before taking action.</div>
          </>}
          {scene === "renewals" && <>
            <div className="nova-user-message">Build me a page that tracks my renewals, riskiest first. Share it with Sam.</div>
            <AssistantMessage><h1>From an idea to a working page.</h1><p>Done. It refreshes from Salesforce every morning. Only you and Sam can open it.</p></AssistantMessage>
            <div className="nova-build-grid">
              <button type="button" className="nova-artifact nova-card" onClick={() => setDetail("dashboard")} aria-label="Open sample renewals dashboard"><div className="nova-artifact-preview"><span>RENEWALS BY RISK</span><RenewalChart /></div><div className="nova-artifact-link"><Globe size={17} /><strong>maya-renewals</strong><ArrowUpRight size={18} /></div></button>
              <div className="nova-build-note"><span className="nova-pill nova-success"><Check size={14} />Ready to open</span><h2>Your work goes with you.</h2><p>Built on your own computer.<br />Available right here in Nova.</p><div className="nova-sharing"><span className="nova-profile">M</span><span className="nova-profile nova-profile-sam">S</span><span>Maya and Sam</span></div></div>
            </div>
            <section className="nova-dashboard-inline nova-card" aria-label="Sample renewals dashboard"><RenewalDashboard /><button type="button" className="nova-dashboard-expand" onClick={() => setDetail("dashboard")}>Open dashboard<ArrowUpRight size={15} /></button></section>
          </>}
          {messages.map((message) => <div className="nova-user-message" key={message.id}>{message.text}</div>)}
          {notice && <p className="nova-notice" role="status">{notice}</p>}
        </div>
      </div>
      <div className="nova-preview-composer">
        <form onSubmit={(event) => { event.preventDefault(); sendMessage(); }}>
          <textarea rows={1} value={draft} maxLength={8192} onChange={(event) => setDraft(event.target.value)} aria-label="Message Nori" placeholder="Ask Nori anything…" onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); sendMessage(); } }} />
          <button type="submit" className="nova-send" disabled={!draft.trim()} aria-label="Send preview message"><ArrowUp size={21} /></button>
        </form>
        <div className="nova-composer-caption"><span>One conversation. Keep talking while Nori works.</span><span>Fictional data · No live actions</span></div>
      </div>
    </main>
    <Dialog.Root open={detail !== null} onOpenChange={(open) => { if (!open) setDetail(null); }}>
      <Dialog.Portal><Dialog.Overlay className="nova-dialog-overlay" /><Dialog.Content className="nova-dialog" aria-describedby="nova-detail-description"><div className="nova-dialog-top"><Dialog.Title>{detailTitle}</Dialog.Title><Dialog.Close className="nova-icon-button" aria-label="Close preview"><X size={20} /></Dialog.Close></div><Dialog.Description id="nova-detail-description" className="nova-dialog-description">Concept preview · Fictional people and data from the redesign deck.</Dialog.Description>
        {detail === "dashboard" ? <RenewalDashboard /> : <div className="nova-brief"><div className="nova-card-label"><FileText size={18} />{detail === "brief" ? "MEETING PREP" : "PREPARED FOR YOU"}</div><h2>{detail === "brief" ? "Start with what’s changed." : detailTitle}</h2>{detail === "brief" ? <><p className="nova-brief-risk"><TriangleAlert size={18} />Usage is down 18%. Your champion has changed roles.</p><h3>Before the call</h3><ul><li>Confirm who owns the renewal decision now.</li><li>Understand what’s behind the change in usage.</li><li>Bring two-year pricing options and the latest usage report.</li></ul><h3>A useful opening</h3><p>“What’s changed for your team since we last spoke, and what would make the next year a success?”</p></> : <><p>{detail === "forecast" ? "Review the quarter with Sam, starting with renewals that need attention." : detail === "contoso" ? "Learn about the team’s current workflow, priorities, and decision process." : "Review adoption, business outcomes, and the plan for the next quarter."}</p><h3>On the agenda</h3><ul><li>Where things stand today</li><li>What’s working and what needs attention</li><li>Agree on next steps and owners</li></ul></>}<div className="nova-source-note"><CalendarDays size={15} />Prepared for Maya’s day · Sample brief</div></div>}
      </Dialog.Content></Dialog.Portal>
    </Dialog.Root>
  </NovaShell>;
}

// Real public pages shown in Nori's task screenshots. Run: node capture/shots.mjs
import { chromium } from "playwright-core";
const b = await chromium.launch({ channel: "chrome" });
const p = await b.newPage({ viewport: { width: 1280, height: 800 }, deviceScaleFactor: 1.5, userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36" });
await p.goto("https://investor.docusign.com/", { waitUntil: "domcontentloaded", timeout: 40000 });
await p.waitForTimeout(3500);
const links = await p.$$eval("a", (as) => [...new Set(as.map((a) => a.href))].filter((h) => /news|financ|quarter|earning|press/i.test(h)));
console.log(links.slice(0, 20).join("\n"));
for (const sel of ["#onetrust-reject-all-handler", "button:has-text('Reject')", "button:has-text('Decline')"]) await p.locator(sel).first().click({ timeout: 800 }).catch(() => {});
await p.waitForTimeout(600);
await p.screenshot({ path: "public/real/ir-home.png" });
const target = links.find((h) => /financial-results|quarterly/i.test(h)) || links.find((h) => /financ/i.test(h)) ;
if (target) { await p.goto(target, { waitUntil: "domcontentloaded" }); await p.waitForTimeout(3500); await p.screenshot({ path: "public/real/ir-results.png" }); console.log("results:", target, await p.title()); }
await b.close();

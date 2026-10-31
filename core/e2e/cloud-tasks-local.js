// Local stand-in for the external Cloud Tasks API, with a persisted delivery queue.
const root = process.env.NORI_WORKER_E2E_ROOT;
if (!root) throw new Error("Set NORI_WORKER_E2E_ROOT to the fresh local fixture directory.");
const ports = await Bun.file(`${root}/ports.json`).json();
let state = { pending: [], deliveries: [], accepted: [] };
if (await Bun.file(`${root}/tasks-state.json`).exists()) state = await Bun.file(`${root}/tasks-state.json`).json();
const save = () => Bun.write(`${root}/tasks-state.json`, JSON.stringify(state, null, 2));
let held = false;
let inFlight = false;
Bun.serve({hostname:"127.0.0.1", port:ports.tasks, async fetch(req) {
 const u = new URL(req.url);
 if (req.method === "GET") return Response.json({...state,held});
 if (u.pathname === "/hold") { held = true; return Response.json({held}); }
 if (u.pathname === "/release") { held = false; return Response.json({held}); }
 if (u.pathname === "/expire") {
  if (!held || inFlight) return Response.json({error:"Fence and drain the local queue before simulating expiry"},{status:409});
  const expired = state.pending.map(x => x.name); state.pending = []; await save(); return Response.json({expired});
 }
 const body = await req.json();
 if (!body.task?.httpRequest || body.task.httpRequest.url !== `http://127.0.0.1:${ports.worker}/run`) return Response.json({error:"invalid Cloud Tasks request"},{status:400});
 const name = `projects/local/locations/local/queues/worker/tasks/${crypto.randomUUID()}`;
 const item = {name, task:body.task, attempt:0, accepted_at:new Date().toISOString()};
 state.pending.push(item); state.accepted.push({name,accepted_at:item.accepted_at,target:item.task.httpRequest.url});
 await save();
 return Response.json({...body.task,name});
}});
while (true) {
 if (held || !state.pending.length) { await Bun.sleep(50); continue; }
 const item = state.pending[0];
 inFlight = true;
 item.attempt++;
 const record = {name:item.name,attempt:item.attempt,started_at:new Date().toISOString()};
 state.deliveries.push(record); await save();
 try {
  const response = await fetch(item.task.httpRequest.url, {method:"POST",headers:{"Content-Type":"application/json","X-CloudTasks-TaskName":item.name,"X-CloudTasks-TaskRetryCount":String(item.attempt-1)},body:Buffer.from(item.task.httpRequest.body,"base64"),signal:AbortSignal.timeout(720_000)});
  record.status=response.status; record.body=await response.text();
  if (response.ok) state.pending.shift();
 } catch(error) { record.error=error.message; }
 record.finished_at=new Date().toISOString();inFlight = false;await save();
 if (state.pending[0]===item) await Bun.sleep(1000);
}

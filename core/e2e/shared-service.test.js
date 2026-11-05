import {beforeAll,afterAll,expect,test} from 'bun:test';
import {createHarness,until} from './shared-service-harness';
const live=process.env.NORI_SHARED_SERVICE_E2E==='1';const journey=live?test:test.skip;let h;
const headers={'content-type':'application/json'};
async function request(path,body,status=200){const res=await fetch(h.address+'/v1/'+path,{method:body===undefined?'GET':'POST',headers,body:body===undefined?undefined:JSON.stringify(body)});const value=await res.json();expect(res.status).toBe(status);return value}
const create=()=>request('conversations',{},201);
const snapshot=id=>request('conversations/'+id);
const message=(id,text,input=crypto.randomUUID(),attachments=[])=>request(`conversations/${id}/messages`,{id:input,text,attachments},202);
const done=id=>until(()=>snapshot(id),c=>c.activeTurn===null,'finished chat');
beforeAll(async()=>{if(live)h=await createHarness()},180000);
afterAll(async()=>{if(h)await h.close()},30000);
journey('shared service refuses requested modes and maps unchanged assistant snapshot',async()=>{
 const meta=await request('capabilities');expect(meta.fixture).toBe(false);expect(meta.persistence).toBe('durable');expect(meta.isolated).toBe(true);
 await request('conversations',{title:'forged',mode:'assistant'},400);
 const old=await fetch(h.address+'/v1/snapshot').then(r=>r.json());const mapped=await snapshot('assistant');expect(mapped.session.messages).toEqual(old.messages);expect(mapped.session.runs).toEqual(old.runs);
 const [a,b]=await Promise.all([create(),create()]);expect(a.id).not.toBe(b.id);
 await Promise.all([message(a.id,'alpha private marker'),message(b.id,'beta private marker')]);
 const [aa,bb]=await Promise.all([done(a.id),done(b.id)]);expect(aa.messages.at(-1).text).toBe('Reply: alpha private marker');expect(bb.messages.at(-1).text).toBe('Reply: beta private marker');
 const calls=h.requests.filter(r=>r.stream);expect(JSON.stringify(calls.find(r=>JSON.stringify(r.messages).includes('alpha private marker')))).not.toContain('beta private marker');expect(JSON.stringify(calls.find(r=>JSON.stringify(r.messages).includes('beta private marker')))).not.toContain('alpha private marker');
},60000);
journey('duplicate accepted IDs survive reload, conflicting content cannot send another request',async()=>{
 const c=await create();const id=crypto.randomUUID();await message(c.id,'single input',id);await done(c.id);const before=h.requests.length;
 await message(c.id,'single input',id);await request(`conversations/${c.id}/messages`,{id,text:'conflicting'},409);
 const reloaded=await snapshot(c.id);expect(reloaded.messages.filter(m=>m.role==='user')).toHaveLength(1);expect(h.requests.length).toBe(before);
},60000);
journey('stop preserves partial text, cannot cancel another chat and events replay full text in order',async()=>{
 const [a,b]=await Promise.all([create(),create()]);const input=crypto.randomUUID();await message(a.id,'slow stream cancellation',input);await message(b.id,'unrelated completed answer');
 const partial=await until(()=>snapshot(a.id),c=>c.messages.at(-1).text.length>8,'partial text');
 await request(`conversations/${b.id}/cancel`,{turnId:input},404);
 const stopped=await request(`conversations/${a.id}/cancel`,{turnId:input});expect(stopped.messages.at(-1).status).toBe('cancelled');expect(stopped.messages.at(-1).text.length).toBeGreaterThan(0);await done(b.id);
 const ctl=new AbortController();const response=await fetch(`${h.address}/v1/conversations/${a.id}/events?after=0`,{signal:ctl.signal});const reader=response.body.getReader();let text='';const target=(await snapshot(a.id)).position;
 while(!text.includes(`"seq":${target},`)){const next=await reader.read();text+=new TextDecoder().decode(next.value)}ctl.abort();await reader.cancel().catch(()=>{});
 const events=text.split('\n').filter(x=>x.startsWith('data:')).map(x=>JSON.parse(x.slice(5)));expect(events.map(e=>e.seq)).toEqual(events.map((e,i)=>i+1));expect(events.at(-1).conversation.messages.at(-1).status).toBe('cancelled');expect(events.at(-1).conversation.id).toBe(a.id);expect(partial.position).toBeGreaterThan(1);
},60000);
journey('scoped upload bytes and assignment reach foreground CSV tool without cross-chat reads',async()=>{
 const [a,b]=await Promise.all([create(),create()]);const upload=crypto.randomUUID();
 for(const [chat,text] of [[a,'name,value\nalpha,17'],[b,'name,value\nbeta,29']]){const res=await fetch(`${h.address}/v1/conversations/${chat.id}/uploads?id=${upload}&filename=sample.csv`,{method:'POST',headers:{'content-type':'text/csv'},body:text});expect(res.status).toBe(200)}
 const [one,two]=await Promise.all([a,b].map(c=>fetch(`${h.address}/v1/conversations/${c.id}/uploads/${upload}`).then(r=>r.text())));expect(one).toContain('alpha,17');expect(two).toContain('beta,29');
 expect((await fetch(`${h.address}/v1/uploads/${upload}`)).status).toBe(404);
 await message(a.id,'Read the attached CSV',crypto.randomUUID(),[upload]);const result=await done(a.id);expect(result.messages.at(-1).operations[0].name).toBe('upload_read');expect(result.messages.at(-1).operations[0].result.rows[1].cells).toEqual(['alpha','17']);expect(result.messages.at(-1).text).not.toContain('beta');
 const c=await create();await request(`conversations/${c.id}/messages`,{id:crypto.randomUUID(),text:'foreign',attachments:[upload]},400);
},60000);
journey('read-only calendar tool is shared and writes consume one exact approval across a restart',async()=>{
 const c=await create();await message(c.id,'Read my calendar');const read=await done(c.id);expect(read.messages.at(-1).operations[0].name).toBe('calendar_read');
 const a=await create();await message(a.id,'Create a calendar event');const waiting=await until(()=>snapshot(a.id),c=>c.messages.at(-1)?.operations?.at(-1)?.outcome==='prepared','approval');const op=waiting.messages.at(-1).operations.at(-1);expect(h.sent).toHaveLength(0);
 await h.stop();await h.start();const saved=await snapshot(a.id);expect(saved.activeTurn).toBe(waiting.activeTurn);expect(saved.messages.at(-1).operations.at(-1).action.question_id).toBe(op.action.question_id);
 await request(`conversations/${c.id}/approvals`,{questionId:op.action.question_id,decision:'approve'},409);
 const attempts=await Promise.all([1,2].map(()=>fetch(`${h.address}/v1/conversations/${a.id}/approvals`,{method:'POST',headers,body:JSON.stringify({questionId:op.action.question_id,decision:'approve'})})));expect(attempts.map(r=>r.status).sort()).toEqual([200,409]);
 const final=await done(a.id);expect(final.messages.at(-1).operations.at(-1).outcome).toBe('confirmed');expect(h.sent).toHaveLength(1);
},60000);

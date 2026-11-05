import {beforeAll,afterAll,expect,test} from 'bun:test';
import {createHarness,until} from './shared-service-harness';
import {follow,apply} from '../../chat/src/contract';
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
 const ctl=new AbortController();const events=[];const target=(await snapshot(a.id)).position;
 const stream=follow(a.id,0,ctl.signal,event=>events.push(event),h.address).catch(error=>{if(!ctl.signal.aborted)throw error});
 try{await until(async()=>events,values=>values.at(-1)?.seq>=target,'complete replay frames')}finally{ctl.abort();await stream}
 expect(events.map(e=>e.seq)).toEqual(events.map((e,i)=>i+1));const rebuilt=events.reduce(apply,{id:a.id,title:'',mode:'chat',messages:[],activeTurn:null,position:0});const saved=await snapshot(a.id);expect(rebuilt.messages).toEqual(saved.messages);expect(rebuilt.messages.at(-1).status).toBe('cancelled');expect(events.at(-1).conversation.id).toBe(a.id);expect(partial.position).toBeGreaterThan(1);
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
 const a=await create();await message(a.id,'Create a calendar event for approval race');const waiting=await until(()=>snapshot(a.id),c=>c.messages.at(-1)?.operations?.at(-1)?.outcome==='prepared','approval');const op=waiting.messages.at(-1).operations.at(-1);expect(h.sent).toHaveLength(0);
 await h.stop();await h.start();const saved=await snapshot(a.id);expect(saved.activeTurn).toBe(waiting.activeTurn);expect(saved.messages.at(-1).operations.at(-1).action.question_id).toBe(op.action.question_id);
 await request(`conversations/${c.id}/approvals`,{questionId:op.action.question_id,decision:'approve'},409);
 const attempts=await Promise.all([1,2].map(()=>fetch(`${h.address}/v1/conversations/${a.id}/approvals`,{method:'POST',headers,body:JSON.stringify({questionId:op.action.question_id,decision:'approve'})})));expect(attempts.map(r=>r.status).sort()).toEqual([200,409]);
 const final=await done(a.id);expect(final.messages.at(-1).operations.at(-1).outcome).toBe('confirmed');expect(final.messages.at(-1).text).toBe('The event was added to your calendar.');expect(h.sent).toHaveLength(1);
},60000);
journey('hosted wake retries a foreign lease until interrupted streaming is durably closed without replay',async()=>{
 const c=await create();const id=crypto.randomUUID();await message(c.id,'slow stream crash marker',id);
 const partial=await until(()=>snapshot(c.id),c=>c.messages.at(-1).text.length>5,'crash partial');const count=h.requests.filter(r=>r.stream&&r.messages.some(m=>m.content==='slow stream crash marker')).length;
 await h.stop('SIGKILL');await h.start();
 // Text saved between the partial read and the kill is kept too; recovery itself must not change it.
 const atKill=await snapshot(c.id);expect(atKill.messages.at(-1).text.startsWith(partial.messages.at(-1).text)).toBe(true);
 const process=await fetch(h.address+'/api/process',{method:'POST',headers,body:'{}'});expect(process.status).toBe(503);
 const recovered=await done(c.id);expect(recovered.messages.at(-1).status).toBe('failed');expect(recovered.messages.at(-1).text).toBe(atKill.messages.at(-1).text);
 expect(h.requests.filter(r=>r.stream&&r.messages.some(m=>m.content==='slow stream crash marker')).length).toBe(count);
 await message(c.id,'slow stream crash marker',id);expect((await snapshot(c.id)).messages.filter(m=>m.role==='user')).toHaveLength(1);
},60000);
journey('Stop then process death preserves an attempted write and recovers by provider read without sending twice',async()=>{
 const c=await create();await message(c.id,'Create a calendar event for crash recovery');
 const waiting=await until(()=>snapshot(c.id),c=>c.messages.at(-1)?.operations?.at(-1)?.outcome==='prepared','prepared write');const op=waiting.messages.at(-1).operations.at(-1);const sent=h.sent.length;h.holdWrites=true;
 await request(`conversations/${c.id}/approvals`,{questionId:op.action.question_id,decision:'approve'});
 await until(()=>h.sent.length,n=>n===sent+1,'provider attempted write');await request(`conversations/${c.id}/cancel`,{turnId:waiting.activeTurn});
 await h.stop('SIGKILL');h.releaseWrites();await h.start();
 await message(c.id,'Create a calendar event with another pending approval');
 const newer=await until(()=>snapshot(c.id),c=>c.messages.at(-1)?.operations?.at(-1)?.outcome==='prepared','newer pending approval');
 const cancelledTurn=c=>c.messages.find(m=>m.id===waiting.activeTurn+'-assistant');
 const recovered=await until(()=>snapshot(c.id),c=>cancelledTurn(c).operations.at(-1).outcome==='confirmed','read-only write recovery',45000);
 expect(recovered.activeTurn).toBe(newer.activeTurn);expect(cancelledTurn(recovered).status).toBe('cancelled');expect(cancelledTurn(recovered).operations.at(-1).action.digest).toBe(op.action.digest);expect(h.sent).toHaveLength(sent+1);
 await request(`conversations/${c.id}/cancel`,{turnId:newer.activeTurn});
},60000);

journey('global tool pause fences chat tool admission while ordinary conversation and Stop remain available',async()=>{
 const control=paused=>fetch(h.documents+'/system/control',{method:'PATCH',headers,body:JSON.stringify({fields:{paused:{booleanValue:paused}}})});
 await control(true);
 try{
 const c=await create();await message(c.id,'ordinary conversation while paused');expect((await done(c.id)).messages.at(-1).status).toBe('completed');
 await message(c.id,'Read my calendar');expect((await done(c.id)).messages.at(-1).status).toBe('failed');
 const input=crypto.randomUUID();await message(c.id,'slow stream pause stop',input);await until(()=>snapshot(c.id),c=>c.messages.at(-1).text.length>5,'partial paused chat');expect((await request(`conversations/${c.id}/cancel`,{turnId:input})).messages.at(-1).status).toBe('cancelled');
 }finally{await control(false)}
},60000);

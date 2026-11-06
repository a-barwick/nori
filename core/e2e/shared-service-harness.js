import { spawn, spawnSync } from 'node:child_process';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:http';
import { tmpdir } from 'node:os';
import { resolve } from 'node:path';

const core = resolve(import.meta.dir, '..');
const wait = ms => new Promise(r => setTimeout(r, ms));
export async function until(read, accept, label, timeout=30000) {
 const deadline=Date.now()+timeout; let last;
 while(Date.now()<deadline){try{last=await read();if(accept(last))return last}catch(e){last=String(e)};await wait(80)}
 throw new Error(`Timeout ${label}: ${JSON.stringify(last).slice(0,1500)}`);
}
async function body(req){const chunks=[];for await(const c of req)chunks.push(c);return JSON.parse(Buffer.concat(chunks).toString()||'{}')}
const json=(res,status,value)=>{res.writeHead(status,{'content-type':'application/json'});res.end(JSON.stringify(value))};
export async function createHarness(options={}) {
 const emulator=options.emulator??process.env.FIRESTORE_EMULATOR_HOST;
 const temporal=options.temporal??process.env.TEMPORAL_ADDRESS;
 if(!/^127\.0\.0\.1:\d+$/.test(emulator??'')||!/^127\.0\.0\.1:\d+$/.test(temporal??''))throw new Error('Isolated loopback Firestore and Temporal required');
 const directory=options.directory??mkdtempSync(resolve(tmpdir(),'nori-shared-'));
 const database=options.database??`nori-shared-${crypto.randomUUID().slice(0,8)}`;
 const deployment=options.deployment??database;
 const requests=[],sent=[],events=new Map();let api,worker,apiLogs='';let holdWrites=false;const heldWrites=[];
 const server=createServer(async(req,res)=>{try{
 const url=new URL(req.url,'http://local');
 if(url.pathname==='/__requests')return json(res,200,requests);
 if(url.pathname==='/__sent')return json(res,200,sent);
 if(url.pathname==='/jev')return json(res,503,{error:'deterministic classifier unavailable'});
 if(url.pathname==='/token')return json(res,200,{access_token:'isolated',token_type:'Bearer',expires_in:3600});
 if(url.pathname.startsWith('/calendar/')){
 if(req.method==='POST'){const value=await body(req);sent.push(value);events.set(value.id,value);if(holdWrites){heldWrites.push(()=>json(res,200,value));return};return json(res,200,value)}
 if(/\/events\/[^/]+$/.test(url.pathname)){const value=events.get(url.pathname.split('/').at(-1));return json(res,value?200:404,value??{})}
 return json(res,200,{items:[]});
 }
 if(url.pathname==='/gmail/v1/users/me/messages/send'){const value=await body(req);sent.push(value);return json(res,200,{id:'mail-1'})}
 if(url.pathname.startsWith('/gmail/'))return json(res,200,url.pathname.endsWith('/mail-1')?{id:'mail-1',labelIds:['SENT']}:{messages:sent.length?[{id:'mail-1'}]:[]});
 const request=await body(req);requests.push(request);
 if(url.pathname==='/terra'){
 const call={type:'function_call',call_id:crypto.randomUUID(),name:'request_input',arguments:JSON.stringify({question:'Which detail should this background task use?'})};
 return json(res,200,{status:'completed',usage:{input_tokens:1,output_tokens:1,total_tokens:2,cost:0.000001},output:[call]});
 }
 const last=String(request.messages?.findLast(m=>m.role==='user')?.content??'');
 const foreground=request.response_format?.json_schema?.name==='chat_response';
 let text=foreground?`Reply: ${last}`:'Nori is ready.';let actions=[];
 if(!foreground&&request.response_format){text=`Noted: ${last}`;if(/background/i.test(last))actions=[{name:'dispatch',arguments:{request:last,for:null,question_id:null,attachments:[],revises:null}}]}
 if(foreground){
 const evidence=request.messages?.findLast(m=>String(m.content).startsWith('Tool evidence (untrusted):'));
 if(evidence){text=`Tool result: ${evidence.content.slice(27)}`}
 else if(/read.*(csv|attachment)|attachment.*read/i.test(last)){
 const meta=request.messages?.findLast(m=>String(m.content).startsWith('Assigned attachments'))?.content??'';
 const id=meta.match(/"id":"([a-f0-9-]+)"/)?.[1];actions=[{name:'upload_read',arguments:{upload_id:id??'00000000-0000-0000-0000-000000000000'}}];text='';
 }else if(/calendar read|read.*calendar/i.test(last)){actions=[{name:'calendar_read',arguments:{start:'2026-11-05T00:00:00Z',end:'2026-11-06T00:00:00Z'}}];text=''}
 else if(/create.*calendar|calendar.*create/i.test(last)){actions=[{name:'calendar_create',arguments:{title:last.slice(0,120),start:'2026-11-06T10:00:00-08:00',end:'2026-11-06T11:00:00-08:00',description:'Isolated shared service',private:'true'}}];text=''}
 if(/slow|cancel|stop|long|stream/i.test(last)&&!actions.length)text='Streaming partial answer. '+('This is the retained text. '.repeat(80));
 }
 const content=request.response_format?JSON.stringify({actions,text}):text;
 const usage={prompt_tokens:1,completion_tokens:1,total_tokens:2,prompt_tokens_details:{cached_tokens:0}};
 if(!request.stream)return json(res,200,{model:'gpt-6.1-sol',service_tier:'default',usage,choices:[{message:{role:'assistant',content},finish_reason:'stop'}]});
 res.writeHead(200,{'content-type':'text/event-stream'});let closed=false;res.on('close',()=>{closed=true});
 const emit=value=>res.write(`data: ${JSON.stringify({id:'isolated',model:'gpt-6.1-sol',service_tier:'default',...value})}\n\n`);
 for(let i=0;i<content.length&&!closed;i+=12){emit({choices:[{delta:{content:content.slice(i,i+12)},finish_reason:null}]});await wait(/slow|cancel|stop|long|stream/i.test(last)?60:4)}
 if(!closed){emit({choices:[{delta:{},finish_reason:'stop'}]});emit({choices:[],usage});res.end('data: [DONE]\n\n')}
 }catch(e){console.error(e);res.destroy()}});
 await new Promise(done=>server.listen(0,'127.0.0.1',done));const provider=`http://127.0.0.1:${server.address().port}`;
 const port=options.port??(22000+Math.floor(Math.random()*15000));const address=`http://127.0.0.1:${port}`;
 writeFileSync(resolve(directory,'empty.env'),'');
 const env={PATH:process.env.PATH,HOME:process.env.HOME,NORI_ENV_FILE:resolve(directory,'empty.env'),NORI_PROJECT_ID:'bot-111549',FIRESTORE_DATABASE_ID:database,FIRESTORE_EMULATOR_HOST:emulator,NORI_LOCAL_ARTIFACT_DIR:resolve(directory,'files'),PORT:String(port),TEMPORAL_ADDRESS:temporal,TEMPORAL_NAMESPACE:process.env.TEMPORAL_NAMESPACE??'default',TEMPORAL_LOCAL:'true',TEMPORAL_TASK_QUEUE:deployment,TEMPORAL_DEPLOYMENT_NAME:deployment,TEMPORAL_BUILD_ID:'shared-service',NORI_EXECUTION_MODE:'temporal',COMPUTER_ZONE:'invalid-e2e',COMPUTER_INSTANCE:'no-computer',OPENAI_API_KEY:'isolated',OPENROUTER_API_KEY:'isolated',JEV_API_KEY:'isolated',NORI_E2E_OPENAI_URL:provider+'/openai',NORI_E2E_MODEL_URL:provider+'/terra',NORI_E2E_JEV_URL:provider+'/jev',NORI_E2E_GOOGLE_URL:provider,GOOGLE_CALENDAR_CLIENT_ID:'isolated',GOOGLE_CALENDAR_CLIENT_SECRET:'isolated',GOOGLE_CALENDAR_REFRESH_TOKEN:'isolated',GOOGLE_CALENDAR_ACCOUNT:'owner@example.test',GOOGLE_CALENDAR_WRITE_ID:'primary',NORI_NOTIFY_EMAIL:'off',...options.env};
 if(options.realProviders){delete env.NORI_E2E_OPENAI_URL;delete env.NORI_E2E_MODEL_URL;delete env.NORI_E2E_JEV_URL;delete env.NORI_E2E_GOOGLE_URL}
 const documents=`http://${emulator}/v1/projects/bot-111549/databases/${database}/documents`;
 const control=await fetch(documents+'/system/control',{method:'PATCH',headers:{'content-type':'application/json'},body:JSON.stringify({fields:{paused:{booleanValue:false}}})});if(!control.ok)throw new Error('seed control failed');
 const binary=options.binary??resolve(directory,'nori-local');
 if(!options.binary){const built=spawnSync('go',['build','-o',binary,'./cmd/nori-local'],{cwd:core,env:{...process.env,GOCACHE:process.env.GOCACHE??'/private/tmp/nori-shared-gocache'},encoding:'utf8'});if(built.status!==0)throw new Error(built.stderr)}
 async function start(binaryOverride=binary){apiLogs='';api=spawn(binaryOverride,[],{cwd:core,env,stdio:['ignore','pipe','pipe']});api.stdout.on('data',b=>apiLogs+=b);api.stderr.on('data',b=>apiLogs+=b);api.exited=new Promise(done=>api.once('exit',done));await until(()=>fetch(address+'/healthz').then(r=>r.ok),x=>x,'API readiness');return api}
 async function stop(signal='SIGTERM'){if(api&&api.exitCode===null){api.kill(signal);await api.exited}}
 await start();
 async function startWorker(){
  const workerBinary=resolve(directory,'nori-worker');const built=spawnSync('go',['build','-o',workerBinary,'./cmd/nori-worker'],{cwd:core,env:{...process.env,GOCACHE:process.env.GOCACHE??'/private/tmp/nori-shared-gocache'},encoding:'utf8'});if(built.status!==0)throw new Error(built.stderr);
  const workerEnv={...env};delete workerEnv.PORT;worker=spawn(workerBinary,[],{cwd:core,env:workerEnv,stdio:'ignore'});worker.exited=new Promise(done=>worker.once('exit',done));
  const cli=(...args)=>{const result=spawnSync('temporal',['--address',temporal,'--namespace',env.TEMPORAL_NAMESPACE,'--output','json',...args],{encoding:'utf8',timeout:10000});if(result.status!==0)throw new Error(result.stderr);return result.stdout};
  await until(()=>cli('worker','deployment','describe-version','--deployment-name',deployment,'--build-id','shared-service'),()=>true,'worker registration',60000);
  cli('worker','deployment','set-current-version','--deployment-name',deployment,'--build-id','shared-service','--yes');return worker;
 }
 async function stopWorker(){if(worker&&worker.exitCode===null){worker.kill('SIGTERM');await worker.exited};worker=null}
 async function close(){await stop();await stopWorker();server.closeAllConnections();await new Promise(done=>server.close(done))}
 return {address,provider,database,documents,directory,env,requests,sent,events,start,stop,startWorker,stopWorker,close,set holdWrites(value){holdWrites=value},releaseWrites(){holdWrites=false;for(const send of heldWrites.splice(0))send()},get logs(){return apiLogs},get api(){return api},binary};
}
if(import.meta.main){const h=await createHarness({directory:process.env.NORI_SHARED_DIRECTORY,binary:process.env.NORI_SHARED_BINARY,database:process.env.NORI_SHARED_DATABASE,port:Number(process.env.NORI_SHARED_PORT)||undefined});if(process.env.NORI_SHARED_WORKER==='1')await h.startWorker();console.log(JSON.stringify({address:h.address,provider:h.provider,database:h.database,directory:h.directory,binary:h.binary}));process.on('SIGTERM',async()=>{await h.close();process.exit(0)});process.on('SIGINT',async()=>{await h.close();process.exit(0)})}

// M27.05 browser pass (maintainer-run, macOS only): the real hclaro Hub with
// --experimental-hub-execution and real Terraform and OpenTofu, offline. Hclaro and the engines
// run under sandbox-exec (scripts/qualify/sandbox.sb) with an allowlisted environment
// (internal/qualify/env.go). Chromium runs outside the sandbox and may reach only the Hub's
// loopback origin. Two projects (fixtures/qualify/hub-hold) are registered with an explicit
// engine preference (terraform, tofu), as the Add project dialog does. In each default workspace
// the UI runs a plan, reviews it and applies it (H1, H2 A1). Screenshots go to OUT; they show
// /tmp paths, so they stay in the evidence folder and are never committed.
// Usage: TF=<OD-1 terraform> TOFU=<OD-1 tofu> MIRROR=<mirror dir> OUT=<evidence dir> \
//   node scripts/qualify/browser-hub-real.mjs [web/node_modules]
import assert from 'node:assert/strict'
import crypto from 'node:crypto'
import fs from 'node:fs/promises'
import path from 'node:path'
import {execFileSync, spawn} from 'node:child_process'
import {pathToFileURL, fileURLToPath} from 'node:url'

const qualify=path.dirname(fileURLToPath(import.meta.url))
const repo=path.resolve(qualify,'../..')
const modules=path.resolve(process.argv[2]||path.join(repo,'web/node_modules'))
const {TF,TOFU,MIRROR,OUT}=process.env
assert(process.platform==='darwin','sandbox-exec isolation is macOS only')
assert(TF&&TOFU&&MIRROR&&OUT,'TF, TOFU, MIRROR and OUT are required')
// OD-1 binaries (internal/qualify/realengine_main_test.go): anything else is refused.
const od1={
 'a4ac9d8302cf1b929d468ad44f40fad9e61eb4ea7a5e6c8ad234d7f02d71115f':'terraform 1.5.5','1ed224ce22941b1a7bc4f291cdafff44c039aaa9aaaac836d1f215fac6d21dc5':'terraform 1.5.7',
 '907c58c510e981f70d625d505a2a13d8c3ff6a23a7fefbb3927be5abf47af92f':'terraform 1.17.2','aacd317bc57d5abeb9d606ff5fcab2e15bd7a8f8c6a198004476df008c8eda31':'tofu 1.6.3',
 '93c5a8e1d523bae0f33e8d51fcedec364b72eb622eb1b0cec8990a854b0dbad4':'tofu 1.13.9',
}
const sha=async p=>crypto.createHash('sha256').update(await fs.readFile(p)).digest('hex')
const engines={}
for(const [name,src] of [['terraform',TF],['tofu',TOFU]]){
 const sum=await sha(src)
 assert(od1[sum]?.startsWith(name+' '),`${name}: ${sum} is not an OD-1 binary`)
 engines[name]={sum,version:od1[sum].split(' ')[1]}
}
const home=await fs.realpath(process.env.HOME)
const run=await fs.realpath(await fs.mkdtemp('/tmp/hclaro-browser-'))
assert(!run.startsWith(home+'/'),'run dir under the home directory')
const out=path.resolve(OUT)
await fs.mkdir(out,{recursive:true})
const canary='HCLARO-QUAL-CANARY-'+crypto.randomBytes(8).toString('hex')
const record={startedAt:new Date().toISOString(),engines,steps:[],flag:'--experimental-hub-execution'}
let server
try{
 for(const d of ['bin','home','tmp','config','projects']) await fs.mkdir(path.join(run,d),{recursive:true})
 await fs.copyFile(path.join(repo,'bin/hclaro'),path.join(run,'hclaro'))
 await fs.copyFile(TF,path.join(run,'bin/terraform'))
 await fs.copyFile(TOFU,path.join(run,'bin/tofu'))
 await fs.copyFile(path.join(qualify,'sandbox.sb'),path.join(run,'sandbox.sb'))
 for(const family of ['terraform','tofu']) await fs.cp(path.join(MIRROR,family),path.join(run,'mirror',family),{recursive:true})
 const tfrc=(await fs.readFile(path.join(qualify,'hub.tfrc'),'utf8')).replaceAll('@MIRROR@',path.join(run,'mirror'))
 await fs.writeFile(path.join(run,'hub.tfrc'),tfrc)
 record.hubTfrcSha256=await sha(path.join(qualify,'hub.tfrc'))
 const proxy='http://127.0.0.1:9'
 const env={PATH:`${run}/bin:/usr/bin:/bin`,HOME:`${run}/home`,TMPDIR:`${run}/tmp`,XDG_CONFIG_HOME:`${run}/home/.config`,XDG_CACHE_HOME:`${run}/home/.cache`,
  HCLARO_CONFIG_DIR:`${run}/config`,TF_CLI_CONFIG_FILE:`${run}/hub.tfrc`,CHECKPOINT_DISABLE:'1',LANG:'C.UTF-8',LC_ALL:'C.UTF-8',TZ:'UTC',HTTPS_PROXY:proxy,HTTP_PROXY:proxy}
 const sandboxed=(cmd,args,cwd)=>['sandbox-exec',['-D',`REAL_HOME=${home}`,'-D',`RUN_TMP=${run}`,'-f',path.join(run,'sandbox.sb'),'/usr/bin/env','-i',...Object.entries(env).map(([k,v])=>`${k}=${v}`),cmd,...args],{cwd}]
 execFileSync('go',['build','-o',path.join(run,'netprobe'),'./internal/qualify/netprobe'],{cwd:repo,env:{...process.env,CGO_ENABLED:'0'}})
 const probe=async tag=>{
  const [c,a,o]=sandboxed(path.join(run,'netprobe'),['isolated','-read',path.join(home,'hclaro-qualify/home-read-canary')],run)
  const json=JSON.parse(execFileSync(c,a,o).toString())
  await fs.writeFile(path.join(out,`netprobe-isolated-${tag}.json`),JSON.stringify(json,null,1))
  const ok=json.checks.filter(k=>'expected' in k).every(k=>k.ok)&&json.grandchild.checks.length>0&&json.grandchild.checks.filter(k=>'expected' in k).every(k=>k.ok)
  record[`isolated${tag}`]=ok
  assert(ok,`netprobe isolated ${tag}: not enforced`)
 }
 await probe('Before')
 const projects={}
 for(const name of ['terraform','tofu']){
  const dir=path.join(run,'projects','p-'+name)
  await fs.cp(path.join(repo,'fixtures/qualify/hub-hold'),dir,{recursive:true})
  // The plan dialog sends no var files; terraform.tfvars is read by the engine itself.
  await fs.writeFile(path.join(dir,'terraform.tfvars'),(await fs.readFile(path.join(dir,'launch.tfvars'),'utf8')).replaceAll('@CANARY@',canary))
  for(const f of ['launch.tfvars','hold.tfvars']) await fs.rm(path.join(dir,f))
  const [c,a,o]=sandboxed(path.join(run,'bin',name),['init','-input=false','-no-color'],dir)
  execFileSync(c,a,{...o,stdio:'ignore'})
  projects[name]=dir
 }
 const [c,a,o]=sandboxed(path.join(run,'hclaro'),['--hub','--no-browser','--experimental-hub-execution'],path.join(run,'home'))
 server=spawn(c,a,o)
 let log=''
 server.stdout.on('data',d=>log+=d); server.stderr.on('data',d=>log+=d)
 let url
 for(const deadline=Date.now()+30000;!url&&Date.now()<deadline;await new Promise(r=>setTimeout(r,100))) url=/http:\/\/127\.0\.0\.1:\d+\/\S*#session=[0-9a-f]+/.exec(log)?.[0]
 assert(url,'Hub did not print its session URL')
 const origin=new URL(url).origin, token=/#session=([0-9a-f]+)/.exec(url)[1]
 // Loopback inside the sandbox may refuse the first connects (F0-LB): retry for 5 s.
 const api=async(method,p,body)=>{
  for(const deadline=Date.now()+5000;;){
   try{
    const headers={Authorization:`Bearer ${token}`}
    if(method!=='GET') Object.assign(headers,{Origin:origin,'Content-Type':'application/json','X-Hclaro-CSRF':csrf})
    const r=await fetch(origin+'/api/v1/'+p,{method,headers,body:method==='GET'?undefined:JSON.stringify(body)})
    return {status:r.status,body:await r.json()}
   }catch(e){if(Date.now()>deadline) throw e; await new Promise(r=>setTimeout(r,100))}
  }
 }
 let csrf=''
 csrf=(await api('GET','session')).body.csrfToken
 const ids={}
 for(const name of ['terraform','tofu']){
  const r=await api('POST','projects',{path:projects[name],alias:'p-'+name,engine:name})
  assert.equal(r.status,201,`register p-${name}`)
  ids[name]=r.body.id
 }

 const {chromium}=await import(pathToFileURL(path.join(modules,'playwright/index.mjs')))
 const browser=await chromium.launch({headless:true,args:['--no-sandbox','--disable-background-networking','--disable-component-update']})
 const context=await browser.newContext({viewport:{width:1512,height:1050},colorScheme:'dark',reducedMotion:'reduce'})
 await context.route('**/*',route=>new URL(route.request().url()).origin===origin?route.continue():route.abort())
 const page=await context.newPage()
 const errors=[]
 page.on('pageerror',e=>errors.push(e.message))
 try{
  await page.goto(url)
  for(const name of ['terraform','tofu']){
   const alias='p-'+name
   await page.goto(`${origin}/projects/${ids[name]}/workspaces/default/plans`)
   const runPlan=page.getByRole('button',{name:'Run plan'}).first()
   await runPlan.waitFor({timeout:60000})
   await page.waitForFunction(()=>[...document.querySelectorAll('button')].some(b=>b.textContent.includes('Run plan')&&!b.disabled),null,{timeout:60000})
   await runPlan.click()
   const preflight=page.getByRole('dialog',{name:'Run a plan'})
   const uses=preflight.getByLabel('This plan will use')
   await uses.waitFor()
   const usesText=await uses.innerText()
   assert(usesText.includes(alias)&&usesText.includes('default'),`${name}: preflight does not name ${alias}/default`)
   await page.screenshot({path:path.join(out,`h1-${name}-plan-preflight.png`),fullPage:true})
   await preflight.getByRole('button',{name:'Run plan'}).click()
   await page.getByRole('button',{name:'Review and apply…'}).waitFor({timeout:300000})
   const body=await page.locator('main').innerText()
   assert(body.includes(engines[name].version),`${name}: plan view does not show engine version ${engines[name].version}`)
   await page.screenshot({path:path.join(out,`h2-${name}-plan-review.png`),fullPage:true})
   await page.getByRole('button',{name:'Review and apply…'}).click()
   const dialog=page.getByRole('dialog',{name:new RegExp(`^Apply to ${alias} / default$`)})
   await dialog.waitFor()
   for(const box of await dialog.getByRole('checkbox').all()) await box.check()
   const phrase=dialog.getByLabel('Confirmation phrase')
   if(await phrase.count()) await phrase.fill('apply default')
   await page.screenshot({path:path.join(out,`h2-${name}-apply-dialog.png`),fullPage:true})
   await dialog.getByRole('button',{name:'Apply plan'}).click()
   // Applied when the state holds both objects (a file read, no engine call).
   let state
   for(const deadline=Date.now()+300000;Date.now()<deadline;await new Promise(r=>setTimeout(r,500))){
    try{state=JSON.parse(await fs.readFile(path.join(projects[name],'terraform.tfstate'),'utf8'))}catch{}
    if(state?.resources?.length===2) break
   }
   assert.equal(state?.resources?.length,2,`${name}: state after the UI apply`)
   await page.waitForTimeout(1000)
   await page.screenshot({path:path.join(out,`h2-${name}-applied.png`),fullPage:true})
   const pageText=await page.content()
   assert(!pageText.includes(canary),`${name}: canary in the page`)
   record.steps.push({project:alias,engine:name,version:engines[name].version,preflight:'names project and workspace',plan:'reviewed',apply:'state holds 2 objects'})
  }
  assert.deepEqual(errors,[])
 }catch(error){await page.screenshot({path:path.join(out,'failure.png'),fullPage:true}).catch(()=>{});throw error}
 finally{await browser.close()}
 server.kill('SIGINT')
 await new Promise(r=>server.once('exit',r))
 assert(!log.includes(canary),'canary in the server output')
 for(const f of await fs.readdir(path.join(run,'config'),{recursive:true})){
  const p=path.join(run,'config',f)
  if((await fs.stat(p)).isFile()) assert(!(await fs.readFile(p,'utf8')).includes(canary),`canary in config/${f}`)
 }
 await probe('After')
 record.result='PASS'
 console.log('M27.05 browser pass: PASS (H1, H2 A1 through the UI with terraform and tofu)')
}catch(error){record.result='FAIL'; record.error=String(error.message||error).replaceAll(run,'<RUN>').replaceAll(home,'<HOME>'); throw error}
finally{
 if(server&&server.exitCode===null) server.kill('SIGKILL')
 record.finishedAt=new Date().toISOString()
 await fs.writeFile(path.join(out,'browser.json'),JSON.stringify(record,null,1))
 await fs.rm(run,{recursive:true,force:true})
}

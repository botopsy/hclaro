// Runs real browser UI + the real Go HTTP handler, using only synthetic state.
// All requests are intercepted; no TCP listener, remote network, or real IaC CLI.
import assert from 'node:assert/strict'
import fs from 'node:fs/promises'
import path from 'node:path'
import {execFileSync} from 'node:child_process'
import {pathToFileURL, fileURLToPath} from 'node:url'
process.chdir(path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..'))
const modules=path.resolve(process.argv[2]||'web/node_modules')
await fs.mkdir('.cache',{recursive:true})
const {chromium}=await import(pathToFileURL(path.join(modules,'playwright/index.mjs')))
const browser=await chromium.launch({headless:true,...(process.env.HCLARO_CHROME ? {executablePath:process.env.HCLARO_CHROME} : {}),args:['--no-sandbox','--disable-background-networking','--disable-component-update']})
const token='a'.repeat(64), host='http://127.0.0.1:9273', canary='DO_NOT_LEAK_HCLARO_TEST_SECRET'
const errors=[],consoleOutput=[],external=[]
let large=false
const context=await browser.newContext({viewport:{width:1512,height:1050},colorScheme:'dark',reducedMotion:'reduce'})
await context.grantPermissions(['clipboard-read','clipboard-write'],{origin:host})
await context.route('**/*',async route=>{
 const req=route.request(),url=new URL(req.url())
 if(url.origin!==host){external.push(url.origin);await route.abort();return}
 const response=JSON.parse(execFileSync(path.resolve('bin/fixture-http'+(process.platform==='win32'?'.exe':'')),{input:JSON.stringify({Method:req.method(),Path:url.pathname+url.search,Headers:req.headers()}),env:{...process.env,HCLARO_FIXTURE_LARGE:large?'1':''},maxBuffer:8<<20}))
 const body=Buffer.from(response.body,'base64')
 if(url.pathname.startsWith('/api/')) assert(!body.includes(Buffer.from(canary)),'secret crossed actual API boundary')
 const headers=Object.fromEntries(Object.entries(response.headers).map(([k,v])=>[k,v.join(', ')]))
 await route.fulfill({status:response.status,headers,body})
})
const page=await context.newPage()
page.on('pageerror',error=>errors.push(error.message))
page.on('console',message=>consoleOutput.push(message.text()))
try{
 await page.goto(`${host}/#session=${token}`)
 await page.getByRole('button',{name:/artifacts.*aws_s3_bucket/}).waitFor()
 assert(!(await page.locator('body').innerText()).includes(canary))
 await page.getByRole('button',{name:/artifacts.*aws_s3_bucket/}).click()
 await page.getByLabel('Resource inspector').getByText('redacted',{exact:true}).first().waitFor()
 await page.getByRole('button',{name:'Copy resource address',exact:true}).click()
 assert.equal(await page.evaluate(()=>navigator.clipboard.readText()),'aws_s3_bucket.artifacts')
 await page.getByRole('button',{name:'Copy redacted JSON',exact:true}).click()
 assert(!(await page.evaluate(()=>navigator.clipboard.readText())).includes(canary))
 await fs.mkdir('docs/screenshots',{recursive:true})
 await page.screenshot({path:'docs/screenshots/resources-dark.png',fullPage:true})
 await page.getByRole('button',{name:'Close inspector',exact:true}).click()
 await page.getByLabel('Search resources',{exact:true}).fill('quote')
 await page.getByRole('button',{name:/node.*aws_instance/}).waitFor()
 await page.getByRole('button',{name:/node.*aws_instance/}).click()
 await page.getByLabel('Resource inspector').getByRole('heading',{name:'node',exact:true}).waitFor()
 await page.keyboard.press('Escape')
 await page.getByRole('button',{name:'Reset',exact:true}).click()
 await page.getByLabel('Filter resource mode').selectOption('data')
 await page.getByRole('button',{name:/current.*aws_caller_identity/}).waitFor()
 await page.getByRole('button',{name:'Reset',exact:true}).click()
 await page.getByRole('button',{name:'Overview',exact:true}).click()
 await page.getByRole('heading',{name:'Workspace overview'}).waitFor()
 await page.screenshot({path:'docs/screenshots/overview-dark.png',fullPage:true})
 await page.getByRole('button',{name:'Settings',exact:true}).click()
 await page.getByRole('button',{name:'light',exact:true}).click()
 assert.equal(await page.locator('html').getAttribute('data-theme'),'light')
 await page.getByRole('button',{name:'Resources',exact:true}).click()
 await page.setViewportSize({width:820,height:1180})
 await page.screenshot({path:'docs/screenshots/resources-tablet-light.png',fullPage:true})
 assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth),true,'tablet horizontal overflow')
 // Exercise 1,000 resources through the same handler; rendering stays bounded to 50 rows.
 large=true
 await page.reload()
 await page.getByRole('button',{name:/app.*aws_instance/}).first().waitFor()
 const before=Date.now()
 await page.getByLabel('Search resources',{exact:true}).fill('app[999]')
 await page.getByRole('button',{name:/app.*999.*aws_instance/}).waitFor()
 assert(Date.now()-before<3000,'search took more than three seconds')
 assert.equal(await page.locator('tbody tr').count(),1)
 assert(!consoleOutput.join('\n').includes(canary),'secret in browser logs')
 assert.equal(external.length,0,'external runtime request')
 assert.deepEqual(errors,[],'browser errors')
 console.log('PASS: resource rendering, actual Go API redaction, inspector, copy, quoted keys, filters, overview, themes, tablet layout, 1,000-resource search, no external requests.')
}catch(error){await page.screenshot({path:'.cache/browser-failure.png',fullPage:true}).catch(()=>{});throw error}
finally{await browser.close()}

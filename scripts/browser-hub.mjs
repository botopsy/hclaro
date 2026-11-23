// Hub workspace browser check: runs the real hclaro binary in Hub mode on 127.0.0.1 with the
// fake Terraform/OpenTofu CLIs (bin/mock) and the synthetic projects from scripts/hub-demo, in
// an isolated config directory. No real engine, provider, backend or registry is used.
// Checks the workspace context bar never makes the page scroll sideways (M24.26) and captures
// the Hub workspace and Tools screenshots.
// Usage: node scripts/browser-hub.mjs [web/node_modules]
import assert from 'node:assert/strict'
import fs from 'node:fs/promises'
import path from 'node:path'
import {execFileSync} from 'node:child_process'
import {fileURLToPath} from 'node:url'
import {launchBrowser, openPage, startHclaro, stop} from '../tests/e2e/lib.mjs'
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'..')
process.chdir(root)
const modules=path.resolve(process.argv[2]||'web/node_modules')
const exe=process.platform==='win32'?'.exe':''
const demo=path.resolve('.cache/hub-browser')
await fs.rm(demo,{recursive:true,force:true})
execFileSync('go',['run','./scripts/hub-demo',demo],{stdio:'inherit'})
const registry=JSON.parse(await fs.readFile(path.join(demo,'config/projects.json'),'utf8'))
const network=registry.projects.find(p=>p.alias==='network').id

const env={...process.env,PATH:`${path.resolve('bin/mock')}${path.delimiter}${process.env.PATH}`,HCLARO_CONFIG_DIR:path.join(demo,'config')}
for(const name of ['TF_WORKSPACE','TF_DATA_DIR','AWS_PROFILE','AWS_REGION','AWS_DEFAULT_REGION']) delete env[name]
// The session URL is a credential for this throwaway server; startHclaro never prints it.
const server=await startHclaro(path.resolve('bin/hclaro'+exe),['--hub'],env,'Hub')
let browser,page
const noOverflow=async label=>{
 for(const width of [1512,1280,1100]) {
  await page.setViewportSize({width,height:1050})
  const {scroll,inner}=await page.evaluate(()=>({scroll:document.documentElement.scrollWidth,inner:window.innerWidth}))
  assert(scroll<=inner,`${label}: page is ${scroll}px wide in a ${inner}px window`)
  // Scope and safety state stay visible at every width.
  const topbar=page.getByRole('banner',{name:'Top bar'})
  for(const control of [topbar.getByRole('button',{name:/Switch workspace/}),topbar.getByRole('button',{name:/^Profile: /}),topbar.getByRole('button',{name:'Read state again'})]) assert(await control.first().isVisible(),`${label} at ${width}px: top bar control hidden`)
 }
 await page.setViewportSize({width:1512,height:1050})
}
try{
 browser=await launchBrowser(modules,['--no-sandbox'])
 const opened=await openPage(browser,server.url,{viewport:{width:1512,height:1050},colorScheme:'dark',reducedMotion:'reduce'})
 page=opened.page
 const {origin,errors}=opened
 await page.goto(`${origin}/projects/${network}/workspaces/dev`)
 await page.getByRole('banner',{name:'Top bar'}).getByRole('button',{name:/Switch workspace/}).waitFor()
 assert.equal(await page.getByRole('banner',{name:'Top bar'}).getByRole('button',{name:/Switch workspace/}).locator('strong').textContent(),'dev')
 await page.getByRole('button',{name:/aws_db_instance\.dev_main/}).waitFor()
 await noOverflow('Hub workspace')
 await fs.mkdir('.cache/screenshots',{recursive:true})
 await page.screenshot({path:'.cache/screenshots/hub-workspace-dark.png',fullPage:true})

 await page.goto(`${origin}/projects/${network}/workspaces/prod/tools`)
 for(const [tool,shot] of [['Format check','tools-fmt-dark.png'],['Outputs','tools-outputs-dark.png']]) {
  const card=page.locator('section.tool-card',{has:page.getByRole('heading',{name:tool,exact:true})})
  await card.getByRole('button',{name:/^Run/}).click()
  await page.getByLabel(`${tool} result`,{exact:true}).waitFor()
  await noOverflow(`Tools ${tool}`)
  // Clicking Run scrolls the card into view; a full-page capture draws the sticky sidebar at that offset.
  await page.evaluate(()=>{document.activeElement?.blur();window.scrollTo(0,0)})
  await page.screenshot({path:`.cache/screenshots/${shot}`,fullPage:true})
 }
 assert.deepEqual(errors,[])
 console.log('Hub browser check passed: no horizontal overflow at 1512, 1280 and 1100px.')
}catch(error){await page?.screenshot({path:'.cache/browser-failure.png',fullPage:true}).catch(()=>{});throw error}
finally{await browser?.close();await stop(server)}

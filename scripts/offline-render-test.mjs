// Supplemental real-React render checks for a cache-only environment; not a Vitest substitute.
import assert from 'node:assert/strict'
import fs from 'node:fs/promises'
import path from 'node:path'
import {createRequire} from 'node:module'
import {execFileSync} from 'node:child_process'
import {pathToFileURL} from 'node:url'
const modules=path.resolve(process.argv[2]||'web/node_modules')
const require=createRequire(path.join(path.dirname(modules),'package.json'))
const {build}=await import(pathToFileURL(path.join(modules,'esbuild/lib/main.js')))
const temp=path.resolve('.cache/offline-preview')
await fs.mkdir(temp,{recursive:true})
const entry=path.join(temp,'render.jsx')
await fs.writeFile(entry,`import React from 'react';import {renderToStaticMarkup} from 'react-dom/server';import App from '../../web/src/App';import {JsonTree} from '../../web/src/components/JsonTree';import {QueryClient,QueryClientProvider} from './query.jsx';
export function render(workspace,resources){const client=new QueryClient();const value=data=>({data,pending:false,error:null,listeners:new Set(),running:false});client.entries.set(JSON.stringify(['workspace']),value(workspace));client.entries.set(JSON.stringify(['resources','query=&provider=&type=&mode=&sort=asc&offset=0&limit=50']),value(resources));return renderToStaticMarkup(<QueryClientProvider client={client}><App/></QueryClientProvider>)}
export function json(value){return renderToStaticMarkup(<JsonTree value={value}/>)}
`)
await build({entryPoints:[entry],bundle:true,platform:'node',format:'cjs',jsx:'automatic',outfile:path.join(temp,'render.cjs'),nodePaths:[modules],plugins:[{name:'cached-render',setup(b){
 b.onResolve({filter:/^react($|\/)|^react-dom($|\/)/},args=>({path:require.resolve(args.path),external:true}))
 b.onResolve({filter:/^@tanstack\/react-query$/},()=>({path:path.join(temp,'query.jsx')}))
 b.onResolve({filter:/components\/ui\/button$/},()=>({path:path.join(temp,'button.jsx')}))
}}]})
const {render,json}=require(path.join(temp,'render.cjs'))
const request=(Path,large=false)=>{const response=JSON.parse(execFileSync(path.resolve('bin/fixture-http'+(process.platform==='win32'?'.exe':'')),{input:JSON.stringify({Method:'GET',Path,Headers:{Authorization:'Bearer '+'a'.repeat(64)}}),env:{...process.env,HCLARO_FIXTURE_LARGE:large?'1':''}}));assert.equal(response.status,200);return JSON.parse(Buffer.from(response.body,'base64').toString())}
const workspace=request('/api/v1/workspace'),resources=request('/api/v1/resources')
const markup=render(workspace,resources)
assert(markup.includes('Resource explorer') && markup.includes('316') && markup.includes('aws_s3_bucket.artifacts'))
assert.equal((markup.match(/data-resource-id=/g)||[]).length,50)
for(const r of resources.resources){assert(!json(request('/api/v1/resources/'+r.id).attributes).includes('DO_NOT_LEAK_HCLARO_TEST_SECRET'))}
assert(render({...workspace,status:'empty'}, {resources:[],total:0}).includes('A clean slate'))
assert(render({...workspace,status:'error',diagnostic:{code:'uninitialized',message:'Run init manually.'}},resources).includes('Initialize this workspace first'))
assert(!json({id:'<img src=x onerror=alert(1)>'}).includes('<img'))
const largeWorkspace=request('/api/v1/workspace',true),largePage=request('/api/v1/resources',true)
assert.equal(largePage.total,1000)
assert.equal((render(largeWorkspace,largePage).match(/data-resource-id=/g)||[]).length,50)
const result=request('/api/v1/resources?query=app%5B999%5D',true)
assert.equal(result.total,1);assert.equal(result.resources[0].address,'aws_instance.app[999]')
console.log('PASS: real React SSR, actual Go API data, 50-row bound, redacted JSON, empty/error states, HTML escaping. Interactive/browser and production-query checks remain unverified.')

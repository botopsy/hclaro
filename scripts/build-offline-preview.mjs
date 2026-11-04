/**
 * Verification-only compiler for a network-restricted environment. The production
 * build remains npm ci + Vite. This preview explicitly substitutes query/button
 * adapters; it must not be described as a verified TanStack/shadcn production build.
 * Usage: node scripts/build-offline-preview.mjs /path/to/cached/node_modules
 */
import fs from 'node:fs/promises'
import path from 'node:path'
import {pathToFileURL} from 'node:url'
import {writeNotices} from './vendor-notices.mjs'
const modules = path.resolve(process.argv[2] || 'web/node_modules')
const root = process.cwd()
const output = path.join(root, 'internal/ui/dist')
const temp = path.join(root, '.cache/offline-preview')
await fs.mkdir(temp, {recursive:true})
await fs.mkdir(output, {recursive:true})
const query = `import React from 'react';
export class QueryClient { constructor() { this.entries = new Map() } }
const Context = React.createContext(null);
export function QueryClientProvider({client,children}) { return React.createElement(Context.Provider,{value:client},children) }
export function useQuery(options) {
 const client = React.useContext(Context);
 const key = JSON.stringify(options.queryKey);
 const [, render] = React.useReducer(x=>x+1,0);
 const latest = React.useRef(options); latest.current = options;
 if (!client.entries.has(key)) client.entries.set(key,{data:undefined,error:null,pending:true,listeners:new Set(),running:false});
 const entry = client.entries.get(key);
 async function fetchEntry() {
  if (entry.running) return;
  entry.running=true;
  try { entry.data=await latest.current.queryFn(); entry.error=null } catch(error) { entry.error=error }
  finally { entry.pending=false; entry.running=false; for(const notify of entry.listeners) notify() }
 }
 const fetchRef=React.useRef(fetchEntry);fetchRef.current=fetchEntry;
 React.useEffect(()=>{
  const notify=()=>render(); entry.listeners.add(notify);
  if(latest.current.enabled!==false && entry.pending) void fetchRef.current();
  const timer=setInterval(()=>{
   const interval=latest.current.refetchInterval;
   if(latest.current.enabled!==false && typeof interval==='function' && interval({state:{data:entry.data}})) void fetchRef.current();
  },700);
  return ()=>{entry.listeners.delete(notify);clearInterval(timer)}
 },[key,entry,options.enabled]);
 return {data:entry.data,error:entry.error,isPending:entry.pending,refetch:()=>fetchRef.current()};
}
`
const button = `import React from 'react';
export function Button({variant='default',size='default',className='',...props}) {return React.createElement('button',{className:['button','button-'+variant,size==='icon'?'button-icon':size==='small'?'button-small':'',className].join(' '),...props})}
`
await fs.writeFile(path.join(temp,'query.jsx'),query)
await fs.writeFile(path.join(temp,'button.jsx'),button)
const {build} = await import(pathToFileURL(path.join(modules,'esbuild/lib/main.js')))
await build({entryPoints:[path.join(root,'web/src/main.tsx')],bundle:true,minify:true,format:'esm',platform:'browser',target:['es2022'],jsx:'automatic',outfile:path.join(output,'app.js'),nodePaths:[modules],define:{'process.env.NODE_ENV':'"production"'},plugins:[{name:'offline-verification',setup(b){
 b.onResolve({filter:/^@tanstack\/react-query$/},()=>({path:path.join(temp,'query.jsx')}))
 b.onResolve({filter:/components\/ui\/button$/},()=>({path:path.join(temp,'button.jsx')}))
 b.onLoad({filter:/styles\.css$/},async args=>({contents:(await fs.readFile(args.path,'utf8')).replace('@import "tailwindcss";',''),loader:'css'}))
}}]})
await fs.writeFile(path.join(output,'index.html'),(await fs.readFile('web/index.html','utf8')).replace('<script type="module" src="/src/main.tsx"></script>','<link rel="stylesheet" href="/app.css"/><script type="module" src="/app.js"></script>'))
await fs.copyFile('web/public/favicon.svg',path.join(output,'favicon.svg'))
await fs.writeFile(path.join(output,'BUILD-INFO.txt'),'Offline verification bundle. React + Lucide compiled with cached esbuild. Local query and native button adapters substitute unavailable dependencies. Rebuild with npm ci and Vite before release.\n')
await writeNotices(modules,output,['react','react-dom','scheduler','lucide-react'])
console.log('Created an offline verification bundle; production Vite build remains unverified.')

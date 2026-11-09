// Writes a CycloneDX 1.5 SBOM for a release: the Go toolchain and modules compiled into the
// binary, and the npm packages bundled into the embedded frontend (the same list as
// THIRD-PARTY-NOTICES.txt), with versions, licenses and integrity hashes from
// web/package-lock.json. It holds no paths, hostnames or build-machine details, and its
// timestamp is the commit time, so the same commit gives the same file.
// Usage: node scripts/release-sbom.mjs <version> <output.json>
import fs from 'node:fs/promises'
import path from 'node:path'
import {execFileSync} from 'node:child_process'
import {fileURLToPath} from 'node:url'
import {bundledPackages} from './vendor-notices.mjs'

const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'..')
const [version,output]=process.argv.slice(2)
if(!/^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$/.test(version||'') || !output) {
 console.error('usage: node scripts/release-sbom.mjs <version> <output.json>'); process.exit(2)
}
const run=(cmd,args)=>execFileSync(cmd,args,{cwd:root,encoding:'utf8'}).trim()
const lock=JSON.parse(await fs.readFile(path.join(root,'web/package-lock.json'),'utf8'))
const purlName=name=>name.startsWith('@')?'%40'+name.slice(1):name

const components=[]
// Go: the toolchain (standard library) and every module except the main one.
const goVersion=run('go',['env','GOVERSION']).replace(/^go/,'')
components.push({'bom-ref':`pkg:golang/stdlib@${goVersion}`,type:'library',name:'stdlib',version:goVersion,description:'Go standard library and toolchain',purl:`pkg:golang/stdlib@${goVersion}`,licenses:[{license:{id:'BSD-3-Clause'}}]})
const modules=run('go',['list','-m','-f','{{if not .Main}}{{.Path}} {{.Version}}{{end}}','all']).split('\n').filter(Boolean)
for(const line of modules) {
 const [name,v]=line.split(' ')
 components.push({'bom-ref':`pkg:golang/${name}@${v}`,type:'library',name,version:v,purl:`pkg:golang/${name}@${v}`})
}
// npm packages bundled into internal/ui/dist.
for(const name of bundledPackages) {
 const entry=lock.packages[`node_modules/${name}`]
 if(!entry?.version) throw new Error(`${name} is not in web/package-lock.json`)
 const c={'bom-ref':`pkg:npm/${purlName(name)}@${entry.version}`,type:'library',name,version:entry.version,purl:`pkg:npm/${purlName(name)}@${entry.version}`}
 if(entry.license) c.licenses=[{license:{id:entry.license}}]
 else {
  const meta=JSON.parse(await fs.readFile(path.join(root,'web/node_modules',name,'package.json'),'utf8'))
  if(typeof meta.license==='string') c.licenses=[{license:{id:meta.license}}]
 }
 const integrity=/^sha512-(.+)$/.exec(entry.integrity||'')
 if(integrity) c.hashes=[{alg:'SHA-512',content:Buffer.from(integrity[1],'base64').toString('hex')}]
 components.push(c)
}

const timestamp=new Date(Number(run('git',['log','-1','--format=%ct']))*1000).toISOString()
const bom={
 bomFormat:'CycloneDX',specVersion:'1.5',version:1,
 metadata:{
  timestamp,
  tools:{components:[{type:'application',name:'release-sbom.mjs',version}]},
  component:{'bom-ref':`pkg:github/botopsy/hclaro@v${version}`,type:'application',name:'hclaro',version,purl:`pkg:github/botopsy/hclaro@v${version}`,licenses:[{license:{id:'Apache-2.0'}}]},
 },
 components,
 dependencies:[{ref:`pkg:github/botopsy/hclaro@v${version}`,dependsOn:components.map(c=>c['bom-ref'])}],
}
await fs.writeFile(output,JSON.stringify(bom,null,2)+'\n')
console.log(`SBOM: ${components.length} components -> ${output}`)

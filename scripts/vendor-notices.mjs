// Bundle the licenses of runtime dependencies with the embedded frontend.
import fs from 'node:fs/promises'
import path from 'node:path'
import {fileURLToPath} from 'node:url'
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'..')
// Runtime packages bundled into the embedded frontend. The release SBOM uses the same list.
export const bundledPackages=['react','react-dom','scheduler','lucide-react','tailwindcss','@tanstack/react-query','@tanstack/query-core','@radix-ui/react-slot','@radix-ui/react-compose-refs','class-variance-authority','clsx','tailwind-merge','@fontsource/jetbrains-mono']
export async function writeNotices(modules,output,packages) {
 let text='Hclaro frontend — third-party licenses\n\n'
 for(const name of packages) {
  const directory=path.join(modules,name)
  const metadata=JSON.parse(await fs.readFile(path.join(directory,'package.json'),'utf8'))
  const filename=(await fs.readdir(directory)).find(file=>/^licen[cs]e(?:\.|$)/i.test(file))
  // Some packages publish only a package.json license declaration; record it with the source repository.
  const declared=typeof metadata.license==='string'?metadata.license:''
  if(!filename && !declared) throw new Error(`Missing license for ${name}`)
  const repository=typeof metadata.repository==='string'?metadata.repository:metadata.repository?.url||''
  const body=filename?await fs.readFile(path.join(directory,filename),'utf8'):`License: ${declared} (declared in package.json; the published package ships no license file)\nSource: ${repository}\n`
  text+=`${name} ${metadata.version}\n${'='.repeat(60)}\n${body}\n\n`
 }
 await fs.writeFile(path.join(output,'THIRD-PARTY-NOTICES.txt'),text)
}
if(process.argv[1] && path.resolve(process.argv[1])===fileURLToPath(import.meta.url)) {
 await writeNotices(path.join(root,'web/node_modules'),path.join(root,'internal/ui/dist'),bundledPackages)
}

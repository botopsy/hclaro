// Bundle the licenses of runtime dependencies with the embedded frontend.
import fs from 'node:fs/promises'
import path from 'node:path'
import {fileURLToPath} from 'node:url'
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'..')
export async function writeNotices(modules,output,packages) {
 let text='Hclaro frontend — third-party licenses\n\n'
 for(const name of packages) {
  const directory=path.join(modules,name)
  const metadata=JSON.parse(await fs.readFile(path.join(directory,'package.json'),'utf8'))
  const filename=(await fs.readdir(directory)).find(file=>/^licen[cs]e(?:\.|$)/i.test(file))
  if(!filename) throw new Error(`Missing license for ${name}`)
  text+=`${name} ${metadata.version}\n${'='.repeat(60)}\n${await fs.readFile(path.join(directory,filename),'utf8')}\n\n`
 }
 await fs.writeFile(path.join(output,'THIRD-PARTY-NOTICES.txt'),text)
}
if(process.argv[1] && path.resolve(process.argv[1])===fileURLToPath(import.meta.url)) {
 await writeNotices(path.join(root,'web/node_modules'),path.join(root,'internal/ui/dist'),['react','react-dom','scheduler','lucide-react','tailwindcss','@tanstack/react-query','@tanstack/query-core','@radix-ui/react-slot','@radix-ui/react-compose-refs','class-variance-authority','clsx','tailwind-merge'])
}

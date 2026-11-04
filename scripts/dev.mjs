// Development always uses the synthetic CLI and project, never the user's infrastructure.
import {spawn} from 'node:child_process'
import path from 'node:path'
import readline from 'node:readline'
const root = process.cwd()
const vite = spawn(process.execPath, ['node_modules/vite/bin/vite.js'], {cwd:path.join(root,'web'),stdio:'inherit'})
const backend = spawn(path.join(root,'bin/hclaro'+(process.platform==='win32'?'.exe':'')), ['--engine','terraform','--path','fixtures/project','--port','9273','--no-browser'], {cwd:root,env:{...process.env,PATH:path.join(root,'bin/mock')+path.delimiter+process.env.PATH,TF_DATA_DIR:path.join(root,'fixtures/backend')},stdio:['ignore','pipe','inherit']})
readline.createInterface({input:backend.stdout}).on('line',line => console.log(line.replace('http://127.0.0.1:9273/','http://127.0.0.1:5173/')))
let stopping = false
function stop(code = 0) {
 if (stopping) return
 stopping = true
 backend.kill('SIGTERM'); vite.kill('SIGTERM'); process.exitCode = code
}
for (const child of [backend,vite]) {
 child.on('error', error => {console.error(error.message);stop(1)})
 child.on('exit', code => stop(code || 0))
}
process.on('SIGINT',()=>stop());process.on('SIGTERM',()=>stop())

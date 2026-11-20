// AI-01.11 saved conversations, end to end: the real hclaro binary on 127.0.0.1 and headless
// Chromium with a fake `claude` (--claude-path). Save a conversation, quit Hclaro, start it again on
// the same configuration folder, reopen and continue (the fake's stdin is the replayed transcript),
// then a planted canary hides a turn, and Delete removes the files. Each run gets its own HOME,
// config dir and PATH; the browser aborts every request outside the local origin.
// Usage: make binary bin/mock/terraform && node tests/e2e/saved-conversations.mjs [web/node_modules]
import assert from 'node:assert/strict'
import fs from 'node:fs/promises'
import os from 'node:os'
import path from 'node:path'
import {fileURLToPath} from 'node:url'
import {aiSwitchOn, launchBrowser, nav, openPage, skipOnWindows, startHclaro, stop} from './lib.mjs'

const repo = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..')
// shortcut: the fake assistant is a POSIX shell script; port it to a Go helper to run on Windows.
skipOnWindows('saved-conversations e2e', 'POSIX shell fakes only')
const work = await fs.mkdtemp(path.join(await fs.realpath(os.tmpdir()), 'hclaro-e2e-saved-'))
const report = path.join(repo, '.cache/e2e-saved-conversations')
await fs.mkdir(report, {recursive: true})
const modules = path.resolve(process.argv[2] || path.join(repo, 'web/node_modules'))
const results = []
const ok = msg => { results.push('PASS ' + msg); console.log('PASS', msg) }

const log = path.join(work, 'claude')
await fs.mkdir(path.join(log, 'turns'), {recursive: true})
const claude = path.join(work, 'claude.sh')
await fs.writeFile(claude, `#!/bin/sh
if [ "$1" = "--version" ]; then echo "2.1.327 (Claude Code)"; exit 0; fi
n=$(/bin/ls "${log}/turns" | /usr/bin/wc -l | /usr/bin/tr -d ' ')
/bin/cat > "${log}/turns/$n.txt"
printf '{"type":"result","is_error":false,"result":"Fake reply %s","session_id":"SESSION_CANARY","total_cost_usd":0.42}\\n' "$n"
`)
await fs.chmod(claude, 0o755)
const turns = async () => (await fs.readdir(path.join(log, 'turns'))).length
const turn = n => fs.readFile(path.join(log, 'turns', n + '.txt'), 'utf8')

const proj = path.join(work, 'proj')
await fs.cp(path.join(repo, 'fixtures/project'), proj, {recursive: true})
const config = path.join(work, 'config')
const home = path.join(work, 'home')
await fs.mkdir(home, {recursive: true})
const env = {PATH: `${repo}/bin/mock:/usr/bin:/bin`, HOME: home, TMPDIR: home, HCLARO_CONFIG_DIR: config, TF_DATA_DIR: path.join(repo, 'fixtures/backend'), LANG: 'en_US.UTF-8'}

const launch = () => startHclaro(path.join(repo, 'bin/hclaro'), ['--engine', 'terraform', '--path', proj, '--claude-path', claude], env)

const browser = await launchBrowser(modules)
const seen = []
async function open(url) {
  const opened = await openPage(browser, url)
  seen.push(opened)
  await opened.page.getByRole('navigation').first().waitFor()
  return opened.page
}
// AI is on by default (AI-01.14); the disclosure is accepted inline by the first Accept and send.
async function aiOn(page) {
  await aiSwitchOn(page)
  await nav(page, 'AI Assistant')
}
async function files() {
  const out = []
  const walk = async d => { for (const e of await fs.readdir(d, {withFileTypes: true}).catch(() => [])) { const p = path.join(d, e.name); if (e.isDirectory()) await walk(p); else if (e.name.endsWith('.json') && e.name !== 'settings.json') out.push(p) } }
  await walk(path.join(config, 'ai-conversations'))
  return out
}

let run
try {
  run = await launch()
  let page = await open(run.url)
  await aiOn(page)
  await page.getByLabel('Message').fill('How do moved blocks work?')
  await page.getByRole('button', {name: 'Accept and send'}).click()
  await page.getByText('Fake reply 0').waitFor()
  assert.deepEqual(await files(), [], 'an unsaved conversation was written')
  const box = page.getByRole('checkbox', {name: /Save conversation/})
  assert(!(await box.isChecked()), 'saved by default')
  await box.click()
  await waitChecked(box)
  const [file] = await files()
  assert(file, 'no file after Save')
  for (const [p, mode] of [[file, 0o600], [path.dirname(file), 0o700], [path.join(config, 'ai-conversations'), 0o700]]) assert.equal((await fs.stat(p)).mode & 0o777, mode, p)
  await page.getByLabel('Follow-up message').fill('And for modules?')
  await page.getByRole('button', {name: 'Send message'}).click()
  await page.getByText('Fake reply 1').waitFor()
  const stored = await fs.readFile(file, 'utf8')
  for (const never of ['SESSION_CANARY', 'total_cost', '0.42', claude, proj, home]) assert(!stored.includes(never), 'stored file contains ' + never)
  assert(stored.includes('Fake reply 1'))
  ok('Save is opt-in; file 0600 in a 0700 folder; no provider metadata, cost, argv or local paths stored')

  await stop(run)
  run = await launch()
  page = await open(run.url)
  await aiOn(page)
  const saved = page.getByRole('region', {name: 'Saved conversations of this project'})
  await saved.getByText('How do moved blocks work?').waitFor()
  await saved.getByRole('button', {name: 'Open'}).click()
  await page.getByText('Fake reply 1').waitFor()
  const before = await turns()
  await page.getByLabel('Follow-up message').fill('After a restart?')
  await page.getByRole('button', {name: 'Accept and send'}).click() // a new process: not accepted yet
  await page.getByText(`Fake reply ${before}`).waitFor()
  const replay = await turn(before)
  assert(replay.startsWith(await turn(1)) && replay.includes('Fake reply 1') && replay.endsWith('After a restart?\n```\n'), 'replayed stdin')
  ok('after a restart: listed, reopened and continued; stdin is the previous stdin plus the saved reply and the new message')

  // Planted canary: the reply is hidden on load and the conversation is read-only.
  await stop(run)
  await fs.writeFile(file, (await fs.readFile(file, 'utf8')).replace('Fake reply 0', 'key AKIAIOSFODNN7EXAMPLE'))
  run = await launch()
  page = await open(run.url)
  await aiOn(page)
  await page.getByRole('region', {name: 'Saved conversations of this project'}).getByRole('button', {name: 'Open'}).click()
  await page.getByText(/Read-only: Some saved text now looks like a secret/).waitFor()
  assert(!(await page.locator('body').innerText()).includes('AKIA'), 'canary shown')
  assert(await page.getByLabel('Follow-up message').isDisabled())
  ok('planted canary: turn hidden, conversation read-only, canary never shown')

  await page.getByRole('button', {name: 'Close conversation'}).click()
  await page.getByRole('button', {name: /Delete saved conversation/}).click()
  await page.getByText(/No other saved conversations/).waitFor()
  assert.deepEqual(await files(), [])
  ok('Delete removes the file')
  for (const s of seen) { assert.deepEqual(s.errors, []); assert.deepEqual(s.external, []) }
  ok(`all ${seen.length} pages: no page errors, no request outside 127.0.0.1`)
} finally {
  await browser.close()
  if (run) await stop(run)
  await fs.writeFile(path.join(report, 'results.txt'), results.join('\n') + '\n')
  await fs.rm(work, {recursive: true, force: true})
}
console.log(`saved-conversations e2e: ${results.length} checks passed`)

async function waitChecked(box) {
  for (const end = Date.now() + 5000; Date.now() < end; await new Promise(r => setTimeout(r, 100))) if (await box.isChecked() && (await files()).length) return
  throw new Error('save did not complete')
}

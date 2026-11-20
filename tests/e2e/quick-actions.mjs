// AI-01.10 quick actions, end to end: the real hclaro binary on 127.0.0.1 and headless Chromium,
// with a fake `claude` (passed by --claude-path, never looked up on PATH), a fake codex, the mock
// terraform and a call-counting wrapper around the fake Infracost (bin/mock). Each run gets its own
// HOME, config dir and PATH; the browser aborts and records every request outside the local origin.
// No real assistant CLI, credentials, backend, cloud account or network.
// Usage: make binary bin/mock/terraform bin/mock/infracost && node tests/e2e/quick-actions.mjs [web/node_modules]
import assert from 'node:assert/strict'
import fs from 'node:fs/promises'
import os from 'node:os'
import path from 'node:path'
import {execFileSync} from 'node:child_process'
import {fileURLToPath} from 'node:url'
import {aiSwitchOn, launchBrowser, nav, openPage, skipOnWindows, startHclaro, stop, waitFor} from './lib.mjs'

const repo = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..')
// shortcut: the fake assistant and wrappers are POSIX shell scripts; port them to Go helpers to run on Windows.
skipOnWindows('quick-actions e2e', 'POSIX shell fakes only')
// Fixtures live in a fresh temp dir outside any Git repository (the blocked case depends on it),
// removed at the end; the check list is kept in .cache/e2e-quick-actions/results.txt.
const work = await fs.mkdtemp(path.join(await fs.realpath(os.tmpdir()), 'hclaro-e2e-qa-'))
const report = path.join(repo, '.cache/e2e-quick-actions')
await fs.mkdir(report, {recursive: true})
const modules = path.resolve(process.argv[2] || path.join(repo, 'web/node_modules'))
const results = []
const ok = msg => { results.push('PASS ' + msg); console.log('PASS', msg) }

async function exe(file, text) { await fs.writeFile(file, text); await fs.chmod(file, 0o755) }
async function fakeClaude(dir, version) {
  await fs.mkdir(path.join(dir, 'turns'), {recursive: true})
  await fs.mkdir(path.join(dir, 'args'), {recursive: true})
  const file = path.join(dir, 'claude')
  await exe(file, `#!/bin/sh
if [ "$1" = "--version" ]; then echo "${version} (Claude Code)"; exit 0; fi
n=$(/bin/ls "${dir}/turns" | /usr/bin/wc -l | /usr/bin/tr -d ' ')
/bin/cat > "${dir}/turns/$n.txt"
printf '%s\\n' "$*" > "${dir}/args/$n.txt"
printf '{"type":"result","is_error":false,"result":"Fake reply %s"}\\n' "$n"
`)
  return file
}
const turns = async dir => (await fs.readdir(path.join(dir, 'turns'))).length
const turn = (dir, n) => fs.readFile(path.join(dir, 'turns', n + '.txt'), 'utf8')

async function project(dir, {git = true, extra = {}} = {}) {
  await fs.mkdir(dir, {recursive: true})
  await fs.cp(path.join(repo, 'fixtures/project'), dir, {recursive: true})
  for (const [k, v] of Object.entries(extra)) await fs.writeFile(path.join(dir, k), v)
  if (git) {
    // Setup only, with no user or system Git config; Hclaro itself never writes to Git.
    const g = (...a) => execFileSync('git', ['-c', 'user.name=T', '-c', 'user.email=t@example.invalid', '-c', 'commit.gpgsign=false', '-c', 'init.defaultBranch=main', ...a],
      {cwd: dir, env: {PATH: process.env.PATH, HOME: dir, GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null'}})
    g('init', '-q'); g('add', '.'); g('commit', '-q', '-m', 'synthetic')
  }
  return dir
}

async function launch(name, args, claude) {
  const dir = path.join(work, name)
  const bin = path.join(dir, 'bin')
  await fs.mkdir(bin, {recursive: true})
  await exe(path.join(bin, 'codex'), '#!/bin/sh\necho "codex-cli 0.50.0"\n')
  const infracostLog = path.join(dir, 'infracost-calls')
  await exe(path.join(bin, 'infracost'), `#!/bin/sh\necho "$*" >> "${infracostLog}"\nexec "${repo}/bin/mock/infracost" "$@"\n`)
  await fs.mkdir(path.join(dir, 'home'), {recursive: true})
  const env = {
    PATH: `${bin}:${repo}/bin/mock:/usr/bin:/bin`, HOME: path.join(dir, 'home'), TMPDIR: path.join(dir, 'home'),
    HCLARO_CONFIG_DIR: path.join(dir, 'config'), TF_DATA_DIR: path.join(repo, 'fixtures/backend'), LANG: 'en_US.UTF-8',
  }
  const server = await startHclaro(path.join(repo, 'bin/hclaro'), [...args, '--claude-path', claude], env, name)
  const infracostCalls = async () => (await fs.readFile(infracostLog, 'utf8').catch(() => '')).split('\n').filter(Boolean).length
  return {...server, infracostCalls}
}

const browser = await launchBrowser(modules)
const pages = []
async function open(url) {
  const opened = await openPage(browser, url)
  pages.push(opened)
  return opened
}
// AI is on by default (AI-01.14): Settings shows the switch already on, and nothing is detected
// until asked (Check now). accept accepts the disclosure through the API, as the first inline
// Accept and send would.
async function aiOn(page, accept) {
  await aiSwitchOn(page)
  assert.equal((await call(page, 'GET', 'ai/settings')).body.checked, false, 'assistants detected before a click')
  await page.getByRole('button', {name: 'Check now'}).click()
  await waitFor(async () => (await call(page, 'GET', 'ai/settings')).body.checked, 'detection')
  if (accept) {
    const st = await call(page, 'GET', 'ai/settings')
    assert.equal((await call(page, 'POST', 'ai/settings', {acceptDisclosure: st.body.disclosure.version})).body.disclosureAccepted, true)
  }
}
// Mutations from the page, like the UI's own client: bearer token + CSRF header + same Origin.
const call = (page, method, p, body) => page.evaluate(async ([method, p, body]) => {
  const token = sessionStorage.getItem('hclaro.session')
  const s = await (await fetch('/api/v1/session', {headers: {Authorization: 'Bearer ' + token}})).json()
  const r = await fetch('/api/v1/' + p, {method, headers: {Authorization: 'Bearer ' + token, 'X-Hclaro-CSRF': s.csrfToken, 'Content-Type': 'application/json'}, body: method === 'GET' ? undefined : JSON.stringify(body)})
  return {status: r.status, body: await r.json()}
}, [method, p, body])
async function estimate(page) {
  const plan = await call(page, 'POST', 'plans', {acknowledgeBackendAccess: true})
  assert.equal(plan.status, 202, JSON.stringify(plan.body))
  const op = await waitFor(async () => { const o = await call(page, 'GET', 'operations/' + plan.body.id); return o.body.status !== 'running' && o.body.status !== 'queued' && o.body.planId ? o.body : null }, 'plan')
  const e = await call(page, 'POST', `plans/${op.planId}/cost-estimates`, {})
  assert.equal(e.status, 202, JSON.stringify(e.body))
  return waitFor(async () => { const x = await call(page, 'GET', 'cost-estimates/' + e.body.id); return x.body.status !== 'running' ? x.body : null }, 'estimate')
}

const actions = [['Describe project', '# Describe this project'], ['Architecture review', '# Architecture review'], ['Security review', '# Security review'],
  ['Cost analysis', '# Cost analysis'], ['Find issues', '# Find issues'], ['Suggest improvements', '# Suggest improvements']]
const previewText = page => page.getByLabel('Text to share: review prompt and context package').textContent()
const servers = []
try {
  // A: headless fake Claude Code 2.1.327 + handoff-only fake codex, Git project.
  const logA = path.join(work, 'claudeA')
  const A = await launch('A', ['--engine', 'terraform', '--path', await project(path.join(work, 'projA'))], await fakeClaude(logA, '2.1.327'))
  servers.push(A)
  const {page, errors, external} = await open(A.url)
  await page.getByRole('navigation').first().waitFor()
  await aiOn(page, false)
  const st = await call(page, 'GET', 'ai/settings')
  assert.equal(st.body.disclosure.version, '10')
  assert(st.body.disclosure.lines.join('\n').includes('Individual cost line items are never shared'), 'cost line in the disclosure')
  ok('AI on by default, assistants detected only on Check now; disclosure v10 with the cost line')
  await nav(page, 'AI Assistant')

  for (const [i, [label, heading]] of actions.entries()) {
    const before = await turns(logA)
    await page.getByRole('button', {name: label, exact: true}).click()
    const text = await waitFor(async () => { const l = page.getByLabel('Text to share: review prompt and context package'); return await l.count() ? l.textContent() : null }, label + ' preview')
    assert(text.startsWith(`# Hclaro review request (qa-`) && text.includes('## Context package') && text.includes('\n' + heading + '\n') && text.includes('no state, no plan, no live cloud data'), label + ' preview shape')
    assert(!text.includes('## Cost estimate (Infracost'), label + ': cost section without opt-in')
    assert.equal(await turns(logA), before, label + ': sent before Start')
    assert.equal(await page.getByRole('button', {name: /^Copy/}).count(), 0, label + ': Copy offered for a quick action')
    // The first Start in the session is Accept and send, with the disclosure shown in the preview.
    await page.getByLabel('AI context preview', {exact: true}).getByRole('button', {name: i === 0 ? 'Accept and send' : 'Start ' + label.toLowerCase(), exact: true}).click()
    await page.getByText(`Fake reply ${before}`).waitFor()
    assert.equal(await turn(logA, before), text, label + ': stdin differs from the preview')
    const args = await fs.readFile(path.join(logA, 'args', before + '.txt'), 'utf8')
    assert(args.includes('--restricted') && args.includes('--tools'), 'restricted argv')
    await page.getByText(/Ran a quick action on the context package/).waitFor()
    ok(`${label}: preview-before-start, turn-1 stdin == preview bytes, prompt ${text.match(/\((qa-[a-z]+@[0-9]+)\)/)[1]}`)
    if (i === actions.length - 1) {
      await page.getByLabel('Follow-up message').fill('Which of these first?')
      await page.getByRole('button', {name: 'Send message'}).click()
      await page.getByText(`Fake reply ${before + 1}`).waitFor()
      const follow = await turn(logA, before + 1)
      assert(follow.startsWith(text) && follow.includes('Which of these first?') && follow.includes(`Fake reply ${before}`), 'follow-up replay')
      ok('follow-up continues the quick action conversation and replays the previewed first turn')
    }
    await page.getByRole('button', {name: 'New conversation'}).click()
  }

  // Cost: nothing to offer before an estimate.
  await page.getByRole('button', {name: 'Cost analysis', exact: true}).click()
  await page.getByText('No cost estimate from this session is attached').waitFor()
  assert.equal(await page.getByLabel(/latest Infracost estimate/).count(), 0)
  await page.getByRole('button', {name: 'Cancel', exact: true}).click()
  ok('Cost analysis without an estimate: no checkbox, says no pricing data is attached')

  assert.equal((await call(page, 'POST', 'cost/opt-in', {acknowledged: true, disclosureVersion: '1'})).status, 200)
  const e1 = await estimate(page)
  assert.equal(e1.status, 'completed', JSON.stringify(e1))
  const calls = await A.infracostCalls()
  await page.getByRole('button', {name: 'Cost analysis', exact: true}).click()
  const box = page.getByLabel(/latest Infracost estimate/)
  await box.waitFor()
  assert(!(await box.isChecked()), 'checkbox checked by default')
  assert(!(await previewText(page)).includes('## Cost estimate (Infracost'), 'cost attached before tick')
  await box.check()
  const withCost = await waitFor(async () => { const t = await page.getByLabel('Text to share: review prompt and context package').textContent().catch(() => ''); return t.includes('## Cost estimate (Infracost') ? t : null }, 'cost preview')
  const section = withCost.split('## Cost estimate (Infracost, from this session)')[1]
  assert(section.includes('these figures are estimates, not a bill or verified pricing') && section.includes('coverage') && !section.includes('infracost.io'), 'cost section fields')
  const hidden = section.includes('[line hidden')
  if (hidden) await page.getByText(/The attached cost summary is incomplete/).waitFor()
  const before = await turns(logA)
  await page.getByRole('button', {name: 'Start cost analysis'}).click()
  await page.getByText(`Fake reply ${before}`).waitFor()
  assert.equal(await turn(logA, before), withCost, 'cost stdin differs from preview')
  assert.equal(await A.infracostCalls(), calls, 'quick action ran Infracost')
  ok(`cost checkbox unchecked by default; ticked preview sent byte for byte; zero Infracost calls by the action (hidden lines: ${hidden ? 'yes, incomplete warning shown' : 'none in this fixture'})`)

  // Estimate replaced while the preview is open: Start is refused, nothing sent.
  await page.getByRole('button', {name: 'New conversation'}).click()
  await page.getByRole('button', {name: 'Cost analysis', exact: true}).click()
  await page.getByLabel(/latest Infracost estimate/).check()
  await waitFor(async () => (await page.getByLabel('Text to share: review prompt and context package').textContent().catch(() => '')).includes('## Cost estimate'), 'cost preview 2')
  const e2 = await estimate(page)
  assert.equal(e2.status, 'completed')
  const n2 = await turns(logA)
  await page.getByRole('button', {name: 'Start cost analysis'}).click()
  await page.getByRole('alert').filter({hasText: /changed|stale|again/i}).first().waitFor()
  assert.equal(await turns(logA), n2, 'stale cost package was sent')
  await page.getByRole('button', {name: 'Cancel', exact: true}).click()
  ok('estimate replaced while the preview was open: Start refused, nothing sent')

  // Opt-out: no longer offered.
  await call(page, 'POST', 'cost/opt-out', {})
  await page.getByRole('button', {name: 'Cost analysis', exact: true}).click()
  await page.getByText('No cost estimate from this session is attached').waitFor()
  await page.getByRole('button', {name: 'Cancel', exact: true}).click()
  ok('after cost opt-out the existing estimate is not offered')
  assert.deepEqual(errors, []); assert.deepEqual(external, [])
  ok('A: no page errors, no external requests')

  // B: Claude Code version not verified -> handoff-only; codex handoff-only.
  const logB = path.join(work, 'claudeB')
  const B = await launch('B', ['--engine', 'terraform', '--path', await project(path.join(work, 'projB'))], await fakeClaude(logB, '2.0.1'))
  servers.push(B)
  const b = await open(B.url)
  await b.page.getByRole('navigation').first().waitFor()
  await aiOn(b.page, false)
  await nav(b.page, 'AI Assistant')
  await b.page.getByText(/Quick actions need an assistant Hclaro can run/).waitFor()
  for (const [label] of actions) assert(await b.page.getByRole('button', {name: label, exact: true}).isDisabled())
  assert.equal(await turns(logB), 0)
  ok('B: handoff-only assistants only -> all six actions disabled with a reason')

  // C: blocked package (.gitignore outside a Git repository fails closed).
  const logC = path.join(work, 'claudeC')
  const C = await launch('C', ['--engine', 'terraform', '--path', await project(path.join(work, 'projC'), {git: false, extra: {'.gitignore': '*.tfvars\n'}})], await fakeClaude(logC, '2.1.327'))
  servers.push(C)
  const c = await open(C.url)
  await c.page.getByRole('navigation').first().waitFor()
  await aiOn(c.page, true)
  await nav(c.page, 'AI Assistant')
  await c.page.getByRole('button', {name: 'Find issues', exact: true}).click()
  await c.page.getByText('Sharing blocked').waitFor()
  assert.equal(await c.page.getByRole('button', {name: 'Start find issues'}).count(), 0)
  assert.equal(await turns(logC), 0)
  ok('C: blocked project package -> "Sharing blocked", no Start, nothing sent')

  // D: Hub launch without a project.
  const logD = path.join(work, 'claudeD')
  const D = await launch('D', ['--hub'], await fakeClaude(logD, '2.1.327'))
  servers.push(D)
  const d = await open(D.url)
  await d.page.waitForLoadState('networkidle')
  await aiOn(d.page, true)
  const navItem = d.page.getByRole('button', {name: 'AI Assistant', exact: true})
  assert(await navItem.count() === 0 || await navItem.isDisabled(), 'AI Assistant usable without a project')
  assert.equal(await turns(logD), 0)
  ok('D: Hub launch without a project -> AI Assistant entry ' + ((await navItem.count()) ? 'shown disabled' : 'not offered') + ', quick actions unreachable, nothing sent')

  for (const p of pages) { assert.deepEqual(p.errors, []); assert.deepEqual(p.external, []) }
  ok(`all ${pages.length} runs: no page errors, no request outside 127.0.0.1`)
} finally {
  await browser.close()
  await Promise.all(servers.map(stop))
  await fs.writeFile(path.join(report, 'results.txt'), results.join('\n') + '\n')
  await fs.rm(work, {recursive: true, force: true})
}
console.log(`quick-actions e2e: ${results.length} checks passed`)

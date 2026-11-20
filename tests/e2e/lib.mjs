// Shared helpers for the synthetic browser checks: start the real hclaro binary on 127.0.0.1,
// open pages that abort every request outside the local origin, and poll for conditions.
import {spawn} from 'node:child_process'
import path from 'node:path'
import {pathToFileURL} from 'node:url'

// SKIP_CODE is the exit status of a check that did not run; callers (Makefile) report it as a
// skip, never as a pass.
export const SKIP_CODE = 77

export function skipOnWindows(name, reason) {
  if (process.platform !== 'win32') return
  console.log(`SKIP ${name}: ${reason}`)
  process.exit(SKIP_CODE)
}

export async function launchBrowser(modules, extraArgs = []) {
  const {chromium} = await import(pathToFileURL(path.join(modules, 'playwright/index.mjs')))
  return chromium.launch({headless: true, ...(process.env.HCLARO_CHROME ? {executablePath: process.env.HCLARO_CHROME} : {}), args: [...extraArgs, '--disable-background-networking', '--disable-component-update']})
}

const sessionURL = /http:\/\/127\.0\.0\.1:\d+\/\S*#session=[0-9a-f]{64}/
// The session URL is a credential for the throwaway server; it never appears in an error.
const hideSession = s => s.replace(/session=[0-9a-f]+/g, 'session=…')

// startHclaro runs the binary and resolves once it prints its session URL.
export async function startHclaro(exe, args, env, name = 'hclaro') {
  const proc = spawn(exe, [...args, '--no-browser'], {env, stdio: ['ignore', 'pipe', 'pipe']})
  const exited = new Promise(r => proc.once('exit', r))
  let out = ''
  const url = await new Promise((resolve, reject) => {
    const t = setTimeout(() => { proc.kill(); reject(new Error(name + ': no URL\n' + hideSession(out))) }, 20000)
    const read = d => { out += d; const m = out.match(sessionURL); if (m) { clearTimeout(t); resolve(m[0]) } }
    proc.stdout.on('data', read); proc.stderr.on('data', read)
    proc.on('exit', c => { clearTimeout(t); reject(new Error(name + ': exited ' + c + '\n' + hideSession(out))) })
  })
  return {proc, url, exited, out: () => out}
}

// stop interrupts a server and waits until it has exited and released its files.
export async function stop(server) {
  if (server.proc.exitCode === null && server.proc.signalCode === null) server.proc.kill('SIGINT')
  await server.exited
}

// openPage opens url in a fresh context; requests to any other origin are aborted and recorded.
export async function openPage(browser, url, contextOptions = {viewport: {width: 1400, height: 1000}}) {
  const context = await browser.newContext(contextOptions)
  const origin = new URL(url).origin
  const external = [], errors = []
  await context.route('**/*', r => { if (new URL(r.request().url()).origin !== origin) { external.push(r.request().url()); return r.abort() } return r.continue() })
  const page = await context.newPage()
  page.on('pageerror', e => errors.push(e.message))
  await page.goto(url)
  return {page, context, origin, errors, external}
}

export async function waitFor(fn, what, ms = 15000) {
  for (const end = Date.now() + ms; Date.now() < end; await new Promise(r => setTimeout(r, 100))) { const v = await fn(); if (v) return v }
  throw new Error('timed out: ' + what)
}

export const nav = (page, label) => page.getByRole('button', {name: label, exact: true}).first().click()

// aiSwitchOn opens Settings and waits for the AI switch, which is on by default (AI-01.14).
export async function aiSwitchOn(page) {
  await nav(page, 'Settings')
  await page.getByRole('group', {name: 'AI assistance'}).getByRole('button', {name: 'On', pressed: true}).waitFor()
}

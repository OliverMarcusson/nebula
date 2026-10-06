import type { EngineInterface, On, Timer } from 'claude-code'

// Only fixed companion commands; no shell or model supplied arguments.
type State = { timer?: Timer; syncing?: Promise<string>; signingIn?: boolean; limited?: boolean; exitAfterTurn?: boolean }
type Companion = { executable: string; projects?: string; env: Record<string, string> }

const unconfigured = 'Nebula is not set up: run nebula setup, or set an absolute NEBULA_COMPANION_PATH.'

async function companion($: EngineInterface): Promise<Companion | undefined> {
  const executable = await $.env.get('NEBULA_COMPANION_PATH')
  const server = await $.env.get('NEBULA_SERVER_URL')
  const token = await $.env.get('NEBULA_TOKEN_FILE')
  const projects = await $.env.get('NEBULA_PROJECTS_DIR')
  // Claude Code profile directories whose signed-in accounts are reported; the companion requires absolute paths.
  const profiles = await $.env.get('NEBULA_PROFILES')
  const absolute = (p: string) => p.startsWith('/') || /^[a-z]:[\\/]/i.test(p)
  // The server and token normally come from the companion's config file (nebula setup).
  if (!executable || !absolute(executable) || (token && !absolute(token)) ||
      (projects && !absolute(projects))) return undefined
  return { executable, projects, env: { ...(server ? { NEBULA_SERVER_URL: server } : {}), ...(token ? { NEBULA_TOKEN_FILE: token } : {}), ...(profiles ? { NEBULA_PROFILES: profiles } : {}) } }
}

async function call($: EngineInterface, command: 'sync' | 'list' | 'accounts'): Promise<string> {
  try {
    const c = await companion($)
    if (!c) return unconfigured
    const argv = [c.executable, command]
    if (command === 'sync' && c.projects) argv.push('--projects', c.projects)
      const result = await $.process.run(argv, { env: c.env, timeoutMs: 60000 })
      if (result.exitCode !== 0) return 'Nebula could not reach or update the archive. Local sessions are retained; check the companion configuration.'
      return result.stdout
  } catch {
    return 'Nebula companion did not finish. Local sessions are retained; background sync will retry.'
  }
}

// Runs `nebula login`, which runs Claude Code's own sign-in into a new profile.
// The browser opens on this machine and completes by itself; the command answers
// with the sign-in link once it appears and toasts the result when it finishes.
async function login($: EngineInterface, state: State): Promise<string> {
  if (state.signingIn) return 'A Claude sign-in is already in progress.'
  const c = await companion($)
  if (!c) return unconfigured
  state.signingIn = true
  return new Promise<string>((resolve) => {
    let answered = false
    const answer = (text: string) => { if (!answered) { answered = true; resolve(text) } }
    void (async () => {
      let output = ''
      try {
        const child = $.process.spawn({ argv: [c.executable, 'login'], env: c.env })
        for await (const { text } of child) {
          output += text
          const url = /visit:\s*(https:\/\/\S+)/.exec(output)?.[1]
          if (url) answer(`Sign in with Claude in the browser that opened. If it did not open, visit:\n${url}`)
        }
        const { code } = await child.result
        const last = output.trim().split('\n').at(-1) ?? ''
        const result = code === 0 ? last : 'Claude sign-in did not complete.'
        answer(result)
        $.ui.toast(result)
      } catch {
        answer('Nebula companion could not start Claude sign-in.')
      } finally {
        state.signingIn = false
      }
    })()
  })
}

// A usage limit is a `rate_limit` failure the session's own limit readings
// confirm: a window at 100%. Ordinary throttling never switches accounts.
async function onLimit($: EngineInterface, state: State, sessionId: string) {
  if (state.limited) return
  const windows = (await $.session.usage()).rateLimits
  const full = windows.filter((w) => w.percentUsed >= 100)
  if (full.length === 0) return
  state.limited = true
  const resetsAt = full.map((w) => w.resetsAt).filter((t): t is string => !!t).sort().at(-1)
  const c = await companion($)
  if (c) {
    const argv = [c.executable, 'limited', ...(resetsAt ? ['--resets-at', resetsAt] : [])]
    try { await $.process.run(argv, { env: c.env, timeoutMs: 20000 }) } catch { /* the launcher reports it too */ }
  }
  // Under the Nebula launcher: hand the session over and exit, so it resumes
  // on the next account. Nothing is resubmitted; the user continues.
  const switchFile = await $.env.get('NEBULA_SWITCH_FILE')
  if (switchFile) {
    await $.fs.write(switchFile, JSON.stringify({ session_id: sessionId, ...(resetsAt ? { resets_at: resetsAt } : {}) }))
    $.ui.toast('Usage limit reached. Nebula is resuming this session on your next account.')
    state.exitAfterTurn = true // /exit cannot run while this turn is held; turn.complete runs it
  } else {
    $.ui.toast('Usage limit reached. New sessions started with nebula claude will use another account.')
  }
}

function sync($: EngineInterface, state: State): Promise<string> {
  if (state.syncing) return state.syncing
  state.syncing = call($, 'sync').finally(() => { state.syncing = undefined })
  return state.syncing
}

export function register(on: On) {
  const state: State = {}
  on('session.start', async ($, e, next) => {
    state.timer?.cancel()
    await $.command.register({ name: 'nebula-sync', description: 'Upload complete session records to Nebula' })
    await $.command.register({ name: 'nebula-sessions', description: 'List archived Nebula session revisions' })
    await $.command.register({ name: 'nebula-accounts', description: 'Report signed-in Claude accounts to Nebula' })
    await $.command.register({ name: 'nebula-login', description: 'Connect another Claude account with Claude sign-in' })
    void sync($, state)
    state.timer = $.clock.every(15000, () => { void sync($, state) })
    return next(e)
  })

  on('command.run', { command: 'nebula-sync' }, async ($) => ({ text: await sync($, state) }))
  on('command.run', { command: 'nebula-sessions' }, async ($) => ({ text: await call($, 'list') }))
  on('command.run', { command: 'nebula-accounts' }, async ($) => ({ text: await call($, 'accounts') }))
  on('command.run', { command: 'nebula-login' }, async ($) => ({ text: await login($, state) }))

  on('classic.StopFailure', async ($, e, next) => {
    if (e.error === 'rate_limit' && e.agent_id === undefined) await onLimit($, state, e.session_id)
    return next(e)
  })

  on('turn.complete', async ($, e, next) => {
    const result = await next(e)
    if (state.exitAfterTurn) {
      state.exitAfterTurn = false
      await $.command.run({ command: 'exit' })
    }
    return result
  })

  on('session.end', ($, e, next) => {
    state.timer?.cancel()
    state.timer = undefined
    // End hooks have a short budget; the independent watcher captures final writes.
    return next(e)
  })
}

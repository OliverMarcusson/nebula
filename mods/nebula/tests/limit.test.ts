import { expect, mock, test } from 'claude-code/testing'

const env = {
  NEBULA_COMPANION_PATH: '/opt/nebula/bin/nebula',
  NEBULA_SERVER_URL: 'https://nebula.example',
  NEBULA_TOKEN_FILE: '/home/u/.nebula/device.token',
  NEBULA_SWITCH_FILE: '/home/u/.config/Nebula/switch/abc.json',
}

function fakes(on: Parameters<Parameters<typeof test>[1]>[1], percent: number) {
  const seen = { argv: [] as string[], written: '' as string, exited: false }
  on('session.usage', async () => ({
    value: {
      startedAt: 0,
      context: { used: 0, total: 1, percent: 0 } as never,
      rateLimits: [{ kind: 'five_hour', percentUsed: percent, resetsAt: '2026-10-07T03:00:00Z' }],
    },
  }))
  on('classic.StopFailure', async () => ({}))
  on('ui.toast', async () => ({ value: undefined }))
  on('turn.complete', async () => ({ text: '' }))
  on('process.run', async (_$, e) => {
    seen.argv = [...e.argv]
    return { value: { exitCode: 0, stdout: '', stderr: '' } }
  })
  on('fs.write', async (_$, e) => {
    seen.written = e.text
    return { value: undefined }
  })
  on('command.run', { command: 'exit' }, async () => {
    seen.exited = true
    return {}
  })
  return seen
}

test('a confirmed usage limit records it, hands the session over, and exits', async ($, on) => {
  mock.env(on, env)
  const seen = fakes(on, 100)
  await $.classic.StopFailure({ error: 'rate_limit', session_id: 'sess-1' })
  expect(seen.argv).toEqual(['/opt/nebula/bin/nebula', 'limited', '--resets-at', '2026-10-07T03:00:00Z'])
  expect(JSON.parse(seen.written)).toEqual({ session_id: 'sess-1', resets_at: '2026-10-07T03:00:00Z' })
  expect(seen.exited).toBe(false)
  await $.turn.complete({ turnId: 't1', reason: 'error', answer: '', durationMs: 1, isAborted: false } as never)
  expect(seen.exited).toBe(true)
})

test('throttling without a full window does not switch', async ($, on) => {
  mock.env(on, env)
  const seen = fakes(on, 40)
  await $.classic.StopFailure({ error: 'rate_limit', session_id: 'sess-1' })
  expect(seen.argv).toEqual([])
  expect(seen.exited).toBe(false)
})

test('in a headless host such as T3 Code, a usage limit restarts Claude Code after the turn', async ($, on) => {
  const { NEBULA_SWITCH_FILE: _, ...rest } = env
  mock.env(on, { ...rest, NEBULA_ACCOUNT_ID: 'acc-1' })
  const seen = fakes(on, 100)
  on('command.register', async () => ({ value: undefined }))
  on('tool.register', async () => ({ value: undefined }))
  on('clock.every', async () => ({ value: { cancel() {} } }))
  on('session.start', async (_$, e) => ({ cwd: e.cwd }))
  await $.session.start({ cwd: '/w', surface: null, isInteractive: false } as never)
  await $.classic.StopFailure({ error: 'rate_limit', session_id: 'sess-1' })
  expect(seen.argv[1]).toBe('limited')
  await $.turn.complete({ turnId: 't1', reason: 'error', answer: '', durationMs: 1, isAborted: false } as never)
  expect(seen.argv).toEqual(['/opt/nebula/bin/nebula', 'restart-claude'])
  expect(seen.exited).toBe(false)
})

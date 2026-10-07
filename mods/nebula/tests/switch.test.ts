import { expect, mock, test } from 'claude-code/testing'

const env: Record<string, string> = {
  NEBULA_COMPANION_PATH: '/opt/nebula/bin/nebula',
  NEBULA_SWITCH_FILE: '/home/u/.config/Nebula/switch/abc.json',
  NEBULA_ACCOUNT_ID: 'acct-old',
}

function fakes(on: Parameters<Parameters<typeof test>[1]>[1], stdout: string) {
  const seen = { argv: [] as string[], env: {} as Record<string, string>, exited: false }
  mock.env(on, env)
  on('session.id', async () => ({ value: 'sess-1' }))
  on('turn.complete', async () => ({ text: '' }))
  on('process.run', async (_$, e) => {
    seen.argv = [...e.argv]
    seen.env = { ...(e.init?.env ?? {}) }
    return { value: { exitCode: 0, stdout, stderr: '' } }
  })
  on('command.run', { command: 'exit' }, async () => {
    seen.exited = true
    return {}
  })
  return seen
}

test('the model switches accounts; the session hands over when its turn ends', async ($, on) => {
  const seen = fakes(on, 'Switching to cyber@example.com: this session resumes on it when the current turn ends.\n')
  const r = await $.tool.call({ tool: 'mcp__nebula__switch_account', input: { account: 'cyber' } })
  expect(seen.argv).toEqual(['/opt/nebula/bin/nebula', 'switch', '--session', 'sess-1', 'cyber'])
  expect(seen.env.NEBULA_SWITCH_FILE).toBe(env.NEBULA_SWITCH_FILE)
  expect(String(r.result)).toContain('Switching to cyber@example.com')
  expect(seen.exited).toBe(false)
  await $.turn.complete({ turnId: 't1', reason: 'end_turn', answer: '', durationMs: 1, isAborted: false } as never)
  expect(seen.exited).toBe(true)
})

test('listing accounts runs the companion without arguments and never exits', async ($, on) => {
  const seen = fakes(on, 'a@example.com\n')
  await $.tool.call({ tool: 'mcp__nebula__switch_account', input: {} })
  expect(seen.argv).toEqual(['/opt/nebula/bin/nebula', 'switch'])
  await $.turn.complete({ turnId: 't1', reason: 'end_turn', answer: '', durationMs: 1, isAborted: false } as never)
  expect(seen.exited).toBe(false)
})

test('an account name that could be a flag is refused', async ($, on) => {
  const seen = fakes(on, '')
  const r = await $.tool.call({ tool: 'mcp__nebula__switch_account', input: { account: '--session=x' } })
  expect(seen.argv).toEqual([])
  expect(String(r.result)).toContain('email')
})

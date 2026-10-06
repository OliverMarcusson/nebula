import { expect, mock, test } from 'claude-code/testing'

const env = {
  NEBULA_COMPANION_PATH: '/opt/nebula/bin/nebula',
  NEBULA_SERVER_URL: 'https://nebula.example',
  NEBULA_TOKEN_FILE: '/home/u/.nebula/device.token',
}

test('nebula-login runs the companion and answers with the sign-in link', async ($, on) => {
  mock.env(on, env)
  let argv: readonly string[] = []
  on('process.spawn', async function* (_$, e) {
    argv = e.argv
    yield { stream: 'stdout' as const, text: "Opening browser to sign in…\nIf the browser didn't open, visit: https://claude.com/cai/oauth/authorize?code=true\n" }
    yield { stream: 'stdout' as const, text: 'Connected second@example.com (profile second).\n' }
    return { code: 0, signal: null }
  })
  const { text } = await $.command.run({ command: 'nebula-login' })
  expect(argv).toEqual(['/opt/nebula/bin/nebula', 'login'])
  expect(text).toContain('https://claude.com/cai/oauth/authorize?code=true')
})

test('nebula-login refuses to run without companion configuration', async ($, on) => {
  mock.env(on, {})
  const { text } = await $.command.run({ command: 'nebula-login' })
  expect(text).toContain('NEBULA_COMPANION_PATH')
})

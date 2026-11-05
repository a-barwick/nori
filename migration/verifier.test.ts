import { expect, test } from 'bun:test';

function verify(...args: string[]) {
  const result = Bun.spawnSync(['python3', 'migration/verify.py', ...args], {
    env: { PATH: process.env.PATH, HOME: process.env.HOME }, stdout: 'pipe', stderr: 'pipe',
  });
  return { code: result.exitCode, output: result.stdout.toString() };
}
test('migration readiness cannot pass on preparation evidence', () => {
  const result = verify('readiness');
  expect(result.code).toBe(2);
  expect(result.output).toContain('"migration_ready": false');
  expect(result.output).toContain('not_verified');
});
test('missing emulator prerequisites fail rather than skipping the baseline', () => {
  const result = verify('baseline', '--suite', '8');
  expect(result.code).toBe(2);
  expect(result.output).toContain('FIRESTORE_EMULATOR_HOST must be an explicit');
  expect(result.output).toContain('"status": "blocked"');
});

// Simulated data for the contract and task lifecycle pages. Endpoints and field names follow the code.
// Values are invented, and "…" marks a shortened value or fields left out to keep the example readable.
(() => {
  const TASK = 'a41d7c02-5e3b-4f68-b2c9-0d8e6f13a975';
  const CMD = 'e7b25d10-96a4-4c1f-8d3e-2f0a7c64b918';
  const TIME = '2026-10-31T16:06:30Z';
  const REQUEST = 'Draft a Q3 launch brief.';
  const QUESTION_ID = `${TASK}-question-m2`;
  const QUESTION = 'Which region should the brief focus on?';

  const command = (kind, fields) => ({ id: CMD, run_id: TASK, kind, text: '', position: 2, revision: 1, delivered: false, created_at: TIME, ...fields });
  const run = fields => ({ id: TASK, status: 'running', '…': '…', ...fields });
  const CODE = { http: 'core/internal/api/http.go', state: 'core/internal/state/records/tasks.go', commands: 'core/internal/conversation/commands.go',
    call: 'core/internal/conversation/call.go', runner: 'core/internal/task/runner.go', steps: 'core/internal/agent/steps.go', events: 'core/internal/state/records/commands.go' };

  const toolFlow = (name, args, saved, note, rejected) => [
    { from: 'Outer model', to: 'Runtime', call: name, source: CODE.commands, note,
      response: { actions: [{ name, arguments: args }], text: '' } },
    { from: 'Runtime', to: 'Postgres', call: 'task_commands/{id}', source: CODE.state, saved },
    { from: 'Runtime', to: 'Postgres', call: 'saved command receipt', source: CODE.call,
      saved: { command: { id: saved.id, kind: saved.kind, position: saved.position }, run: { id: saved.run_id, status: 'running' } },
      note: 'Later model context contains historical receipt facts as text. No native tool-result message is sent to the outer model.', other: rejected },
  ];
  const STALE = ['A rejection is saved as a command result, such as {"error": "question is no longer current"}. The turn itself doesn\'t fail.'];
  const NOT_OPEN = ['A rejection is saved as a command result, such as {"error": "task is not open"}. The turn itself doesn\'t fail.'];

  const panelFlow = (body, receipt, other) => ({ from: 'Client', to: 'Go service', call: 'POST /api/command', source: CODE.http, request: body,
    response: { status: 202, body: receipt }, other });
  const COMMON = ['403 if execution is paused', '503 if it saved but the wake couldn\'t be confirmed. Retrying the same request is safe.'];

  const event = (id, kind, sequence, fields, note) => [{ from: 'Task', to: 'Postgres', call: 'conversation_inputs/event-{id}', source: CODE.events, note,
    saved: { id: `event-${id}`, position: 14, created_at: TIME, event: { id, run_id: TASK, kind, sequence, instruction_revision: 2, ...fields, created_at: TIME } } }];

  const askFlow = [
    { from: 'Inner model', to: 'Worker', call: 'request_input', source: CODE.steps, note: 'It must be the only call in the response.',
      response: { functionCall: { id: 'call_7', name: 'request_input', args: { question: QUESTION } } } },
    { from: 'Worker', to: 'Postgres', call: 'runs/{id}', source: CODE.state, note: 'The record still says running. The question field is what makes it a waiting task.',
      saved: run({ waiting: QUESTION, event_sequence: 1, question: { id: QUESTION_ID, kind: 'input', text: QUESTION, call_id: 'call_7', instruction_revision: 1 } }) },
    ...event(QUESTION_ID, 'needs_input', 1, { text: QUESTION, reason: 'input', instruction_revision: 1 }, 'Saved in the same transaction as the run. This starts an outer turn.'),
  ];

  window.FLOWS = {
    dispatch: toolFlow('dispatch', { request: REQUEST, for: null, question_id: null, attachments: [], revises: null }, command('start', { id: TASK, text: REQUEST, position: 1, revision: 1, source_input_ids: ['3f6c…'] }),
      'Every call in a response is validated before any is accepted, up to 16 calls.'),
    'tool-steer': toolFlow('steer', { run_id: TASK, text: 'Keep it under one page.', interrupt: false }, command('steer', { text: 'Keep it under one page.', revision: 2, source_input_ids: ['8d21…'] }),
      'The model has to use the exact task ID from the task records.', NOT_OPEN),
    'tool-answer': toolFlow('answer', { run_id: TASK, question_id: QUESTION_ID, text: 'EMEA.' }, command('answer', { text: 'EMEA.', question_id: QUESTION_ID }),
      'The question ID has to match the task\'s open question.', STALE),
    'tool-stop': toolFlow('stop', { run_id: TASK }, command('stop', { revision: 2 }), 'Other tasks carry on unchanged.', NOT_OPEN),

    wait: askFlow,
    'event-needs_input': event(QUESTION_ID, 'needs_input', 1, { text: QUESTION, reason: 'input' }, 'Approval interactions emit the same event shape and appear under pending interactions.'),
    'event-result': event(`${TASK}-terminal`, 'result', 2, { text: 'Saved the one-page EMEA launch brief.', operation_refs: ['op-…'] }, 'A task has one final event, so its ID ends in -terminal.'),
    'event-failure': event(`${TASK}-terminal`, 'failure', 2, { reason: 'Reached the $1.00 spending limit before finishing.', operation_refs: ['op-…'] }, 'operation_refs lists what the task did before it stopped. Unknown model cost is recorded as cost_unknown without failing a response.'),
    'event-stopped': event(`${TASK}-terminal`, 'stopped', 2, { reason: 'Stopped by the user.', operation_refs: ['op-…'] }, 'operation_refs lists effects that had already happened.'),

    start: [
      { from: 'Outer loop', to: 'Postgres', call: 'runs/{id} + nori_task_leases', source: CODE.state, note: 'This example starts from an outer-loop dispatch; the runnable lease row commits in the same transaction.',
        saved: run({ request: REQUEST, revision: 1, position: 1, started_at: TIME }) },
      { from: 'Worker pool', to: 'Postgres', call: 'ClaimTask + AcquireTaskControl', source: 'core/internal/worker/pool.go',
        saved: { run_id: TASK, owner: 'worker-…', fence: 1, runnable: true } },
      { from: 'Runner', to: 'Inner model', call: 'next model call', source: CODE.runner, request: { role: 'user', parts: [{ text: REQUEST }] } },
    ],
    steer: [
      panelFlow({ id: CMD, run_id: TASK, kind: 'steer', text: 'Keep it under one page.' },
        { command: command('steer', { text: 'Keep it under one page.', revision: 2 }), run: run({ revision: 2, position: 2 }) },
        ['409 if the task has finished', ...COMMON]),
      { from: 'Runner', to: 'Inner model', call: 'next model call', source: CODE.runner, note: 'The words go in as written. Anything decided on the old revision is thrown away.',
        request: { role: 'user', parts: [{ text: 'Keep it under one page.' }] } },
    ],
    answer: [
      panelFlow({ id: CMD, run_id: TASK, kind: 'answer', text: 'EMEA.', question_id: QUESTION_ID },
        { command: command('answer', { text: 'EMEA.', question_id: QUESTION_ID }), run: run({ revision: 1, position: 2, question: { id: QUESTION_ID, resolved_by: CMD, '…': '…' } }) },
        ['409 “question is no longer current” if the ID doesn\'t match the open question', ...COMMON]),
      { from: 'Runner', to: 'Inner model', call: 'tool response', source: CODE.runner, note: 'It reaches the model as the result of its request_input call. The revision doesn\'t change.',
        request: { functionResponse: { id: 'call_7', name: 'request_input', response: { answer: 'EMEA.', question_id: QUESTION_ID } } } },
    ],
    stop: [
      panelFlow({ id: CMD, run_id: TASK, kind: 'stop' },
        { command: command('stop', { revision: 2 }), run: run({ cancel_requested: true, revision: 2, position: 2 }) },
        ['400 if an ID isn\'t a UUID', '404 if there\'s no such task', '409 if the task has finished', 'Stop is still accepted while execution is paused']),
      { from: 'Worker', to: 'Postgres', call: 'runs/{id}', source: CODE.state, note: 'Requested isn\'t stopped. This is only written once the runner has wound down.',
        saved: run({ status: 'cancelled', error: 'Stopped by the user.', cancel_requested: true, finished_at: TIME }) },
    ],

    'move-ask': askFlow,
    'move-budget': [
      { from: 'Runner', to: 'Postgres', call: 'runs/{id}', source: CODE.runner, note: 'Checked between model calls. Reaching the spending ceiling ends the task incomplete (expected_incomplete on the publish request) with no extra model call or continue question; the user can start a linked continuation task from Work.',
        saved: run({ status: 'failed', spent_usd: 1.0021, budget_usd: 1, error: 'Reached the $1.00 spending limit before finishing.', finished_at: TIME }) },
      ...event(`${TASK}-terminal`, 'failure', 2, { reason: 'Reached the $1.00 spending limit before finishing.', operation_refs: ['op-…'] }),
    ],
    'move-done': [
      { from: 'Worker', to: 'Postgres', call: 'runs/{id}', source: CODE.state, note: 'If new input is still waiting to be routed, this holds until it has been.',
        saved: run({ status: 'completed', result: 'Saved the one-page EMEA launch brief.', waiting: '', question: null, finished_at: TIME }) },
      ...event(`${TASK}-terminal`, 'result', 2, { text: 'Saved the one-page EMEA launch brief.', operation_refs: ['op-…'] }),
    ],
    'move-failed': [
      { from: 'Worker', to: 'Postgres', call: 'runs/{id}', source: CODE.state,
        saved: run({ status: 'failed', error: 'Upstream model request failed with HTTP 502.', finished_at: TIME }) },
      ...event(`${TASK}-terminal`, 'failure', 2, { reason: 'Upstream model request failed with HTTP 502.', operation_refs: ['op-…'] }),
    ],
    'move-stopped': [
      { from: 'Worker', to: 'Postgres', call: 'runs/{id}', source: CODE.state,
        saved: run({ status: 'cancelled', error: 'Stopped by the user.', cancel_requested: true, finished_at: TIME }) },
      ...event(`${TASK}-terminal`, 'stopped', 2, { reason: 'Stopped by the user.', operation_refs: ['op-…'] }),
    ],
  };
})();

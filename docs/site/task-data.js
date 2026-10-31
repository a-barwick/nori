// Simulated data for the contract and task lifecycle pages. Endpoints and field names follow the code on main.
// Values are invented, and "…" marks a shortened value or fields left out to keep the example readable.
(() => {
  const TASK = 'a41d7c02-5e3b-4f68-b2c9-0d8e6f13a975';
  const CMD = 'e7b25d10-96a4-4c1f-8d3e-2f0a7c64b918';
  const TIME = '2026-10-31T16:06:30Z';
  const REQUEST = 'Find a time for dinner.';
  const QUESTION_ID = `${TASK}-question-m2`;
  const BUDGET_ID = `${TASK}-budget-p3`;
  const QUESTION = 'Which night should I look at?';

  const command = (kind, fields) => ({ id: CMD, run_id: TASK, kind, text: '', position: 2, revision: 1, delivered: false, created_at: TIME, ...fields });
  const run = fields => ({ id: TASK, status: 'running', '…': '…', ...fields });
  const CODE = { http: 'core/internal/wake/http.go', state: 'core/internal/state/task_google.go', commands: 'core/internal/conversation/commands.go',
    call: 'core/internal/conversation/call.go', workflow: 'core/internal/task/workflow.go', steps: 'core/internal/agent/steps.go', events: 'core/internal/state/task_events.go' };

  // What the outer model sends, and what it gets back
  const toolFlow = (name, args, saved, note, rejected) => [
    { from: 'Outer model', to: 'Runtime', call: name, source: CODE.commands, note,
      request: { id: 'call_1', type: 'function', function: { name, arguments: JSON.stringify(args) } } },
    { from: 'Runtime', to: 'Firestore', call: 'task_commands/{id}', source: CODE.state, saved },
    { from: 'Runtime', to: 'Outer model', call: 'tool result', source: CODE.call,
      response: { role: 'tool', tool_call_id: 'call_1', content: JSON.stringify({ accepted: true, run_id: saved.run_id, status: 'running', command: { id: saved.id, kind: saved.kind, position: saved.position } }) },
      other: rejected },
  ];
  const STALE = ['A rejection comes back as a tool result, such as {"error": "question is no longer current"}. The turn itself doesn\'t fail.'];
  const NOT_OPEN = ['A rejection comes back as a tool result, such as {"error": "task is not open"}. The turn itself doesn\'t fail.'];

  // What a button in the task panel sends
  const panelFlow = (body, receipt, other) => ({ from: 'Browser', to: 'Go service', call: 'POST /api/command', source: CODE.http, request: body,
    response: { status: 202, body: receipt }, other });
  const COMMON = ['403 if the project is paused', '503 if it saved but the wake couldn\'t be confirmed. Retrying the same request is safe.'];

  // The inbox record for each kind of event
  const event = (id, kind, sequence, fields, note) => [{ from: 'Task', to: 'Firestore', call: 'conversation_inputs/event-{id}', source: CODE.events, note,
    saved: { id: `event-${id}`, position: 14, created_at: TIME, event: { id, run_id: TASK, kind, sequence, instruction_revision: 2, ...fields, created_at: TIME } } }];

  const askFlow = [
    { from: 'Inner model', to: 'Worker', call: 'wait_for_input', source: CODE.steps, note: 'It must be the only call in the response.',
      response: { functionCall: { id: 'call_7', name: 'wait_for_input', args: { question: QUESTION } } } },
    { from: 'Worker', to: 'Firestore', call: 'runs/{id}', source: CODE.state, note: 'The record still says running. The question field is what makes it a waiting task.',
      saved: run({ waiting: QUESTION, event_sequence: 1, question: { id: QUESTION_ID, kind: 'input', text: QUESTION, call_id: 'call_7', instruction_revision: 1 } }) },
    ...event(QUESTION_ID, 'needs_input', 1, { text: QUESTION, reason: 'input' }, 'Saved in the same transaction as the run. This starts an outer turn.'),
  ];

  window.FLOWS = {
    // ---- Contract page: outer loop tools
    dispatch: toolFlow('dispatch', { request: REQUEST }, command('start', { id: TASK, text: REQUEST, position: 1, revision: 1, source_input_ids: ['3f6c…'] }),
      'Every call in a response is validated before any is accepted, up to 16 calls.'),
    'tool-steer': toolFlow('steer', { run_id: TASK, text: 'After seven, please.' }, command('steer', { text: 'After seven, please.', revision: 2, source_input_ids: ['8d21…'] }),
      'The model has to use the exact task ID from the task records.', NOT_OPEN),
    'tool-answer': toolFlow('answer', { run_id: TASK, question_id: QUESTION_ID, text: 'Saturday.' }, command('answer', { text: 'Saturday.', question_id: QUESTION_ID }),
      'The question ID has to match the task\'s open question.', STALE),
    'tool-continue': toolFlow('continue', { run_id: TASK, question_id: BUDGET_ID }, command('continue', { position: 4, question_id: BUDGET_ID }),
      'Only when Austin has said to keep going. It takes no text.', STALE),
    'tool-stop': toolFlow('stop', { run_id: TASK }, command('stop', { revision: 2 }), 'Other tasks carry on unchanged.', NOT_OPEN),

    // ---- Contract page: inner loop control tool and events
    wait: askFlow,
    'event-needs_input': event(QUESTION_ID, 'needs_input', 1, { text: QUESTION, reason: 'input' }, 'A budget question looks the same, with its own ID such as …-budget-p3.'),
    'event-result': event(`${TASK}-terminal`, 'result', 2, { text: 'Saturday at 7:30 is open.', operation_refs: ['op-…'] }, 'A task has one final event, so its ID ends in -terminal.'),
    'event-failure': event(`${TASK}-terminal`, 'failure', 2, { reason: 'Model cost is unknown for a provider request; task stopped before further model calls.', operation_refs: ['op-…'] }, 'operation_refs lists what the task did before it failed.'),
    'event-stopped': event(`${TASK}-terminal`, 'stopped', 2, { reason: 'Stopped by Austin.', operation_refs: ['op-…'] }, 'operation_refs lists effects that had already happened.'),

    // ---- Task lifecycle page: commands
    start: [
      { from: 'Outer loop', to: 'Firestore', call: 'runs/{id}', source: CODE.state, note: 'Only dispatch creates this command. The panel API has no start.',
        saved: run({ request: REQUEST, temporal: true, protocol_version: 2, revision: 1, position: 1, started_at: TIME }) },
      { from: 'Relay', to: 'Temporal', call: 'SignalWithStartWorkflow', source: 'core/internal/task/client.go',
        request: { workflow_id: `nori-task-${TASK}`, workflow_type: 'nori-task-v2', args: [TASK], signal: 'task-command' } },
      { from: 'Workflow', to: 'Inner model', call: 'next model call', source: CODE.workflow, request: { role: 'user', parts: [{ text: REQUEST }] } },
    ],
    steer: [
      panelFlow({ id: CMD, run_id: TASK, kind: 'steer', text: 'After seven, please.' },
        { command: command('steer', { text: 'After seven, please.', revision: 2 }), run: run({ revision: 2, position: 2 }) },
        ['409 if the task has finished', ...COMMON]),
      { from: 'Workflow', to: 'Inner model', call: 'next model call', source: CODE.workflow, note: 'The words go in as written. Anything decided on the old revision is thrown away.',
        request: { role: 'user', parts: [{ text: 'After seven, please.' }] } },
    ],
    answer: [
      panelFlow({ id: CMD, run_id: TASK, kind: 'answer', text: 'Saturday.', question_id: QUESTION_ID },
        { command: command('answer', { text: 'Saturday.', question_id: QUESTION_ID }), run: run({ revision: 1, position: 2, question: { id: QUESTION_ID, resolved_by: CMD, '…': '…' } }) },
        ['409 “question is no longer current” if the ID doesn\'t match the open question', ...COMMON]),
      { from: 'Workflow', to: 'Inner model', call: 'tool response', source: CODE.workflow, note: 'It reaches the model as the result of its wait_for_input call. The revision doesn\'t change.',
        request: { functionResponse: { id: 'call_7', name: 'wait_for_input', response: { answer: 'Saturday.', question_id: QUESTION_ID } } } },
    ],
    continue: [
      panelFlow({ id: CMD, run_id: TASK, kind: 'continue', question_id: BUDGET_ID },
        { command: command('continue', { position: 4, question_id: BUDGET_ID }), run: run({ revision: 1, position: 4, spent_usd: 1.0021, budget_usd: 1 }) },
        ['409 if there\'s no matching budget question open', ...COMMON]),
      { from: 'Workflow', to: 'Inner model', call: 'next model call', source: CODE.workflow, note: 'The workflow adds $1 to its budget, then tells the model. The Firestore copy catches up afterwards.',
        request: { role: 'user', parts: [{ text: 'Austin approved another $1.00 of model spend for this task. Continue th…' }] } },
    ],
    stop: [
      { from: 'Browser', to: 'Go service', call: 'POST /api/cancel', source: CODE.http, note: 'The Stop button uses this endpoint. POST /api/command with kind stop does the same.',
        request: { id: TASK }, response: { status: 202, body: { run: run({ cancel_requested: true, revision: 2 }) } }, other: ['400 if the ID isn\'t a UUID', '404 if there\'s no such task', '409 if the task has finished', 'Stop is still accepted while the project is paused'] },
      { from: 'Worker', to: 'Firestore', call: 'runs/{id}', source: CODE.state, note: 'Requested isn\'t stopped. This is only written once the workflow has wound down.',
        saved: run({ status: 'cancelled', error: 'Stopped by Austin.', cancel_requested: true, finished_at: TIME }) },
    ],

    // ---- Task lifecycle page: moving between states
    'move-ask': askFlow,
    'move-budget': [
      { from: 'Workflow', to: 'Firestore', call: 'runs/{id}', source: CODE.workflow, note: 'The text is built from recorded facts, so asking costs no model call.',
        saved: run({ spent_usd: 1.0021, budget_usd: 1, question: { id: BUDGET_ID, kind: 'budget', instruction_revision: 1,
          text: 'I reached this task\'s $1.00 model budget ($1.0021 spent over 14 model calls).\n\nDone so far: 6 tool calls (browser_navigate ×6).\n\nRemaining: the task isn\'t finished. Request: Find a time for dinner.\n\nReply continue to grant another $1.00, or Stop to end the task.' } }) },
    ],
    'move-done': [
      { from: 'Worker', to: 'Firestore', call: 'runs/{id}', source: CODE.state, note: 'If new input is still waiting to be routed, this holds until it has been.',
        saved: run({ status: 'completed', result: 'Saturday at 7:30 is open.', waiting: '', question: null, finished_at: TIME }) },
      ...event(`${TASK}-terminal`, 'result', 2, { text: 'Saturday at 7:30 is open.', operation_refs: ['op-…'] }),
    ],
    'move-failed': [
      { from: 'Worker', to: 'Firestore', call: 'runs/{id}', source: CODE.state,
        saved: run({ status: 'failed', error: 'Model cost is unknown for a provider request; task stopped before further model calls.', cost_unknown: true, finished_at: TIME }) },
      ...event(`${TASK}-terminal`, 'failure', 2, { reason: 'Model cost is unknown for a provider request; task stopped before further model calls.', operation_refs: ['op-…'] }),
    ],
    'move-stopped': [
      { from: 'Worker', to: 'Firestore', call: 'runs/{id}', source: CODE.state,
        saved: run({ status: 'cancelled', error: 'Stopped by Austin.', cancel_requested: true, finished_at: TIME }) },
      ...event(`${TASK}-terminal`, 'stopped', 2, { reason: 'Stopped by Austin.', operation_refs: ['op-…'] }),
    ],
  };
})();

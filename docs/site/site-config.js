// The site's name and navigation. Each page is ['file name without .html', 'Title in the rail'].
// A group named 'Planned' labels its pages as not built yet.
window.SITE = {
  name: 'Dokyu',
  nav: [
    ['Start', [['index', 'Overview'], ['product', 'What Nori should do'], ['request', 'Following a request']]],
    ['The loops', [['outer-loop', 'Outer loop'], ['classification', 'Classifying a message'], ['outer-model-failure', 'Outer model failure'], ['inner-loop', 'Inner loop'], ['transcript', 'Task transcript'], ['contract', 'How the loops talk'], ['tasks', 'Task lifecycle'], ['completion', 'Finishing a task']]],
    ['Agent service', [['system', 'Service overview'], ['records', 'Records and delivery'], ['tools', 'Tools'], ['catalog', 'Tool catalog'], ['memory', 'Memory and context'], ['memory-recall', 'Memory recall and screen'], ['results', 'Results and files'], ['budgets', 'Budgets and controls'], ['observability', 'Observability'], ['deploy', 'Deployment'], ['authority', 'Tool authority']]],
    ['Assistant', [['apps', 'Connections and MCP'], ['permissions', 'Permissions and approvals'], ['schedules', 'Automations']]],
    ['Clients', [['clients', 'The host app and clients'], ['api', 'Service API'], ['web-client', 'Test workbench'], ['frontend', 'Workbench architecture'], ['theme', 'Theme']]],
    ['Planned', [['roadmap', 'Roadmap']]],
    ['Reference', [['decisions', 'Decisions'], ['open-questions', 'Open questions'], ['code', 'Code map'], ['words', 'Words we use'], ['reading', 'Reading list'], ['sources', 'Sources']]],
  ],
};

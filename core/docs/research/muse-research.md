# Muse research

Research cutoff: **October 24, 2026**.

Scope: Meta's Muse personal agent, its virtual-machine and browser architecture, model capabilities, memory, and background execution. Official descriptions and independent runtime observations are identified separately. The September architecture post describes the launch system; later model announcements are dated where relevant.

## Product and model boundaries

Muse is a personal-agent application with a persistent working environment, connected services, memory, and background tasks. Its product design centers on an ongoing main conversation, with side chats for separate subjects. Users can inspect activity and edit memory. Background results are evaluated before they generate a notification. [Meta: How We Designed Muse](https://introducing.muse.ai/)

The application, its models, and the open-weight release are separate artifacts:

| Name | Meaning |
| --- | --- |
| Muse | The personal-agent product and its surrounding infrastructure |
| Muse Spark 1.3 | A model trained for agentic and coding workloads |
| Muse Wonder | A newer model family for demanding agentic work |
| Muse Wonder Full | A higher-capability variant offered through Muse Code subscriptions |
| Muse Spark Open | A downloadable checkpoint with a published model card |

Meta introduced the Muse product on September 8, 2026. Its October 12 Wonder announcement said Wonder would subsequently power Muse experiences, beginning with subscribers. That announcement does not establish which checkpoint serves a particular consumer session. [Muse launch](https://about.fb.com/news/2026/09/introducing-muse-personal-ai-agent/), [Wonder announcement](https://research.meta.ai/blog/muse-wonder), [Model availability](https://dev.meta.ai/docs/models)

## Published VM architecture

Meta describes a dedicated Linux VM per user as the system of record. Clients connect to it, while constrained proxies reach external inference and telemetry infrastructure.

| Boundary | Published components |
| --- | --- |
| Runtime cell | Hatch agent harness, workspace, tools; Debian environment in `systemd-nspawn` |
| Protected VM services | Postgres, `hatch-authd`, `privsep` workers, `hatch-safety`, Sentinel |
| External infrastructure | Model inference and connected services |

Runtime root is remapped to an unprivileged user. Capabilities and syscalls are restricted. Unix sockets use authenticated peer identities and access controls. [Meta: How We Built Safety Into Muse](https://research.meta.ai/blog/security-and-safety-for-ai-agents-our-approach-with-muse)

Sentinel authorizes connector operations and concrete outbound requests. Surrogate credentials are replaced with real credentials at approved egress. Kernel-based data tracking removes narrow automatic permissions after private-data reads. Approvals bind destinations and scopes. [Meta: Sentinel and network egress](https://research.meta.ai/blog/security-and-safety-for-ai-agents-our-approach-with-muse)

`systemd-nspawn` is the container layer inside the VM; it does not identify the VM's hypervisor. A runtime container, a user VM, and the infrastructure serving model inference are distinct boundaries.

## Computer use

### Documented product browser

Meta describes a specialized browser subagent using accessibility snapshots through a restricted Chrome DevTools Protocol broker. That interface excludes raw DOM inspection and arbitrary page JavaScript. Secure credential entry and user takeover pause the agent. [Meta: Browser architecture](https://research.meta.ai/blog/security-and-safety-for-ai-agents-our-approach-with-muse)

An accessibility snapshot gives the agent named controls and element references. A screenshot supplies visual information that structured page descriptions may omit. These observations serve different purposes; a browser workflow need not use pixels for every action.

### Findings from runtime inspection

Peter James's September 28 investigation reconstructs additional browser machinery from runtime files, binary strings, and documentation. It reports:

- A Rust browser broker and a remote pool of pre-warmed browser VMs, with a local fallback.
- Persistent browser profiles for cookies and session state.
- Accessibility-reference resolution, actionability checks, consent gates, input-dispatch verification, and monitoring of page changes after input.
- Separate broker directories associated with interactive, scheduled, and proactive work.
- Configurable residential egress and location-aware provisioning.

The evidence indicates available components and execution paths. It does not establish how frequently each path runs or a universal deployment configuration. [James: Inside Muse's browser stack](https://mouse.dev/blog/muse-browser)

The same investigation identifies a lighter retrieval path through a browser service, including direct HTTP and third-party retrieval services. Some information-gathering tasks can therefore avoid an interactive browser session. [James: Lightweight retrieval path](https://mouse.dev/blog/muse-browser)

### Public model API

Meta's native computer-use API defines a screenshot-and-action protocol. The application supplies the environment and driver:

```text
Task and initial screenshot
    → model returns computer_call actions
    → driver executes the action batch
    → driver captures a fresh screenshot
    → application returns computer_call_output with the matching call_id
    → next model decision
```

The documented endpoint is the Responses API. The driver owns execution, screenshot capture, safety checks, logging, and termination. Coordinates refer to the screenshot the model received. If an action fails or changes the interface unexpectedly, the driver should stop the batch and return a new observation. The API does not provision a hosted VM. [Meta: Build a computer-use agent](https://dev.meta.ai/docs/computer-use)

Meta also publishes a Spark cookbook using OpenCode, a Cua Linux desktop container, and an MCP bridge for screenshots, input, and shell commands. It demonstrates another supported integration route, rather than documenting the consumer product's complete harness. [Meta: Computer-use cookbook](https://dev.meta.ai/docs/cookbook/computer-use)

## Memory, skills, and background work

Meta's product description establishes persistent memory, inspectable memory files, scheduled work, event-driven work, and a decision about whether a result deserves a proactive message. It does not publish the complete scheduling or memory-selection algorithms. [Meta: Product design](https://introducing.muse.ai/)

A separate filesystem investigation reports the following organization in one environment:

| Material | Reported purpose |
| --- | --- |
| `MEMORY.md` | Short record of facts, preferences, and commitments |
| Dated memory files | Detailed daily records |
| `memory/bank/` | Categorized material with citations to source lines |
| Skill directories | Instructions paired with commands or supporting code |
| Nightly “dream” runs | Review recent conversations and write guidance for future sessions |
| Self-improvement records | Receipts for changes to memory and workspace files |

This evidence supports file-based retrieval and maintenance as part of the system. It does not establish that every user's environment has the same files or that personalization updates model weights. [James: Runtime and memory inspection](https://mouse.dev/blog/muse-runtime-export)

Research note: persistent files can change later behavior by changing what the agent reads. That mechanism should be distinguished from training a new checkpoint. The available descriptions do not establish per-user online weight training.

## Model capabilities

### Agent training

Meta says Spark 1.3 was trained across diverse harnesses to sustain longer tasks, follow detailed constraints, incorporate corrections, manage multiple workflows in one thread, and recover from gaps in its plan. These capabilities are relevant to a persistent agent that receives new instructions while work is underway. They remain vendor descriptions rather than an isolated measurement of the consumer product. [Meta: Spark 1.3](https://research.meta.ai/blog/introducing-muse-spark-1-3)

Wonder introduces two named capabilities:

- **Active Memory:** carrying important context across conversations and retrieving relevant earlier details.
- **Active Focus:** continuing toward a difficult goal, trying alternatives, working through blockers, and adapting to new information.

The announcement connects these capabilities to long-running agent tasks and stronger base models. It does not disclose a complete implementation of either mechanism. [Meta: Wonder capabilities](https://research.meta.ai/blog/muse-wonder)

### Published architecture of Spark Open

The released Spark Open checkpoint has a more concrete specification:

| Attribute | Published value |
| --- | --- |
| Architecture | Mixture-of-experts transformer with a perception encoder |
| Parameters | 816B total; 42B active |
| Transformer layers | 62 |
| Experts | 256; eight selected per token |
| Attention | Grouped-query attention; 128 query heads and 16 key/value heads |
| Perception encoder | Approximately 1.8B parameters |
| Input | Text, images, audio, and video |
| Output | Text |
| Context | Approximately one million tokens |

These specifications apply to Spark Open. They do not identify the weights, post-training, serving configuration, or orchestration used by every Muse session. [Official Spark Open model card](https://huggingface.co/meta-models/Muse-Spark-Open-816B-A42B)

Meta lists approximately 1.65 TB of BF16 weights or 537 GB of NVFP4 weights, excluding cache and runtime overhead. The user's working VM should therefore be distinguished from the accelerator infrastructure serving a model of this scale. [Meta: Spark Open architecture and hardware](https://dev.meta.ai/docs/muse-spark-open)

## Evaluation notes

The Wonder model card distinguishes average partial reward on computer-use tasks from strict binary completion. It also identifies comparisons that use different harnesses. A high partial score does not establish a matching probability of completing an entire unattended task. The card lists inaccurate responses, multi-step reasoning errors, and visual/spatial limitations among known failure modes. [Wonder model card](https://research.meta.ai/static/muse-wonder-model-card)

Spark 1.3's evaluation methodology likewise describes best-effort competitor configurations and material differences between benchmark setups. Published results need to be read with their harness, task version, reasoning settings, and grading method. [Spark 1.3 evaluation methodology](https://research.meta.ai/static/muse-spark-1-3-multimodal-evaluation-methodology)

## Secure VM and Confidential VM

Meta's launch announcement treats Secure VM and Confidential VM as separate stages. Confidential VM was announced as future work intended to prevent Meta from accessing the VM's contents through cryptographic protections. The sources reviewed do not establish general availability of that mode by the research cutoff. A claim about isolation between users should not be expanded into a claim that the service operator cannot access their data. [Meta: Launch and confidentiality plans](https://about.fb.com/news/2026/09/introducing-muse-personal-ai-agent/)

## Unresolved questions

- Which model variants and routing policies serve specific Muse features and subscription tiers?
- Which hypervisor and VM lifecycle mechanisms are used in current production deployments?
- What are the exact checkpoint, replay, retry, and effect-reconciliation semantics of the consumer harness?
- How are browser leases, profiles, and simultaneous subagents coordinated across tasks?
- How are conflicting memories resolved, and how is memory quality evaluated over repeated maintenance cycles?
- Which parts of the observed runtime organization are stable product contracts?
- What independent evidence verifies the final confidentiality boundary when Confidential VM is released?

## Source register

| Source | Evidence type | Main use |
| --- | --- | --- |
| [Muse launch](https://about.fb.com/news/2026/09/introducing-muse-personal-ai-agent/) | Official announcement; September 8, 2026 | Product scope and confidentiality plans |
| [How We Built Safety Into Muse](https://research.meta.ai/blog/security-and-safety-for-ai-agents-our-approach-with-muse) | Official engineering description; September 8, 2026 | Launch architecture, privilege separation, browser controls |
| [How We Designed Muse](https://introducing.muse.ai/) | Official product account; September 2026 | Interaction, memory, background behavior |
| [Spark 1.3](https://research.meta.ai/blog/introducing-muse-spark-1-3) | Official model announcement; September 2, 2026 | Agent training and behavior |
| [Wonder](https://research.meta.ai/blog/muse-wonder) | Official model announcement; October 12, 2026 | Memory, focus, and rollout language |
| [Model documentation](https://dev.meta.ai/docs/models) | Official developer documentation | Model and product boundaries |
| [Computer-use API](https://dev.meta.ai/docs/computer-use) | Official developer documentation | Native action protocol |
| [Computer-use cookbook](https://dev.meta.ai/docs/cookbook/computer-use) | Official worked example | Spark, Cua, and MCP integration |
| [Spark Open model card](https://huggingface.co/meta-models/Muse-Spark-Open-816B-A42B) | Official released-checkpoint documentation; October 15, 2026 | Neural architecture and limitations |
| [Spark Open developer documentation](https://dev.meta.ai/docs/muse-spark-open) | Official developer documentation | Weight footprint and serving requirements |
| [Wonder model card](https://research.meta.ai/static/muse-wonder-model-card) | Official evaluation report | Metrics and limitations |
| [Spark 1.3 methodology](https://research.meta.ai/static/muse-spark-1-3-multimodal-evaluation-methodology) | Official evaluation report | Comparison conditions |
| [Browser-stack investigation](https://mouse.dev/blog/muse-browser) | Independent firsthand inspection; Peter James, September 28, 2026 | Browser pools and broker machinery |
| [Runtime inspection](https://mouse.dev/blog/muse-runtime-export) | Independent firsthand inspection; Peter James, September 22, 2026 | Files, memory, and skills |

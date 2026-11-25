// The honest "not available yet" pages (ADR 0007 table, UI-SPEC section 6, the read-only notices of the mockup).
// Each says what the page will show, why it is not available in this release, and where the thing is configured today.
// Text between backticks is rendered as code. There is no data here, and none is invented.

export interface PlannedContent {
    title: string;
    strip: string;
    noticeTitle: string;
    notice: string;
    willShow: string[];
    why: string;
    today: string;
}

export interface Seg {
    text: string;
    code: boolean;
}

/** Splits "Set `gateway.models` in values" into text and code segments. */
export function segments(s: string): Seg[] {
    return s
        .split('`')
        .map((text, i) => ({ text, code: i % 2 === 1 }))
        .filter((x) => x.text !== '');
}

export const PLANNED: Record<string, PlannedContent> = {
    overview: {
        title: 'Overview',
        strip: 'platform catalog · policy · gateway metrics',
        noticeTitle: 'Not available yet',
        notice: 'The overview needs spend and turn counts from the gateway metrics and the schedules, which the console API does not read in this release.',
        willShow: [
            'Agents by state, and definitions used against `maxAgents`',
            'Spend today and turns in the last 24 hours',
            'What needs attention: definitions that are not Ready, failed schedules, components that are off but referenced',
            'Active adapters and optional components, and the latest changes to definitions',
        ],
        why: 'Part of it needs the observability adapter (ADR 0009), which has no route in the console API yet. A page with half of its tiles would show invented zeros, so it waits for all of its sources.',
        today: 'The Agents registry lists every definition with its state and conditions, and its toolbar shows the definitions used. The platform limits are in the policy summary of the builder.',
    },
    playground: {
        title: 'Playground',
        strip: 'A2A gate · caller console',
        noticeTitle: 'Not available yet',
        notice: 'Sending a message goes through the A2A gate as caller `console`. The console API has no route for it in this release.',
        willShow: [
            'A conversation with one Ready agent, with its tool calls as collapsed rows',
            'A footer per turn: model calls, tokens, cost, duration and a link to the trace',
            'The caller `console` and the user id the agent sees',
        ],
        why: 'The playground needs a gate caller rule for `console`, the gate switched on, and a route that sends the message. ADR 0007 puts it in a later slice than the registry and the builder.',
        today: 'Call an agent through the A2A gate with a caller rule from `governance.a2a.callers` in chart values, or ask it through a channel binding in `channels.bindings`.',
    },
    skills: {
        title: 'Skills',
        strip: 'chart values · skills (digest-pinned OCI images)',
        noticeTitle: 'Read-only until the Skill kind exists',
        notice: 'Today skills are OCI images listed under `skills` in chart values with a pinned digest. Change them there and run `install.sh`.',
        willShow: [
            'Each skill with its state and shortened digest, and the agents that use it',
            'Versions and the full reference of the selected skill',
            '`SKILL.md` when it can be read',
        ],
        why: 'The `Skill` kind and the organisation skill registry do not exist yet. The names a definition may use are shown in the builder, from the platform catalog.',
        today: '`skills` in chart values, one digest-pinned OCI image each; the policy `allowedSkills` decides which of them a built agent may use.',
    },
    models: {
        title: 'Models',
        strip: 'agentgateway · AgentgatewayModel · chart values gateway.models',
        noticeTitle: 'Read-only until the ModelProvider and ModelRoute kinds exist',
        notice: 'Today models are set in chart values under `gateway.models`, and provider keys are Secrets in `agentgateway-system`. Change them there and run `install.sh`.',
        willShow: [
            'Providers and routes: each gateway model with its provider, endpoint and health rules',
            "Keys and budgets: one row per gateway user, its token limit and today's spend",
            'The agents that use each model',
        ],
        why: 'The `ModelProvider` and `ModelRoute` kinds do not exist yet (ADR 0008), and spend needs the gateway metrics, which the console API does not read.',
        today: '`gateway.models` in chart values; the names a built agent may pick come from the catalog and the policy `allowedModels`.',
    },
    tools: {
        title: 'Tools',
        strip: 'ToolHive · MCPServer, MCPServerEntry, VirtualMCPServer · chart values mcp.servers, mcp.profiles',
        noticeTitle: 'Read-only until the ToolServer and ToolProfile kinds exist',
        notice: 'Today servers and profiles are set in chart values under `mcp.servers` and `mcp.profiles`. A new server image is a supply-chain decision and will need operator approval.',
        willShow: [
            'MCP servers with their tools, status and calls in the last 24 hours',
            'Tool profiles: the tool server each one is served by, code mode, and the agents that use it',
            'Which profiles offer which tool',
        ],
        why: 'The `ToolServer` and `ToolProfile` kinds do not exist yet, and the console API does not read ToolHive resources. The tool profiles of the catalog are already offered in the builder.',
        today: '`mcp.servers` and `mcp.profiles` in chart values.',
    },
    knowledge: {
        title: 'Knowledge',
        strip: 'kb service · namespace openagentix-knowledge',
        noticeTitle: 'Read-only until the KnowledgeCollection and KnowledgeSource kinds exist',
        notice: 'Today documents are added with `hack/kb-ingest.sh <dir> <collection>` and curated facts with `hack/kb-graph-load.sh`.',
        willShow: [
            'Collections with documents, chunks and last ingest; graph statistics where the graph is on',
            'Sources and their sync schedule',
            'A graph view with the path of a question and the document each fact came from',
        ],
        why: 'The `KnowledgeCollection` and `KnowledgeSource` kinds do not exist yet, and the console API has no route to the knowledge service.',
        today: "`hack/kb-ingest.sh` and `hack/kb-graph-load.sh`; whether a built agent may use knowledge is the policy `allowKnowledge` and the builder's Knowledge switch.",
    },
    channels: {
        title: 'Channels and triggers',
        strip: 'Agent Kourier · ChatConnection, Binding · Telegram bridge',
        noticeTitle: 'Read-only until the Channel and Trigger kinds exist',
        notice: 'Today connections and bindings are set in chart values under `channels.connections` and `channels.bindings`. Credentials will be entered once and never shown again.',
        willShow: [
            'Connections: platform, verification scheme, whether the secret is set, readiness',
            'Bindings: connection, channel, agent, trigger, limits and the user id the agent sees',
            'The Telegram bridge: allowlist size and memory commands',
        ],
        why: 'The `Channel` and `Trigger` kinds do not exist yet, and the console API does not read Kourier resources.',
        today: '`channels.connections` and `channels.bindings` in chart values.',
    },
    schedules: {
        title: 'Schedules',
        strip: 'CronJob · namespace openagentix-schedules · chart values schedules[]',
        noticeTitle: 'Read-only until the Schedule kind exists',
        notice: 'Today schedules are set in chart values under `schedules`. Every firing is at least one model call.',
        willShow: [
            'Each schedule with its agent, cron expression in plain English, state and next run',
            'The last runs with outcome and duration',
            'The user id the agent sees, which is the scope of its memory',
        ],
        why: 'The `Schedule` kind does not exist yet, and the console API does not read CronJobs.',
        today: '`schedules` in chart values.',
    },
    traces: {
        title: 'Traces',
        strip: 'Observability adapter · bundled Jaeger · last 2,000 traces in memory',
        noticeTitle: 'Not available yet',
        notice: 'The console API does not read the trace store in this release. Prompts and answers are not stored (content capture is off).',
        willShow: [
            'Recent turns with time, agent, caller, duration, model calls, cost and outcome',
            'One turn as a waterfall of gate, agent, model, tool, memory and knowledge spans',
            'Attributes of the selected span, in platform names',
        ],
        why: 'It needs the observability adapter of ADR 0009. No route of the console API reads Jaeger yet, so the page would have nothing true to show.',
        today: 'The bundled Jaeger, when `observability.enabled` is true in chart values.',
    },
    costs: {
        title: 'Costs',
        strip: 'gateway metrics · agentgateway_gen_ai_client_cost_usd_total · VictoriaMetrics',
        noticeTitle: 'Not available yet',
        notice: 'The console API does not read the gateway metrics in this release. USD budgets come with the `Budget` kind.',
        willShow: [
            'Spend for a window, tokens in and out, cache read share, the most expensive agent',
            'Daily spend by gateway user',
            "Token budgets against today's use",
        ],
        why: 'It needs the gateway metrics (ADR 0009) and, for budgets, the `Budget` kind. Neither is behind a console route yet, and a cost page with sample numbers would break the rule that nothing is invented.',
        today: 'VictoriaMetrics, when `observability.enabled` is true in chart values; the gateway price catalog in `gateway.models`.',
    },
    memory: {
        title: 'Memory',
        strip: 'kagent memory · PostgreSQL with pgvector · CronJob memory-expiry',
        noticeTitle: 'Settings are edited per agent in the builder',
        notice: "A person's memories are never listed in the console. A Telegram user can list and forget their own with `/memories` and `/forget`.",
        willShow: [
            'The agents that have memory on, and for how many days',
            'The expiry job `memory-expiry`: schedule and the counts of its last run',
            'The secret scan mode and the last counts per pattern',
        ],
        why: 'The console API does not read the expiry CronJob in this release. Whether an agent remembers, and for how long, is already a field of its definition.',
        today: 'The Memory switch and its days in the agent builder; `memory` in the platform policy and the catalog.',
    },
    governance: {
        title: 'Governance',
        strip: 'ConfigMap control-plane-policy · A2A gate caller rules · EgressPolicy',
        noticeTitle: 'Owned by operators',
        notice: 'The platform policy and the gate rules are not edited in the console. Change `controlPlane.policy` and `governance.a2a.callers` in chart values or Git.',
        willShow: [
            'Policy: the platform policy as a list and as YAML',
            'Gate callers: caller, agents, user ids it may assert and methods',
            'Egress per agent: origins the definition allows, origins in effect, and the policy patterns',
        ],
        why: 'This page stays read-only in the community edition even when it is built: the policy is owned by operators. It is not built yet in this release.',
        today: '`controlPlane.policy` and `governance.a2a.callers` in chart values or Git. The builder shows the policy that applies to a draft.',
    },
};

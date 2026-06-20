# Memory

Memory enables your agent to remember user messages, agent replies, and tool results across interactions, giving it the context it needs to stay consistent, maintain conversation flow, and produce better answers over time.

Mastra agents can be configured to store [message history](https://mastra.ai/docs/memory/message-history). Additionally, you can enable:

- [Observational Memory](https://mastra.ai/docs/memory/observational-memory) (Recommended): Uses background agents to maintain a dense observation log that replaces raw message history as it grows. This keeps the context window small while preserving long-term memory.
- [Working memory](https://mastra.ai/docs/memory/working-memory): Stores persistent, structured user data such as names, preferences, and goals.
- [Semantic recall](https://mastra.ai/docs/memory/semantic-recall): Retrieves relevant past messages based on semantic meaning rather than exact keywords.
- [Multi-user threads](https://mastra.ai/docs/memory/multi-user-threads): Share one thread between multiple users.

If the combined memory exceeds the model's context limit, [memory processors](https://mastra.ai/docs/memory/memory-processors) can filter, trim, or prioritize content so the most relevant information is preserved.

Memory results will be stored in one or more of your configured [storage providers](https://mastra.ai/docs/memory/storage).

## When to use memory

Use memory when your agent needs to maintain multi-turn conversations that reference prior exchanges, recall user preferences or facts from earlier in a session, or build context over time within a conversation thread. Skip memory for single-turn requests where each interaction is independent.

## Quickstart

1. Install the `@mastra/memory` package.

   **npm**:

   ```bash
   npm install @mastra/memory@latest
   ```

   **pnpm**:

   ```bash
   pnpm add @mastra/memory@latest
   ```

   **Yarn**:

   ```bash
   yarn add @mastra/memory@latest
   ```

   **Bun**:

   ```bash
   bun add @mastra/memory@latest
   ```

2. Memory **requires** a storage provider to persist message history, including user messages and agent responses.

   For the purposes of this quickstart, use `@mastra/libsql`.

   **npm**:

   ```bash
   npm install @mastra/libsql@latest
   ```

   **pnpm**:

   ```bash
   pnpm add @mastra/libsql@latest
   ```

   **Yarn**:

   ```bash
   yarn add @mastra/libsql@latest
   ```

   **Bun**:

   ```bash
   bun add @mastra/libsql@latest
   ```

   > **Note:** For more details on available providers and how storage works in Mastra, visit the [storage](https://mastra.ai/docs/memory/storage) documentation.

3. Add the storage provider to your main Mastra instance to enable memory across all configured agents.

   ```typescript
   import { Mastra } from '@mastra/core'
   import { LibSQLStore } from '@mastra/libsql'

   export const mastra = new Mastra({
     storage: new LibSQLStore({
       id: 'mastra-storage',
       url: ':memory:',
     }),
   })
   ```

4. Create a `Memory` instance and pass it to the agent's `memory` option.

   ```typescript
   import { Agent } from '@mastra/core/agent'
   import { Memory } from '@mastra/memory'

   export const memoryAgent = new Agent({
     id: 'memory-agent',
     name: 'Memory Agent',
     memory: new Memory({
       options: {
         lastMessages: 20,
       },
     }),
   })
   ```

   > **Note:** Visit [Memory Class](https://mastra.ai/reference/memory/memory-class) for a full list of configuration options.

5. Call your agent, for example in [Studio](https://mastra.ai/docs/studio/overview). Inside Studio, start a new chat with your agent and take a look at the right sidebar. It'll now display various memory-related information.

## Message history

Pass a `memory` object with `resource` and `thread` to track message history.

- `resource`: A stable identifier for the user or entity.
- `thread`: An ID that isolates a specific conversation or session.

```typescript
const response = await memoryAgent.generate('Remember my favorite color is blue.', {
  memory: {
    resource: 'user-123',
    thread: 'conversation-123',
  },
})
```

To recall information stored in memory, call the agent with the same `resource` and `thread` values used in the original conversation.

```typescript
const response = await memoryAgent.generate("What's my favorite color?", {
  memory: {
    resource: 'user-123',
    thread: 'conversation-123',
  },
})

// Response: "Your favorite color is blue."
```

> **Warning:** Each thread has an owner (`resourceId`) that can't be changed after creation. Avoid reusing the same thread ID for threads with different owners, as this will cause errors when querying.

To list all threads for a resource, or retrieve a specific thread, [use the memory API directly](https://mastra.ai/docs/memory/message-history).

## Observational Memory

For long-running conversations, raw message history grows until it fills the context window, degrading agent performance. [Observational Memory](https://mastra.ai/docs/memory/observational-memory) solves this by running background agents that compress old messages into dense observations, keeping the context window small while preserving long-term memory.

```typescript
import { Agent } from '@mastra/core/agent'
import { Memory } from '@mastra/memory'

export const memoryAgent = new Agent({
  id: 'memory-agent',
  name: 'Memory Agent',
  memory: new Memory({
    options: {
      observationalMemory: true,
    },
  }),
})
```

> **Note:** See [Observational Memory](https://mastra.ai/docs/memory/observational-memory) for details on how observations and reflections work, and [the reference](https://mastra.ai/reference/memory/observational-memory) for all configuration options.

## Memory in multi-agent systems

When a [supervisor agent](https://mastra.ai/docs/agents/supervisor-agents) delegates to a subagent, Mastra isolates subagent memory automatically. No flag enables this as it happens on every delegation. Understanding how this scoping works lets you decide what stays private and what to share intentionally.

### How delegation scopes memory

Each delegation creates a fresh `threadId` and a deterministic `resourceId` for the subagent:

- **Thread ID**: Unique per delegation. The subagent starts with a clean message history every time it's called.
- **Resource ID**: Derived as `{parentResourceId}-{agentName}`. Because the resource ID is stable across delegations, resource-scoped memory persists between calls. A subagent remembers facts from previous delegations by the same user.
- **Memory instance**: If a subagent has no memory configured, it inherits the supervisor's `Memory` instance. If the subagent defines its own, that takes precedence.

The supervisor forwards its conversation context to the subagent so it has enough background to complete the task. Only the delegation prompt and the subagent's response are saved — the full parent conversation isn't stored. You can control which messages reach the subagent with the [`messageFilter`](https://mastra.ai/docs/agents/supervisor-agents) callback.

> **Note:** Subagent resource IDs are always suffixed with the agent name (`{parentResourceId}-{agentName}`). Two different subagents under the same supervisor never share a resource ID through delegation.

To go beyond this default isolation, you can share memory between agents by passing matching identifiers when you call them directly.

### Share memory between agents

When you call agents directly (outside the delegation flow), memory sharing is controlled by two identifiers: `resourceId` and `threadId`. Agents that use the same values read and write to the same data. This is useful when agents collaborate on a shared context — for example, a researcher that saves notes and a writer that reads them.

**Resource-scoped sharing** is the most common pattern. [Working memory](https://mastra.ai/docs/memory/working-memory) and [semantic recall](https://mastra.ai/docs/memory/semantic-recall) default to `scope: 'resource'`. If two agents share a `resourceId`, they share observations, working memory, and embeddings — even across different threads:

```typescript
// Both agents share the same resource-scoped memory
await researcher.generate('Find information about quantum computing.', {
  memory: { resource: 'project-42', thread: 'research-session' },
})

await writer.generate('Write a summary from the research notes.', {
  memory: { resource: 'project-42', thread: 'writing-session' },
})
```

Because both calls use `resource: 'project-42'`, the writer can access the researcher's observations, working memory, and semantic embeddings. Each agent still has its own thread, so message histories stay separate.

**Thread-scoped sharing** gives tighter coupling. [Observational Memory](https://mastra.ai/docs/memory/observational-memory) uses `scope: 'thread'` by default. If two agents use the same `resource` _and_ `thread`, they share the full message history. Each agent sees every message the other has written. This is useful when agents need to build on each other's exact outputs.

## Observability

Enable [Tracing](https://mastra.ai/docs/observability/tracing/overview) to monitor and debug memory in action. Traces show you exactly which messages and observations the agent included in its context for each request, helping you understand agent behavior and verify that memory retrieval is working as expected.

Open [Studio](https://mastra.ai/docs/studio/overview) and select the **Observability** tab in the sidebar. Open the trace of a recent agent request, then look for spans of LLMs calls.

## Switch memory per request

Use [`RequestContext`](https://mastra.ai/docs/server/request-context) to access request-specific values. This lets you conditionally select different memory or storage configurations based on the context of the request.

```typescript
export type UserTier = {
  'user-tier': 'enterprise' | 'pro'
}

const premiumMemory = new Memory()
const standardMemory = new Memory()

export const memoryAgent = new Agent({
  id: 'memory-agent',
  name: 'Memory Agent',
  memory: ({ requestContext }) => {
    const userTier = requestContext.get('user-tier') as UserTier['user-tier']

    return userTier === 'enterprise' ? premiumMemory : standardMemory
  },
})
```

> **Note:** Visit [Request Context](https://mastra.ai/docs/server/request-context) for more information.

## Related

- [`Memory` reference](https://mastra.ai/reference/memory/memory-class)
- [Tracing](https://mastra.ai/docs/observability/tracing/overview)
- [Request Context](https://mastra.ai/docs/server/request-context)
- [Mastra Code](https://code.mastra.ai/): A coding agent using Mastra's memory system
# Semantic recall

If you ask your friend what they did last weekend, they will search in their memory for events associated with "last weekend" and then tell you what they did. That's sort of like how semantic recall works in Mastra.

> **Watch 📹:** What semantic recall is, how it works, and how to configure it in Mastra → [YouTube (5 minutes)](https://youtu.be/UVZtK8cK8xQ)

## How semantic recall works

Semantic recall is RAG-based search that helps agents maintain context across longer interactions when messages are no longer within [recent message history](https://mastra.ai/docs/memory/message-history).

It uses vector embeddings of messages for similarity search, integrates with various vector stores, and has configurable context windows around retrieved messages.

![Diagram showing Mastra Memory semantic recall](/assets/images/semantic-recall-fd7b9336a6d0d18019216cb6d3dbe710.png)

When it's enabled, new messages are used to query a vector DB for semantically similar messages.

After getting a response from the LLM, all new messages (user, assistant, and tool calls/results) are inserted into the vector DB to be recalled in later interactions.

## Quickstart

Semantic recall is disabled by default. To enable it, set `semanticRecall: true` in `options` and provide a `vector` store and `embedder`:

```typescript
import { Agent } from '@mastra/core/agent'
import { Memory } from '@mastra/memory'
import { LibSQLStore, LibSQLVector } from '@mastra/libsql'
import { ModelRouterEmbeddingModel } from '@mastra/core/llm'

const agent = new Agent({
  id: 'support-agent',
  name: 'SupportAgent',
  instructions: 'You are a helpful support agent.',
  model: 'openai/gpt-5.5',
  memory: new Memory({
    storage: new LibSQLStore({
      id: 'agent-storage',
      url: 'file:./local.db',
    }),
    vector: new LibSQLVector({
      id: 'agent-vector',
      url: 'file:./local.db',
    }),
    embedder: new ModelRouterEmbeddingModel('openai/text-embedding-3-small'),
    options: {
      semanticRecall: true,
    },
  }),
})
```

## Using the `recall()` method

While `listMessages` retrieves messages by thread ID with basic pagination, [`recall()`](https://mastra.ai/reference/memory/recall) adds support for **semantic search**. When you need to find messages by meaning rather than recency, use `recall()` with a `vectorSearchString`:

```typescript
const memory = await agent.getMemory()

// Basic recall - similar to listMessages
const { messages } = await memory!.recall({
  threadId: 'thread-123',
  perPage: 50,
})

// Semantic recall - find messages by meaning
const { messages: relevantMessages } = await memory!.recall({
  threadId: 'thread-123',
  vectorSearchString: 'What did we discuss about the project deadline?',
  threadConfig: {
    semanticRecall: true,
  },
})
```

## Storage configuration

Semantic recall relies on a [storage and vector db](https://mastra.ai/reference/memory/memory-class) to store messages and their embeddings.

```ts
import { Memory } from '@mastra/memory'
import { Agent } from '@mastra/core/agent'
import { LibSQLStore, LibSQLVector } from '@mastra/libsql'

const agent = new Agent({
  memory: new Memory({
    // this is the default storage db if omitted
    storage: new LibSQLStore({
      id: 'agent-storage',
      url: 'file:./local.db',
    }),
    // this is the default vector db if omitted
    vector: new LibSQLVector({
      id: 'agent-vector',
      url: 'file:./local.db',
    }),
    options: {
      semanticRecall: true,
    },
  }),
})
```

Each vector store page below includes installation instructions, configuration parameters, and usage examples:

- [Astra](https://mastra.ai/reference/vectors/astra)
- [Chroma](https://mastra.ai/reference/vectors/chroma)
- [Cloudflare Vectorize](https://mastra.ai/reference/vectors/vectorize)
- [Convex](https://mastra.ai/reference/vectors/convex)
- [Couchbase](https://mastra.ai/reference/vectors/couchbase)
- [DuckDB](https://mastra.ai/reference/vectors/duckdb)
- [Elasticsearch](https://mastra.ai/reference/vectors/elasticsearch)
- [LanceDB](https://mastra.ai/reference/vectors/lance)
- [libSQL](https://mastra.ai/reference/vectors/libsql)
- [MongoDB](https://mastra.ai/reference/vectors/mongodb)
- [OpenSearch](https://mastra.ai/reference/vectors/opensearch)
- [Pinecone](https://mastra.ai/reference/vectors/pinecone)
- [PostgreSQL](https://mastra.ai/reference/vectors/pg)
- [Qdrant](https://mastra.ai/reference/vectors/qdrant)
- [S3 Vectors](https://mastra.ai/reference/vectors/s3vectors)
- [Turbopuffer](https://mastra.ai/reference/vectors/turbopuffer)
- [Upstash](https://mastra.ai/reference/vectors/upstash)

## Recall configuration

The following options control semantic recall behavior:

1. **topK**: The number of similar messages to retrieve
2. **messageRange**: The surrounding messages to include with each match
3. **scope**: Whether to search the current thread or all threads for a resource
4. **filter**: Metadata criteria that restrict search results

```typescript
const agent = new Agent({
  memory: new Memory({
    options: {
      semanticRecall: {
        topK: 3, // Retrieve 3 similar messages
        messageRange: 2, // Include 2 messages before and after each match
        scope: 'resource', // Search all threads for this resource
        filter: { projectId: { $eq: 'project-a' } },
      },
    },
  }),
})
```

> **Note:** `scope: 'resource'` is supported by the LibSQL, PostgreSQL, and Upstash storage adapters.

### Metadata filtering

The `filter` option restricts semantic recall results to messages with matching thread metadata.

```typescript
const agent = new Agent({
  memory: new Memory({
    options: {
      semanticRecall: {
        scope: 'resource',
        filter: {
          projectId: { $eq: 'project-a' },
          category: { $in: ['work', 'personal'] },
        },
      },
    },
  }),
})
```

Filters match metadata stored on message embeddings when messages are saved. If thread metadata changes later, existing embeddings keep their previous metadata until those messages are saved or indexed again.

Supported filter operators:

- `$and`: Logical AND
- `$eq`: Equal to
- `$gt`: Greater than
- `$gte`: Greater than or equal
- `$in`: In array
- `$lt`: Less than
- `$lte`: Less than or equal
- `$ne`: Not equal to
- `$nin`: Not in array
- `$or`: Logical OR

The following example demonstrates metadata filters for common use cases:

```typescript
// Filter by project
const options = {
  semanticRecall: { filter: { projectId: { $eq: 'my-project' } } },
}

// Filter by multiple categories
const options = {
  semanticRecall: { filter: { category: { $in: ['work', 'research'] } } },
}

// Filter by project and priority
const options = {
  semanticRecall: {
    filter: {
      $and: [{ projectId: { $eq: 'project-a' } }, { priority: { $gte: 3 } }],
    },
  },
}
```

## Embedder configuration

Semantic recall relies on an [embedding model](https://mastra.ai/reference/memory/memory-class) to convert messages into embeddings. Mastra supports embedding models through the model router using `provider/model` strings, or you can use any [embedding model](https://sdk.vercel.ai/docs/ai-sdk-core/embeddings) compatible with the AI SDK.

### Using the Model Router (Recommended)

The simplest way is to use a `provider/model` string with autocomplete support:

```ts
import { Memory } from '@mastra/memory'
import { Agent } from '@mastra/core/agent'
import { ModelRouterEmbeddingModel } from '@mastra/core/llm'

const agent = new Agent({
  memory: new Memory({
    embedder: new ModelRouterEmbeddingModel('openai/text-embedding-3-small'),
    options: {
      semanticRecall: true,
    },
  }),
})
```

Supported embedding models:

- **OpenAI**: `text-embedding-3-small`, `text-embedding-3-large`, `text-embedding-ada-002`
- **Google**: `gemini-embedding-001`
- **OpenRouter**: Access embedding models from various providers

```ts
import { Agent } from '@mastra/core/agent'
import { Memory } from '@mastra/memory'
import { ModelRouterEmbeddingModel } from '@mastra/core/llm'

const agent = new Agent({
  memory: new Memory({
    embedder: new ModelRouterEmbeddingModel({
      providerId: 'openrouter',
      modelId: 'openai/text-embedding-3-small',
    }),
  }),
})
```

The model router automatically handles API key detection from environment variables (`OPENAI_API_KEY`, `GOOGLE_API_KEY`, `OPENROUTER_API_KEY`). Google models also fall back to `GOOGLE_GENERATIVE_AI_API_KEY`.

### Using AI SDK Packages

You can also use AI SDK embedding models directly:

```ts
import { Memory } from '@mastra/memory'
import { Agent } from '@mastra/core/agent'
import { ModelRouterEmbeddingModel } from '@mastra/core/llm'

const agent = new Agent({
  memory: new Memory({
    embedder: new ModelRouterEmbeddingModel('openai/text-embedding-3-small'),
  }),
})
```

### Using FastEmbed (local)

To use FastEmbed (a local embedding model), install `@mastra/fastembed`:

**npm**:

```bash
npm install @mastra/fastembed@latest
```

**pnpm**:

```bash
pnpm add @mastra/fastembed@latest
```

**Yarn**:

```bash
yarn add @mastra/fastembed@latest
```

**Bun**:

```bash
bun add @mastra/fastembed@latest
```

Then configure it in your memory:

```ts
import { Memory } from '@mastra/memory'
import { Agent } from '@mastra/core/agent'
import { fastembed } from '@mastra/fastembed'

const agent = new Agent({
  memory: new Memory({
    embedder: fastembed,
  }),
})
```

## PostgreSQL index optimization

When using PostgreSQL as your vector store, you can optimize semantic recall performance by configuring the vector index. This is particularly important for large-scale deployments with thousands of messages.

PostgreSQL supports both IVFFlat and HNSW indexes. By default, Mastra creates an IVFFlat index, but HNSW indexes typically provide better performance, especially with OpenAI embeddings which use inner product distance.

```typescript
import { Memory } from '@mastra/memory'
import { PgStore, PgVector } from '@mastra/pg'

const agent = new Agent({
  memory: new Memory({
    storage: new PgStore({
      id: 'agent-storage',
      connectionString: process.env.DATABASE_URL,
    }),
    vector: new PgVector({
      id: 'agent-vector',
      connectionString: process.env.DATABASE_URL,
    }),
    options: {
      semanticRecall: {
        topK: 5,
        messageRange: 2,
        indexConfig: {
          type: 'hnsw', // Use HNSW for better performance
          metric: 'dotproduct', // Best for OpenAI embeddings
          m: 16, // Number of bi-directional links (default: 16)
          efConstruction: 64, // Size of candidate list during construction (default: 64)
        },
      },
    },
  }),
})
```

For detailed information about index configuration options and performance tuning, see the [PgVector configuration guide](https://mastra.ai/reference/vectors/pg).

## Disable semantic recall

Semantic recall is disabled by default (`semanticRecall: false`). Each call adds latency because new messages are converted into embeddings and used to query a vector database before the LLM receives them.

Keep semantic recall disabled when:

- Message history provides sufficient context for the current conversation.
- You're building performance-sensitive applications, like realtime two-way audio, where embedding and vector query latency is noticeable.

## Viewing recalled messages

When tracing is enabled, any messages retrieved via semantic recall will appear in the agent's trace output, alongside recent message history (if configured).
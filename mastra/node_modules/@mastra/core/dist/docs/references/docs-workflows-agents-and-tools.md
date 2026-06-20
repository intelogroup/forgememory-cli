# Agents and tools

Workflow steps can call agents to leverage LLM reasoning or call tools for type-safe logic. You can either invoke them from within a step's `execute()` function or compose them directly as steps using `createStep()`.

## Using agents in workflows

Use agents in workflow steps when you need reasoning, language generation, or other LLM-based tasks. Call from a step's `execute()` function for more control over the agent call (e.g., track message history or return structured output). Compose agents as steps when you don't need to modify how the agent is invoked.

### Calling agents

Call agents inside a step's `execute()` function using `.generate()` or `.stream()`. This lets you modify the agent call and handle the response before passing it to the next step.

```typescript
const step1 = createStep({
  execute: async ({ inputData, mastra }) => {
    const { message } = inputData

    const testAgent = mastra.getAgent('testAgent')
    const response = await testAgent.generate(
      `Convert this message into bullet points: ${message}`,
      {
        memory: {
          thread: 'user-123',
          resource: 'test-123',
        },
      },
    )

    return {
      list: response.text,
    }
  },
})
```

### Agents as steps

Compose an agent as a step using `createStep()` when you don't need to modify the agent call. Use `.map()` to transform the previous step's output into a `prompt` the agent can use.

![Agent as step](/assets/images/workflows-agent-tools-agent-step-b2f5be22552ce514f7f8cd785ffc5604.jpg)

```typescript
import { testAgent } from '../agents/test-agent'
const step1 = createStep(testAgent)

export const testWorkflow = createWorkflow({})
  .map(async ({ inputData }) => {
    const { message } = inputData
    return {
      prompt: `Convert this message into bullet points: ${message}`,
    }
  })
  .then(step1)
  .then(step2)
  .commit()
```

> **Info:** Visit [Input Data Mapping](https://mastra.ai/docs/workflows/control-flow) for more information.

When no `structuredOutput` option is provided, Mastra agents use a default schema that expects a `prompt` string as input and returns a `text` string as output:

```typescript
{
  inputSchema: {
    prompt: string
  },
  outputSchema: {
    text: string
  }
}
```

### Agents with structured output

When you need the agent to return structured data instead of plain text, pass the `structuredOutput` option to `createStep()`. The step's output schema will match your provided schema, enabling type-safe chaining to subsequent steps.

```typescript
const articleSchema = z.object({
  title: z.string(),
  summary: z.string(),
  tags: z.array(z.string()),
})

const agentStep = createStep(testAgent, {
  structuredOutput: { schema: articleSchema },
})

// Next step receives typed structured data
const processStep = createStep({
  id: 'process',
  inputSchema: articleSchema, // Matches agent's outputSchema
  outputSchema: z.object({ tagCount: z.number() }),
  execute: async ({ inputData }) => ({
    tagCount: inputData.tags.length, // Fully typed
  }),
})

export const testWorkflow = createWorkflow({})
  .map(async ({ inputData }) => ({
    prompt: `Generate an article about: ${inputData.topic}`,
  }))
  .then(agentStep)
  .then(processStep)
  .commit()
```

The `structuredOutput.schema` option accepts any Standard JSON Schema. The agent will generate output conforming to this schema, and the step's `outputSchema` will be automatically set to match.

> **Info:** Visit [Structured Output](https://mastra.ai/docs/agents/structured-output) for more options like error handling strategies and streaming with structured output.

## Using tools in workflows

Use tools in workflow steps to leverage existing tool logic. Call from a step's `.execute()` function when you need to prepare context or process responses. Compose tools as steps when you don't need to modify how the tool is used.

### Calling tools

Call tools inside a step's `.execute()` function. This gives you more control over the tool's input context, or process its response before passing it to the next step.

```typescript
import { testTool } from '../tools/test-tool'

const step2 = createStep({
  execute: async ({ inputData, requestContext }) => {
    const { text } = inputData

    const response = await testTool.execute({ text }, { requestContext })

    return {
      emphasized: response.emphasized,
    }
  },
})
```

### Tools as steps

Compose a tool as a step using `createStep()` when the previous step's output matches the tool's input context. You can use `.map()` to transform the previous step's output if they don't.

![Tool as step](/assets/images/workflows-agent-tools-tool-step-cfd56227ce83c2d03a8c8d0496faeeef.jpg)

```typescript
import { testTool } from '../tools/test-tool'

const step2 = createStep(testTool)

export const testWorkflow = createWorkflow({})
  .then(step1)
  .map(async ({ inputData }) => {
    const { formatted } = inputData
    return {
      text: formatted,
    }
  })
  .then(step2)
  .commit()
```

> **Info:** Visit [Input Data Mapping](https://mastra.ai/docs/workflows/control-flow) for more information.

## Related

- [Using Agents](https://mastra.ai/docs/agents/overview)
- [MCP Overview](https://mastra.ai/docs/mcp/overview)
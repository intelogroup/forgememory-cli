# Human-in-the-loop (HITL)

Some workflows need to pause for human input before continuing. When a workflow is [suspended](https://mastra.ai/docs/workflows/suspend-and-resume), it can return a message explaining why it paused and what’s needed to proceed. The workflow can then either [resume](#resuming-workflows-with-human-input) or [bail](#handling-human-rejection-with-bail) based on the input received. This approach works well for manual approvals, rejections, gated decisions, or any step that requires human oversight.

## Pausing workflows for human input

Human-in-the-loop input works much like [pausing a workflow](https://mastra.ai/docs/workflows/suspend-and-resume) using `suspend()`. The key difference is that when human input is required, you can return `suspend()` with a payload that provides context or guidance to the user on how to continue.

![Pausing a workflow with suspend()](/assets/images/workflows-suspend-4540783670f918109ac35beaf4db914b.jpg)

```typescript
import { createWorkflow, createStep } from '@mastra/core/workflows'
import { z } from 'zod'

const step1 = createStep({
  id: 'step-1',
  inputSchema: z.object({
    userEmail: z.string(),
  }),
  outputSchema: z.object({
    output: z.string(),
  }),
  resumeSchema: z.object({
    approved: z.boolean(),
  }),
  suspendSchema: z.object({
    reason: z.string(),
  }),
  execute: async ({ inputData, resumeData, suspend }) => {
    const { userEmail } = inputData
    const { approved } = resumeData ?? {}

    if (!approved) {
      return await suspend({
        reason: 'Human approval required.',
      })
    }

    return {
      output: `Email sent to ${userEmail}`,
    }
  },
})

export const testWorkflow = createWorkflow({
  id: 'test-workflow',
  inputSchema: z.object({
    userEmail: z.string(),
  }),
  outputSchema: z.object({
    output: z.string(),
  }),
})
  .then(step1)
  .commit()
```

## Providing user feedback

When a workflow is suspended, you can access the payload returned by `suspend()` by identifying the suspended step and reading its `suspendPayload`.

```typescript
const workflow = mastra.getWorkflow('testWorkflow')
const run = await workflow.createRun()

const result = await run.start({
  inputData: {
    userEmail: 'alex@example.com',
  },
})

if (result.status === 'suspended') {
  const suspendStep = result.suspended[0]
  const suspendedPayload = result.steps[suspendStep[0]].suspendPayload

  console.log(suspendedPayload)
}
```

### Example output

The data returned by the step can include a reason and help the user understand what's needed to resume the workflow.

```typescript
{
  reason: 'Confirm to send email.'
}
```

## Resuming workflows with human input

As with [restarting a workflow](https://mastra.ai/docs/workflows/suspend-and-resume), use `resume()` with `resumeData` to continue a workflow after receiving input from a human. The workflow resumes from the step where it was paused.

![Restarting a workflow with resume()](/assets/images/workflows-resume-1e54b4d0c753ff79571f6d6b05109a60.jpg)

```typescript
const workflow = mastra.getWorkflow('testWorkflow')
const run = await workflow.createRun()

await run.start({
  inputData: {
    userEmail: 'alex@example.com',
  },
})

const handleResume = async () => {
  const result = await run.resume({
    step: 'step-1',
    resumeData: { approved: true },
  })
}
```

### Handling human rejection with `bail()`

Use `bail()` to stop workflow execution at a step without triggering an error. This can be useful when a human explicitly rejects an action. The workflow completes with a `success` status, and any logic after the call to `bail()` is skipped.

```typescript
const step1 = createStep({
  execute: async ({ inputData, resumeData, suspend, bail }) => {
    const { userEmail } = inputData
    const { approved } = resumeData ?? {}

    if (approved === false) {
      return bail({
        reason: 'User rejected the request.',
      })
    }

    if (!approved) {
      return await suspend({
        reason: 'Human approval required.',
      })
    }

    return {
      message: `Email sent to ${userEmail}`,
    }
  },
})
```

## Multi-turn human input

For workflows that require input at multiple stages, the suspend pattern remains the same. Each step defines a `resumeSchema`, and `suspendSchema` typically with a reason that can be used to provide user feedback.

```typescript
const step1 = createStep({...});

const step2 = createStep({
  id: "step-2",
  inputSchema: z.object({
    message: z.string()
  }),
  outputSchema: z.object({
    output: z.string()
  }),
  resumeSchema: z.object({
    approved: z.boolean()
  }),
  suspendSchema: z.object({
    reason: z.string()
  }),
  execute: async ({ inputData, resumeData, suspend }) => {
    const { message } = inputData;
    const { approved } = resumeData ?? {};

    if (!approved) {
      return await suspend({
        reason: "Human approval required."
      });
    }

    return {
      output: `${message} - Deleted`
    };
  }
});

export const testWorkflow = createWorkflow({
  id: "test-workflow",
  inputSchema: z.object({
    userEmail: z.string()
  }),
  outputSchema: z.object({
    output: z.string()
  })
})
  .then(step1)
  .then(step2)
  .commit();
```

Each step must be resumed in sequence, with a separate call to `resume()` for each suspended step. This approach helps manage multi-step approvals with consistent UI feedback and clear input handling at each stage.

```typescript
const handleResume = async () => {
  const result = await run.resume({
    step: 'step-1',
    resumeData: { approved: true },
  })
}

const handleDelete = async () => {
  const result = await run.resume({
    step: 'step-2',
    resumeData: { approved: true },
  })
}
```

## Related

- [Control Flow](https://mastra.ai/docs/workflows/control-flow)
- [Suspend & Resume](https://mastra.ai/docs/workflows/suspend-and-resume)
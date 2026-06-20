# PubSub

Mastra uses a publish/subscribe (pub/sub) system as its internal event bus. Components publish events to named topics, and other components subscribe to those topics to react. The backend you configure decides how far those events travel: within one process, across processes on one host, or across separate instances.

You set the backend once on the `Mastra` instance, and the rest of the system uses it without changes. By default, Mastra uses an in-process backend that needs no setup.

## How Mastra uses PubSub

Several built-in systems publish and subscribe to events through the same pub/sub bus:

- **Workflow execution**: The scheduler publishes a `workflow.start` event, and a long-lived worker consumes it to run the workflow. Step and lifecycle events flow over pub/sub during execution.
- **Scheduled workflows**: The scheduler dispatches due runs by publishing to the workflow topic. See [Scheduled workflows](https://mastra.ai/docs/workflows/scheduled-workflows).
- **Background tasks**: A task manager dispatches work to a worker group and fans task lifecycle updates out to subscribers, which is how background task streams stay live. See [Background task streaming](https://mastra.ai/docs/streaming/background-task-streaming).
- **Agent signals**: Sending a signal to an active agent run publishes an event on a thread topic, so a run executing in another process receives the signal.
- **Resumable streams**: Stream chunks are published per run, so a client that reconnects can replay what it missed.

Because these systems run on one bus, the backend you choose applies to all of them at once.

## Delivery modes

Backends deliver events in one of two modes, defined by the [`PubSub`](https://mastra.ai/reference/pubsub/base) contract:

- **Pull**: Consumers read from the backend on their own, which Mastra does with a long-lived worker loop. Distributed backends such as [`RedisStreamsPubSub`](https://mastra.ai/reference/pubsub/redis-streams) use this mode.
- **Push**: Events arrive without the consumer asking, either in process or over HTTP. The default [`EventEmitterPubSub`](https://mastra.ai/reference/pubsub/event-emitter) delivers this way in process.

Subscribers can also opt into work distribution. A group of subscribers that share a consumer group split the events between them, so each event is handled once. Subscribers without a group each receive every event, which fans the stream out to all of them.

## Default backend

When you don't set the `pubsub` option, Mastra uses [`EventEmitterPubSub`](https://mastra.ai/reference/pubsub/event-emitter). It delivers events in process using a Node.js [`EventEmitter`](https://nodejs.org/api/events.html#class-eventemitter), so it works without any external service.

```typescript
import { Mastra } from '@mastra/core'

// No pubsub option: Mastra uses EventEmitterPubSub
export const mastra = new Mastra({})
```

Because it runs in process, events aren't persisted and don't reach other processes. The default suits a single instance, which covers most applications.

## Choosing a backend

Set the `pubsub` option on the `Mastra` instance to choose a backend. Each backend implements the same [`PubSub`](https://mastra.ai/reference/pubsub/base) contract, so the rest of your application doesn't change.

The deciding question is where the subscriber runs.

| Backend                                                                       | Scope                        | Mode          | Package                       |
| ----------------------------------------------------------------------------- | ---------------------------- | ------------- | ----------------------------- |
| [`EventEmitterPubSub`](https://mastra.ai/reference/pubsub/event-emitter)      | Single process               | Pull and push | `@mastra/core`                |
| [`UnixSocketPubSub`](https://mastra.ai/reference/pubsub/unix-socket-pubsub)   | Multiple processes, one host | Push          | `@mastra/core`                |
| [`RedisStreamsPubSub`](https://mastra.ai/reference/pubsub/redis-streams)      | Distributed, multiple hosts  | Pull          | `@mastra/redis-streams`       |
| [`GoogleCloudPubSub`](https://mastra.ai/reference/pubsub/google-cloud-pubsub) | Distributed, multiple hosts  | Pull          | `@mastra/google-cloud-pubsub` |

### Multiple processes on one host

Use [`UnixSocketPubSub`](https://mastra.ai/reference/pubsub/unix-socket-pubsub) when several processes on the same machine need to share a stream. It delivers events over a Unix domain socket and elects one process as a broker. If the broker exits, the remaining processes elect a new one.

```typescript
import { Mastra } from '@mastra/core'
import { UnixSocketPubSub } from '@mastra/core/events'

export const mastra = new Mastra({
  pubsub: new UnixSocketPubSub('/tmp/mastra/events.sock'),
})
```

### Distributed deployments

Use a distributed backend when you run more than one instance or host, so every instance receives the same events. This matters whenever a request handled by one instance must reach work running on another. For example, sending a signal to an agent run requires the signal event to cross the process boundary to the instance that owns the run. With the in-process default, that instance never receives the event.

Both backends below deliver across processes and hosts and persist events for redelivery.

The following example uses [Redis Streams](https://redis.io/docs/latest/develop/data-types/streams/):

```typescript
import { Mastra } from '@mastra/core'
import { RedisStreamsPubSub } from '@mastra/redis-streams'

export const mastra = new Mastra({
  pubsub: new RedisStreamsPubSub({
    url: 'redis://localhost:6379',
  }),
})
```

The following example uses [Google Cloud Pub/Sub](https://cloud.google.com/pubsub/docs):

```typescript
import { Mastra } from '@mastra/core'
import { GoogleCloudPubSub } from '@mastra/google-cloud-pubsub'

export const mastra = new Mastra({
  pubsub: new GoogleCloudPubSub({
    projectId: 'my-project',
  }),
})
```

## Resumable streams

Resumable streams let a client reconnect and replay events it missed, which requires the backend to keep recent history. Distributed backends such as [`RedisStreamsPubSub`](https://mastra.ai/reference/pubsub/redis-streams) persist events, so they support replay on their own.

In-process delivery doesn't keep history. To add replay on top of [`EventEmitterPubSub`](https://mastra.ai/reference/pubsub/event-emitter), wrap it in [`CachingPubSub`](https://mastra.ai/reference/pubsub/caching-pubsub), which records published events per topic so a late or reconnecting subscriber can catch up before continuing with live events.

```typescript
import { Mastra } from '@mastra/core'
import { CachingPubSub, EventEmitterPubSub } from '@mastra/core/events'
import { InMemoryServerCache } from '@mastra/core/cache'

const cache = new InMemoryServerCache()

export const mastra = new Mastra({
  pubsub: new CachingPubSub(new EventEmitterPubSub(), cache),
})
```

> **Note:** Visit the [PubSub reference](https://mastra.ai/reference/pubsub/base) for the full delivery contract and the configuration options for each backend.

## Related

- [PubSub reference](https://mastra.ai/reference/pubsub/base)
- [Mastra class](https://mastra.ai/reference/core/mastra-class)
- [Background task streaming](https://mastra.ai/docs/streaming/background-task-streaming)
- [Scheduled workflows](https://mastra.ai/docs/workflows/scheduled-workflows)
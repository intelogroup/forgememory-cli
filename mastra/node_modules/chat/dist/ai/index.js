import {
  toAiMessages
} from "../chunk-HD375J7S.js";

// src/ai/tools/channels.ts
import { tool } from "ai";
import { z } from "zod";
var getChannelInfo = (chat) => tool({
  description: "Fetch metadata for a channel: name, member count, DM status, visibility, etc. Use to identify a channel before posting.",
  inputSchema: z.object({
    channelId: z.string().describe("Full channel id including adapter prefix")
  }),
  execute: async ({ channelId }) => {
    const channel = chat.channel(channelId);
    const info = await channel.fetchMetadata();
    return {
      id: info.id,
      name: info.name,
      isDM: info.isDM ?? false,
      memberCount: info.memberCount,
      channelVisibility: info.channelVisibility
    };
  }
});

// src/ai/tools/messages.ts
import { tool as tool2 } from "ai";
import { z as z2 } from "zod";
var POSTABLE_INPUT = z2.union([
  z2.string().describe("Plain text body"),
  z2.object({ markdown: z2.string() }).describe("Markdown body, converted to the platform's native format"),
  z2.object({ raw: z2.string() }).describe("Raw body, passed through to the platform untouched")
]).describe("Message body");
var postMessage = (chat, { needsApproval = true } = {}) => tool2({
  description: "Post a message inside an existing thread. Use this to reply within a conversation the bot already has context for. The threadId is the full id (e.g. 'slack:C123:1234567890.123456').",
  needsApproval,
  inputSchema: z2.object({
    threadId: z2.string().describe("Full thread id including adapter prefix"),
    message: POSTABLE_INPUT
  }),
  execute: async ({ threadId, message }) => {
    const thread = chat.thread(threadId);
    const sent = await thread.post(toPostable(message));
    return {
      messageId: sent.id,
      threadId: sent.threadId
    };
  }
});
var postChannelMessage = (chat, { needsApproval = true } = {}) => tool2({
  description: "Post a top-level message to a channel (not threaded under an existing message). The channelId is the full id (e.g. 'slack:C123ABC').",
  needsApproval,
  inputSchema: z2.object({
    channelId: z2.string().describe("Full channel id including adapter prefix"),
    message: POSTABLE_INPUT
  }),
  execute: async ({ channelId, message }) => {
    const channel = chat.channel(channelId);
    const sent = await channel.post(toPostable(message));
    return {
      messageId: sent.id,
      threadId: sent.threadId
    };
  }
});
var sendDirectMessage = (chat, { needsApproval = true } = {}) => tool2({
  description: "Open (or reuse) a 1:1 direct-message conversation with a user and post a message in it. The userId format is platform-specific (e.g. 'U123456' for Slack, 'users/123' for Google Chat).",
  needsApproval,
  inputSchema: z2.object({
    userId: z2.string().describe("Platform-specific user id; the adapter is auto-detected"),
    message: POSTABLE_INPUT
  }),
  execute: async ({ userId, message }) => {
    const dm = await chat.openDM(userId);
    const sent = await dm.post(toPostable(message));
    return {
      messageId: sent.id,
      threadId: sent.threadId
    };
  }
});
var editMessage = (chat, { needsApproval = true } = {}) => tool2({
  description: "Edit a previously posted message in a thread. Replaces the existing message body. Only messages the bot itself authored can be edited on most platforms.",
  needsApproval,
  inputSchema: z2.object({
    threadId: z2.string().describe("Full thread id"),
    messageId: z2.string().describe("Platform-specific message id of the message to edit"),
    message: POSTABLE_INPUT
  }),
  execute: async ({ threadId, messageId, message }) => {
    const thread = chat.thread(threadId);
    const result = await thread.adapter.editMessage(
      threadId,
      messageId,
      toPostable(message)
    );
    return { messageId: result.id, threadId: result.threadId };
  }
});
var deleteMessage = (chat, { needsApproval = true } = {}) => tool2({
  description: "Delete a message from a thread. Only messages the bot itself authored can be deleted on most platforms.",
  needsApproval,
  inputSchema: z2.object({
    threadId: z2.string().describe("Full thread id"),
    messageId: z2.string().describe("Platform-specific message id of the message to delete")
  }),
  execute: async ({ threadId, messageId }) => {
    const thread = chat.thread(threadId);
    await thread.adapter.deleteMessage(threadId, messageId);
    return { deleted: true, messageId, threadId };
  }
});
function toPostable(input) {
  if (typeof input === "string") {
    return input;
  }
  if ("markdown" in input) {
    return { markdown: input.markdown };
  }
  return { raw: input.raw };
}

// src/ai/tools/reactions.ts
import { tool as tool3 } from "ai";
import { z as z3 } from "zod";
var addReaction = (chat, { needsApproval = true } = {}) => tool3({
  description: "Add an emoji reaction to a specific message. Use a well-known emoji name (e.g. 'thumbs_up', 'heart', 'check') or a platform-native shorthand.",
  needsApproval,
  inputSchema: z3.object({
    threadId: z3.string().describe("Full thread id"),
    messageId: z3.string().describe("Platform-specific message id to react to"),
    emoji: z3.string().describe(
      "Emoji name or platform shortcode (e.g. 'thumbs_up', 'white_check_mark')"
    )
  }),
  execute: async ({ threadId, messageId, emoji }) => {
    const thread = chat.thread(threadId);
    await thread.adapter.addReaction(threadId, messageId, emoji);
    return { added: true, emoji, messageId, threadId };
  }
});
var removeReaction = (chat, { needsApproval = true } = {}) => tool3({
  description: "Remove an emoji reaction the bot previously added to a message.",
  needsApproval,
  inputSchema: z3.object({
    threadId: z3.string().describe("Full thread id"),
    messageId: z3.string().describe("Platform-specific message id to remove the reaction from"),
    emoji: z3.string().describe(
      "Emoji name or platform shortcode previously added by the bot"
    )
  }),
  execute: async ({ threadId, messageId, emoji }) => {
    const thread = chat.thread(threadId);
    await thread.adapter.removeReaction(threadId, messageId, emoji);
    return { removed: true, emoji, messageId, threadId };
  }
});

// src/ai/tools/threads.ts
import { tool as tool4 } from "ai";
import { z as z4 } from "zod";
var FETCH_DIRECTION = z4.enum(["forward", "backward"]).optional().default("backward");
function projectMessage(message) {
  return {
    id: message.id,
    threadId: message.threadId,
    text: message.text,
    author: {
      userId: message.author.userId,
      userName: message.author.userName,
      fullName: message.author.fullName,
      isBot: message.author.isBot,
      isMe: message.author.isMe
    },
    dateSent: message.metadata.dateSent?.toISOString(),
    edited: message.metadata.edited,
    isMention: message.isMention,
    attachments: (message.attachments ?? []).map((att) => ({
      type: att.type,
      name: att.name,
      mimeType: att.mimeType,
      url: att.url
    }))
  };
}
var fetchMessages = (chat) => tool4({
  description: "Fetch recent messages from a thread, ordered chronologically (oldest first within the page). Use to read the conversation before responding.",
  inputSchema: z4.object({
    threadId: z4.string().describe("Full thread id"),
    limit: z4.number().int().min(1).max(100).optional().default(20).describe("Maximum number of messages to fetch"),
    cursor: z4.string().optional().describe("Pagination cursor from a previous fetchMessages call"),
    direction: FETCH_DIRECTION.describe(
      "'backward' (default) returns the most recent messages; 'forward' iterates from the oldest"
    )
  }),
  execute: async ({ threadId, limit, cursor, direction }) => {
    const thread = chat.thread(threadId);
    const result = await thread.adapter.fetchMessages(threadId, {
      limit,
      cursor,
      direction
    });
    return {
      messages: result.messages.map(projectMessage),
      nextCursor: result.nextCursor
    };
  }
});
var fetchChannelMessages = (chat) => tool4({
  description: "Fetch top-level messages in a channel (not thread replies). Returns messages in chronological order within the page.",
  inputSchema: z4.object({
    channelId: z4.string().describe("Full channel id"),
    limit: z4.number().int().min(1).max(100).optional().default(20),
    cursor: z4.string().optional(),
    direction: FETCH_DIRECTION
  }),
  execute: async ({ channelId, limit, cursor, direction }) => {
    const adapterName = channelId.split(":")[0];
    const adapter = adapterName ? chat.getAdapter(adapterName) : void 0;
    if (!adapter?.fetchChannelMessages) {
      throw new Error(
        `Adapter "${adapterName}" does not support fetching channel messages`
      );
    }
    const result = await adapter.fetchChannelMessages(channelId, {
      limit,
      cursor,
      direction
    });
    return {
      messages: result.messages.map(projectMessage),
      nextCursor: result.nextCursor
    };
  }
});
var fetchThread = (chat) => tool4({
  description: "Fetch metadata about a thread (channel id, channel name, visibility, DM status, etc).",
  inputSchema: z4.object({
    threadId: z4.string().describe("Full thread id")
  }),
  execute: async ({ threadId }) => {
    const thread = chat.thread(threadId);
    const info = await thread.adapter.fetchThread(threadId);
    return {
      id: info.id,
      channelId: info.channelId,
      channelName: info.channelName,
      channelVisibility: info.channelVisibility,
      isDM: info.isDM ?? false
    };
  }
});
var listThreads = (chat) => tool4({
  description: "List recent threads in a channel. Returns lightweight summaries with the root message of each thread.",
  inputSchema: z4.object({
    channelId: z4.string().describe("Full channel id"),
    limit: z4.number().int().min(1).max(100).optional().default(20),
    cursor: z4.string().optional()
  }),
  execute: async ({ channelId, limit, cursor }) => {
    const adapterName = channelId.split(":")[0];
    const adapter = adapterName ? chat.getAdapter(adapterName) : void 0;
    if (!adapter?.listThreads) {
      throw new Error(
        `Adapter "${adapterName}" does not support listing threads`
      );
    }
    const result = await adapter.listThreads(channelId, { limit, cursor });
    return {
      threads: result.threads.map((t) => ({
        id: t.id,
        replyCount: t.replyCount,
        lastReplyAt: t.lastReplyAt?.toISOString(),
        rootMessage: projectMessage(t.rootMessage)
      })),
      nextCursor: result.nextCursor
    };
  }
});
var getThreadParticipants = (chat) => tool4({
  description: "Return the unique non-bot participants in a thread. Useful for deciding whether to subscribe (1:1) or stay quiet (group).",
  inputSchema: z4.object({
    threadId: z4.string().describe("Full thread id")
  }),
  execute: async ({ threadId }) => {
    const thread = chat.thread(threadId);
    const participants = await thread.getParticipants();
    return {
      participants: participants.map((author) => ({
        userId: author.userId,
        userName: author.userName,
        fullName: author.fullName,
        isBot: author.isBot
      }))
    };
  }
});
var subscribeThread = (chat, { needsApproval = true } = {}) => tool4({
  description: "Subscribe to all future messages in a thread. After subscribing, the bot will receive every message in this thread (not just @mentions).",
  needsApproval,
  inputSchema: z4.object({
    threadId: z4.string().describe("Full thread id to subscribe to")
  }),
  execute: async ({ threadId }) => {
    const thread = chat.thread(threadId);
    await thread.subscribe();
    return { subscribed: true, threadId };
  }
});
var unsubscribeThread = (chat, { needsApproval = true } = {}) => tool4({
  description: "Unsubscribe from a thread. The bot will stop receiving non-mention messages in this thread.",
  needsApproval,
  inputSchema: z4.object({
    threadId: z4.string().describe("Full thread id to unsubscribe from")
  }),
  execute: async ({ threadId }) => {
    const thread = chat.thread(threadId);
    await thread.unsubscribe();
    return { subscribed: false, threadId };
  }
});
var startTyping = (chat) => tool4({
  description: "Show a typing indicator in a thread. Use this when starting a long-running operation so users know the bot is working.",
  inputSchema: z4.object({
    threadId: z4.string().describe("Full thread id"),
    status: z4.string().optional().describe(
      "Optional human-readable status (some platforms display this, others ignore it)"
    )
  }),
  execute: async ({ threadId, status }) => {
    const thread = chat.thread(threadId);
    await thread.startTyping(status);
    return { typing: true, threadId };
  }
});

// src/ai/tools/users.ts
import { tool as tool5 } from "ai";
import { z as z5 } from "zod";
var getUser = (chat) => tool5({
  description: "Look up profile information about a user by their platform-specific id (e.g. 'U123456' for Slack, '29:...' for Teams, 'users/123' for Google Chat). Returns null if the user is unknown.",
  inputSchema: z5.object({
    userId: z5.string().describe("Platform-specific user id; the adapter is auto-detected")
  }),
  execute: async ({ userId }) => {
    const user = await chat.getUser(userId);
    if (!user) {
      return null;
    }
    return {
      userId: user.userId,
      userName: user.userName,
      fullName: user.fullName,
      email: user.email,
      isBot: user.isBot,
      avatarUrl: user.avatarUrl
    };
  }
});

// src/ai/index.ts
var PROTECTED_TOOL_FIELDS = /* @__PURE__ */ new Set([
  "args",
  "execute",
  "id",
  "inputSchema",
  "outputSchema",
  "supportsDeferredResults",
  "type"
]);
var PRESET_TOOLS = {
  reader: [
    "fetchMessages",
    "fetchChannelMessages",
    "fetchThread",
    "listThreads",
    "getThreadParticipants",
    "getChannelInfo",
    "getUser"
  ],
  messenger: [
    "fetchMessages",
    "fetchThread",
    "getChannelInfo",
    "getUser",
    "postMessage",
    "postChannelMessage",
    "sendDirectMessage",
    "addReaction",
    "removeReaction",
    "startTyping"
  ],
  moderator: [
    "fetchMessages",
    "fetchChannelMessages",
    "fetchThread",
    "listThreads",
    "getThreadParticipants",
    "getChannelInfo",
    "getUser",
    "postMessage",
    "postChannelMessage",
    "sendDirectMessage",
    "editMessage",
    "deleteMessage",
    "addReaction",
    "removeReaction",
    "subscribeThread",
    "unsubscribeThread",
    "startTyping"
  ]
};
function resolveApproval(toolName, config) {
  if (typeof config === "boolean") {
    return config;
  }
  return config[toolName] ?? true;
}
function resolvePresetTools(preset) {
  const presets = Array.isArray(preset) ? preset : [preset];
  const tools = /* @__PURE__ */ new Set();
  for (const p of presets) {
    for (const t of PRESET_TOOLS[p]) {
      tools.add(t);
    }
  }
  return tools;
}
function applyOverrides(tool6, overrides) {
  if (!overrides) {
    return tool6;
  }
  const safeOverrides = Object.fromEntries(
    Object.entries(overrides).filter(
      ([key]) => !PROTECTED_TOOL_FIELDS.has(key)
    )
  );
  return { ...tool6, ...safeOverrides };
}
function createChatTools({
  chat,
  requireApproval = true,
  preset,
  overrides
}) {
  if (!chat) {
    throw new Error(
      "createChatTools requires a `chat` instance. Pass your `new Chat({ ... })` instance as the `chat` option."
    );
  }
  const approval = (name) => ({
    needsApproval: resolveApproval(name, requireApproval)
  });
  const allowed = preset ? resolvePresetTools(preset) : null;
  const factories = {
    fetchMessages: () => fetchMessages(chat),
    fetchChannelMessages: () => fetchChannelMessages(chat),
    fetchThread: () => fetchThread(chat),
    listThreads: () => listThreads(chat),
    getThreadParticipants: () => getThreadParticipants(chat),
    getChannelInfo: () => getChannelInfo(chat),
    getUser: () => getUser(chat),
    startTyping: () => startTyping(chat),
    postMessage: () => postMessage(chat, approval("postMessage")),
    postChannelMessage: () => postChannelMessage(chat, approval("postChannelMessage")),
    sendDirectMessage: () => sendDirectMessage(chat, approval("sendDirectMessage")),
    editMessage: () => editMessage(chat, approval("editMessage")),
    deleteMessage: () => deleteMessage(chat, approval("deleteMessage")),
    addReaction: () => addReaction(chat, approval("addReaction")),
    removeReaction: () => removeReaction(chat, approval("removeReaction")),
    subscribeThread: () => subscribeThread(chat, approval("subscribeThread")),
    unsubscribeThread: () => unsubscribeThread(chat, approval("unsubscribeThread"))
  };
  const entries = Object.entries(factories).filter(([name]) => !allowed || allowed.has(name)).map(([name, build]) => {
    const built = build();
    return [name, applyOverrides(built, overrides?.[name])];
  });
  return Object.fromEntries(entries);
}
export {
  addReaction,
  createChatTools,
  deleteMessage,
  editMessage,
  fetchChannelMessages,
  fetchMessages,
  fetchThread,
  getChannelInfo,
  getThreadParticipants,
  getUser,
  listThreads,
  postChannelMessage,
  postMessage,
  removeReaction,
  sendDirectMessage,
  startTyping,
  subscribeThread,
  toAiMessages,
  unsubscribeThread
};

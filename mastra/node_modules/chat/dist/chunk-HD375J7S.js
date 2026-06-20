// src/ai/messages.ts
var TEXT_MIME_PREFIXES = [
  "text/",
  "application/json",
  "application/xml",
  "application/javascript",
  "application/typescript",
  "application/yaml",
  "application/x-yaml",
  "application/toml"
];
function isTextMimeType(mimeType) {
  return TEXT_MIME_PREFIXES.some(
    (prefix) => mimeType === prefix || mimeType.startsWith(prefix)
  );
}
async function attachmentToPart(att) {
  if (att.type === "image") {
    if (att.fetchData) {
      try {
        const buffer = await att.fetchData();
        const mimeType = att.mimeType ?? "image/png";
        return {
          type: "file",
          data: `data:${mimeType};base64,${buffer.toString("base64")}`,
          mediaType: mimeType,
          filename: att.name
        };
      } catch (error) {
        console.error("toAiMessages: failed to fetch image data", error);
        return null;
      }
    }
    return null;
  }
  if (att.type === "file" && att.mimeType && isTextMimeType(att.mimeType)) {
    if (att.fetchData) {
      try {
        const buffer = await att.fetchData();
        return {
          type: "file",
          data: `data:${att.mimeType};base64,${buffer.toString("base64")}`,
          filename: att.name,
          mediaType: att.mimeType
        };
      } catch (error) {
        console.error("toAiMessages: failed to fetch file data", error);
        return null;
      }
    }
    return null;
  }
  return null;
}
async function toAiMessages(messages, options) {
  const includeNames = options?.includeNames ?? false;
  const transformMessage = options?.transformMessage;
  const onUnsupported = options?.onUnsupportedAttachment ?? ((att) => {
    console.warn(
      `toAiMessages: unsupported attachment type "${att.type}"${att.name ? ` (${att.name})` : ""} \u2014 skipped`
    );
  });
  const sorted = [...messages].sort(
    (a, b) => (a.metadata.dateSent?.getTime() ?? 0) - (b.metadata.dateSent?.getTime() ?? 0)
  );
  const filtered = sorted.filter((msg) => msg.text.trim());
  const results = await Promise.all(
    filtered.map(async (msg) => {
      const role = msg.author.isMe ? "assistant" : "user";
      let textContent = includeNames && role === "user" ? `[${msg.author.userName}]: ${msg.text}` : msg.text;
      if (msg.links && msg.links.length > 0) {
        const linkParts = msg.links.map((link) => {
          const parts = link.fetchMessage ? [`[Embedded message: ${link.url}]`] : [link.url];
          if (link.title) {
            parts.push(`Title: ${link.title}`);
          }
          if (link.description) {
            parts.push(`Description: ${link.description}`);
          }
          if (link.siteName) {
            parts.push(`Site: ${link.siteName}`);
          }
          return parts.join("\n");
        }).join("\n\n");
        textContent += `

Links:
${linkParts}`;
      }
      let aiMessage;
      if (role === "user") {
        const attachmentParts = [];
        for (const att of msg.attachments ?? []) {
          const part = await attachmentToPart(att);
          if (part) {
            attachmentParts.push(part);
          } else if (att.type === "video" || att.type === "audio") {
            onUnsupported(att, msg);
          }
        }
        if (attachmentParts.length > 0) {
          aiMessage = {
            role,
            content: [
              { type: "text", text: textContent },
              ...attachmentParts
            ]
          };
        } else {
          aiMessage = { role, content: textContent };
        }
      } else {
        aiMessage = { role, content: textContent };
      }
      if (transformMessage) {
        return { result: await transformMessage(aiMessage, msg), source: msg };
      }
      return { result: aiMessage, source: msg };
    })
  );
  return results.filter(
    (r) => r.result != null
  ).map((r) => r.result);
}

export {
  toAiMessages
};

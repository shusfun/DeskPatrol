import assert from "node:assert/strict";
import test from "node:test";

import {
  originalMeshUserSha256,
  originalMultiplexSha256,
  patchMeshUserSource,
  patchMultiplexSource,
} from "./patch-meshcentral.mjs";

const original = `before
            case 1: // Key Events, forward to agent
                if (viewer.viewOnly == false) { obj.sendToAgent(data); }
                break;
            case 2: // Mouse events, forward to agent
                if (viewer.viewOnly == false) { obj.sendToAgent(data); }
                break;
            case 12: // SET DISPLAY, forward to agent
                if (viewer.viewOnly == false) { obj.sendToAgent(data); }
                break;
            case 85: // Unicode Key Events, forward to agent
                if (viewer.viewOnly == false) { obj.sendToAgent(data); }
                break;
after`;

test("只读补丁只改变 SetDisplay，并校验长度、物理屏幕与已知显示器", () => {
  const patched = patchMultiplexSource(original, originalMultiplexSha256);
  assert.match(patched, /data\.length != 6/);
  assert.match(patched, /display == 65535/);
  assert.match(patched, /displayFound/);
  assert.match(patched, /if \(displayFound\) \{ obj\.sendToAgent\(data\); \}/);
  assert.match(patched, /case 1:[\s\S]*viewer\.viewOnly == false/);
  assert.match(patched, /case 2:[\s\S]*viewer\.viewOnly == false/);
  assert.match(patched, /case 85:[\s\S]*viewer\.viewOnly == false/);
});

test("原文件摘要或补丁上下文漂移时直接失败", () => {
  assert.throws(() => patchMultiplexSource(original, "0".repeat(64)), /SHA-256 不匹配/);
  assert.throws(() => patchMultiplexSource("no context", originalMultiplexSha256), /上下文数量不正确/);
});

const originalMeshUser = `function cleanup(docs, now, parent) {
                        for (var i = 0; i < docs.length; i++) {
                            const doc = docs[i];
                            if (doc.expireTime < now) { parent.db.Remove(doc._id, function () { }); delete docs[i]; } else {
                                delete doc._id; delete doc.domain; delete doc.nodeid; delete doc.type; delete doc.xmeshid;
                            }
                        }
                        return docs;
}`;

test("过期设备分享被紧密移除，后续事件脱敏不会访问空元素", () => {
  const patched = patchMeshUserSource(originalMeshUser, originalMeshUserSha256);
  assert.match(patched, /docs\.splice\(i, 1\); i--;/);
  assert.doesNotMatch(patched, /delete docs\[i\]/);

  const cleanup = Function(`"use strict"; return (${patched});`)();
  const removed = [];
  const shares = cleanup([
    { _id: "expired", userid: "user/a", expireTime: 1 },
    { _id: "valid", userid: "user/b", expireTime: 20 },
  ], 10, { db: { Remove(id) { removed.push(id); } } });

  assert.deepEqual(removed, ["expired"]);
  assert.equal(shares.length, 1);
  assert.equal(shares[0].userid, "user/b");
  assert.doesNotThrow(() => {
    for (const index in shares) {
      if (shares[index].userid !== "user/a") delete shares[index].url;
    }
  });
});

test("meshuser 原文件摘要或补丁上下文漂移时直接失败", () => {
  assert.throws(() => patchMeshUserSource(originalMeshUser, "0".repeat(64)), /SHA-256 不匹配/);
  assert.throws(() => patchMeshUserSource("no context", originalMeshUserSha256), /上下文数量不正确/);
});

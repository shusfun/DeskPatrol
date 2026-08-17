import assert from "node:assert/strict";
import test from "node:test";

import { originalMultiplexSha256, patchMultiplexSource } from "./patch-meshcentral.mjs";

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

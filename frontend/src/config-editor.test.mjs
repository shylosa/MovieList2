import assert from 'node:assert/strict';
import {createConfigEditor, highlightEnvText} from './config-editor.js';
const input = new EventTarget(); input.value = ''; input.disabled = true;
const status = {}, path = {};
let revision = 'r1', disk = 'KEY=old\n', calls = 0, reads = 0, release;
const api = {
    GetEnvConfig: async () => { reads++; return {content: disk, revision, path: '/app/.env'}; },
    SaveEnvConfig: async (content, expected) => {
        calls++; assert.equal(expected, revision);
        if (release) await new Promise(resolve => { release = resolve; });
        if (content === 'invalid') throw new Error('Invalid syntax');
        disk = content; revision = `r${calls + 1}`; return revision;
    },
};
const editor = createConfigEditor({input, status, path, api, delay: 10});
await editor.load(); assert.equal(input.value, disk); assert.equal(input.disabled, false);
await editor.load(); assert.equal(reads, 1, 'reopening discarded edits');
input.value = 'KEY=new\n'; input.dispatchEvent(new Event('input'));
await new Promise(resolve => setTimeout(resolve, 25));
assert.equal(disk, 'KEY=new\n'); assert.match(status.textContent, /Збережено/);
input.value = 'invalid'; assert.equal(await editor.flush(), false);
assert.equal(disk, 'KEY=new\n'); assert.equal(input.value, 'invalid');
assert.match(status.textContent, /Invalid syntax/);
input.value = 'KEY=valid\n'; assert.equal(await editor.flush(), true);
release = true;
input.value = 'KEY=first\n'; const first = editor.flush();
await Promise.resolve();
input.value = 'KEY=latest\n'; const closing = editor.flush();
const finish = release; release = null; finish();
await first; assert.equal(await closing, true);
assert.equal(disk, 'KEY=latest\n', 'closing lost edits made during an in-flight save');
const highlight = {children: [], replaceChildren() { this.children = []; }, append(...children) { this.children.push(...children); }};
highlight.ownerDocument = {createElement: () => ({children: [], textContent: '', append(...children) { this.children.push(...children); }}), createTextNode: text => ({textContent: text})};
highlightEnvText(highlight, 'KEY=<img src=x onerror=alert(1)>\n# comment');
assert.equal(highlight.children[0].children[0].textContent, 'KEY=');
assert.equal(highlight.children[0].children[1].textContent, '<img src=x onerror=alert(1)>');
assert.equal(highlight.children[2].className, 'env-comment');
console.log('configuration editor tests passed');

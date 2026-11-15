import assert from 'node:assert/strict';
import { createFolderTables } from './folder-tables.js';

class Element {
    constructor(tag) { this.tag = tag; this.children = []; this.disabled = false; this.textContent = ''; }
    append(...nodes) { this.children.push(...nodes); }
    appendChild(node) { this.append(node); }
    replaceChildren() { this.children = []; }
    setAttribute() {}
    querySelectorAll(tag) { return this.children.flatMap(child => [...(child.tag === tag ? [child] : []), ...child.querySelectorAll(tag)]); }
}
const nodes = Object.fromEntries(['panel-folders', 'folders-status', 'scan-rows', 'excluded-rows', 'scan-count', 'excluded-count', 'add-scan-folder', 'add-excluded-folder'].map(id => [id, new Element(id.startsWith('add-') ? 'button' : 'div')]));
nodes['panel-folders'].append(...Object.entries(nodes).filter(([id]) => id !== 'panel-folders').map(([, node]) => node));
const doc = {getElementById: id => nodes[id], createElement: tag => new Element(tag)};
let state = {folders: ['D:/Movies'], excluded: ['D:/Movies/Skip']};
let mutations = 0;
let pending;
const api = {
    GetScanFolders: async () => structuredClone(state),
    SetScanFolders: async folders => { mutations++; state.folders = folders; },
    SetExcludedFolders: async excluded => { mutations++; state.excluded = excluded; },
    SelectScanFolder: async () => { mutations++; state.folders.push('E:/Movies'); },
    SelectExcludedFolder: async () => { mutations++; if (pending) await pending; state.excluded.push('E:/Movies/Skip'); },
};
const controller = createFolderTables({doc, api});
await controller.load();
assert.equal(nodes['scan-count'].textContent, '1');
await nodes['add-scan-folder'].onclick();
assert.deepEqual(state.folders, ['D:/Movies', 'E:/Movies']);
assert.equal(nodes['scan-count'].textContent, '2');
await nodes['scan-rows'].querySelectorAll('button')[0].onclick();
assert.deepEqual(state.folders, ['E:/Movies']);
await nodes['excluded-rows'].querySelectorAll('button')[0].onclick();
assert.deepEqual(state.excluded, []);
assert.equal(nodes['excluded-count'].textContent, '0');
assert.equal(nodes['excluded-rows'].children[0].children[0].textContent, 'Виключених папок немає');
controller.setScanning(true);
const before = mutations;
await nodes['add-scan-folder'].onclick();
assert.equal(mutations, before);
assert.equal(nodes['scan-rows'].querySelectorAll('button')[0].disabled, true);
controller.setScanning(false);
let finish;
pending = new Promise(resolve => { finish = resolve; });
const adding = nodes['add-excluded-folder'].onclick();
assert.equal(nodes['add-scan-folder'].disabled, true);
await nodes['add-scan-folder'].onclick();
assert.equal(mutations, before + 1, 'concurrent mutation was allowed');
finish(); await adding;
assert.equal(nodes['add-scan-folder'].disabled, false);
api.SetScanFolders = async () => { throw new Error('Folder unavailable'); };
await nodes['scan-rows'].querySelectorAll('button')[0].onclick();
assert.match(nodes['folders-status'].textContent, /Folder unavailable/);
assert.deepEqual(state.folders, ['E:/Movies'], 'failed operation changed visible state');
console.log('folder table interaction tests passed');

import assert from 'node:assert/strict';
import {createModelTables, modelRows, moveModel} from './model-tables.js';

assert.deepEqual(moveModel(['a', 'b'], 'b', -1), ['b', 'a']);
assert.deepEqual(moveModel(['a', 'b'], 'a', -1), ['a', 'b']);
assert.deepEqual(modelRows(['missing', 'b'], ['a', 'b'], true), [
    {name: 'missing', selected: true, unavailable: true},
    {name: 'b', selected: true, unavailable: false},
    {name: 'a', selected: false, unavailable: false},
]);
class Element {
    constructor(tag) { this.tag = tag; this.children = []; this.listeners = {}; }
    append(...nodes) { this.children.push(...nodes); }
    replaceChildren() { this.children = []; }
    setAttribute() {}
    addEventListener(name, handler) { this.listeners[name] = handler; }
    all(tag) { return this.children.flatMap(child => [...(child.tag === tag ? [child] : []), ...child.all(tag)]); }
}
const host = new Element('div');
const doc = {getElementById: () => host, createElement: tag => new Element(tag)};
let catalogCalls = 0, saves = [];
const api = {
    GetModelSelections: async () => ({gemini: {current: ['a', 'b'], configured: true}, groq: {current: ['g'], configured: false}}),
    GetAIModelCatalog: async () => { catalogCalls++; return {available: ['b', 'c']}; },
    GetGroqModelCatalog: async () => { throw new Error('unexpected Groq request'); },
    SetProviderModels: async (provider, names) => { saves.push([provider, names]); },
};
const controller = createModelTables({api, document: doc});
await controller.load();
assert.equal(catalogCalls, 0);
const gemini = host.children[0], groq = host.children[1];
assert.equal(groq.all('button')[0].disabled, true);
await gemini.all('button')[0].listeners.click();
assert.equal(catalogCalls, 1);
assert.equal(gemini.all('input').length, 3);
assert.equal(gemini.all('small')[0].textContent, 'Немає в отриманому каталозі');
gemini.all('input')[2].checked = true;
await gemini.all('input')[2].listeners.change();
await new Promise(resolve => setTimeout(resolve, 0));
assert.deepEqual(saves, [['gemini', ['a', 'b', 'c']]]);
api.SetProviderModels = async () => { throw new Error('write failed'); };
gemini.all('input')[1].checked = false;
await gemini.all('input')[1].listeners.change();
await new Promise(resolve => setTimeout(resolve, 0));
assert.equal(gemini.all('input')[1].checked, true, 'failed save must restore checkbox');
assert.match(gemini.all('p')[0].textContent, /write failed/);
console.log('model table interaction tests passed');

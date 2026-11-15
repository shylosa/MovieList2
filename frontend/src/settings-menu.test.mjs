import assert from 'node:assert/strict';
import { bindSettingsMenu } from './settings-menu.js';

class Node extends EventTarget {
    constructor(doc, parent = null) { super(); this.doc = doc; this.parent = parent; this.attributes = {}; this.hidden = false; }
    contains(node) { return node === this || Boolean(node?.parent && this.contains(node.parent)); }
    closest(selector) { return selector === 'button' ? this : this.doc.anchor; }
    setAttribute(key, value) { this.attributes[key] = value; }
    focus() {
        this.doc.activeElement = this;
        this.doc.dispatchEvent(Object.assign(new Event('focusin'), {source: this}));
    }
}
// Route focus events with their actual element as target.
const doc = {activeElement: null, handlers: {}, addEventListener(type, handler) { (this.handlers[type] ||= []).push(handler); }, dispatchEvent(event) { for (const handler of this.handlers[event.type] || []) handler({target: event.source, key: event.key, preventDefault: () => event.preventDefault()}); }};
const anchor = new Node(doc); doc.anchor = anchor;
const toggle = new Node(doc, anchor);
const menu = new Node(doc, anchor); menu.hidden = true;
const items = Array.from({length: 4}, () => new Node(doc, menu));
menu.querySelector = () => items[0]; menu.querySelectorAll = () => items;
bindSettingsMenu(toggle, menu, doc);

for (const item of items) {
    toggle.onclick();
    assert.equal(doc.activeElement, items[0]);
    // WebView can run microtasks after blur, before focusing the clicked item.
    doc.activeElement = null;
    items[0].dispatchEvent(new Event('focusout'));
    await Promise.resolve();
    assert.equal(menu.hidden, false, 'menu hid before clicked item received focus');
    item.focus();
    assert.equal(menu.hidden, false, 'moving focus within menu closed it');
    let calls = 0;
    item.onclick = () => calls++;
    item.onclick();
    const click = new Event('click'); Object.defineProperty(click, 'target', {value: item});
    menu.dispatchEvent(click);
    assert.equal(calls, 1);
    assert.equal(menu.hidden, true);
}
toggle.onclick();
const arrow = new Event('keydown', {cancelable: true}); Object.assign(arrow, {key: 'ArrowDown'}); doc.dispatchEvent(arrow);
assert.equal(doc.activeElement, items[1]);
const escape = new Event('keydown', {cancelable: true}); Object.assign(escape, {key: 'Escape'}); doc.dispatchEvent(escape);
assert.equal(menu.hidden, true); assert.equal(doc.activeElement, toggle); assert.equal(escape.defaultPrevented, true);
toggle.onclick(); new Node(doc).focus(); assert.equal(menu.hidden, true, 'Tab outside did not close menu');
toggle.onclick(); doc.dispatchEvent(Object.assign(new Event('click'), {source: new Node(doc)})); assert.equal(menu.hidden, true);
console.log('settings menu interaction tests passed');

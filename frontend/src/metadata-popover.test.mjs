import assert from 'assert';
import { createMetadataPopover } from './metadata-popover.js';

// A small DOM adapter exercises the actual controller's event listeners.
class Element extends EventTarget {
    constructor(doc) {
        super(); this.doc = doc; this.children = []; this.attributes = {}; this.style = {};
        this.offsetWidth = 300; this.offsetHeight = 180;
    }
    append(...children) { for (const child of children) { child.parent = this; this.children.push(child); } }
    remove() { if (this.parent) this.parent.children = this.parent.children.filter(child => child !== this); }
    setAttribute(name, value) { this.attributes[name] = value; }
    removeAttribute(name) { delete this.attributes[name]; }
    contains(element) { return this === element || this.children.some(child => child.contains(element)); }
    getBoundingClientRect() { return {left: 950, top: 650, bottom: 675}; }
    focus() { this.doc.activeElement = this; this.dispatchEvent(new Event('focus')); }
}
class Document extends EventTarget {
    constructor() { super(); this.body = new Element(this); this.activeElement = null; }
    createElement() { return new Element(this); }
}
const doc = new Document();
const viewport = new EventTarget();
viewport.innerWidth = 1000; viewport.innerHeight = 700;
const controller = createMetadataPopover(doc, viewport);
const description = doc.createElement('button');
const actors = doc.createElement('button');
const externalText = '<img src=x onerror=alert(1)> Український опис';
controller.attach(description, 'Опис', externalText);
controller.attach(actors, 'Актори', 'Gary Oldman, Tom Hardy');
const panel = () => doc.body.children[0];
description.dispatchEvent(new Event('mouseenter'));
assert.strictEqual(doc.body.children.length, 1);
assert.strictEqual(panel().children[1].textContent, externalText);
assert.strictEqual(panel().children[1].children.length, 0);
assert.strictEqual(description.attributes['aria-expanded'], 'true');
assert.strictEqual(panel().style.left, '692px');
assert.strictEqual(panel().style.top, '462px');
description.dispatchEvent(new Event('click'));
description.dispatchEvent(new Event('mouseleave'));
await new Promise(resolve => setTimeout(resolve, 170));
assert.strictEqual(doc.body.children.length, 1, 'pinned content disappeared after leaving trigger');
actors.dispatchEvent(new Event('mouseenter'));
assert.strictEqual(panel().children[1].textContent, externalText, 'hover replaced pinned content');
// Scrolling the content keeps it open; scrolling the editor closes it.
const insideScroll = new Event('scroll');
Object.defineProperty(insideScroll, 'target', {value: panel().children[1]});
viewport.dispatchEvent(insideScroll);
assert.strictEqual(doc.body.children.length, 1);
viewport.dispatchEvent(new Event('scroll'));
assert.strictEqual(doc.body.children.length, 0);
assert.strictEqual(description.attributes['aria-expanded'], 'false');
actors.focus();
assert.strictEqual(panel().children[1].textContent, 'Gary Oldman, Tom Hardy');
const escape = new Event('keydown', {cancelable: true});
Object.defineProperty(escape, 'key', {value: 'Escape'});
doc.dispatchEvent(escape);
assert.strictEqual(doc.body.children.length, 0);
assert.strictEqual(escape.defaultPrevented, true);
assert.strictEqual(actors.attributes['aria-describedby'], undefined);
description.dispatchEvent(new Event('mouseenter'));
actors.dispatchEvent(new Event('mouseenter'));
assert.strictEqual(doc.body.children.length, 1, 'switching controls left old content open');
assert.strictEqual(description.attributes['aria-expanded'], 'false');
actors.dispatchEvent(new Event('click'));
actors.dispatchEvent(new Event('click'));
assert.strictEqual(doc.body.children.length, 0, 'second click did not unpin');
description.dispatchEvent(new Event('mouseenter'));
doc.dispatchEvent(new Event('pointerdown'));
assert.strictEqual(doc.body.children.length, 0, 'outside click did not close');
actors.dispatchEvent(new Event('mouseenter'));
viewport.dispatchEvent(new Event('resize'));
assert.strictEqual(doc.body.children.length, 0);
controller.hide();
const missingActors = doc.createElement('button');
controller.attach(missingActors, 'Актори', 'Актори не вказані.');
missingActors.focus();
assert.strictEqual(panel().children[1].textContent, 'Актори не вказані.');
doc.activeElement = null;
missingActors.dispatchEvent(new Event('blur'));
await new Promise(resolve => setTimeout(resolve, 170));
assert.strictEqual(doc.body.children.length, 0, 'unfixed popover did not close after blur');
description.focus();
panel().children[0].children[1].dispatchEvent(new Event('click'));
assert.strictEqual(doc.body.children.length, 0, 'close button reopened popover on focus restoration');
assert.strictEqual(doc.activeElement, description);
console.log('metadata popover tests passed');

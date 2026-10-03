import assert from 'node:assert/strict';
import {mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {syncVersion} from './sync-version.mjs';

const root = mkdtempSync(join(tmpdir(), 'movielist-version-'));
try {
    mkdirSync(join(root, 'internal/version'), {recursive: true});
    mkdirSync(join(root, 'frontend'));
    writeFileSync(join(root, 'internal/version/VERSION'), '3.4.5\n');
    writeFileSync(join(root, 'wails.json'), JSON.stringify({info: {productVersion: 'old', companyName: 'Keep'}}));
    writeFileSync(join(root, 'frontend/package.json'), JSON.stringify({version: 'old', scripts: {test: 'keep'}}));
    writeFileSync(join(root, 'frontend/package-lock.json'), JSON.stringify({version: 'old', packages: {'': {version: 'old'}, 'node_modules/dependency': {version: '9.8.7'}}}));
    writeFileSync(join(root, 'frontend/index.html'), '<title>MovieList old</title>\n');
    assert.equal(syncVersion(root), '3.4.5');
    const readJSON = file => JSON.parse(readFileSync(join(root, file), 'utf8'));
    assert.equal(readJSON('wails.json').info.productVersion, '3.4.5');
    assert.equal(readJSON('wails.json').info.companyName, 'Keep');
    assert.equal(readJSON('frontend/package.json').version, '3.4.5');
    const lock = readJSON('frontend/package-lock.json');
    assert.equal(lock.version, '3.4.5'); assert.equal(lock.packages[''].version, '3.4.5');
    assert.equal(lock.packages['node_modules/dependency'].version, '9.8.7');
    assert.equal(readFileSync(join(root, 'frontend/index.html'), 'utf8'), '<title>MovieList 3.4.5</title>\n');
    const before = readFileSync(join(root, 'wails.json'), 'utf8');
    assert.equal(syncVersion(root), '3.4.5');
    assert.equal(readFileSync(join(root, 'wails.json'), 'utf8'), before);
    for (const invalid of ['', '3.4', '3.4.5-beta', '01.2.3', '65536.0.0']) {
        writeFileSync(join(root, 'internal/version/VERSION'), invalid);
        assert.throws(() => syncVersion(root));
        assert.equal(readFileSync(join(root, 'wails.json'), 'utf8'), before, 'invalid version changed metadata');
    }
} finally { rmSync(root, {recursive: true, force: true}); }
console.log('version synchronization tests passed');

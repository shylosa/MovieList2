import {readFileSync, writeFileSync} from 'node:fs';
import {resolve, dirname} from 'node:path';
import {fileURLToPath} from 'node:url';

export function syncVersion(root) {
    const version = readFileSync(resolve(root, 'internal/version/VERSION'), 'utf8').trim();
    if (!/^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/.test(version) || version.split('.').some(part => Number(part) > 65535)) {
        throw new Error('VERSION must contain major.minor.patch, each component between 0 and 65535');
    }
    const outputs = [];
    for (const file of ['wails.json', 'frontend/package.json', 'frontend/package-lock.json']) {
        const path = resolve(root, file);
        const original = readFileSync(path, 'utf8');
        const data = JSON.parse(original);
        if (file === 'wails.json') data.info = {...data.info, productVersion: version};
        else {
            data.version = version;
            if (file.endsWith('package-lock.json')) data.packages[''].version = version;
        }
        outputs.push({path, original, updated: `${JSON.stringify(data, null, 2)}\n`});
    }
    const path = resolve(root, 'frontend/index.html');
    const original = readFileSync(path, 'utf8');
    if (!/<title>[^<]*<\/title>/.test(original)) throw new Error('Missing frontend HTML title');
    outputs.push({path, original, updated: original.replace(/<title>[^<]*<\/title>/, `<title>MovieList ${version}</title>`).replace(/\r\n/g, '\n')});
    // Validate and prepare every output before writing any of them.
    for (const output of outputs) if (output.original !== output.updated) writeFileSync(output.path, output.updated, 'utf8');
    return version;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
    const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
    console.log(`Release version: ${syncVersion(root)}`);
}

// Copies the static export into the Go package that embeds it.
//
// The embed directive lives in internal/webui, which is where a Go build looks
// for these bytes; Next writes them to web/out. Rather than have the Go package
// reach outside its own directory, which go:embed does not allow, the export is
// copied across here as the last step of `npm run build`.
import { cpSync, mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const web = dirname(dirname(fileURLToPath(import.meta.url)));
const dest = join(web, '..', 'internal', 'webui', 'out');

// A clean copy: a page deleted from the app should not survive in the binary.
rmSync(dest, { recursive: true, force: true });
mkdirSync(dest, { recursive: true });
cpSync(join(web, 'out'), dest, { recursive: true });
// Keeps the directory, and so the go:embed, alive once the export is cleaned.
writeFileSync(join(dest, '.gitkeep'), '');
console.log(`embedded frontend -> ${dest}`);

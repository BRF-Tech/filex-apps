// Makes envelope-vectors.json with Node's own JSON and crypto, independently
// of the Go code: canonical JSON = JSON.stringify of the tree with object
// keys sorted (no whitespace, no trailing newline), then ed25519 over the
// lower-hex sha256 of those bytes. Run from this directory:
//
//   node make-vectors.mjs > envelope-vectors.json
//
// signedjson_test.go checks that the Go signer writes the same bytes and
// the same signatures (ed25519 is deterministic).
import { createHash, createPrivateKey, createPublicKey, sign } from 'node:crypto';

const seedHex = '9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60';
const pkcs8 = Buffer.concat([Buffer.from('302e020100300506032b657004220420', 'hex'), Buffer.from(seedHex, 'hex')]);
const priv = createPrivateKey({ key: pkcs8, format: 'der', type: 'pkcs8' });
const spki = createPublicKey(priv).export({ format: 'der', type: 'spki' });
const publicHex = Buffer.from(spki).subarray(-32).toString('hex');

function sortTree(v) {
  if (Array.isArray(v)) return v.map(sortTree);
  if (v !== null && typeof v === 'object') {
    const out = {};
    for (const k of Object.keys(v).sort()) out[k] = sortTree(v[k]);
    return out;
  }
  return v;
}

const payloads = [
  {
    result: 'valid', app: 'filex-sign', licensee: 'Zoë Ångström & Co <legal@example.com>', seats: 3, seats_used: 1,
    valid_until: '2027-10-04', instance_id: 'inst-01HZ/x:y', checked_at: '2026-10-04T12:00:00Z',
    next_check_by: '2026-10-05T12:00:00Z', grace_until: '2026-10-18T12:00:00Z',
  },
  {
    result: 'invalid', app: 'hello', instance_id: 'a', checked_at: '2026-10-04T12:00:00Z',
    next_check_by: '2026-10-05T12:00:00Z', grace_until: '2026-10-18T12:00:00Z',
  },
  {
    store: 'https://apps.example.com', filex_origin: 'https://files.example.com:8443', token_id: '0123456789abcdef', app: 'filex-drawio', kind: 'app',
    version: '31.5.3', repo: 'BRF-Tech/filex-drawio', ref: 'v31.5.3', commit: 'c'.repeat(40),
    manifest_sha256: 'a'.repeat(64), ui_sha256: 'e'.repeat(64), permissions: ['files:read', 'files:write'],
    filex_range: '>=0.50.0 <0.60.0', paid: true, license_key: 'FXL-7K2Q-9WXM-3HDF-0PZA-QRST-V8MN-4C',
    expires_at: '2026-10-04T12:30:00Z',
  },
  { quote: 'a "b" \\ c / d', emoji: 'ünïcödé 😀 日本', empty: [], nested: { z: [1, { b: false, a: null }], A: 0 } },
];

const cases = payloads.map((p) => {
  const canonical = JSON.stringify(sortTree(p));
  const sha256 = createHash('sha256').update(Buffer.from(canonical, 'utf8')).digest('hex');
  const signature = sign(null, Buffer.from(sha256, 'utf8'), priv).toString('hex');
  return { payload: p, canonical, sha256, signature };
});

process.stdout.write(JSON.stringify({ seed_hex: seedHex, public_hex: publicHex, cases }, null, 2) + '\n');

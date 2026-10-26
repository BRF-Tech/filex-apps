// make-request-vector.mjs: the instance request signature test vector
// (request-vector.json), made with Node's own crypto - an implementation
// independent of the Go code it checks. Run: node make-request-vector.mjs > request-vector.json
import crypto from 'node:crypto';
const seed = Buffer.alloc(32, 7);
const pkcs8 = Buffer.concat([Buffer.from('302e020100300506032b657004220420', 'hex'), seed]);
const priv = crypto.createPrivateKey({ key: pkcs8, format: 'der', type: 'pkcs8' });
const pubDer = crypto.createPublicKey(priv).export({ format: 'der', type: 'spki' });
const pub = pubDer.subarray(pubDer.length - 32);
const fpr = crypto.createHash('sha256').update(pub).digest('hex');
const instance = '0f1e2d3c-4b5a-4968-8776-655443322110';
const method = 'POST';
const path = `/v1/instances/${instance}/intents`;
const ts = '1790000000';
const nonce = 'n0nce_AAAAAAAAAAAAAAAA';
const body = '{"app":"hello","version":"1.2.0"}';
const bodySha = crypto.createHash('sha256').update(body).digest('hex');
const text = ['FAPPS-INSTANCE-REQUEST-1', method, path, instance, fpr, ts, nonce, bodySha].join('\n');
const textSha = crypto.createHash('sha256').update(text).digest('hex');
const sig = crypto.sign(null, Buffer.from(textSha, 'ascii'), priv).toString('hex');
console.log(JSON.stringify({ seed_hex: seed.toString('hex'), public_key: pub.toString('hex'), key_fingerprint: fpr, method, path, instance_id: instance, timestamp: ts, nonce, body, body_sha256: bodySha, text_sha256: textSha, signature: sig }, null, 2));

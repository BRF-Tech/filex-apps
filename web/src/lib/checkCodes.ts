// What a pipeline code means, in a sentence a publisher can act on. The
// server's own message stays beside it (it names the file, the hash, the
// validator); this says what kind of problem it is. A code the interface
// does not know yet still shows, raw, in a general sentence: a new code on
// the server never turns into a blank line here.
import { S, fmt } from '@/strings';

const CODES: Readonly<Record<string, string>> = S.codes;

export function knownCode(code: string | null | undefined): boolean {
  return !!code && Object.prototype.hasOwnProperty.call(CODES, code);
}

/** The sentence for a code; a general one, naming the code, when it is new. */
export function explainCode(code: string | null | undefined): string {
  if (!code) return '';
  return knownCode(code) ? CODES[code] : fmt(S.submission.unknownCode, { code });
}

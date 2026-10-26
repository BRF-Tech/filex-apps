import { describe, expect, it } from 'vitest';
import { explainCode } from '@/lib/checkCodes';

// The pipeline table's codes (docs/ARCHITECTURE.md "Submission pipeline")
// and the validator's own (2026-10-03): each has a sentence of its own.
const KNOWN = [
  'repo_private', 'repo_address', 'repo_permission', 'owner_mismatch', 'repo_archived',
  'not_a_tag', 'tag_not_found',
  'manifest_missing', 'manifest_unreadable', 'too_large', 'sha256_required', 'range_invalid',
  'consent_missing', 'consent_publisher', 'consent_invalid',
  'name_taken', 'name_reserved', 'repo_changed', 'version_exists', 'license_missing',
  'sha256_mismatch', 'fetch_failed', 'host_not_allowed',
  'media_invalid',
  'no_validator_in_range', 'manifest_sha256_mismatch', 'kind_mismatch', 'incompatible', 'permissions_mismatch',
  'answer_incomplete', 'out_of_range',
  // contract 7194988: policy, the pipeline's own failures, the submission's refusals
  'name_changed', 'publisher_changed', 'kind_changed', 'version_invalid', 'version_backport',
  'resolve_error', 'resolve_incomplete', 'resolver_unavailable', 'resolution_unreadable', 'dry_run_error', 'validator_unavailable',
  'not_a_member', 'publisher_not_active', 'github_required', 'github_token_invalid', 'github_unavailable',
  // a storage plugin's submission
  'platform_missing', 'platform_unknown', 'build_not_native', 'no_build_for_platform', 'name_mismatch', 'version_mismatch',
  'conformance_unverified', 'conformance_failed',
];

describe('pipeline codes in words', () => {
  it('every known code has its own sentence', () => {
    const generic = explainCode('zz_not_a_code');
    for (const code of KNOWN) {
      const s = explainCode(code);
      expect(s, code).not.toBe(generic.replace('zz_not_a_code', code));
      expect(s.length, code).toBeGreaterThan(20);
    }
  });

  it('a code it does not know yet still shows, raw, in a general sentence', () => {
    expect(explainCode('brand_new_code')).toContain('"brand_new_code"');
  });

  it('says nothing for no code', () => {
    expect(explainCode('')).toBe('');
    expect(explainCode(undefined)).toBe('');
  });

  it('does not answer for inherited object keys', () => {
    expect(explainCode('constructor')).toContain('"constructor"');
  });
});

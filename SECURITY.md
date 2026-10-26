# Security Policy

## Supported versions

filex Apps is pre-1.0 and ships from the latest tagged release
(`ghcr.io/brf-tech/filex-apps`). A fix for a hole that can be exploited in a
released version ships at once, as a release of its own. Please run a recent
version.

## Reporting a vulnerability

**Please do not open a public issue for security problems.**

Report privately instead, with this repository's **private vulnerability
reporting**: the **Security** tab, then **Report a vulnerability** (GitHub
Security Advisories). Only the maintainers see it.

Include, where you can:

- the affected version or commit,
- a description and the impact,
- steps to reproduce or a proof of concept,
- a suggested fix, if you have one.

We aim to acknowledge a report within a few days and to ship a fix or a
mitigation as soon as practical, agreeing a disclosure timeline with you.

## Scope

The store is self-hosted: its operator controls the host, the reverse proxy,
the keys and who administers it. Reports about the software itself are in
scope, for example:

- a filex accepting something the store did not sign, or the store signing
  what it did not review: the signed index, an artifact signature, a storage
  plugin build's signature, an install intent or a license verdict
  ([docs/ARCHITECTURE.md](docs/ARCHITECTURE.md), "Signatures and keys");
- a submission listed without its checks (the manifest at the commit, every
  artifact's sha256, the validators' dry runs, the repository owner's consent);
- a store role (reviewer, administrator) reached by someone the store's
  configuration does not name;
- the store's outbound requests reaching a non-public address (SSRF through a
  manifest, a listing, a webhook or a push endpoint), or a redirect from https
  to plain http;
- a session, a CSRF token, an install link or a connection code usable beyond
  what it names, or after it expired or was spent;
- a license key, a saved filex address or a webhook's address or secret read
  back from the database, the logs or an answer;
- script running in the store's pages from listing content (Markdown,
  screenshots, names).

Misconfiguration of one deployment is not in scope (we still welcome
hardening suggestions), nor is anything that needs a shell in the store's
container: its environment holds the keys, so that is full control already.
A malicious app attacking the filex validators is in scope only as far as
it reaches beyond them: they hold no data, take no traffic from outside, and
the storage plugin validator has no network.

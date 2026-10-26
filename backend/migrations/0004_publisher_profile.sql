-- A publisher's public profile for the publisher list (GET /api/v1/publishers):
-- the GitHub owner's profile text and avatar, read when the publisher becomes
-- active. The avatar is mirrored like any listing image (media, by sha256);
-- the store never links to GitHub for it. Both are optional.
ALTER TABLE publishers ADD COLUMN description text CHECK (length(description) <= 500);
ALTER TABLE publishers ADD COLUMN avatar_sha256 text REFERENCES media (sha256);

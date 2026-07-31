-- 0018_maturity_range: retire the app-wide NSFW display MODE and replace it with
-- a PG..XXX maturity RANGE, and invalidate the community cache that was captured
-- under the old fetch shape.
--
-- 1. THE SETTING
-- ---------------------------------------------------------------------------
-- The old `nsfw_display` row held one of hide | blur | show. All three were
-- DISPLAY treatments over a feed that was fetched the same way regardless:
-- `blur` was a browser-side CSS filter (the bytes still went over the wire),
-- `show` was plain, and `hide` had already been normalized away to `blur` in
-- code, so nobody was actually on it.
--
-- The replacement, `maturity_range`, is a real server-side filter: content
-- outside the band is never rendered at all. Its value is "<min>:<max>" over the
-- slugs pg | pg13 | r | x | xxx (web.maturityRange.String).
--
-- EVERY old value maps to the FULL range, "pg:xxx" — hide included.
--
--   Why not map hide -> a narrow band? Because nothing the user can currently
--   SEE may disappear on upgrade. blur and show both rendered every level (blur
--   merely smeared it), and hide was unreachable in production, so the only
--   mapping that preserves what is on screen is "everything". The consequence —
--   that previously-blurred content now renders in the clear — is deliberate and
--   was accepted explicitly: blur was never an access control, and the user
--   picks a narrower band from the nav control the moment they want one.
--
-- The old row is then DELETED. Leaving it would be a lie: nothing reads it, and
-- a future reader would find a mode that no longer has any meaning.
--
-- The row is only written when one existed before, so a FRESH install stays
-- setting-less and falls through to the code default (which is also the full
-- range, web.fullMaturityRange). Storing a row on a fresh install would make the
-- default un-changeable-by-code forever.

INSERT INTO settings (key, value, updated_at)
SELECT 'maturity_range', 'pg:xxx', (SELECT updated_at FROM settings WHERE key = 'nsfw_display')
WHERE EXISTS (SELECT 1 FROM settings WHERE key = 'nsfw_display')
  AND NOT EXISTS (SELECT 1 FROM settings WHERE key = 'maturity_range');

DELETE FROM settings WHERE key = 'nsfw_display';

-- 2. THE COMMUNITY CACHE
-- ---------------------------------------------------------------------------
-- community_cache is keyed (model_id, version_id, nsfw) since 0017, and `nsfw`
-- still means exactly what it meant there: the CivitAI browsing CEILING the body
-- was fetched under. The key does NOT need to change — the ceiling is now
-- derived from the range MAX (web.maturityRange.imagesNSFWCeiling) instead of
-- being the compile-time constant "Mature", and the existing key already keeps
-- one ceiling's body from being served for another.
--
-- What DID change is the body SHAPE. Every cached row was fetched with limit=12,
-- one page's worth. The range filter now over-fetches (communityFetchLimit) and
-- clamps to the page size, so a 12-item body cannot fill a narrow band and the
-- feed would look mysteriously short on exactly the models the user has already
-- visited. The rows are therefore discarded.
--
-- This is a fail-open image-feed cache, never a system of record (see 0010), so
-- the whole cost is one refetch per model version.
--
-- 🔴 communityFetchLimit is part of the cached body's shape but NOT part of the
-- key. Changing it needs another invalidation exactly like this one.

DELETE FROM community_cache;

-- Files and account management, for the administrator who already exists.
--
-- The same shape as 0003, and for the same reason: permissions are a JSON array
-- written into the row when an account is created, so adding a constant to
-- AdministratorPermissions reaches only accounts made afterwards. Without this
-- the person who set the machine up would be the one person who could not add
-- anybody to it — and the screens for doing so would simply not appear, with
-- nothing anywhere saying why.
--
-- Granted to whoever already holds system.manage. That is what "administrator"
-- means here, it is the predicate 0003 used, and an account deliberately created
-- with less stays with less.
--
-- Only ever grants. A migration that takes a permission away is a migration that
-- can lock somebody out of their own server, and there is no undo.
UPDATE users
SET permissions = json_insert(permissions, '$[#]', 'files.read')
WHERE json_valid(permissions)
  AND EXISTS (SELECT 1 FROM json_each(users.permissions) WHERE value = 'system.manage')
  AND NOT EXISTS (SELECT 1 FROM json_each(users.permissions) WHERE value = 'files.read');

UPDATE users
SET permissions = json_insert(permissions, '$[#]', 'files.write')
WHERE json_valid(permissions)
  AND EXISTS (SELECT 1 FROM json_each(users.permissions) WHERE value = 'system.manage')
  AND NOT EXISTS (SELECT 1 FROM json_each(users.permissions) WHERE value = 'files.write');

UPDATE users
SET permissions = json_insert(permissions, '$[#]', 'accounts.manage')
WHERE json_valid(permissions)
  AND EXISTS (SELECT 1 FROM json_each(users.permissions) WHERE value = 'system.manage')
  AND NOT EXISTS (SELECT 1 FROM json_each(users.permissions) WHERE value = 'accounts.manage');

-- The right to use the local assistant, for administrators who already exist.
--
-- Permissions are a JSON array written into the row when the account is
-- created, so adding a constant to AdministratorPermissions only affects
-- accounts made after the change. Without this, the person who installed
-- Homebase — the only account on most machines — would be the one person who
-- could not see the assistant, and nothing would say why.
--
-- Granted to whoever already holds system.manage: that is what "administrator"
-- means here, and it is the same test the first-run account passes. An account
-- deliberately created without it stays without it.
UPDATE users
SET permissions = json_insert(permissions, '$[#]', 'assistant.use')
WHERE json_valid(permissions)
  AND EXISTS (
      SELECT 1 FROM json_each(users.permissions) WHERE value = 'system.manage'
  )
  AND NOT EXISTS (
      SELECT 1 FROM json_each(users.permissions) WHERE value = 'assistant.use'
  );

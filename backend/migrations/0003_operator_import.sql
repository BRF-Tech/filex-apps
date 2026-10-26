-- Operator import of an official publisher's repository
-- (docs/ARCHITECTURE.md "Operator import (official publisher)").
--
-- The operator's listing (the .filex-apps.json fields, the description's
-- text and the images' bytes) travels with the submission, so whichever
-- worker resolves it reads the same listing. NULL for every submission made
-- through the web interface.
ALTER TABLE submissions ADD COLUMN operator_listing jsonb;

-- The account the operator command acts as: the submitter and the reviewer
-- of an operator import, an audit actor. It has no identity, so nobody signs
-- in as it. At most one.
ALTER TABLE accounts ADD COLUMN operator boolean NOT NULL DEFAULT false;
CREATE UNIQUE INDEX accounts_one_operator ON accounts (operator) WHERE operator;
